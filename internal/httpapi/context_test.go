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
	"time"

	"github.com/yeswearebot/prompt-hub/internal/ledger"
	"github.com/yeswearebot/prompt-hub/internal/render"
	"github.com/yeswearebot/prompt-hub/internal/store"
)

func fakeHost(t *testing.T, values map[string]string, status int, wantToken string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wantToken != "" && r.Header.Get("Authorization") != "Bearer "+wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		var req struct {
			Fields []string `json:"fields"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		out := map[string]string{}
		for _, f := range req.Fields {
			if v, ok := values[f]; ok {
				out[f] = v
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"values": out})
	}))
	t.Cleanup(ts.Close)
	return ts
}

func newContextServer(t *testing.T, root string, st *store.Store, endpoint, token string) *httptest.Server {
	t.Helper()
	reg := render.NewRegistry()
	reg.Register(render.TemplateRuntime{})
	lg, err := ledger.Open(filepath.Join(root, "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Config{
		Resolver: &store.Resolver{Store: st}, Runtimes: reg, Ledger: lg, Token: testToken,
		ContextEndpoint: endpoint, ContextToken: token, ContextTimeout: 2 * time.Second,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func setupNekochan(t *testing.T) (string, *store.Store) {
	t.Helper()
	root, st := freshStore(t)
	installPack(t, st, filepath.Join("..", "..", "examples", "nekochan"))
	return root, st
}

func TestPullContextFillsMissingFields(t *testing.T) {
	root, st := setupNekochan(t)
	host := fakeHost(t, map[string]string{
		"chat.user.nickname": "小兔",
		"chat.channel.name":  "辩论队吹水群",
	}, 0, "host-secret")
	ts := newContextServer(t, root, st, host.URL, "host-secret")

	resp := do(t, ts, "/v1/render?pack="+exampleRef("nekochan")+"&turn=t&ctx.chat.user.nickname=%E5%B0%8F%E5%85%94", authed())
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("状态 %d: %s", resp.StatusCode, b)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "辩论队吹水群") {
		t.Fatalf("回调取到的字段没进渲染: %s", body)
	}
	if !strings.Contains(string(body), "小兔") {
		t.Fatalf("inline 字段丢了: %s", body)
	}
}

func TestInlineContextWinsOverCallback(t *testing.T) {
	root, st := setupNekochan(t)
	host := fakeHost(t, map[string]string{
		"chat.user.nickname": "回调给的",
		"chat.channel.name":  "群",
	}, 0, "")
	ts := newContextServer(t, root, st, host.URL, "")

	resp := do(t, ts, "/v1/render?pack="+exampleRef("nekochan")+"&turn=t&ctx.chat.user.nickname=%E5%B0%8F%E5%85%94", authed())
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "回调给的") {
		t.Fatalf("回调覆盖了 inline 值: %s", body)
	}
	if !strings.Contains(string(body), "小兔") {
		t.Fatalf("inline 值应生效: %s", body)
	}
}

func TestContextEndpointMustBeLoopback(t *testing.T) {
	root, st := setupNekochan(t)
	ts := newContextServer(t, root, st, "http://10.1.2.3:9999/ctx", "")

	resp := do(t, ts, "/v1/render?pack="+exampleRef("nekochan")+"&turn=t", authed())
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("状态 %d，期望 502", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "回环") {
		t.Fatalf("应说明只允许回环地址: %s", b)
	}
}

func TestContextCallbackFailureIsExplicit(t *testing.T) {
	root, st := setupNekochan(t)
	host := fakeHost(t, nil, http.StatusInternalServerError, "")
	ts := newContextServer(t, root, st, host.URL, "")

	resp := do(t, ts, "/v1/render?pack="+exampleRef("nekochan")+"&turn=t", authed())
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("状态 %d，期望 502", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "500") {
		t.Fatalf("应带上宿主的响应码: %s", b)
	}
}

func TestNoCallbackKeepsContractError(t *testing.T) {
	root, st := setupNekochan(t)
	ts := newContextServer(t, root, st, "", "")

	resp := do(t, ts, "/v1/render?pack="+exampleRef("nekochan")+"&turn=t", authed())
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("状态 %d，期望 422", resp.StatusCode)
	}
}

func TestPullContextOnlyAsksForMissingFields(t *testing.T) {
	root, st := setupNekochan(t)

	var asked []string
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Fields []string `json:"fields"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		asked = append(asked, req.Fields...)
		_ = json.NewEncoder(w).Encode(map[string]any{"values": map[string]string{
			"chat.channel.name": "群",
		}})
	}))
	t.Cleanup(host.Close)

	ts := newContextServer(t, root, st, host.URL, "")
	resp := do(t, ts, "/v1/render?pack="+exampleRef("nekochan")+"&turn=t&ctx.chat.user.nickname=%E5%B0%8F%E5%85%94", authed())
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("状态 %d: %s", resp.StatusCode, b)
	}
	if len(asked) != 1 || asked[0] != "chat.channel.name" {
		t.Fatalf("只该索取缺的字段，实际问了 %v", asked)
	}
}

var _ = os.Remove
