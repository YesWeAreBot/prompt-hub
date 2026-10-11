package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postInitialize(t *testing.T, ts *httptest.Server, body string) (*http.Response, initializeResponse) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/initialize", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Ph-Token", testToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out initializeResponse
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("解析 initialize 响应失败: %v\n%s", err, b)
		}
	}

	resp.Body = io.NopCloser(bytes.NewReader(b))
	return resp, out
}

func TestInitializeNegotiatesCapabilities(t *testing.T) {
	root, st := setupNekochan(t)
	ts := newContextServer(t, root, st, "", "")

	resp, out := postInitialize(t, ts, `{"protocolVersion":"1","app":"koishi-neko",
		"capabilities":["pull-context","validate","不存在的能力"]}`)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("状态 %d: %s", resp.StatusCode, b)
	}
	if out.ProtocolVersion != ProtocolVersion || out.App != "koishi-neko" {
		t.Fatalf("响应不对: %+v", out)
	}

	got := strings.Join(out.Agreed, ",")
	if !strings.Contains(got, CapPullContext) || !strings.Contains(got, CapValidate) {
		t.Fatalf("agreed 缺少应有能力: %v", out.Agreed)
	}
	if strings.Contains(got, "不存在的能力") {
		t.Fatalf("agreed 不该包含应用单方面声明的能力: %v", out.Agreed)
	}

	if len(out.Capabilities) < len(out.Agreed) {
		t.Fatalf("Capabilities 应当是完整表: %+v", out)
	}
}

func TestInitializeRejectsVersionMismatch(t *testing.T) {
	root, st := setupNekochan(t)
	ts := newContextServer(t, root, st, "", "")

	resp, _ := postInitialize(t, ts, `{"protocolVersion":"99","app":"x"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("状态 %d，期望 409", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "99") || !strings.Contains(string(b), ProtocolVersion) {
		t.Fatalf("错误信息应同时给出两个版本: %s", b)
	}
}

func TestInitializeValidatesEndpoint(t *testing.T) {
	root, st := setupNekochan(t)
	ts := newContextServer(t, root, st, "", "")

	for _, ep := range []string{
		"http://10.1.2.3:9999/ctx",
		"ftp://127.0.0.1/ctx",
		"not a url at all",
	} {
		body := `{"protocolVersion":"1","app":"x","contextEndpoint":"` + ep + `"}`
		resp, _ := postInitialize(t, ts, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("端点 %q 应被拒，得到状态 %d", ep, resp.StatusCode)
		}
	}

	resp, _ := postInitialize(t, ts, `{"protocolVersion":"1","app":"  "}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("空 app 应被拒，得到状态 %d", resp.StatusCode)
	}

	resp2, _ := postInitialize(t, ts, `{"protocolVersion":"1","app":"猫娘插件"}`)
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("非 ASCII 应用名应被拒，得到 %d", resp2.StatusCode)
	}
	b, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(b), "ASCII") {
		t.Fatalf("错误信息应说明只允许 ASCII: %s", b)
	}

	for _, name := range []string{"my-bot", "koishi.neko", "app_1", "a:b"} {
		r, _ := postInitialize(t, ts, `{"protocolVersion":"1","app":"`+name+`"}`)
		if r.StatusCode != http.StatusOK {
			t.Errorf("应用名 %q 应当合法，得到 %d", name, r.StatusCode)
		}
	}
}

func TestTwoAppsUseTheirOwnContextEndpoints(t *testing.T) {
	root, st := setupNekochan(t)
	hostA := fakeHost(t, map[string]string{
		"chat.user.nickname": "甲", "chat.channel.name": "甲的群",
	}, 0, "token-a")
	hostB := fakeHost(t, map[string]string{
		"chat.user.nickname": "乙", "chat.channel.name": "乙的群",
	}, 0, "token-b")
	ts := newContextServer(t, root, st, "", "")

	for app, host := range map[string]string{"app-a": hostA.URL, "app-b": hostB.URL} {
		body := `{"protocolVersion":"1","app":"` + app + `","contextEndpoint":"` + host +
			`","contextToken":"token-` + app[len(app)-1:] + `"}`
		if resp, _ := postInitialize(t, ts, body); resp.StatusCode != http.StatusOK {
			t.Fatalf("注册 %s 失败: %d", app, resp.StatusCode)
		}
	}

	render := func(app string) string {
		resp := do(t, ts, "/v1/render?pack="+exampleRef("nekochan")+"&turn=t", map[string]string{
			"X-Ph-Token": testToken, "X-Ph-App": app,
		})
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s 渲染失败 %d: %s", app, resp.StatusCode, b)
		}
		return string(b)
	}

	if got := render("app-a"); !strings.Contains(got, "甲的群") || strings.Contains(got, "乙") {
		t.Fatalf("app-a 拿到了别人的上下文: %s", got)
	}
	if got := render("app-b"); !strings.Contains(got, "乙的群") || strings.Contains(got, "甲") {
		t.Fatalf("app-b 拿到了别人的上下文: %s", got)
	}
}

func TestUnknownAppDoesNotFallBack(t *testing.T) {
	root, st := setupNekochan(t)

	defaultHost := fakeHost(t, map[string]string{
		"chat.user.nickname": "默认来源", "chat.channel.name": "默认群",
	}, 0, "")
	ts := newContextServer(t, root, st, defaultHost.URL, "")

	resp := do(t, ts, "/v1/render?pack=nekochan@1.0.0&turn=t", map[string]string{
		"X-Ph-Token": testToken, "X-Ph-App": "从没注册过",
	})
	b, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("未注册的应用应得到 400，实测 %d: %s", resp.StatusCode, b)
	}
	if !strings.Contains(string(b), "未注册") {
		t.Fatalf("错误信息应说明未注册: %s", b)
	}
	if strings.Contains(string(b), "默认群") {
		t.Fatalf("未注册的应用拿到了默认来源的上下文: %s", b)
	}

	resp2 := do(t, ts, "/v1/render?pack=nekochan@1.0.0&turn=t", authed())
	b2, _ := io.ReadAll(resp2.Body)
	if resp2.StatusCode != http.StatusOK || !strings.Contains(string(b2), "默认群") {
		t.Fatalf("默认来源应仍然可用: %d %s", resp2.StatusCode, b2)
	}
}

func TestInitializeSessionCap(t *testing.T) {
	root, st := setupNekochan(t)
	ts := newContextServer(t, root, st, "", "")

	for i := 0; i < maxSessions; i++ {
		body := `{"protocolVersion":"1","app":"app-` + string(rune('a'+i%26)) + string(rune('a'+i/26)) + `"}`
		if resp, _ := postInitialize(t, ts, body); resp.StatusCode != http.StatusOK {
			t.Fatalf("第 %d 个应用注册失败: %d", i, resp.StatusCode)
		}
	}
	resp, _ := postInitialize(t, ts, `{"protocolVersion":"1","app":"over-the-limit"}`)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("超出上限应被拒，得到 %d", resp.StatusCode)
	}

	resp2, _ := postInitialize(t, ts, `{"protocolVersion":"1","app":"app-aa"}`)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("重注册已有应用应成功，得到 %d", resp2.StatusCode)
	}
}
