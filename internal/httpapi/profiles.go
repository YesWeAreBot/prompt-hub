package httpapi

import (
	"net/http"
	"strings"

	"github.com/yeswearebot/prompt-hub/internal/pack"
	"github.com/yeswearebot/prompt-hub/internal/profile"
)

func (s *Server) profileReport(m *pack.Manifest) (profile.Report, bool) {
	if s.cfg.Profiles == nil {
		return profile.Report{}, false
	}
	p, ok := s.cfg.Profiles.Get(m.Profile)
	if !ok {
		return profile.Report{}, false
	}
	return p.Classify(m.Requires), true
}

func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	type item struct {
		Profile     string `json:"profile"`
		Description string `json:"description,omitempty"`
		Fields      int    `json:"fields"`
	}
	out := []item{}
	if s.cfg.Profiles != nil {
		for _, n := range s.cfg.Profiles.Names() {
			if p, ok := s.cfg.Profiles.Get(n); ok {
				out = append(out, item{Profile: p.Name, Description: p.Description, Fields: len(p.Fields)})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": out})
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name") + "/" + r.PathValue("version")
	if s.cfg.Profiles == nil {
		writeErr(w, http.StatusNotFound, "本机没有装载任何 profile")
		return
	}
	p, ok := s.cfg.Profiles.Get(name)
	if !ok {
		writeErr(w, http.StatusNotFound, "没有 profile "+name+
			"；可用："+strings.Join(s.cfg.Profiles.Names(), ", "))
		return
	}
	writeJSON(w, http.StatusOK, p)
}
