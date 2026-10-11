package render

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yeswearebot/prompt-hub/internal/pack"
)

var (
	wasmOnce sync.Once
	wasmFile string
	wasmErr  error
)

func buildWasmStub(t *testing.T) string {
	t.Helper()
	wasmOnce.Do(func() {
		if _, err := exec.LookPath("go"); err != nil {
			wasmErr = err
			return
		}
		out, err := filepath.Abs(filepath.Join("testdata", "wasmstub", "wasmstub.wasm"))
		if err != nil {
			wasmErr = err
			return
		}
		cmd := exec.Command("go", "build", "-o", out, "./testdata/wasmstub")
		cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "GOFLAGS=-buildvcs=false")
		if b, err := cmd.CombinedOutput(); err != nil {
			wasmErr = fmt.Errorf("编译 wasmstub 失败: %v\n%s", err, b)
			return
		}
		wasmFile = out
	})
	if wasmErr != nil {
		t.Skipf("跳过 WASM 测试：%v", wasmErr)
	}
	return wasmFile
}

func newWasmReq(t *testing.T, turn string, ctx map[string]string, outMax int) Request {
	t.Helper()
	src := buildWasmStub(t)
	dir, err := os.MkdirTemp(".", "ph-wasm-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	bin, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.wasm"), bin, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "index.prompt"), []byte("来自视图内的文件"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := &pack.Manifest{
		PackID: "wpack", Version: "1.0.0", Profile: "chat/1", Kind: "content",
		Entry: "index.wasm", Runtime: "wasm",
		Requires: []string{"chat.user.nickname"}, OutputMax: outMax,
	}
	m.ApplyDefaults()
	return Request{
		Dir: dir, Manifest: m, TreeHash: "sha256-wasmtest",
		Turn: turn, Now: time.Date(2026, 10, 10, 21, 30, 0, 0, time.UTC),
		Context: ctx,
	}
}

func TestWasmRuntimeAndFilesystemSandbox(t *testing.T) {
	rt := NewWasmRuntime(Limits{WallTimeout: 20 * time.Second})
	defer rt.Close()

	res, err := rt.Render(newWasmReq(t, "ws-ok", map[string]string{"chat.user.nickname": "小兔"}, 8000))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"PACK=wpack@1.0.0",
		"NICK=小兔",
		"TURN=ws-ok",
		"PROBE=来自视图内的文件",
		"WRITE=blocked",
	} {
		if !strings.Contains(res.Body, want) {
			t.Fatalf("输出缺少 %q：\n%s", want, res.Body)
		}
	}
}

func TestWasmRuntimeInterruptsInfiniteLoop(t *testing.T) {
	rt := NewWasmRuntime(Limits{WallTimeout: time.Second})
	defer rt.Close()

	start := time.Now()
	_, err := rt.Render(newWasmReq(t, "ws-spin", map[string]string{"chat.user.nickname": "x"}, 8000))
	elapsed := time.Since(start)

	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("自旋必须被中断，得到 %v", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("中断用了 %s，太久了", elapsed)
	}
	t.Logf("死循环在 %s 被中断: %v", elapsed.Round(time.Millisecond), err)
}

func TestWasmRuntimeFailure(t *testing.T) {
	rt := NewWasmRuntime(Limits{WallTimeout: 20 * time.Second})
	defer rt.Close()

	_, err := rt.Render(newWasmReq(t, "ws-fail", map[string]string{"chat.user.nickname": "x"}, 8000))
	if err == nil || !strings.Contains(err.Error(), "故意失败") {
		t.Fatalf("应把 stderr 作为错误信息，得到 %v", err)
	}
}

func TestWasmRuntimeKeepsDiagnostics(t *testing.T) {
	rt := NewWasmRuntime(Limits{WallTimeout: 20 * time.Second})
	defer rt.Close()

	res, err := rt.Render(newWasmReq(t, "ws-warn", map[string]string{"chat.user.nickname": "x"}, 8000))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "运行时诊断") {
		t.Fatalf("应保留运行时诊断，得到 %v", res.Warnings)
	}
}

func TestWasmRuntimeTruncates(t *testing.T) {
	rt := NewWasmRuntime(Limits{WallTimeout: 20 * time.Second})
	defer rt.Close()

	res, err := rt.Render(newWasmReq(t, "ws-flood", map[string]string{"chat.user.nickname": "x"}, 64))
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

func TestWasmRuntimeEgressDenied(t *testing.T) {
	rt := NewWasmRuntime(Limits{WallTimeout: 20 * time.Second})
	defer rt.Close()

	req := newWasmReq(t, "ws-ok", map[string]string{"chat.user.nickname": "x"}, 8000)
	req.Manifest.Egress = []string{"api.example.com"}

	_, err := rt.Render(req)
	var denied *EgressDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("应为 EgressDeniedError，得到 %T: %v", err, err)
	}
}

func TestMemoryPages(t *testing.T) {
	if got := MemoryPages(0); got != 0 {
		t.Fatalf("未设上限应为 0，得到 %d", got)
	}
	if got := MemoryPages(8 << 20); got != 0 {
		t.Fatalf("低于下限应为 0，得到 %d", got)
	}
	if got := MemoryPages(256 << 20); got != 4096 {
		t.Fatalf("256MB 应为 4096 页，得到 %d", got)
	}
}

func TestWasmWarmRenderCost(t *testing.T) {
	rt := NewWasmRuntime(Limits{WallTimeout: 30 * time.Second})
	defer rt.Close()

	req := newWasmReq(t, "ws-ok", map[string]string{"chat.user.nickname": "小兔"}, 8000)

	start := time.Now()
	if _, err := rt.Render(req); err != nil {
		t.Fatal(err)
	}
	cold := time.Since(start)

	start = time.Now()
	if _, err := rt.Render(req); err != nil {
		t.Fatal(err)
	}
	warm := time.Since(start)

	t.Logf("wasm 渲染：首次（含编译）%s，热渲染（缓存命中，实例新建）%s，快 %.0f 倍",
		cold.Round(time.Millisecond), warm.Round(time.Millisecond),
		float64(cold)/float64(warm))

	if warm*4 > cold {
		t.Fatalf("热渲染 %s 相对首次（含编译）%s 没有明显变快，编译缓存似乎没生效",
			warm.Round(time.Millisecond), cold.Round(time.Millisecond))
	}
}
