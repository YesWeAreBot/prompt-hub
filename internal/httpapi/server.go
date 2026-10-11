package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yeswearebot/prompt-hub/internal/ledger"
	"github.com/yeswearebot/prompt-hub/internal/pack"
	"github.com/yeswearebot/prompt-hub/internal/profile"
	"github.com/yeswearebot/prompt-hub/internal/render"
	"github.com/yeswearebot/prompt-hub/internal/store"
)

type Config struct {
	Resolver *store.Resolver
	Runtimes *render.Registry
	Ledger   *ledger.Ledger
	Token    string
	Log      *slog.Logger

	AllowedEgress []string

	Tiers []TierDesc

	Profiles *profile.Registry

	ContextEndpoint string
	ContextToken    string
	ContextTimeout  time.Duration

	AllowNoToken bool
}

type Server struct {
	cfg Config

	sessions *sessionStore
}

func New(cfg Config) *Server {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Server{cfg: cfg, sessions: newSessionStore()}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", s.handleHealth)
	mux.HandleFunc("/v1/render", s.handleRender)
	mux.HandleFunc("/v1/ledger", s.handleLedger)
	mux.HandleFunc("/v1/packs", s.handlePacks)
	mux.HandleFunc("/v1/renderers", s.handleRenderers)
	mux.HandleFunc("/v1/validate", s.handleValidate)
	mux.HandleFunc("/v1/packs/{id}/{version}/describe", s.handleDescribe)
	mux.HandleFunc("/v1/initialize", s.handleInitialize)
	mux.HandleFunc("/v1/profiles", s.handleProfiles)
	mux.HandleFunc("/v1/profiles/{name}/{version}", s.handleProfile)
	return s.guard(mux)
}

func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		if r.Header.Get("Origin") != "" {
			s.writeError(w, http.StatusForbidden, "origin-rejected", "本机 daemon 拒绝一切带 Origin 的请求", false, nil)
			return
		}
		if !s.cfg.AllowNoToken {
			tok := r.Header.Get("X-Ph-Token")
			if tok == "" {
				if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
					tok = strings.TrimPrefix(a, "Bearer ")
				}
			}
			if tok == "" || tok != s.cfg.Token {
				s.writeError(w, http.StatusUnauthorized, "unauthorized", "缺少或错误的 token", false, nil)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	all, _ := s.cfg.Resolver.All()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"packs":   len(all),
		"version": 0,
	})
}

func (s *Server) handlePacks(w http.ResponseWriter, r *http.Request) {
	all, err := s.cfg.Resolver.All()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "store", err.Error(), false, nil)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"packs": all})
}

func (s *Server) handleLedger(w http.ResponseWriter, r *http.Request) {
	n := 20
	if v := r.URL.Query().Get("n"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			n = parsed
		}
	}
	entries, err := s.cfg.Ledger.Tail(n)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "ledger", err.Error(), false, nil)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"entries": entries})
}

func (s *Server) handleRender(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	q := r.URL.Query()

	rawRef := q.Get("pack")
	ref, err := pack.ParseRef(rawRef)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "bad-ref",
			fmt.Sprintf("pack 参数不合法: %v", err), false, nil)
		return
	}
	turn := q.Get("turn")

	ctx := map[string]string{}
	for k, vs := range q {
		if !strings.HasPrefix(k, "ctx.") || len(vs) == 0 {
			continue
		}
		ctx[strings.TrimPrefix(k, "ctx.")] = vs[0]
	}

	resolved, err := s.cfg.Resolver.Resolve(ref.ID, ref.Version)
	if err != nil {
		var notInstalled *store.NotInstalledError
		if errors.As(err, &notInstalled) {
			s.writeError(w, http.StatusNotFound, "not-installed", err.Error(), false, nil)
			return
		}
		s.writeError(w, http.StatusInternalServerError, "store", err.Error(), false, nil)
		return
	}
	dir, treeHash := resolved.Dir, resolved.TreeHash

	loaded, err := pack.Load(dir)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "store-load", err.Error(), false, nil)
		return
	}

	if got := loaded.Tree.Hash(); got != treeHash {
		s.writeError(w, http.StatusInternalServerError, "integrity",
			fmt.Sprintf("store 内容与 tree hash 不符（目录 %s，实际 %s）", treeHash, got), false, nil)
		return
	}
	if loaded.Manifest.Kind != "content" {
		s.writeError(w, http.StatusBadRequest, "not-content",
			fmt.Sprintf("%s 是 %s pack，不能被 render（library pack 只能被 include）",
				ref.String(), loaded.Manifest.Kind), false, nil)
		return
	}

	if bad := render.CheckEgress(loaded.Manifest, s.cfg.AllowedEgress); len(bad) > 0 {
		msg := fmt.Sprintf("%s 声明了未授权的出网目标: %s（已授权: %s）",
			ref.String(), strings.Join(bad, ", "), strings.Join(s.cfg.AllowedEgress, ", "))
		s.cfg.Log.Warn("出网未授权", "pack", ref.String(), "hosts", bad)
		_ = s.cfg.Ledger.Append(ledger.Entry{
			Time: time.Now(), Pack: ref.ID, Version: ref.Version, TreeHash: treeHash,
			Turn: turn, Runtime: loaded.Manifest.Runtime, Status: "egress-denied", Degraded: true,
			Ms: time.Since(start).Milliseconds(), Error: msg,
		})
		s.writeError(w, http.StatusForbidden, "egress-denied", msg, false, nil)
		return
	}

	rep, profileKnown := s.profileReport(loaded.Manifest)
	if profileKnown && len(rep.Unknown) > 0 {
		s.cfg.Log.Warn("pack 依赖了 profile 里没有的字段（多半是拼错）",
			"pack", ref.String(), "profile", loaded.Manifest.Profile, "字段", rep.Unknown)
	}

	if missing := render.CheckRequires(loaded.Manifest, ctx); len(missing) > 0 {

		fetched, cerr := s.fetchContext(r.Context(), missing, r.Header.Get("X-Ph-App"))
		if cerr != nil {

			status, code := http.StatusBadGateway, "context-callback"
			if errors.Is(cerr, errAppNotRegistered) {
				status, code = http.StatusBadRequest, "app-not-registered"
			}
			s.cfg.Log.Warn("取 context 失败", "pack", ref.String(), "code", code, "err", cerr)
			_ = s.cfg.Ledger.Append(ledger.Entry{
				Time: time.Now(), Pack: ref.ID, Version: ref.Version, TreeHash: treeHash,
				Turn: turn, Runtime: loaded.Manifest.Runtime, Source: string(resolved.Source),
				Status: code, Degraded: true,
				Ms: time.Since(start).Milliseconds(), Error: cerr.Error(),
			})
			s.writeError(w, status, code, cerr.Error(), status != http.StatusBadRequest, nil)
			return
		}
		ctx = mergeContext(ctx, fetched)
	}

	deps, err := s.cfg.Resolver.ResolveClosure(loaded)
	if err != nil {
		s.cfg.Log.Warn("依赖解析失败", "pack", ref.String(), "err", err)
		_ = s.cfg.Ledger.Append(ledger.Entry{
			Time: time.Now(), Pack: ref.ID, Version: ref.Version, TreeHash: treeHash,
			Turn: turn, Runtime: loaded.Manifest.Runtime, Source: string(resolved.Source),
			Status: "dependency", Degraded: true,
			Ms: time.Since(start).Milliseconds(), Error: err.Error(),
		})
		s.writeError(w, http.StatusFailedDependency, "dependency", err.Error(), false, nil)
		return
	}

	rt, err := s.cfg.Runtimes.Get(loaded.Manifest.Runtime)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "runtime", err.Error(), false, nil)
		return
	}

	res, err := rt.Render(render.Request{
		Dir:      dir,
		Manifest: loaded.Manifest,
		TreeHash: treeHash,

		Tree:    loaded.Tree,
		Deps:    deps,
		Turn:    turn,
		Now:     time.Now(),
		Context: ctx,
	})
	if err != nil {
		var missing *render.MissingContextError
		if errors.As(err, &missing) {
			s.cfg.Log.Warn("渲染缺少必需 context 字段", "pack", ref.String(), "missing", missing.Fields)
			_ = s.cfg.Ledger.Append(ledger.Entry{
				Time: time.Now(), Pack: ref.ID, Version: ref.Version, TreeHash: treeHash,
				Turn: turn, Runtime: rt.Name(), Status: "missing-context", Degraded: true,
				Ms: time.Since(start).Milliseconds(), Error: err.Error(),
			})
			s.writeError(w, http.StatusUnprocessableEntity, "missing-context", err.Error(), false, missing.Fields)
			return
		}
		var denied *render.EgressDeniedError
		if errors.As(err, &denied) {
			_ = s.cfg.Ledger.Append(ledger.Entry{
				Time: time.Now(), Pack: ref.ID, Version: ref.Version, TreeHash: treeHash,
				Turn: turn, Runtime: rt.Name(), Status: "egress-denied", Degraded: true,
				Ms: time.Since(start).Milliseconds(), Error: err.Error(),
			})
			s.writeError(w, http.StatusForbidden, "egress-denied", err.Error(), false, nil)
			return
		}
		_ = s.cfg.Ledger.Append(ledger.Entry{
			Time: time.Now(), Pack: ref.ID, Version: ref.Version, TreeHash: treeHash,
			Turn: turn, Runtime: rt.Name(), Status: "error", Degraded: true,
			Ms: time.Since(start).Milliseconds(), Error: err.Error(),
		})
		s.writeError(w, http.StatusInternalServerError, "render", err.Error(), false, nil)
		return
	}

	bodyHash := sha256.Sum256([]byte(res.Body))
	etag := `"sha256:` + hashStrings(treeHash, res.Body) + `"`
	elapsed := time.Since(start)

	h := w.Header()
	h.Set("ETag", etag)
	h.Set("X-Pack-Revs", ref.String())
	h.Set("X-Tree-Hash", treeHash)

	h.Set("X-Ph-Source", string(resolved.Source))
	h.Set("X-Render-Ms", strconv.FormatInt(elapsed.Milliseconds(), 10))
	if len(res.Warnings) > 0 {
		h.Set("X-Ph-Warnings", strings.Join(res.Warnings, " | "))
	}

	status := "ok"
	degraded := false
	if len(res.Warnings) > 0 {
		status, degraded = "truncated", true
	}

	if match := r.Header.Get("If-None-Match"); match != "" && match == etag {
		_ = s.cfg.Ledger.Append(ledger.Entry{
			Time: time.Now(), Pack: ref.ID, Version: ref.Version, TreeHash: treeHash,
			Turn: turn, Runtime: rt.Name(), Source: string(resolved.Source),
			Placement: string(rep.Placement), MaxPrivacy: rep.MaxPrivacy,
			Status: status, Degraded: degraded,
			BodyHash: hex.EncodeToString(bodyHash[:]), ETag: etag, NotMod: true,
			Ms: elapsed.Milliseconds(), Warnings: res.Warnings,
		})
		w.WriteHeader(http.StatusNotModified)
		return
	}

	h.Set("Content-Type", "text/plain; charset=utf-8")
	_ = s.cfg.Ledger.Append(ledger.Entry{
		Time: time.Now(), Pack: ref.ID, Version: ref.Version, TreeHash: treeHash,
		Turn: turn, Runtime: rt.Name(), Source: string(resolved.Source),
		Placement: string(rep.Placement), MaxPrivacy: rep.MaxPrivacy,
		Status: status, Degraded: degraded,
		BodyHash: hex.EncodeToString(bodyHash[:]), ETag: etag,
		Ms: elapsed.Milliseconds(), Warnings: res.Warnings,
	})
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(res.Body))
}

func hashStrings(parts ...string) string {
	h := sha256.New()
	for i, p := range parts {
		if i > 0 {
			h.Write([]byte{'\n'})
		}
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

type errorBody struct {
	Code      string   `json:"code"`
	Error     string   `json:"error"`
	Partial   bool     `json:"partial"`
	Retryable bool     `json:"retryable"`
	Missing   []string `json:"missing,omitempty"`
}

func (s *Server) writeError(w http.ResponseWriter, status int, code, msg string, retryable bool, missing []string) {
	if len(missing) > 1 {
		sort.Strings(missing)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorBody{
		Code: code, Error: msg, Partial: false, Retryable: retryable, Missing: missing,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
