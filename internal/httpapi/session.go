package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const ProtocolVersion = "1"

const DaemonVersion = "prompt-hub/0.1.0"

const (
	CapPullContext = "pull-context"
	CapValidate    = "validate"
	CapDescribe    = "describe"
	CapDeps        = "dependencies"
	CapEmbedLayer  = "embed-layer"
	CapExactPin    = "exact-version-pin"
)

func daemonCapabilities() []string {
	return []string{CapPullContext, CapValidate, CapDescribe, CapDeps, CapEmbedLayer, CapExactPin}
}

const maxSessions = 64

var appNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type session struct {
	endpoint string
	token    string
	seen     time.Time
}

type sessionStore struct {
	mu sync.Mutex
	m  map[string]session
}

func newSessionStore() *sessionStore { return &sessionStore{m: map[string]session{}} }

func (s *sessionStore) put(app, endpoint, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.m[app]; !exists && len(s.m) >= maxSessions {
		return fmt.Errorf("在册应用数已达上限 %d；重启 daemon 可清空", maxSessions)
	}
	s.m[app] = session{endpoint: endpoint, token: token, seen: time.Now()}
	return nil
}

func (s *sessionStore) get(app string) (session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[app]
	if ok {
		sess.seen = time.Now()
		s.m[app] = sess
	}
	return sess, ok
}

func (s *sessionStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}

type initializeRequest struct {
	ProtocolVersion string   `json:"protocolVersion"`
	App             string   `json:"app"`
	ContextEndpoint string   `json:"contextEndpoint"`
	ContextToken    string   `json:"contextToken"`
	Capabilities    []string `json:"capabilities"`
}

type initializeResponse struct {
	ProtocolVersion string `json:"protocolVersion"`
	Daemon          string `json:"daemon"`
	App             string `json:"app"`

	Agreed       []string   `json:"agreed"`
	Capabilities []string   `json:"capabilities"`
	Tiers        []TierDesc `json:"tiers"`

	ContextConfigured bool `json:"contextConfigured"`
}

func (s *Server) handleInitialize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "initialize 只接受 POST")
		return
	}
	var req initializeRequest

	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体必须是 JSON: "+err.Error())
		return
	}
	if req.ProtocolVersion != ProtocolVersion {
		writeErr(w, http.StatusConflict, fmt.Sprintf(
			"协议版本不匹配：本机是 %s，应用声明 %q", ProtocolVersion, req.ProtocolVersion))
		return
	}
	if strings.TrimSpace(req.App) == "" {
		writeErr(w, http.StatusBadRequest, "app 不能为空（多应用场景靠它区分上下文来源）")
		return
	}
	if !appNameRe.MatchString(req.App) {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf(
			"app 只能是 ASCII 标识符（字母数字与 . _ : -，最长 128），收到 %q。"+
				"它会作为 X-Ph-App 请求头发回，非 ASCII 放不进 HTTP 头", req.App))
		return
	}

	if req.ContextEndpoint != "" {
		u, err := url.Parse(req.ContextEndpoint)
		if err != nil || !isLoopbackHost(u.Hostname()) ||
			(u.Scheme != "http" && u.Scheme != "https") {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf(
				"contextEndpoint 必须是回环 http/https 地址，收到 %q", req.ContextEndpoint))
			return
		}
	}
	if err := s.sessions.put(req.App, req.ContextEndpoint, req.ContextToken); err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	caps := daemonCapabilities()
	writeJSON(w, http.StatusOK, initializeResponse{
		ProtocolVersion:   ProtocolVersion,
		Daemon:            DaemonVersion,
		App:               req.App,
		Agreed:            intersectCaps(req.Capabilities, caps),
		Capabilities:      caps,
		Tiers:             s.cfg.Tiers,
		ContextConfigured: req.ContextEndpoint != "" || s.cfg.ContextEndpoint != "",
	})
}

var errAppNotRegistered = errors.New("app 未注册")

func (s *Server) contextSourceFor(app string) (endpoint, token string, err error) {
	if app == "" {
		return s.cfg.ContextEndpoint, s.cfg.ContextToken, nil
	}
	sess, ok := s.sessions.get(app)
	if !ok {
		return "", "", fmt.Errorf("%w：%q 没有 POST /v1/initialize 过。"+
			"这里不回退到默认上下文来源，否则会把别的应用的上下文灌进这个 prompt",
			errAppNotRegistered, app)
	}
	return sess.endpoint, sess.token, nil
}

func intersectCaps(want, have []string) []string {
	set := map[string]bool{}
	for _, c := range want {
		set[c] = true
	}
	out := []string{}
	for _, c := range have {
		if set[c] {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}
