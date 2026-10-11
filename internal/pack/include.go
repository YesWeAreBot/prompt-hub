package pack

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var rangeBaseRe = regexp.MustCompile(`^\d+\.\d+(?:\.\d+)?(?:-[0-9A-Za-z.-]+)?$`)

const LockName = "pack.lock"

type IncludeConstraint struct {
	ID string

	Spec string

	Exact string

	Op string

	Base string
}

func ParseInclude(s string) (IncludeConstraint, error) {
	id, spec, ok := strings.Cut(strings.TrimSpace(s), "@")
	if !ok || id == "" || spec == "" {
		return IncludeConstraint{}, fmt.Errorf(
			"includes %q 要写成 <id>@<版本或范围>（如 cats-role@^1.1）", s)
	}
	if !idRe.MatchString(id) {
		return IncludeConstraint{}, fmt.Errorf("includes 里的 pack id %q 不合法", id)
	}
	c := IncludeConstraint{ID: id, Spec: spec}
	switch spec[0] {
	case '^', '~':
		c.Op, c.Base = spec[:1], spec[1:]
		if !rangeBaseRe.MatchString(c.Base) {
			return IncludeConstraint{}, fmt.Errorf(
				"includes %q 的基准版本 %q 不合法（写 ^1.2 或 ^1.2.3）", s, c.Base)
		}
	default:
		if !verRe.MatchString(spec) {
			return IncludeConstraint{}, fmt.Errorf(
				"includes %q 的版本 %q 不合法；范围只支持 ^ 与 ~（如 ^1.1.0 / ~1.2.0）", s, spec)
		}
		c.Exact = spec
	}
	return c, nil
}

func (c IncludeConstraint) IsRange() bool { return c.Exact == "" }

func (c IncludeConstraint) String() string { return c.ID + "@" + c.Spec }

func (c IncludeConstraint) Satisfies(version string) bool {
	if c.Exact != "" {
		return CompareVersions(version, c.Exact) == 0
	}
	if _, pre := splitVersion(version); pre != "" {
		return false
	}
	if CompareVersions(version, c.Base) < 0 {
		return false
	}
	base, _ := splitVersion(c.Base)
	var upper string
	switch {
	case c.Op == "~":
		upper = fmt.Sprintf("%d.%d.0", base[0], base[1]+1)
	case base[0] > 0:
		upper = fmt.Sprintf("%d.0.0", base[0]+1)
	case base[1] > 0:

		upper = fmt.Sprintf("0.%d.0", base[1]+1)
	default:
		upper = fmt.Sprintf("0.0.%d", base[2]+1)
	}
	return CompareVersions(version, upper) < 0
}

type LockEntry struct {
	Ref      string `yaml:"ref" json:"ref"`
	TreeHash string `yaml:"treeHash" json:"treeHash"`
}

type Lock struct {
	Entries []LockEntry `yaml:"entries" json:"entries"`
}

func (l *Lock) Get(id string) (LockEntry, bool) {
	if l == nil {
		return LockEntry{}, false
	}
	for _, e := range l.Entries {
		if r, err := ParseRef(e.Ref); err == nil && r.ID == id {
			return e, true
		}
	}
	return LockEntry{}, false
}

func (l *Lock) Set(id, version, treeHash string) {
	for i := range l.Entries {
		if r, err := ParseRef(l.Entries[i].Ref); err == nil && r.ID == id {
			l.Entries[i] = LockEntry{Ref: id + "@" + version, TreeHash: treeHash}
			return
		}
	}
	l.Entries = append(l.Entries, LockEntry{Ref: id + "@" + version, TreeHash: treeHash})
}

func (l *Lock) Marshal() ([]byte, error) {
	sort.Slice(l.Entries, func(i, j int) bool { return l.Entries[i].Ref < l.Entries[j].Ref })
	var b strings.Builder
	b.WriteString("# 由 `ph lock` 生成 —— 不要手改。\n")
	b.WriteString("# 它把 pack.yaml 里的版本范围钉成精确版本，并记下依赖的 tree hash。\n")
	b.WriteString("# 运行时只认精确版本，所以带范围的依赖没有它就不能渲染。\n")
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(l); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

func ParseLock(raw []byte) (*Lock, error) {
	var l Lock
	if err := yaml.Unmarshal(raw, &l); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", LockName, err)
	}
	for _, e := range l.Entries {
		if _, err := ParseRef(e.Ref); err != nil {
			return nil, fmt.Errorf("%s 里的 %q 不合法: %w", LockName, e.Ref, err)
		}
	}
	return &l, nil
}

func LockFromTree(t Tree) (*Lock, error) {
	raw, ok := t[LockName]
	if !ok {
		return &Lock{}, nil
	}
	return ParseLock(raw)
}

func (m *Manifest) ExactRefs(lock *Lock) ([]Ref, error) {
	var out []Ref
	for _, inc := range m.Includes {
		c, err := ParseInclude(inc)
		if err != nil {
			return nil, err
		}
		if !c.IsRange() {
			out = append(out, Ref{ID: c.ID, Version: c.Exact})
			continue
		}
		e, ok := lock.Get(c.ID)
		if !ok {
			return nil, fmt.Errorf("依赖 %s 用的是范围，但 %s 里没有 %s 的条目：运行 ph lock <pack 路径>",
				inc, LockName, c.ID)
		}
		ref, err := ParseRef(e.Ref)
		if err != nil {
			return nil, err
		}
		if !c.Satisfies(ref.Version) {
			return nil, fmt.Errorf("%s 里钉的是 %s，不满足 %s 的范围：运行 ph lock --update",
				LockName, e.Ref, inc)
		}
		out = append(out, ref)
	}
	return out, nil
}
