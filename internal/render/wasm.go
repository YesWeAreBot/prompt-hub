package render

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

const wasmMinMemoryBytes = 16 << 20

const wasmPageSize = 64 << 10

type WasmRuntime struct {
	limits   Limits
	egress   []string
	viewRoot string

	mu    sync.Mutex
	rt    wazero.Runtime
	cache map[string]wazero.CompiledModule
}

type wasmView struct{ root string }

func (v wasmView) Build(req Request) (string, func(), error) {
	abs, err := filepath.Abs(req.Dir)
	if err != nil {
		return "", nil, err
	}
	if len(req.Deps) == 0 {
		return abs, func() {}, nil
	}
	if v.root == "" {
		return "", nil, fmt.Errorf("未配置视图根目录")
	}
	root, err := filepath.Abs(v.root)
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp(root, "view-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if err := writeTree(dir, req.Tree); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := MaterializeVendor(dir, req.Deps); err != nil {
		cleanup()
		return "", nil, err
	}
	return dir, cleanup, nil
}

func NewWasmRuntime(l Limits) *WasmRuntime {
	return &WasmRuntime{limits: l.Normalized(), cache: map[string]wazero.CompiledModule{}}
}

func (w *WasmRuntime) Name() string { return "wasm" }

func (w *WasmRuntime) WithEgress(hosts []string) *WasmRuntime {
	w.egress = hosts
	return w
}

func (w *WasmRuntime) WithViewRoot(dir string) *WasmRuntime {
	w.viewRoot = dir
	return w
}

func (w *WasmRuntime) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.rt == nil {
		return nil
	}
	err := w.rt.Close(context.Background())
	w.rt = nil
	w.cache = map[string]wazero.CompiledModule{}
	return err
}

func MemoryPages(bytes int64) uint32 {
	if bytes < wasmMinMemoryBytes {
		return 0
	}
	return uint32(bytes / wasmPageSize)
}

func (w *WasmRuntime) runtime(ctx context.Context) (wazero.Runtime, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.rt != nil {
		return w.rt, nil
	}
	cfg := wazero.NewRuntimeConfig().WithCloseOnContextDone(true)
	if pages := MemoryPages(w.limits.MemoryBytes); pages > 0 {
		cfg = cfg.WithMemoryLimitPages(pages)
	}
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("实例化 WASI 失败: %w", err)
	}
	w.rt = rt
	return rt, nil
}

func (w *WasmRuntime) compiledModule(ctx context.Context, rt wazero.Runtime, key string, wasm []byte) (wazero.CompiledModule, error) {
	w.mu.Lock()
	if m, ok := w.cache[key]; ok {
		w.mu.Unlock()
		return m, nil
	}
	w.mu.Unlock()

	m, err := rt.CompileModule(ctx, wasm)
	if err != nil {
		return nil, fmt.Errorf("编译 wasm 失败: %w", err)
	}
	w.mu.Lock()
	w.cache[key] = m
	w.mu.Unlock()
	return m, nil
}

func (w *WasmRuntime) Prewarm(dir, entry, treeHash string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(abs, filepath.FromSlash(entry)))
	if err != nil {
		return err
	}
	ctx := context.Background()
	rt, err := w.runtime(ctx)
	if err != nil {
		return err
	}
	_, err = w.compiledModule(ctx, rt, treeHash+"\x00"+entry, b)
	return err
}

func (w *WasmRuntime) Render(req Request) (Result, error) {
	if req.Manifest == nil {
		return Result{}, fmt.Errorf("缺少 manifest")
	}
	if missing := CheckRequires(req.Manifest, req.Context); len(missing) > 0 {
		return Result{}, &MissingContextError{Fields: missing}
	}
	if bad := CheckEgress(req.Manifest, w.egress); len(bad) > 0 {
		return Result{}, &EgressDeniedError{Hosts: bad, Allowed: w.egress}
	}

	viewRoot, cleanup, err := wasmView{root: w.viewRoot}.Build(req)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	wasmBytes, err := os.ReadFile(filepath.Join(viewRoot, filepath.FromSlash(req.Manifest.Entry)))
	if err != nil {
		return Result{}, fmt.Errorf("读取 wasm 入口 %s 失败: %w", req.Manifest.Entry, err)
	}

	envelope, err := json.Marshal(envelopeFor(req, viewRoot))
	if err != nil {
		return Result{}, err
	}

	outCap := req.Manifest.OutputMax
	if outCap <= 0 {
		outCap = 8000
	}
	stdout := &capWriter{max: outCap}
	stderr := &capWriter{max: maxStderr}

	warnings := []string{}
	if w.limits.MemoryBytes > 0 && MemoryPages(w.limits.MemoryBytes) == 0 {
		warnings = append(warnings, fmt.Sprintf(
			"内存上限 %dMB 低于 WASI 下限 %dMB，本次未对 wasm 设内存上限",
			w.limits.MemoryBytes>>20, wasmMinMemoryBytes>>20))
	}

	ctx, cancel := context.WithTimeout(context.Background(), w.limits.WallTimeout)
	defer cancel()

	rt, err := w.runtime(ctx)
	if err != nil {
		return Result{}, err
	}
	compiled, err := w.compiledModule(ctx, rt, req.TreeHash+"\x00"+req.Manifest.Entry, wasmBytes)
	if err != nil {
		return Result{}, err
	}

	cfg := wazero.NewModuleConfig().
		WithName("").
		WithStdin(bytes.NewReader(envelope)).
		WithStdout(stdout).
		WithStderr(stderr).
		WithFSConfig(wazero.NewFSConfig().WithReadOnlyDirMount(viewRoot, "/")).
		WithArgs(req.Manifest.PackID).
		WithEnv("PH_PROTOCOL", fmt.Sprintf("%d", ProtocolVersion)).
		WithEnv("PH_PACK", req.Manifest.PackID+"@"+req.Manifest.Version).
		WithEnv("PH_TREE_HASH", req.TreeHash).
		WithEnv("PH_TURN", req.Turn).
		WithEnv("PH_EGRESS_ALLOW", strings.Join(w.egress, ","))

	_, runErr := rt.InstantiateModule(ctx, compiled, cfg)

	stderrText := stderr.buf.String()
	if ctx.Err() != nil {

		detail := ""
		if s := lastNonEmptyLine(stderrText); s != "" {
			detail += "；最后诊断: " + s
		}
		if s := lastNonEmptyLine(stdout.buf.String()); s != "" {
			detail += "；最后输出: " + s
		}
		return Result{}, fmt.Errorf("运行时 wasm 超时（%s），模块已中断%s", w.limits.WallTimeout, detail)
	}
	if runErr != nil {
		msg := strings.TrimSpace(stderrText)
		if msg == "" {
			msg = runErr.Error()
		}
		return Result{}, fmt.Errorf("运行时 wasm 失败: %s", firstLine(msg))
	}

	res := Result{Body: stdout.buf.String(), Warnings: warnings}
	if stdout.over {
		res.Warnings = append(res.Warnings, fmt.Sprintf("输出超过 outputMax=%d，已截断", outCap))
	}
	if warn := strings.TrimSpace(stderrText); warn != "" {
		res.Warnings = append(res.Warnings, "运行时诊断: "+firstLine(warn))
	}
	return res, nil
}
