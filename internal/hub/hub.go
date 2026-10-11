package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yeswearebot/prompt-hub/internal/pack"
)

var ErrNotFound = errors.New("hub: 未找到")

type Meta struct {
	Manifest   pack.Manifest `json:"manifest"`
	TreeHash   string        `json:"treeHash"`
	Size       int64         `json:"size"`
	UploadedAt time.Time     `json:"uploadedAt"`

	MissingDeps []string `json:"missingDeps,omitempty"`

	Lock *pack.Lock `json:"lock,omitempty"`
}

func (m Meta) Ref() string { return m.Manifest.PackID + "@" + m.Manifest.Version }

type Registry struct {
	Root string
}

func NewRegistry(root string) *Registry { return &Registry{Root: root} }

func (r *Registry) metaPath(id, version string) string {
	return filepath.Join(r.Root, "packs", id, version+".json")
}

func (r *Registry) blobPath(treeHash string) string {
	return filepath.Join(r.Root, "blobs", treeHash+pack.ArchiveExt)
}

func (r *Registry) Publish(archive []byte) (Meta, error) {
	tree, err := pack.ReadArchive(archive)
	if err != nil {
		return Meta{}, err
	}
	m, err := tree.Manifest()
	if err != nil {
		return Meta{}, err
	}
	if err := m.Validate(); err != nil {
		return Meta{}, fmt.Errorf("manifest 不合法: %w", err)
	}
	if m.Kind == "content" {
		if _, ok := tree[m.Entry]; !ok {
			return Meta{}, fmt.Errorf("entry %q 不在归档里", m.Entry)
		}
	}

	lock, err := pack.LockFromTree(tree)
	if err != nil {
		return Meta{}, err
	}
	exact, err := m.ExactRefs(lock)
	if err != nil {
		return Meta{}, fmt.Errorf("拒绝发布 %s@%s: %w（带范围的依赖必须带上 pack.lock）",
			m.PackID, m.Version, err)
	}

	var missing []string
	for _, ref := range exact {
		dep, err := r.Get(ref.ID, ref.Version)
		if errors.Is(err, ErrNotFound) {
			missing = append(missing, ref.String())
			continue
		}
		if err != nil {
			return Meta{}, err
		}
		if dep.Manifest.Kind != "library" {
			return Meta{}, fmt.Errorf(
				"拒绝发布 %s@%s：依赖 %s 是 %s pack，而只有 library pack 能被 include",
				m.PackID, m.Version, ref, dep.Manifest.Kind)
		}
	}
	sort.Strings(missing)

	hash := tree.Hash()

	old, err := r.Get(m.PackID, m.Version)
	switch {
	case err == nil:
		if old.TreeHash != hash {
			return Meta{}, fmt.Errorf(
				"拒绝发布 %s@%s：同版本已存在不同内容（已有 %s，本次 %s）。"+
					"同版本重发会静默改变所有使用者的行为，必须改用新版本号",
				m.PackID, m.Version, old.TreeHash, hash)
		}
		return old, nil
	case !errors.Is(err, ErrNotFound):
		return Meta{}, err
	}

	if err := os.MkdirAll(filepath.Dir(r.blobPath(hash)), 0o755); err != nil {
		return Meta{}, err
	}
	if err := os.WriteFile(r.blobPath(hash), archive, 0o644); err != nil {
		return Meta{}, err
	}

	meta := Meta{
		Manifest: *m, TreeHash: hash, Size: int64(len(archive)),
		UploadedAt: time.Now().UTC(), MissingDeps: missing, Lock: lock,
	}
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return Meta{}, err
	}
	mp := r.metaPath(m.PackID, m.Version)
	if err := os.MkdirAll(filepath.Dir(mp), 0o755); err != nil {
		return Meta{}, err
	}
	return meta, os.WriteFile(mp, b, 0o644)
}

func (r *Registry) Get(id, version string) (Meta, error) {
	m, err := r.get(id, version)
	if err != nil {
		return Meta{}, err
	}
	r.refreshMissingDeps(&m)
	return m, nil
}

func (r *Registry) get(id, version string) (Meta, error) {
	b, err := os.ReadFile(r.metaPath(id, version))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Meta{}, fmt.Errorf("%w: %s@%s", ErrNotFound, id, version)
		}
		return Meta{}, err
	}
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return Meta{}, err
	}
	return m, nil
}

func (r *Registry) refreshMissingDeps(m *Meta) {

	exact, err := m.Manifest.ExactRefs(m.Lock)
	if err != nil {

		incs := append([]string{}, m.Manifest.Includes...)
		sort.Strings(incs)
		m.MissingDeps = incs
		return
	}
	var missing []string
	for _, ref := range exact {
		if _, err := r.get(ref.ID, ref.Version); err != nil {
			if errors.Is(err, ErrNotFound) {
				missing = append(missing, ref.String())
			}
		}
	}
	sort.Strings(missing)
	m.MissingDeps = missing
}

func (r *Registry) Blob(treeHash string) ([]byte, error) {
	b, err := os.ReadFile(r.blobPath(treeHash))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: blob %s", ErrNotFound, treeHash)
		}
		return nil, err
	}
	return b, nil
}

func (r *Registry) Index() ([]Meta, error) {
	var out []Meta
	root := filepath.Join(r.Root, "packs")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".json") {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		var m Meta
		if json.Unmarshal(b, &m) != nil {
			return nil
		}
		r.refreshMissingDeps(&m)
		out = append(out, m)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Manifest.PackID != out[j].Manifest.PackID {
			return out[i].Manifest.PackID < out[j].Manifest.PackID
		}
		return out[i].Manifest.Version < out[j].Manifest.Version
	})
	return out, nil
}

type CapabilityDiff struct {
	From, To        string
	RuntimeChange   string
	ProfileChange   string
	AddedRequires   []string
	RemovedRequires []string
	AddedEgress     []string
	RemovedEgress   []string
	OutputMaxFrom   int
	OutputMaxTo     int
}

func DiffCapabilities(from, to pack.Manifest) CapabilityDiff {
	d := CapabilityDiff{
		From:          from.PackID + "@" + from.Version,
		To:            to.PackID + "@" + to.Version,
		OutputMaxFrom: from.OutputMax,
		OutputMaxTo:   to.OutputMax,
	}
	if from.Runtime != to.Runtime {
		d.RuntimeChange = from.Runtime + " -> " + to.Runtime
	}
	if from.Profile != to.Profile {
		d.ProfileChange = from.Profile + " -> " + to.Profile
	}
	d.AddedRequires, d.RemovedRequires = diffSets(from.Requires, to.Requires)
	d.AddedEgress, d.RemovedEgress = diffSets(from.Egress, to.Egress)
	return d
}

func (d CapabilityDiff) Widening() bool {
	return len(d.AddedRequires) > 0 || len(d.AddedEgress) > 0 ||
		d.OutputMaxTo > d.OutputMaxFrom ||
		d.RuntimeChange != "" || d.ProfileChange != ""
}

func (d CapabilityDiff) Empty() bool {
	return len(d.AddedRequires) == 0 && len(d.RemovedRequires) == 0 &&
		len(d.AddedEgress) == 0 && len(d.RemovedEgress) == 0 &&
		d.OutputMaxFrom == d.OutputMaxTo &&
		d.RuntimeChange == "" && d.ProfileChange == ""
}

func (d CapabilityDiff) String() string {
	if d.Empty() {
		return fmt.Sprintf("%s -> %s：能力声明无变化", d.From, d.To)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s -> %s\n", d.From, d.To)
	if d.RuntimeChange != "" {
		fmt.Fprintf(&b, "  运行时     %s\n", d.RuntimeChange)
	}
	if d.ProfileChange != "" {
		fmt.Fprintf(&b, "  profile    %s\n", d.ProfileChange)
	}
	if d.OutputMaxFrom != d.OutputMaxTo {
		fmt.Fprintf(&b, "  输出预算   %d -> %d\n", d.OutputMaxFrom, d.OutputMaxTo)
	}
	line := func(label string, add, del []string) {
		if len(add) > 0 {
			fmt.Fprintf(&b, "  %s +%s\n", label, strings.Join(add, ", "))
		}
		if len(del) > 0 {
			fmt.Fprintf(&b, "  %s -%s\n", label, strings.Join(del, ", "))
		}
	}
	line("新增 context 字段", d.AddedRequires, nil)
	line("移除 context 字段", nil, d.RemovedRequires)
	line("新增出网目标", d.AddedEgress, nil)
	line("移除出网目标", nil, d.RemovedEgress)
	return strings.TrimRight(b.String(), "\n")
}

func diffSets(from, to []string) (added, removed []string) {
	f := map[string]bool{}
	for _, s := range from {
		f[s] = true
	}
	t := map[string]bool{}
	for _, s := range to {
		t[s] = true
	}
	for _, s := range to {
		if !f[s] {
			added = append(added, s)
		}
	}
	for _, s := range from {
		if !t[s] {
			removed = append(removed, s)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

type Server struct {
	Reg          *Registry
	PublishToken string
	Log          *slog.Logger

	MaxPublishBytes int64
}

func (s *Server) Handler() http.Handler {
	if s.Log == nil {
		s.Log = slog.Default()
	}
	if s.MaxPublishBytes <= 0 {
		s.MaxPublishBytes = 128 << 20
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		idx, _ := s.Reg.Index()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "packs": len(idx)})
	})
	mux.HandleFunc("GET /v1/index", func(w http.ResponseWriter, r *http.Request) {
		idx, err := s.Reg.Index()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"packs": idx})
	})
	mux.HandleFunc("GET /v1/packs/{id}/{version}", func(w http.ResponseWriter, r *http.Request) {
		m, err := s.Reg.Get(r.PathValue("id"), r.PathValue("version"))
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, ErrNotFound) {
				status = http.StatusNotFound
			}
			writeErr(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, m)
	})
	mux.HandleFunc("GET /v1/blobs/{hash}", func(w http.ResponseWriter, r *http.Request) {
		b, err := s.Reg.Blob(r.PathValue("hash"))
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, ErrNotFound) {
				status = http.StatusNotFound
			}
			writeErr(w, status, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Ph-Tree-Hash", r.PathValue("hash"))
		_, _ = w.Write(b)
	})
	mux.HandleFunc("POST /v1/packs", func(w http.ResponseWriter, r *http.Request) {
		if s.PublishToken != "" && r.Header.Get("X-Ph-Publish-Token") != s.PublishToken {
			writeErr(w, http.StatusUnauthorized, "发布需要 token")
			return
		}
		body := http.MaxBytesReader(w, r.Body, s.MaxPublishBytes)
		archive, err := io.ReadAll(body)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "读取归档失败: "+err.Error())
			return
		}
		meta, err := s.Reg.Publish(archive)
		if err != nil {

			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "同版本已存在不同内容") {
				status = http.StatusConflict
			}
			s.Log.Warn("发布被拒", "err", err)
			writeErr(w, status, err.Error())
			return
		}
		s.Log.Info("已发布", "pack", meta.Ref(), "treeHash", meta.TreeHash, "size", meta.Size)
		writeJSON(w, http.StatusCreated, meta)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
