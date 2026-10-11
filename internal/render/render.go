package render

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yeswearebot/prompt-hub/internal/pack"
)

type Request struct {
	Dir      string
	Manifest *pack.Manifest
	TreeHash string

	Tree pack.Tree

	Deps map[string]pack.Tree
	Turn string
	Now  time.Time

	Context map[string]string
}

type Result struct {
	Body     string
	Warnings []string
}

type Runtime interface {
	Name() string
	Render(Request) (Result, error)
}

type Registry struct {
	runtimes map[string]Runtime
	hints    map[string]string
}

func NewRegistry() *Registry {
	return &Registry{runtimes: map[string]Runtime{}, hints: map[string]string{}}
}

func (r *Registry) Register(rt Runtime) { r.runtimes[rt.Name()] = rt }

func (r *Registry) SetHint(tier, how string) {
	if how != "" {
		r.hints[tier] = how
	}
}

func (r *Registry) Tiers() []string {
	names := make([]string, 0, len(r.runtimes))
	for n := range r.runtimes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (r *Registry) Get(name string) (Runtime, error) {
	if rt, ok := r.runtimes[name]; ok {
		return rt, nil
	}
	msg := fmt.Sprintf("运行时 %q 未启用（当前可用：%s）", name, strings.Join(r.Tiers(), ", "))
	if how := r.hints[name]; how != "" {
		msg += "。" + how
	}
	return nil, errors.New(msg)
}

var varRe = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)

type TemplateRuntime struct{}

func (TemplateRuntime) Name() string { return "template" }

func (t TemplateRuntime) Render(req Request) (Result, error) {
	if req.Manifest == nil {
		return Result{}, fmt.Errorf("缺少 manifest")
	}
	entry := filepath.Join(req.Dir, filepath.FromSlash(req.Manifest.Entry))
	raw, err := os.ReadFile(entry)
	if err != nil {
		return Result{}, fmt.Errorf("读取 entry %s 失败: %w", req.Manifest.Entry, err)
	}

	if body, stripped := pack.StripHeaderComment(raw); stripped {
		raw = body
	}

	vars, missing, err := t.buildVars(req)
	if err != nil {
		return Result{}, err
	}
	if len(missing) > 0 {
		return Result{}, &MissingContextError{Fields: missing}
	}

	body, unknown, err := expandTemplate(string(raw), vars, req.Tree, req.Deps)
	if err != nil {
		return Result{}, err
	}
	if len(unknown) > 0 {
		return Result{}, formatUnknown(unknown)
	}

	res := Result{Body: body}
	if out, truncated := truncateUTF8(body, req.Manifest.OutputMax); truncated {
		res.Body = out
		res.Warnings = append(res.Warnings,
			fmt.Sprintf("输出超过 outputMax=%d，已截断", req.Manifest.OutputMax))
	}
	return res, nil
}

func truncateUTF8(s string, max int) (string, bool) {
	if max <= 0 || len(s) <= max {
		return s, false
	}
	end := max
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end], true
}

func (t TemplateRuntime) buildVars(req Request) (map[string]string, []string, error) {
	vars := map[string]string{
		"packId":  req.Manifest.PackID,
		"version": req.Manifest.Version,
		"turn":    req.Turn,

		"now":     req.Now.UTC().Format("2006-01-02 15:04:05"),
		"nowUnix": fmt.Sprintf("%d", req.Now.Unix()),
	}
	for k, v := range req.Context {
		vars[k] = v
	}

	var missing []string
	for _, need := range req.Manifest.Requires {
		if _, ok := req.Context[need]; !ok {
			missing = append(missing, need)
		}
	}
	sort.Strings(missing)
	return vars, missing, nil
}

type MissingContextError struct {
	Fields []string
}

func (e *MissingContextError) Error() string {
	return fmt.Sprintf("缺少 pack 声明的必需 context 字段: %s", strings.Join(e.Fields, ", "))
}
