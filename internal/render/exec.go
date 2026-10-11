package render

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yeswearebot/prompt-hub/internal/pack"
)

type Envelope struct {
	Protocol  int               `json:"protocol"`
	PackID    string            `json:"packId"`
	Version   string            `json:"version"`
	Profile   string            `json:"profile"`
	Kind      string            `json:"kind"`
	TreeHash  string            `json:"treeHash"`
	Entry     string            `json:"entry"`
	Turn      string            `json:"turn"`
	Now       string            `json:"now"`
	OutputMax int               `json:"outputMax"`
	Context   map[string]string `json:"context"`

	ViewDir string `json:"viewDir"`
}

const ProtocolVersion = 1

const maxStderr = 8 << 10

type ViewBuilder interface {
	Build(req Request) (dir string, cleanup func(), err error)
}

type ProcessRuntime struct {
	name     string
	bin      string
	argvs    []string
	limits   Limits
	views    ViewBuilder
	egress   []string
	EntryArg func(viewDir string, entry string) []string
}

func NewProcessRuntime(name, bin string, entryArgs []string, limits Limits, views ViewBuilder) *ProcessRuntime {
	return &ProcessRuntime{
		name: name, bin: bin, argvs: entryArgs,
		limits: limits.Normalized(), views: views,
	}
}

func (p *ProcessRuntime) WithEgress(hosts []string) *ProcessRuntime {
	p.egress = hosts
	return p
}

func (p *ProcessRuntime) Name() string { return p.name }

func (p *ProcessRuntime) Render(req Request) (Result, error) {
	if p.bin == "" {
		return Result{}, fmt.Errorf("运行时 %s 不可用：未配置可执行文件", p.name)
	}
	if req.Manifest == nil {
		return Result{}, fmt.Errorf("缺少 manifest")
	}
	if missing := CheckRequires(req.Manifest, req.Context); len(missing) > 0 {
		return Result{}, &MissingContextError{Fields: missing}
	}
	if bad := CheckEgress(req.Manifest, p.egress); len(bad) > 0 {
		return Result{}, &EgressDeniedError{Hosts: bad, Allowed: p.egress}
	}

	viewDir, cleanup, err := p.views.Build(req)
	if err != nil {
		return Result{}, fmt.Errorf("构造渲染视图失败: %w", err)
	}
	defer cleanup()

	entry := filepath.Join(viewDir, filepath.FromSlash(req.Manifest.Entry))
	args := append([]string{}, p.argvs...)
	if p.EntryArg != nil {
		args = append(args, p.EntryArg(viewDir, req.Manifest.Entry)...)
	} else {
		args = append(args, entry)
	}

	stdin, err := json.Marshal(envelopeFor(req, viewDir))
	if err != nil {
		return Result{}, err
	}

	j, jobErr := newJob(p.limits)
	if jobErr != nil && !p.limits.AllowUnsandboxed {
		return Result{}, fmt.Errorf("沙箱未生效（Job Object 创建失败）: %w", jobErr)
	}
	defer j.close()

	ctx, cancel := context.WithTimeout(context.Background(), p.limits.WallTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, p.bin, args...)
	cmd.Dir = viewDir
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.SysProcAttr = procAttr()
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = append(os.Environ(),
		"PH_PROTOCOL=1",
		"PH_PACK="+req.Manifest.PackID+"@"+req.Manifest.Version,
		"PH_TREE_HASH="+req.TreeHash,
		"PH_TURN="+req.Turn,
		"PH_VIEW="+viewDir,
		"PH_EGRESS_ALLOW="+strings.Join(p.egress, ","),
	)

	cap := req.Manifest.OutputMax
	if cap <= 0 {
		cap = 8000
	}
	stdout := &capWriter{max: cap}
	stderr := &capWriter{max: maxStderr}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("启动运行时 %s 失败: %w", p.name, err)
	}

	warnings := []string{}
	if jobErr == nil {
		if aerr := j.assign(cmd.Process); aerr != nil {

			if !p.limits.AllowUnsandboxed {
				j.terminate()
				_ = cmd.Process.Kill()
				_, _ = cmd.Process.Wait()
				return Result{}, fmt.Errorf("沙箱未生效（无法把进程放进 Job Object）: %w", aerr)
			}
			warnings = append(warnings, "沙箱未生效，本次渲染未受限: "+aerr.Error())
		}
	} else {
		warnings = append(warnings, "沙箱未生效，本次渲染未受限: "+jobErr.Error())
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var runErr error
	timedOut := false
	select {
	case runErr = <-done:
	case <-ctx.Done():
		timedOut = true

		j.terminate()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		runErr = <-done
	}

	stderrText := stderr.buf.String()
	if timedOut {

		detail := ""
		if s := lastNonEmptyLine(stderrText); s != "" {
			detail += "；最后诊断: " + s
		}
		if s := lastNonEmptyLine(stdout.buf.String()); s != "" {
			detail += "；最后输出: " + s
		}
		return Result{}, fmt.Errorf("运行时 %s 超时（%s），进程树已终止%s", p.name, p.limits.WallTimeout, detail)
	}
	if runErr != nil {
		msg := strings.TrimSpace(stderrText)
		if msg == "" {
			msg = runErr.Error()
		}
		return Result{}, fmt.Errorf("运行时 %s 失败: %s", p.name, firstLine(msg))
	}

	res := Result{Body: stdout.buf.String(), Warnings: warnings}
	if stdout.over {
		res.Warnings = append(res.Warnings,
			fmt.Sprintf("输出超过 outputMax=%d，已截断", cap))
	}

	if warn := strings.TrimSpace(stderrText); warn != "" {
		res.Warnings = append(res.Warnings, "运行时诊断: "+firstLine(warn))
	}
	return res, nil
}

func envelopeFor(req Request, viewDir string) Envelope {
	return Envelope{
		Protocol:  ProtocolVersion,
		PackID:    req.Manifest.PackID,
		Version:   req.Manifest.Version,
		Profile:   req.Manifest.Profile,
		Kind:      req.Manifest.Kind,
		TreeHash:  req.TreeHash,
		Entry:     req.Manifest.Entry,
		Turn:      req.Turn,
		Now:       req.Now.Format(time.RFC3339),
		OutputMax: req.Manifest.OutputMax,
		Context:   req.Context,
		ViewDir:   viewDir,
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			if len(t) > 300 {
				t = t[:300] + "…"
			}
			return t
		}
	}
	return ""
}

func CheckRequires(m *pack.Manifest, ctx map[string]string) []string {
	if m == nil {
		return nil
	}
	var missing []string
	for _, need := range m.Requires {
		if _, ok := ctx[need]; !ok {
			missing = append(missing, need)
		}
	}
	sort.Strings(missing)
	return missing
}

type EgressDeniedError struct {
	Hosts   []string
	Allowed []string
}

func (e *EgressDeniedError) Error() string {
	msg := fmt.Sprintf("pack 声明的出网目标未获授权: %s", strings.Join(e.Hosts, ", "))
	if len(e.Allowed) == 0 {
		return msg + "（这台机器不允许任何 pack 出网；如确需，用 --allow-egress 显式授权）"
	}
	return msg + fmt.Sprintf("（已授权: %s）", strings.Join(e.Allowed, ", "))
}

type capWriter struct {
	max  int
	buf  bytes.Buffer
	over bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if !w.over {
		room := w.max - w.buf.Len()
		if room <= 0 {
			w.over = true
		} else if len(p) > room {
			w.buf.Write(p[:room])
			w.over = true
		} else {
			w.buf.Write(p)
		}
	}
	return len(p), nil
}
