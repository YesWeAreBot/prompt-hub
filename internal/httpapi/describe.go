package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/yeswearebot/prompt-hub/internal/pack"
	"github.com/yeswearebot/prompt-hub/internal/profile"
	"github.com/yeswearebot/prompt-hub/internal/render"
	"github.com/yeswearebot/prompt-hub/internal/store"
)

type TierDesc struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Detail    string `json:"detail,omitempty"`
	Hint      string `json:"hint,omitempty"`
}

type Describe struct {
	Ref      string `json:"ref"`
	TreeHash string `json:"treeHash"`
	Profile  string `json:"profile"`
	Kind     string `json:"kind"`
	Runtime  string `json:"runtime"`
	Entry    string `json:"entry"`

	Source string `json:"source"`

	Requires  []string `json:"requires,omitempty"`
	Egress    []string `json:"egress,omitempty"`
	Includes  []string `json:"includes,omitempty"`
	OutputMax int      `json:"outputMax"`

	RuntimeAvailable bool   `json:"runtimeAvailable"`
	RuntimeHint      string `json:"runtimeHint,omitempty"`

	EgressAllowed bool     `json:"egressAllowed"`
	EgressDenied  []string `json:"egressDenied,omitempty"`

	Deps      []string `json:"deps,omitempty"`
	DepsError string   `json:"depsError,omitempty"`

	ProfileKnown bool `json:"profileKnown"`

	FieldMeta []profile.Field `json:"fieldMeta,omitempty"`

	Placement string `json:"placement,omitempty"`

	MaxPrivacy string `json:"maxPrivacy,omitempty"`

	UnknownFields []string `json:"unknownFields,omitempty"`

	ContextChecked bool     `json:"contextChecked"`
	Satisfied      bool     `json:"satisfied"`
	Missing        []string `json:"missing,omitempty"`
}

func (s *Server) describePack(ref pack.Ref, resolved store.Resolved, loaded *pack.LoadResult) Describe {
	m := loaded.Manifest
	d := Describe{
		Ref: ref.String(), TreeHash: resolved.TreeHash, Profile: m.Profile,
		Kind: m.Kind, Runtime: m.Runtime, Entry: m.Entry, Source: string(resolved.Source),
		Requires: m.Requires, Egress: m.Egress, Includes: m.Includes, OutputMax: m.OutputMax,
	}

	if _, err := s.cfg.Runtimes.Get(m.Runtime); err != nil {

		d.RuntimeHint = err.Error()
	} else {
		d.RuntimeAvailable = true
	}

	if bad := render.CheckEgress(m, s.cfg.AllowedEgress); len(bad) > 0 {
		d.EgressDenied = bad
	} else {
		d.EgressAllowed = true
	}

	if rep, known := s.profileReport(m); known {
		d.ProfileKnown = true
		d.FieldMeta = rep.Fields
		d.Placement = string(rep.Placement)
		d.MaxPrivacy = rep.MaxPrivacy
		d.UnknownFields = rep.Unknown
	}

	deps, err := s.cfg.Resolver.ResolveClosure(loaded)
	if err != nil {
		d.DepsError = err.Error()
	} else {
		for id, tree := range deps {
			d.Deps = append(d.Deps, fmt.Sprintf("%s（%d 个文件）", id, len(tree)))
		}
		sort.Strings(d.Deps)
	}
	return d
}

func (s *Server) resolveFor(ref pack.Ref) (store.Resolved, *pack.LoadResult, int, error) {
	resolved, err := s.cfg.Resolver.Resolve(ref.ID, ref.Version)
	if err != nil {
		var notInstalled *store.NotInstalledError
		if errors.As(err, &notInstalled) {
			return store.Resolved{}, nil, http.StatusNotFound, err
		}
		return store.Resolved{}, nil, http.StatusInternalServerError, err
	}
	loaded, err := pack.Load(resolved.Dir)
	if err != nil {
		return store.Resolved{}, nil, http.StatusInternalServerError, err
	}
	if loaded.Manifest.Kind != "content" {
		return store.Resolved{}, nil, http.StatusBadRequest, fmt.Errorf(
			"%s 是 %s pack，不能被 render 或 validate（library pack 只能被 include）",
			ref, loaded.Manifest.Kind)
	}
	return resolved, loaded, http.StatusOK, nil
}

func (s *Server) handleRenderers(w http.ResponseWriter, r *http.Request) {
	tiers := s.cfg.Tiers
	if tiers == nil {

		for _, name := range s.cfg.Runtimes.Tiers() {
			tiers = append(tiers, TierDesc{Name: name, Available: true})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tiers": tiers})
}

func (s *Server) handleDescribe(w http.ResponseWriter, r *http.Request) {
	ref, err := pack.ParseRef(r.PathValue("id") + "@" + r.PathValue("version"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	resolved, loaded, status, err := s.resolveFor(ref)
	if err != nil {
		writeErr(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.describePack(ref, resolved, loaded))
}

func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ref, err := pack.ParseRef(q.Get("pack"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("pack 参数不合法: %v", err))
		return
	}
	resolved, loaded, status, err := s.resolveFor(ref)
	if err != nil {
		writeErr(w, status, err.Error())
		return
	}

	d := s.describePack(ref, resolved, loaded)

	gave := false
	ctx := map[string]string{}
	for k, vs := range q {
		if !strings.HasPrefix(k, "ctx.") || len(vs) == 0 {
			continue
		}
		gave = true
		ctx[strings.TrimPrefix(k, "ctx.")] = vs[0]
	}
	if gave {
		d.ContextChecked = true
		d.Missing = render.CheckRequires(loaded.Manifest, ctx)
		d.Satisfied = len(d.Missing) == 0
	}
	writeJSON(w, http.StatusOK, d)
}
