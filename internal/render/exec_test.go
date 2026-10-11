package render

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yeswearebot/prompt-hub/internal/pack"
)

func TestMain(m *testing.M) {
	if os.Getenv("PH_STUB_RUNTIME") == "1" {
		stubMain()
		return
	}
	os.Exit(m.Run())
}

func stubMain() {

	if os.Getenv("PH_STUB_MODE") == "writer" {
		file := os.Getenv("PH_STUB_FILE")
		for {
			if file != "" {
				if fh, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
					fmt.Fprintf(fh, "%d\n", time.Now().UnixNano())
					_ = fh.Close()
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	raw, _ := io.ReadAll(os.Stdin)
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		fmt.Fprintln(os.Stderr, "stub: 无法解析 envelope")
		os.Exit(1)
	}

	switch env.Turn {
	case "stub-fail":
		fmt.Fprintln(os.Stderr, "stub: 故意失败")
		os.Exit(1)

	case "stub-hang":
		time.Sleep(30 * time.Second)
		os.Exit(0)

	case "stub-flood":
		fmt.Print(strings.Repeat("x", env.OutputMax+500))
		os.Exit(0)

	case "stub-warn":
		fmt.Fprintln(os.Stderr, "stub: 一条诊断")

	case "stub-spawn":
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "PH_STUB_MODE=writer")
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "stub: 派生孙进程失败: "+err.Error())
			os.Exit(1)
		}
		time.Sleep(30 * time.Second)
		os.Exit(0)

	case "stub-hog":

		var hold [][]byte
		for i := 0; i < 64; i++ {
			chunk := make([]byte, 8<<20)
			for j := range chunk {
				chunk[j] = byte(i)
			}
			hold = append(hold, chunk)
			fmt.Fprintf(os.Stderr, "alloc %d MB\n", (i+1)*8)
		}
		fmt.Fprintln(os.Stderr, "stub-hog: 没有被内存上限杀掉（上限未生效）")
		os.Exit(9)
	}

	fmt.Printf("PACK=%s\nNICK=%s\nTURN=%s\nNOW=%s\n",
		env.PackID+"@"+env.Version,
		env.Context["chat.user.nickname"],
		env.Turn,
		env.Now)
	os.Exit(0)
}

type dirView struct{}

func (dirView) Build(req Request) (string, func(), error) { return req.Dir, func() {}, nil }

func newReq(t *testing.T, turn string, ctx map[string]string, outMax int) Request {
	t.Helper()
	dir, err := os.MkdirTemp(".", "ph-rt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	m := &pack.Manifest{
		PackID: "demo", Version: "1.0.0", Profile: "chat/1", Kind: "content",
		Entry: "index.prompt", Runtime: "stub",
		Requires: []string{"chat.user.nickname"}, OutputMax: outMax,
	}
	m.ApplyDefaults()
	return Request{
		Dir: dir, Manifest: m, TreeHash: "sha256-test",
		Tree: pack.Tree{"index.prompt": []byte("stub")},
		Turn: turn, Now: time.Date(2026, 10, 10, 21, 30, 0, 0, time.UTC),
		Context: ctx,
	}
}

func stubRuntime(t *testing.T, l Limits) *ProcessRuntime {
	t.Helper()

	t.Setenv("PH_STUB_RUNTIME", "1")
	return NewProcessRuntime("stub", os.Args[0], nil, l, dirView{})
}

func TestProcessRuntimeOK(t *testing.T) {
	rt := stubRuntime(t, Limits{WallTimeout: 5 * time.Second})
	res, err := rt.Render(newReq(t, "ev_1", map[string]string{"chat.user.nickname": "小兔"}, 8000))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PACK=demo@1.0.0", "NICK=小兔", "TURN=ev_1", "NOW=2026-10-10T21:30:00Z"} {
		if !strings.Contains(res.Body, want) {
			t.Fatalf("输出缺少 %q：\n%s", want, res.Body)
		}
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("不应有 warning: %v", res.Warnings)
	}
}

func TestProcessRuntimeMissingContextBeforeSpawn(t *testing.T) {
	rt := stubRuntime(t, Limits{WallTimeout: 5 * time.Second})
	_, err := rt.Render(newReq(t, "ev_1", map[string]string{}, 8000))
	var missing *MissingContextError
	if !errors.As(err, &missing) {
		t.Fatalf("应为 MissingContextError，得到 %T: %v", err, err)
	}
	if len(missing.Fields) != 1 || missing.Fields[0] != "chat.user.nickname" {
		t.Fatalf("缺失字段列表不对: %v", missing.Fields)
	}
}

func TestProcessRuntimeFailure(t *testing.T) {
	rt := stubRuntime(t, Limits{WallTimeout: 5 * time.Second})
	_, err := rt.Render(newReq(t, "stub-fail", map[string]string{"chat.user.nickname": "x"}, 8000))
	if err == nil || !strings.Contains(err.Error(), "故意失败") {
		t.Fatalf("应把 stderr 作为错误信息，得到 %v", err)
	}
}

func TestProcessRuntimeTimeout(t *testing.T) {
	rt := stubRuntime(t, Limits{WallTimeout: 300 * time.Millisecond})
	start := time.Now()
	_, err := rt.Render(newReq(t, "stub-hang", map[string]string{"chat.user.nickname": "x"}, 8000))
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("应超时报错，得到 %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("超时未生效，耗时 %s", elapsed)
	}
}

func TestProcessRuntimeTruncates(t *testing.T) {
	rt := stubRuntime(t, Limits{WallTimeout: 5 * time.Second})
	res, err := rt.Render(newReq(t, "stub-flood", map[string]string{"chat.user.nickname": "x"}, 64))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Body) != 64 {
		t.Fatalf("正文长度 %d，期望 64", len(res.Body))
	}
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "截断") {
		t.Fatalf("应给出截断告警，得到 %v", res.Warnings)
	}
}

func TestProcessRuntimeKeepsDiagnostics(t *testing.T) {
	rt := stubRuntime(t, Limits{WallTimeout: 5 * time.Second})
	res, err := rt.Render(newReq(t, "stub-warn", map[string]string{"chat.user.nickname": "x"}, 8000))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "运行时诊断") {
		t.Fatalf("应保留运行时诊断，得到 %v", res.Warnings)
	}
}

func TestJobKillsProcessTree(t *testing.T) {
	dir, err := os.MkdirTemp(".", "ph-tree-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	logFile, err := filepath.Abs(filepath.Join(dir, "writer.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PH_STUB_FILE", logFile)

	rt := stubRuntime(t, Limits{WallTimeout: 800 * time.Millisecond})
	_, err = rt.Render(newReq(t, "stub-spawn", map[string]string{"chat.user.nickname": "x"}, 8000))
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("应超时，得到 %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	before := fileSize(t, logFile)
	if before == 0 {
		t.Fatal("孙进程从未写入，测试前提不成立")
	}
	time.Sleep(800 * time.Millisecond)
	after := fileSize(t, logFile)
	if after != before {
		t.Fatalf("孙进程仍在写（%d -> %d 字节）：进程树没被杀掉", before, after)
	}
}

func TestJobBoundsMemoryGrowth(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("非 Windows 平台目前只用进程组，不做内存上限（见 job_other.go）")
	}
	rt := stubRuntime(t, Limits{WallTimeout: 3 * time.Second, MemoryBytes: 64 << 20})

	start := time.Now()
	_, err := rt.Render(newReq(t, "stub-hog", map[string]string{"chat.user.nickname": "x"}, 8000))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("内存被限制时必须失败")
	}
	if strings.Contains(err.Error(), "没有被内存上限杀掉") {
		t.Fatalf("内存上限未生效：hog 把 512MB 全吃完了：%v", err)
	}
	if elapsed > 15*time.Second {
		t.Fatalf("耗时 %s，不符合「分配被拒 + wall timeout 兜底」应有的表现", elapsed)
	}
	t.Logf("内存上限生效（分配被拒，由 wall timeout 结束，耗时 %s）: %v", elapsed.Round(time.Millisecond), err)
}

func TestEgressDeniedByDefault(t *testing.T) {
	rt := stubRuntime(t, Limits{WallTimeout: 5 * time.Second})
	req := newReq(t, "ev_1", map[string]string{"chat.user.nickname": "x"}, 8000)
	req.Manifest.Egress = []string{"api.example.com"}

	_, err := rt.Render(req)
	var denied *EgressDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("应为 EgressDeniedError，得到 %T: %v", err, err)
	}
	if len(denied.Hosts) != 1 || denied.Hosts[0] != "api.example.com" {
		t.Fatalf("被拒 host 列表不对: %v", denied.Hosts)
	}
}

func TestEgressAllowlist(t *testing.T) {
	rt := stubRuntime(t, Limits{WallTimeout: 5 * time.Second}).WithEgress([]string{"api.example.com"})

	ok := newReq(t, "ev_1", map[string]string{"chat.user.nickname": "x"}, 8000)
	ok.Manifest.Egress = []string{"API.Example.com"}
	if _, err := rt.Render(ok); err != nil {
		t.Fatalf("已授权 host 应能渲染: %v", err)
	}

	bad := newReq(t, "ev_1", map[string]string{"chat.user.nickname": "x"}, 8000)
	bad.Manifest.Egress = []string{"api.example.com", "evil.example"}
	_, err := rt.Render(bad)
	var denied *EgressDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("列表外的 host 必须被拒，得到 %v", err)
	}
	if len(denied.Hosts) != 1 || denied.Hosts[0] != "evil.example" {
		t.Fatalf("只应拒绝 evil.example，得到 %v", denied.Hosts)
	}
}

func TestFindPHPReportsReason(t *testing.T) {

	dir, err := os.MkdirTemp(".", "ph-find-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	if p, reason := FindPHP("", dir); p == "" && reason == "" {
		t.Fatal("找不到 php 时必须给出原因")
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}
