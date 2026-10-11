package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeswearebot/prompt-hub/internal/ledger"
	"github.com/yeswearebot/prompt-hub/internal/profile"
	"github.com/yeswearebot/prompt-hub/internal/render"
	"github.com/yeswearebot/prompt-hub/internal/store"
)

func newProfileServer(t *testing.T, root string, st *store.Store, withProfiles bool) *httptest.Server {
	t.Helper()
	reg := render.NewRegistry()
	reg.Register(render.TemplateRuntime{})
	lg, err := ledger.Open(filepath.Join(root, "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Resolver: &store.Resolver{Store: st}, Runtimes: reg, Ledger: lg, Token: testToken,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if withProfiles {
		pr, err := profile.NewRegistry()
		if err != nil {
			t.Fatal(err)
		}
		cfg.Profiles = pr
	}
	ts := httptest.NewServer(New(cfg).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestProfilesEndpointListsBuiltin(t *testing.T) {
	root, st := freshStore(t)
	ts := newProfileServer(t, root, st, true)

	var got struct {
		Profiles []struct {
			Profile string `json:"profile"`
			Fields  int    `json:"fields"`
		} `json:"profiles"`
	}
	if code := getJSON(t, ts, "/v1/profiles", &got); code != http.StatusOK {
		t.Fatalf("状态 %d", code)
	}
	if len(got.Profiles) != 1 || got.Profiles[0].Profile != "chat/1" {
		t.Fatalf("内建的 chat/1 应当可见: %+v", got.Profiles)
	}
	if got.Profiles[0].Fields < 14 {
		t.Fatalf("字段数太少: %+v", got.Profiles[0])
	}
}

func TestProfileEndpointReturnsTable(t *testing.T) {
	root, st := freshStore(t)
	ts := newProfileServer(t, root, st, true)

	var p profile.Profile
	if code := getJSON(t, ts, "/v1/profiles/chat/1", &p); code != http.StatusOK {
		t.Fatalf("状态 %d", code)
	}
	byName := map[string]profile.Field{}
	for _, f := range p.Fields {
		byName[f.Name] = f
	}

	if f := byName["chat.turn.user_text"]; f.Stability != profile.StabilityPerRequest || f.Privacy != profile.PrivacyPII {
		t.Fatalf("chat.turn.user_text 的元数据不对: %+v", f)
	}
	if f := byName["chat.channel.name"]; f.Stability != profile.StabilitySession {
		t.Fatalf("chat.channel.name 应当是 session: %+v", f)
	}
}

func TestProfileEndpointUnknownIsExplicit(t *testing.T) {
	root, st := freshStore(t)
	ts := newProfileServer(t, root, st, true)

	resp := do(t, ts, "/v1/profiles/chat/9", authed())
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("状态 %d，期望 404", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)

	if !strings.Contains(string(b), "chat/1") {
		t.Fatalf("错误信息应列出可用的 profile: %s", b)
	}
}

func TestDescribeCarriesPlacement(t *testing.T) {
	root, st := freshStore(t)
	installPack(t, st, writePackDir(t, filepath.Join(root, "src-session"),
		"packId: sess\nversion: 1.0.0\nentry: i.prompt\nprofile: chat/1\n"+
			"requires:\n  - chat.user.nickname\n  - chat.channel.name\n",
		map[string]string{"i.prompt": "x"}))
	installPack(t, st, writePackDir(t, filepath.Join(root, "src-perreq"),
		"packId: perreq\nversion: 1.0.0\nentry: i.prompt\nprofile: chat/1\n"+
			"requires:\n  - chat.clock.now\n  - chat.turn.user_text\n",
		map[string]string{"i.prompt": "x"}))
	ts := newProfileServer(t, root, st, true)

	var sess Describe
	if code := getJSON(t, ts, "/v1/packs/sess/1.0.0/describe", &sess); code != http.StatusOK {
		t.Fatalf("状态 %d", code)
	}
	if !sess.ProfileKnown {
		t.Fatalf("chat/1 已知: %+v", sess)
	}
	if sess.Placement != string(profile.PlacementFrozenSession) {
		t.Fatalf("只依赖 session 字段 → 应当按会话冻结，得到 %s", sess.Placement)
	}
	if sess.MaxPrivacy != profile.PrivacyInternal {
		t.Fatalf("隐私级别不对: %s", sess.MaxPrivacy)
	}
	if len(sess.FieldMeta) != 2 {
		t.Fatalf("每个已知字段都该带元数据: %+v", sess.FieldMeta)
	}

	var perreq Describe
	if code := getJSON(t, ts, "/v1/packs/perreq/1.0.0/describe", &perreq); code != http.StatusOK {
		t.Fatalf("状态 %d", code)
	}
	if perreq.Placement != string(profile.PlacementAppendOnly) {
		t.Fatalf("依赖 per-request → 必须 append-only，得到 %s", perreq.Placement)
	}

	if perreq.MaxPrivacy != profile.PrivacyPII {
		t.Fatalf("应当报出 pii: %s", perreq.MaxPrivacy)
	}
}

func TestDescribeReportsUnknownFields(t *testing.T) {
	root, st := freshStore(t)
	installPack(t, st, writePackDir(t, filepath.Join(root, "src-typo"),
		"packId: typo\nversion: 1.0.0\nentry: i.prompt\nprofile: chat/1\n"+
			"requires:\n  - chat.user.nickname\n  - chat.user.nicknamee\n",
		map[string]string{"i.prompt": "x"}))
	ts := newProfileServer(t, root, st, true)

	var d Describe
	if code := getJSON(t, ts, "/v1/packs/typo/1.0.0/describe", &d); code != http.StatusOK {
		t.Fatalf("状态 %d", code)
	}
	if len(d.UnknownFields) != 1 || d.UnknownFields[0] != "chat.user.nicknamee" {
		t.Fatalf("应点名未知字段: %+v", d.UnknownFields)
	}

	if d.Placement != string(profile.PlacementAppendOnly) {
		t.Fatalf("含未知字段时应当保守报 append-only，得到 %s", d.Placement)
	}
}

func TestDescribeWithoutProfileRegistry(t *testing.T) {
	root, st := freshStore(t)
	installPack(t, st, writePackDir(t, filepath.Join(root, "src-nop"),
		"packId: nop\nversion: 1.0.0\nentry: i.prompt\nprofile: chat/1\n"+
			"requires:\n  - chat.user.nickname\n",
		map[string]string{"i.prompt": "x"}))
	ts := newProfileServer(t, root, st, false)

	var d Describe
	if code := getJSON(t, ts, "/v1/packs/nop/1.0.0/describe", &d); code != http.StatusOK {
		t.Fatalf("状态 %d", code)
	}
	if d.ProfileKnown {
		t.Fatalf("没有装载 profile 时不该报 true: %+v", d)
	}
	if d.Placement != "" {
		t.Fatalf("不知道就不该给放置建议: %q", d.Placement)
	}
}

func TestLedgerRecordsPlacementAndPrivacy(t *testing.T) {
	root, st := freshStore(t)
	installPack(t, st, writePackDir(t, filepath.Join(root, "src-mem"),
		"packId: mem\nversion: 1.0.0\nentry: i.prompt\nprofile: chat/1\n"+
			"requires:\n  - chat.memory.recent\n",
		map[string]string{"i.prompt": "记忆片段"}))

	reg := render.NewRegistry()
	reg.Register(render.TemplateRuntime{})
	lg, err := ledger.Open(filepath.Join(root, "ledger2.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	pr, err := profile.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Config{
		Resolver: &store.Resolver{Store: st}, Runtimes: reg, Ledger: lg, Token: testToken,
		Profiles: pr, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp := do(t, ts, "/v1/render?pack=mem@1.0.0&turn=t&ctx.chat.memory.recent=%E8%AE%B0%E5%BF%86",
		authed())
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("状态 %d: %s", resp.StatusCode, b)
	}

	entries, err := lg.Tail(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("应当记了一条")
	}
	e := entries[len(entries)-1]
	if e.Placement != string(profile.PlacementFrozenSession) {
		t.Fatalf("ledger 里的放置建议不对: %q", e.Placement)
	}
	if e.MaxPrivacy != profile.PrivacyPII {
		t.Fatalf("ledger 应记下隐私级别: %q", e.MaxPrivacy)
	}
}

var _ = json.Marshal
var _ = os.Remove
