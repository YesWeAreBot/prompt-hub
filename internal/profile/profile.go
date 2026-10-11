package profile

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	StabilityStatic = "static"

	StabilitySession = "session"

	StabilityPerRequest = "per-request"
)

const (
	PrivacyPublic   = "public"
	PrivacyInternal = "internal"
	PrivacyPII      = "pii"
)

type Placement string

const (
	PlacementFrozenGlobal Placement = "frozen-global"

	PlacementFrozenSession Placement = "frozen-session"

	PlacementAppendOnly Placement = "append-only"
)

type Field struct {
	Name      string `yaml:"name" json:"name"`
	Type      string `yaml:"type" json:"type"`
	Stability string `yaml:"stability" json:"stability"`
	Privacy   string `yaml:"privacy" json:"privacy"`
	Desc      string `yaml:"description" json:"description,omitempty"`
}

type Profile struct {
	Name        string  `yaml:"profile" json:"profile"`
	Description string  `yaml:"description" json:"description,omitempty"`
	Fields      []Field `yaml:"fields" json:"fields"`

	index map[string]Field
}

type Report struct {
	Fields []Field `json:"fields"`

	Unknown []string `json:"unknown,omitempty"`

	Placement Placement `json:"placement"`

	MaxPrivacy string `json:"maxPrivacy"`

	HasPII bool `json:"hasPii"`
}

var (
	profileNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*/[0-9]+$`)
	fieldNameRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*(\.[a-z0-9][a-z0-9._-]*)+$`)
)

func Parse(raw []byte) (*Profile, error) {
	var p Profile
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("解析 profile 失败: %w", err)
	}
	if err := p.prepare(); err != nil {
		return nil, err
	}
	return &p, nil
}

func (p *Profile) prepare() error {
	if !profileNameRe.MatchString(p.Name) {
		return fmt.Errorf("profile 名必须是 <name>/<version> 形式（收到 %q）", p.Name)
	}
	if len(p.Fields) == 0 {
		return fmt.Errorf("%s 一个字段都没有", p.Name)
	}
	p.index = make(map[string]Field, len(p.Fields))
	for i, f := range p.Fields {
		if !fieldNameRe.MatchString(f.Name) {
			return fmt.Errorf("第 %d 个字段名 %q 不合法（需点分小写标识符）", i+1, f.Name)
		}
		if _, dup := p.index[f.Name]; dup {
			return fmt.Errorf("字段 %s 重复出现", f.Name)
		}
		switch f.Stability {
		case StabilityStatic, StabilitySession, StabilityPerRequest:
		default:
			return fmt.Errorf("字段 %s 的 stability %q 不合法（%s / %s / %s）",
				f.Name, f.Stability, StabilityStatic, StabilitySession, StabilityPerRequest)
		}
		switch f.Privacy {
		case PrivacyPublic, PrivacyInternal, PrivacyPII:
		default:
			return fmt.Errorf("字段 %s 的 privacy %q 不合法（%s / %s / %s）",
				f.Name, f.Privacy, PrivacyPublic, PrivacyInternal, PrivacyPII)
		}
		if f.Type == "" {
			return fmt.Errorf("字段 %s 缺 type", f.Name)
		}
		p.index[f.Name] = f
	}
	return nil
}

func (p *Profile) Lookup(name string) (Field, bool) {
	f, ok := p.index[name]
	return f, ok
}

func (p *Profile) Classify(names []string) Report {
	rep := Report{Placement: PlacementFrozenGlobal, MaxPrivacy: PrivacyPublic}
	worst := 0
	for _, n := range names {
		f, ok := p.Lookup(n)
		if !ok {
			rep.Unknown = append(rep.Unknown, n)
			worst = 2
			continue
		}
		rep.Fields = append(rep.Fields, f)
		switch f.Stability {
		case StabilityPerRequest:
			worst = 2
		case StabilitySession:
			if worst < 1 {
				worst = 1
			}
		}
		if privacyRank(f.Privacy) > privacyRank(rep.MaxPrivacy) {
			rep.MaxPrivacy = f.Privacy
		}
	}
	rep.HasPII = rep.MaxPrivacy == PrivacyPII
	switch worst {
	case 2:
		rep.Placement = PlacementAppendOnly
	case 1:
		rep.Placement = PlacementFrozenSession
	default:
		rep.Placement = PlacementFrozenGlobal
	}
	sort.Strings(rep.Unknown)
	return rep
}

func privacyRank(p string) int {
	switch p {
	case PrivacyPII:
		return 2
	case PrivacyInternal:
		return 1
	default:
		return 0
	}
}

//go:embed *.yaml
var builtinProfiles embed.FS

type Registry struct {
	m map[string]*Profile
}

func NewRegistry(dirs ...string) (*Registry, error) {
	r := &Registry{m: map[string]*Profile{}}
	entries, err := fs.ReadDir(builtinProfiles, ".")
	if err != nil {
		return nil, fmt.Errorf("读取内建 profile 失败（这是程序缺陷）: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		raw, err := fs.ReadFile(builtinProfiles, e.Name())
		if err != nil {
			return nil, err
		}
		p, err := Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("内建 profile %s 不合法（这是程序缺陷）: %w", e.Name(), err)
		}
		r.m[p.Name] = p
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			vers, err := os.ReadDir(filepath.Join(dir, e.Name()))
			if err != nil {
				return nil, err
			}
			for _, v := range vers {
				if v.IsDir() || !strings.HasSuffix(v.Name(), ".yaml") {
					continue
				}
				raw, err := os.ReadFile(filepath.Join(dir, e.Name(), v.Name()))
				if err != nil {
					return nil, err
				}
				p, err := Parse(raw)
				if err != nil {
					return nil, fmt.Errorf("%s/%s: %w", e.Name(), v.Name(), err)
				}
				r.m[p.Name] = p
			}
		}
	}
	return r, nil
}

func (r *Registry) Get(name string) (*Profile, bool) {
	p, ok := r.m[name]
	return p, ok
}

func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.m))
	for n := range r.m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
