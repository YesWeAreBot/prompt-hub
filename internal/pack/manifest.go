package pack

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const ManifestName = "pack.yaml"

const CanonicalManifestName = "pack.json"

var (
	idRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	verRe     = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$`)
	rangeSet  = regexp.MustCompile(`[\^~><*x]|\s`)
	runtimeRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

type Manifest struct {
	PackID    string   `yaml:"packId" json:"packId"`
	Version   string   `yaml:"version" json:"version"`
	Profile   string   `yaml:"profile" json:"profile"`
	Kind      string   `yaml:"kind,omitempty" json:"kind,omitempty"`
	Entry     string   `yaml:"entry,omitempty" json:"entry,omitempty"`
	Runtime   string   `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	Exports   []string `yaml:"exports,omitempty" json:"exports,omitempty"`
	Includes  []string `yaml:"includes,omitempty" json:"includes,omitempty"`
	Requires  []string `yaml:"requires,omitempty" json:"requires,omitempty"`
	Egress    []string `yaml:"egress,omitempty" json:"egress,omitempty"`
	OutputMax int      `yaml:"outputMax,omitempty" json:"outputMax,omitempty"`
	Targets   []string `yaml:"targets,omitempty" json:"targets,omitempty"`
	License   string   `yaml:"license,omitempty" json:"license,omitempty"`
}

func (m *Manifest) ApplyDefaults() {
	if m.Kind == "" {
		m.Kind = "content"
	}
	if m.Runtime == "" {
		m.Runtime = "template"
	}
	if m.Profile == "" {
		m.Profile = "chat/1"
	}
	if m.OutputMax == 0 {
		m.OutputMax = 8000
	}
	for i, inc := range m.Includes {
		m.Includes[i] = strings.TrimSpace(inc)
	}
}

func (m *Manifest) Validate() error {
	if m.PackID == "" {
		return fmt.Errorf("packId 必填")
	}
	if !idRe.MatchString(m.PackID) {
		return fmt.Errorf("packId %q 不合法（应为 [a-z0-9][a-z0-9._-]*）", m.PackID)
	}
	if m.Version == "" {
		return fmt.Errorf("version 必填")
	}
	if !verRe.MatchString(m.Version) {
		return fmt.Errorf("version %q 不是合法语义化版本", m.Version)
	}
	switch m.Kind {
	case "content":
		if m.Entry == "" {
			return fmt.Errorf("content pack 必须声明 entry")
		}
	case "library":
		if m.Entry != "" {
			return fmt.Errorf("library pack 不得声明 entry")
		}
		if len(m.Exports) == 0 {
			return fmt.Errorf("library pack 必须声明 exports")
		}
	default:
		return fmt.Errorf("kind %q 不合法（content|library）", m.Kind)
	}

	if !runtimeRe.MatchString(m.Runtime) {
		return fmt.Errorf("runtime %q 不合法（应为 [a-z][a-z0-9-]*）", m.Runtime)
	}
	for _, inc := range m.Includes {
		if inc == "" {
			return fmt.Errorf("includes 含空项")
		}

		if _, err := ParseInclude(inc); err != nil {
			return fmt.Errorf("includes %q 不合法: %w", inc, err)
		}
	}
	return nil
}

func (m *Manifest) Canonical() ([]byte, error) {
	m.ApplyDefaults()
	return yaml.Marshal(m)
}

type Ref struct {
	ID      string
	Version string
}

func (r Ref) String() string { return r.ID + "@" + r.Version }

func ParseRef(s string) (Ref, error) {
	i := strings.LastIndex(s, "@")
	if i <= 0 || i == len(s)-1 {
		return Ref{}, fmt.Errorf("引用必须形如 id@version")
	}
	id, ver := s[:i], s[i+1:]
	if !idRe.MatchString(id) {
		return Ref{}, fmt.Errorf("packId %q 不合法", id)
	}
	if rangeSet.MatchString(ver) {
		return Ref{}, fmt.Errorf("必须使用精确版本，收到范围 %q", ver)
	}
	if !verRe.MatchString(ver) {
		return Ref{}, fmt.Errorf("version %q 不合法", ver)
	}
	return Ref{ID: id, Version: ver}, nil
}

type Tree map[string][]byte

func (t Tree) Hash() string {
	paths := make([]string, 0, len(t))
	for p := range t {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	h := sha256.New()
	for _, p := range paths {
		sum := sha256.Sum256(t[p])

		fmt.Fprintf(h, "%s\x000644\x00%s\n", p, hex.EncodeToString(sum[:]))
	}
	return "sha256-" + hex.EncodeToString(h.Sum(nil))
}

type LoadResult struct {
	Manifest *Manifest
	Tree     Tree

	SingleFile bool

	Archived bool

	Warnings []string
}

var defaultIgnore = map[string]bool{
	".git":         true,
	"node_modules": true,
	".installed":   true,
}

func ignored(rel string) bool {
	if strings.HasSuffix(rel, ".tmp") {
		return true
	}
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if defaultIgnore[seg] {
			return true
		}
	}
	return false
}

func Load(path string) (*LoadResult, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return loadDir(path)
	}
	if strings.EqualFold(filepath.Ext(path), ArchiveExt) {
		return loadArchiveFile(path)
	}
	return loadSingleFile(path)
}

func loadArchiveFile(path string) (*LoadResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	tree, err := ReadArchive(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	m, err := tree.Manifest()
	if err != nil {
		return nil, err
	}
	return &LoadResult{Manifest: m, Tree: tree, Archived: true}, nil
}

func loadDir(dir string) (*LoadResult, error) {
	raw, err := os.ReadFile(filepath.Join(dir, ManifestName))
	name := ManifestName
	if err != nil {
		if raw, err = os.ReadFile(filepath.Join(dir, CanonicalManifestName)); err != nil {
			return nil, fmt.Errorf("读取 %s 失败: %w", ManifestName, err)
		}
		name = CanonicalManifestName
	}
	var m Manifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", name, err)
	}
	m.ApplyDefaults()

	res := &LoadResult{Manifest: &m}

	entryAbs := filepath.Join(dir, filepath.FromSlash(m.Entry))
	if hdr, herr := parseHeaderFile(entryAbs); herr == nil && hdr != nil {
		if hdr.Version != "" && hdr.Version != m.Version {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("%s 头部 version=%s 与 pack.yaml version=%s 不一致，以 pack.yaml 为准",
					m.Entry, hdr.Version, m.Version))
		}
	}

	tree := Tree{}
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if ignored(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		tree[rel] = b
		return nil
	})
	if err != nil {
		return nil, err
	}

	if m.Kind == "content" {
		if m.Entry == "" {
			return nil, fmt.Errorf("content pack 必须声明 entry")
		}
		if _, ok := tree[filepath.ToSlash(m.Entry)]; !ok {
			return nil, fmt.Errorf("entry %q 在 pack 目录中不存在", m.Entry)
		}
	}
	res.Tree = tree
	return res, nil
}

func loadSingleFile(path string) (*LoadResult, error) {
	m, err := parseHeaderFile(path)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("单文件 pack 必须在头部注释中声明 manifest；" +
			"或改用 pack.yaml 目录形态")
	}
	base := filepath.Base(path)
	if m.Entry == "" || m.Entry == "self" {
		m.Entry = base
	}
	if m.Entry != base {
		return nil, fmt.Errorf("单文件 pack 的 entry 只能是自身（%s），收到 %q", base, m.Entry)
	}
	m.ApplyDefaults()

	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	canon, err := m.CanonicalJSON()
	if err != nil {
		return nil, err
	}
	return &LoadResult{
		Manifest:   m,
		SingleFile: true,
		Tree:       Tree{CanonicalManifestName: canon, base: content},
	}, nil
}

var headerStart = regexp.MustCompile(`^\s*(?:<\?php\s*)?/\*`)

func parseHeaderFile(path string) (*Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := string(raw)
	if !headerStart.MatchString(text) {
		return nil, nil
	}
	start := strings.Index(text, "/*")
	end := strings.Index(text, "*/")
	if start < 0 || end < 0 || end < start {
		return nil, fmt.Errorf("头部注释未闭合")
	}
	body := text[start+2 : end]

	fields := map[string]string{}
	var order []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "*"))
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, ":") {
			continue
		}
		k, v, _ := strings.Cut(line, ":")
		key := normalizeKey(strings.TrimSpace(k))
		if key == "" {
			continue
		}
		if _, seen := fields[key]; !seen {
			order = append(order, key)
		}
		fields[key] = strings.TrimSpace(v)
	}
	_ = order

	m := &Manifest{
		PackID:   fields["packId"],
		Version:  fields["version"],
		Profile:  fields["profile"],
		Kind:     fields["kind"],
		Entry:    fields["entry"],
		Runtime:  fields["runtime"],
		License:  fields["license"],
		Exports:  splitList(fields["exports"]),
		Includes: splitList(fields["includes"]),
		Requires: splitList(fields["requires"]),
		Egress:   splitList(fields["egress"]),
		Targets:  splitList(fields["targets"]),
	}
	if v := fields["outputMax"]; v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			m.OutputMax = n
		}
	}
	if m.PackID == "" && m.Version == "" {
		return nil, nil
	}
	return m, nil
}

func normalizeKey(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	k = strings.NewReplacer("-", "", "_", "", " ", "").Replace(k)
	switch k {
	case "packid", "id", "name":
		return "packId"
	case "version", "ver":
		return "version"
	case "profile":
		return "profile"
	case "kind":
		return "kind"
	case "entry", "main":
		return "entry"
	case "runtime":
		return "runtime"
	case "exports":
		return "exports"
	case "includes", "dependencies":
		return "includes"
	case "requires", "context":
		return "requires"
	case "egress", "network":
		return "egress"
	case "outputmax", "maxchars":
		return "outputMax"
	case "targets":
		return "targets"
	case "license":
		return "license"
	default:
		return ""
	}
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (t Tree) Manifest() (*Manifest, error) {
	raw, ok := t[ManifestName]
	name := ManifestName
	if !ok {
		raw, ok = t[CanonicalManifestName]
		name = CanonicalManifestName
	}
	if !ok {
		return nil, fmt.Errorf("pack 树里既没有 %s 也没有 %s", ManifestName, CanonicalManifestName)
	}
	var m Manifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", name, err)
	}
	m.ApplyDefaults()
	return &m, nil
}

func (m *Manifest) CanonicalJSON() ([]byte, error) {
	m.ApplyDefaults()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func StripHeaderComment(content []byte) ([]byte, bool) {
	text := string(content)
	if !headerStart.MatchString(text) {
		return content, false
	}
	start := strings.Index(text, "/*")
	end := strings.Index(text, "*/")
	if start < 0 || end < start {
		return content, false
	}

	looksLikeManifest := false
	for _, line := range strings.Split(text[start+2:end], "\n") {
		l := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "*"))
		k, _, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		switch normalizeKey(strings.TrimSpace(k)) {
		case "packId", "version", "runtime", "entry", "requires", "profile":
			looksLikeManifest = true
		}
	}
	if !looksLikeManifest {
		return content, false
	}
	return []byte(strings.TrimLeft(text[end+2:], "\r\n")), true
}
