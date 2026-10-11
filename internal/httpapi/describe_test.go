package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeswearebot/prompt-hub/internal/ledger"
	"github.com/yeswearebot/prompt-hub/internal/pack"
	"github.com/yeswearebot/prompt-hub/internal/render"
	"github.com/yeswearebot/prompt-hub/internal/store"
)

func freshStore(t *testing.T) (string, *store.Store) {
	t.Helper()
	root, err := os.MkdirTemp(".", "ph-desc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root, store.New(filepath.Join(root, "store"))
}

func newDescribeServer(t *testing.T, root string, st *store.Store, reg *render.Registry, tiers []TierDesc) *httptest.Server {
	t.Helper()
	if reg == nil {
		reg = render.NewRegistry()
		reg.Register(render.TemplateRuntime{})
	}
	lg, err := ledger.Open(filepath.Join(root, "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Config{
		Resolver: &store.Resolver{Store: st}, Runtimes: reg, Ledger: lg,
		Token: testToken, Tiers: tiers,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func getJSON(t *testing.T, ts *httptest.Server, url string, out any) int {
	t.Helper()
	resp := do(t, ts, url, authed())
	b, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("解析 %s 的响应失败: %v\n%s", url, err, b)
		}
	}
	return resp.StatusCode
}

func TestRenderersListsTiers(t *testing.T) {
	root, st := freshStore(t)
	ts := newDescribeServer(t, root, st, nil, []TierDesc{
		{Name: "template", Available: true, Detail: "内置"},
		{Name: "php", Available: false, Hint: "ph runtime enable php"},
	})

	var got struct {
		Tiers []TierDesc `json:"tiers"`
	}
	if code := getJSON(t, ts, "/v1/renderers", &got); code != http.StatusOK {
		t.Fatalf("状态 %d", code)
	}
	if len(got.Tiers) != 2 {
		t.Fatalf("档位数 %d，期望 2", len(got.Tiers))
	}
	if !got.Tiers[0].Available || got.Tiers[1].Available {
		t.Fatalf("可用性不对: %+v", got.Tiers)
	}

	if !strings.Contains(got.Tiers[1].Hint, "ph runtime enable php") {
		t.Fatalf("缺启用提示: %+v", got.Tiers[1])
	}
}

func TestDescribeReportsContract(t *testing.T) {
	root, st := freshStore(t)
	installPack(t, st, filepath.Join("..", "..", "examples", "nekochan"))
	ts := newDescribeServer(t, root, st, nil, nil)

	var d Describe
	if code := getJSON(t, ts, "/v1/packs/"+exampleRefPath("nekochan")+"/describe", &d); code != http.StatusOK {
		t.Fatalf("状态 %d", code)
	}
	if d.Ref != "nekochan@1.0.0" || d.Runtime != "template" || d.Source != "store" {
		t.Fatalf("基本信息不对: %+v", d)
	}
	if len(d.Requires) != 2 || d.OutputMax == 0 {
		t.Fatalf("契约信息不对: %+v", d)
	}
	if !d.RuntimeAvailable {
		t.Fatalf("template 档应当可用: %+v", d)
	}
	if !d.EgressAllowed {
		t.Fatalf("没有 egress 声明就应当视为已授权: %+v", d)
	}

	if d.ContextChecked {
		t.Fatalf("describe 不该做 context 校验: %+v", d)
	}
}

func TestValidateNeverExecutesPack(t *testing.T) {
	root, st := freshStore(t)
	installPack(t, st, writePackDir(t, filepath.Join(root, "src-boom"),
		"packId: boom\nversion: 1.0.0\nentry: i.prompt\nruntime: boom\nrequires:\n  - chat.user.nickname\n",
		map[string]string{"i.prompt": "x"}))

	called := false
	reg := render.NewRegistry()
	reg.Register(failingRuntime{called: &called})
	ts := newDescribeServer(t, root, st, reg, nil)

	var d Describe
	if code := getJSON(t, ts, "/v1/validate?pack=boom@1.0.0", &d); code != http.StatusOK {
		t.Fatalf("状态 %d", code)
	}
	if called {
		t.Fatal("validate 执行了 pack 的代码 —— 这会让它无法用于不可信 pack")
	}
	if !d.RuntimeAvailable {
		t.Fatalf("boom 档已注册，应报可用: %+v", d)
	}
}

func TestValidateWithContext(t *testing.T) {
	root, st := freshStore(t)
	installPack(t, st, filepath.Join("..", "..", "examples", "nekochan"))
	ts := newDescribeServer(t, root, st, nil, nil)

	var partial Describe
	if code := getJSON(t, ts, "/v1/validate?pack="+exampleRef("nekochan")+"&ctx.chat.user.nickname=%E5%B0%8F%E5%85%94", &partial); code != http.StatusOK {
		t.Fatalf("状态 %d", code)
	}
	if !partial.ContextChecked || partial.Satisfied {
		t.Fatalf("应当判定为不满足: %+v", partial)
	}
	if len(partial.Missing) != 1 || partial.Missing[0] != "chat.channel.name" {
		t.Fatalf("缺失字段不对: %+v", partial.Missing)
	}

	var full Describe
	if code := getJSON(t, ts, "/v1/validate?pack="+exampleRef("nekochan")+
		"&ctx.chat.user.nickname=%E5%B0%8F%E5%85%94"+
		"&ctx.chat.channel.name=%E8%BE%A9%E8%AE%BA%E9%98%9F", &full); code != http.StatusOK {
		t.Fatalf("状态 %d", code)
	}
	if !full.Satisfied || len(full.Missing) != 0 {
		t.Fatalf("应当判定为满足: %+v", full)
	}
}

func TestDescribeReportsUnavailableRuntime(t *testing.T) {
	root, st := freshStore(t)
	installPack(t, st, writePackDir(t, filepath.Join(root, "src-future"),
		"packId: future\nversion: 1.0.0\nentry: i.prompt\nruntime: future-tier\n",
		map[string]string{"i.prompt": "x"}))
	ts := newDescribeServer(t, root, st, nil, nil)

	var d Describe
	if code := getJSON(t, ts, "/v1/packs/future/1.0.0/describe", &d); code != http.StatusOK {
		t.Fatalf("状态 %d", code)
	}
	if d.RuntimeAvailable {
		t.Fatalf("未注册的档位不该报可用: %+v", d)
	}
	if !strings.Contains(d.RuntimeHint, "未启用") {
		t.Fatalf("应说明未启用并给出启用方式: %+v", d.RuntimeHint)
	}
}

func TestDescribeReportsDependencyProblem(t *testing.T) {
	root, st := freshStore(t)
	installPack(t, st, writePackDir(t, filepath.Join(root, "src-orphan"),
		"packId: orphan\nversion: 1.0.0\nentry: i.prompt\nruntime: template\nincludes:\n  - ghost@1.0.0\n",
		map[string]string{"i.prompt": "x"}))
	ts := newDescribeServer(t, root, st, nil, nil)

	var d Describe
	if code := getJSON(t, ts, "/v1/packs/orphan/1.0.0/describe", &d); code != http.StatusOK {
		t.Fatalf("状态 %d", code)
	}
	if !strings.Contains(d.DepsError, "ghost@1.0.0") {
		t.Fatalf("应报告依赖问题: %+v", d.DepsError)
	}
}

func TestDescribeRejectsLibraryPack(t *testing.T) {
	root, st := freshStore(t)
	installPack(t, st, filepath.Join("..", "..", "examples", "cats-role"))
	ts := newDescribeServer(t, root, st, nil, nil)

	resp := do(t, ts, "/v1/packs/"+exampleRefPath("cats-role")+"/describe", authed())
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("状态 %d，期望 400", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "library") {
		t.Fatalf("应说明 library pack 不能被 render/validate: %s", b)
	}
}

type failingRuntime struct{ called *bool }

func (f failingRuntime) Name() string { return "boom" }

func (f failingRuntime) Render(render.Request) (render.Result, error) {
	*f.called = true
	return render.Result{}, errors.New("不该被执行")
}

var _ = pack.ManifestName
