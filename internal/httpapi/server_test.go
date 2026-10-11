package httpapi

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/yeswearebot/prompt-hub/internal/ledger"
	"github.com/yeswearebot/prompt-hub/internal/pack"
	"github.com/yeswearebot/prompt-hub/internal/render"
	"github.com/yeswearebot/prompt-hub/internal/store"
)

const testToken = "test-token"

func newTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	root, err := os.MkdirTemp(".", "ph-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	st := store.New(filepath.Join(root, "store"))
	installExample(t, st, filepath.Join("..", "..", "examples", "nekochan"))

	lg, err := ledger.Open(filepath.Join(root, "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	reg := render.NewRegistry()
	reg.Register(render.TemplateRuntime{})

	srv := New(Config{
		Resolver: &store.Resolver{Store: st}, Runtimes: reg, Ledger: lg, Token: testToken,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

func installExample(t *testing.T, st *store.Store, path string) {
	t.Helper()
	loaded, err := pack.Load(path)
	if err != nil {
		t.Fatalf("加载 %s: %v", path, err)
	}
	if err := loaded.Manifest.Validate(); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if _, err := st.Install(loaded); err != nil {
		t.Fatalf("安装: %v", err)
	}
}

func do(t *testing.T, ts *httptest.Server, url string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func authed() map[string]string { return map[string]string{"X-Ph-Token": testToken} }

const nekoURL = "/v1/render?pack=nekochan@1.0.0&turn=ev_8812" +
	"&ctx.chat.user.nickname=%E5%B0%8F%E5%85%94" +
	"&ctx.chat.channel.name=%E8%BE%A9%E8%AE%BA%E9%98%9F%E5%90%B9%E6%B0%B4%E7%BE%A4"

func TestRenderAndConditional(t *testing.T) {
	ts, _ := newTestServer(t)

	resp := do(t, ts, nekoURL, authed())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("首次渲染状态 %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "小兔") || !strings.Contains(string(body), "辩论队吹水群") {
		t.Fatalf("渲染结果未包含 context 值:\n%s", body)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("缺少 ETag")
	}
	if got := resp.Header.Get("X-Pack-Revs"); got != "nekochan@1.0.0" {
		t.Fatalf("X-Pack-Revs = %q", got)
	}
	if got := resp.Header.Get("X-Tree-Hash"); !strings.HasPrefix(got, "sha256-") {
		t.Fatalf("X-Tree-Hash = %q", got)
	}

	cond := map[string]string{"X-Ph-Token": testToken, "If-None-Match": etag}
	resp2 := do(t, ts, nekoURL, cond)
	if resp2.StatusCode != http.StatusNotModified {
		t.Fatalf("条件请求状态 %d，期望 304", resp2.StatusCode)
	}
	if b, _ := io.ReadAll(resp2.Body); len(b) != 0 {
		t.Fatalf("304 不应有正文，收到 %q", b)
	}

	other := strings.Replace(nekoURL, "%E5%B0%8F%E5%85%94", "%E9%98%BF%E7%8C%AB", 1)
	resp3 := do(t, ts, other, cond)
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("内容变化后状态 %d，期望 200", resp3.StatusCode)
	}
	if resp3.Header.Get("ETag") == etag {
		t.Fatal("内容变化后 ETag 未变——会把变化误判成 304")
	}
}

func TestGuards(t *testing.T) {
	ts, _ := newTestServer(t)

	if got := do(t, ts, nekoURL, nil).StatusCode; got != http.StatusUnauthorized {
		t.Fatalf("无 token 状态 %d，期望 401", got)
	}
	if got := do(t, ts, nekoURL, map[string]string{"X-Ph-Token": "wrong"}).StatusCode; got != http.StatusUnauthorized {
		t.Fatalf("错 token 状态 %d，期望 401", got)
	}
	origin := map[string]string{"X-Ph-Token": testToken, "Origin": "https://evil.example"}
	if got := do(t, ts, nekoURL, origin).StatusCode; got != http.StatusForbidden {
		t.Fatalf("带 Origin 状态 %d，期望 403", got)
	}
}

func TestContractErrors(t *testing.T) {
	ts, _ := newTestServer(t)

	resp := do(t, ts, "/v1/render?pack=nekochan@1.0.0", authed())
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("缺 context 状态 %d，期望 422", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"chat.channel.name", "chat.user.nickname", "missing-context"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("错误体缺少 %q: %s", want, b)
		}
	}

	if got := do(t, ts, "/v1/render?pack=nekochan@%5E1.0", authed()).StatusCode; got != http.StatusBadRequest {
		t.Fatalf("范围版本状态 %d，期望 400", got)
	}

	if got := do(t, ts, "/v1/render?pack=nope@9.9.9", authed()).StatusCode; got != http.StatusNotFound {
		t.Fatalf("未安装状态 %d，期望 404", got)
	}
}

func TestLibraryCannotRender(t *testing.T) {
	ts, st := newTestServer(t)

	dir, err := os.MkdirTemp(".", "ph-lib-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	yaml := "packId: cats-role\nversion: 1.0.0\nprofile: chat/1\nkind: library\nexports:\n  - catsRole\n"
	if err := os.WriteFile(filepath.Join(dir, "pack.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := pack.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.Manifest.Validate(); err != nil {
		t.Fatalf("library manifest 应合法: %v", err)
	}
	if _, err := st.Install(loaded); err != nil {
		t.Fatal(err)
	}

	resp := do(t, ts, "/v1/render?pack=cats-role@1.0.0", authed())
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("library pack render 状态 %d，期望 400", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "not-content") {
		t.Fatalf("错误码应为 not-content: %s", b)
	}
}

func TestSameVersionDifferentContentRejected(t *testing.T) {
	root, err := os.MkdirTemp(".", "ph-conflict-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	st := store.New(filepath.Join(root, "store"))

	src, err := os.MkdirTemp(".", "ph-src-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(src) })
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(src, "pack.yaml"),
		[]byte("packId: t\nversion: 1.0.0\nentry: i.prompt\n"), 0o644))
	must(os.WriteFile(filepath.Join(src, "i.prompt"), []byte("第一版"), 0o644))

	first, err := pack.Load(src)
	must(err)
	if _, err := st.Install(first); err != nil {
		t.Fatalf("首次安装应成功: %v", err)
	}

	must(os.WriteFile(filepath.Join(src, "i.prompt"), []byte("第二版"), 0o644))
	second, err := pack.Load(src)
	must(err)
	_, err = st.Install(second)
	if err == nil {
		t.Fatal("同版本不同内容必须被拒绝")
	}
	var conflict *store.VersionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("错误类型应为 VersionConflictError，得到 %T: %v", err, err)
	}
}

type recordingRuntime struct{ t *testing.T }

func (r recordingRuntime) Name() string { return "recording" }

func (r recordingRuntime) Render(req render.Request) (render.Result, error) {
	if req.Manifest == nil {
		r.t.Error("Manifest 为空")
		return render.Result{}, errors.New("Manifest 为空")
	}
	if len(req.Tree) == 0 {
		r.t.Error("Tree 为空——外部运行时靠它构造渲染视图，漏传会让 php pack 找不到入口文件")
		return render.Result{}, errors.New("Tree 为空")
	}
	if _, ok := req.Tree[req.Manifest.Entry]; !ok {
		r.t.Errorf("Tree 中没有 entry %q", req.Manifest.Entry)
	}
	if req.TreeHash == "" {
		r.t.Error("TreeHash 为空")
	}
	return render.Result{Body: "recorded:" + req.Manifest.PackID + "@" + req.Manifest.Version}, nil
}

func TestHandlerPassesTreeToRuntime(t *testing.T) {
	root, err := os.MkdirTemp(".", "ph-wire-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	src, err := os.MkdirTemp(".", "ph-wiresrc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(src) })
	if err := os.WriteFile(filepath.Join(src, "pack.yaml"),
		[]byte("packId: wired\nversion: 1.0.0\nentry: i.prompt\nruntime: recording\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "i.prompt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := pack.Load(src)
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(filepath.Join(root, "store"))
	if _, err := st.Install(loaded); err != nil {
		t.Fatal(err)
	}

	reg := render.NewRegistry()
	reg.Register(recordingRuntime{t: t})
	reg.Register(render.TemplateRuntime{})
	lg, err := ledger.Open(filepath.Join(root, "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Config{
		Resolver: &store.Resolver{Store: st}, Runtimes: reg, Ledger: lg, Token: testToken,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp := do(t, ts, "/v1/render?pack=wired@1.0.0&turn=t", authed())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态 %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "recorded:wired@1.0.0") {
		t.Fatalf("正文 = %q", b)
	}
}

func writePackDir(t *testing.T, dir, manifestYaml string, files map[string]string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, pack.ManifestName), []byte(manifestYaml), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		abs := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

type depRecordingRuntime struct{ t *testing.T }

func (r depRecordingRuntime) Name() string { return "deprec" }

func (r depRecordingRuntime) Render(req render.Request) (render.Result, error) {
	ids := make([]string, 0, len(req.Deps))
	for id := range req.Deps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return render.Result{Body: "deps=" + strings.Join(ids, ",")}, nil
}

func newDepServer(t *testing.T, root string, st *store.Store) *httptest.Server {
	t.Helper()
	reg := render.NewRegistry()
	reg.Register(depRecordingRuntime{t: t})
	reg.Register(render.TemplateRuntime{})
	lg, err := ledger.Open(filepath.Join(root, "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Config{
		Resolver: &store.Resolver{Store: st}, Runtimes: reg, Ledger: lg, Token: testToken,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func exampleRef(name string) string {
	loaded, err := pack.Load(filepath.Join("..", "..", "examples", name))
	if err != nil {
		panic(err)
	}
	return loaded.Manifest.PackID + "@" + loaded.Manifest.Version
}

func exampleRefPath(name string) string {
	return strings.Replace(exampleRef(name), "@", "/", 1)
}

func installPack(t *testing.T, st *store.Store, src string) {
	t.Helper()
	loaded, err := pack.Load(src)
	if err != nil {
		t.Fatalf("加载 %s: %v", src, err)
	}
	if err := loaded.Manifest.Validate(); err != nil {
		t.Fatalf("%s 的 manifest 不合法: %v", src, err)
	}
	if _, err := st.Install(loaded); err != nil {
		t.Fatal(err)
	}
}

func TestDependenciesResolvedIncludingTransitive(t *testing.T) {
	root, err := os.MkdirTemp(".", "ph-deps-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	st := store.New(filepath.Join(root, "store"))

	installPack(t, st, writePackDir(t, filepath.Join(root, "src-base"),
		"packId: base\nversion: 1.0.0\nkind: library\nexports:\n  - b\n", map[string]string{"b.php": "<?php\n"}))
	installPack(t, st, writePackDir(t, filepath.Join(root, "src-mid"),
		"packId: mid\nversion: 1.0.0\nkind: library\nexports:\n  - m\nincludes:\n  - base@1.0.0\n",
		map[string]string{"m.php": "<?php\n"}))
	installPack(t, st, writePackDir(t, filepath.Join(root, "src-top"),
		"packId: top\nversion: 1.0.0\nentry: i.prompt\nruntime: deprec\nincludes:\n  - mid@1.0.0\n",
		map[string]string{"i.prompt": "x"}))

	ts := newDepServer(t, root, st)
	resp := do(t, ts, "/v1/render?pack=top@1.0.0&turn=t", authed())
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("状态 %d: %s", resp.StatusCode, b)
	}
	body, _ := io.ReadAll(resp.Body)

	if string(body) != "deps=base,mid" {
		t.Fatalf("运行时收到的依赖 = %q，期望 deps=base,mid", body)
	}
}

func TestMissingDependencyIsExplicit(t *testing.T) {
	root, err := os.MkdirTemp(".", "ph-depx-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	st := store.New(filepath.Join(root, "store"))

	installPack(t, st, writePackDir(t, filepath.Join(root, "src-top"),
		"packId: top\nversion: 1.0.0\nentry: i.prompt\nruntime: deprec\nincludes:\n  - ghost@1.0.0\n",
		map[string]string{"i.prompt": "x"}))

	ts := newDepServer(t, root, st)
	resp := do(t, ts, "/v1/render?pack=top@1.0.0&turn=t", authed())
	if resp.StatusCode != http.StatusFailedDependency {
		t.Fatalf("状态 %d，期望 424", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "ghost@1.0.0") {
		t.Fatalf("错误信息应点名缺哪个依赖: %s", b)
	}
}

func TestContentPackCannotBeIncluded(t *testing.T) {
	root, err := os.MkdirTemp(".", "ph-depc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	st := store.New(filepath.Join(root, "store"))

	installPack(t, st, writePackDir(t, filepath.Join(root, "src-other"),
		"packId: other\nversion: 1.0.0\nentry: i.prompt\n", map[string]string{"i.prompt": "y"}))
	installPack(t, st, writePackDir(t, filepath.Join(root, "src-top"),
		"packId: top\nversion: 1.0.0\nentry: i.prompt\nruntime: deprec\nincludes:\n  - other@1.0.0\n",
		map[string]string{"i.prompt": "x"}))

	ts := newDepServer(t, root, st)
	resp := do(t, ts, "/v1/render?pack=top@1.0.0&turn=t", authed())
	if resp.StatusCode != http.StatusFailedDependency {
		t.Fatalf("状态 %d，期望 424", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "library") {
		t.Fatalf("错误信息应说明只有 library pack 能被 include: %s", b)
	}
}
