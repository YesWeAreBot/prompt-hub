package render

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/yeswearebot/prompt-hub/internal/pack"
)

const (
	maxAssetBytes    = 256 << 10
	maxAssetDepth    = 8
	maxExpandedBytes = 1 << 20
)

var builtinVars = map[string]bool{
	"packId": true, "version": true, "turn": true, "now": true, "nowUnix": true,
}

func IsBuiltinVar(name string) bool { return builtinVars[name] }

var assetArgRe = regexp.MustCompile(`^asset(?:\s+(.*))?$`)

func parseAssetArg(inner string) (ref string, isAsset bool, err error) {
	m := assetArgRe.FindStringSubmatch(inner)
	if m == nil {
		return "", false, nil
	}
	arg := strings.TrimSpace(m[1])
	if arg == "" {
		return "", true, fmt.Errorf(
			"asset 需要一个路径，例如 {{asset './partials/head.md'}} 或 {{asset 'cats-role/tone.md'}}")
	}
	if len(arg) < 2 || (arg[0] != '\'' && arg[0] != '"') || arg[len(arg)-1] != arg[0] {
		return "", true, fmt.Errorf(
			"asset 的路径要加引号，例如 {{asset 'cats-role/tone.md'}}，收到 %q", inner)
	}
	return arg[1 : len(arg)-1], true, nil
}

type assetTarget struct {
	own  bool
	id   string
	path string
}

func (t assetTarget) String() string {
	if t.own {
		return "./" + t.path
	}
	return t.id + "/" + t.path
}

func parseAssetTarget(ref string) (assetTarget, error) {
	if rest, ok := strings.CutPrefix(ref, "./"); ok {
		if rest == "" {
			return assetTarget{}, fmt.Errorf("`./` 后面要跟路径，收到 %q", ref)
		}
		if err := checkAssetPath(rest); err != nil {
			return assetTarget{}, err
		}
		return assetTarget{own: true, path: rest}, nil
	}
	if strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "../") {
		return assetTarget{}, fmt.Errorf(
			"资产引用 %q 不能用绝对路径或 `../`；引用自己的文件请写成 ./<路径>", ref)
	}
	id, path, ok := strings.Cut(ref, "/")
	if !ok || id == "" || path == "" {
		return assetTarget{}, fmt.Errorf(
			"资产引用要写成 <packId>/<路径>（依赖）或 ./<路径>（自己），收到 %q", ref)
	}
	if err := checkAssetPath(path); err != nil {
		return assetTarget{}, err
	}
	return assetTarget{id: id, path: path}, nil
}

func checkAssetPath(path string) error {
	for _, seg := range strings.Split(path, "/") {
		if seg == ".." || seg == "." || seg == "" {
			return fmt.Errorf("资产路径 %q 不合法", path)
		}
	}
	return nil
}

type unknownVar struct {
	name  string
	where string
}

type expander struct {
	lookup func(string) (string, bool)
	own    pack.Tree
	deps   map[string]pack.Tree

	unresolvedDeps bool

	stack   []string
	assets  []string
	out     strings.Builder
	unknown []unknownVar
}

func expandTemplate(text string, vars map[string]string, own pack.Tree, deps map[string]pack.Tree) (string, []unknownVar, error) {
	e := &expander{
		lookup: func(k string) (string, bool) { v, ok := vars[k]; return v, ok },
		own:    own,
		deps:   deps,
	}
	if err := e.expand(text); err != nil {
		return "", nil, err
	}
	return e.out.String(), e.unknown, nil
}

func (e *expander) expand(text string) error {
	for {
		loc := varRe.FindStringSubmatchIndex(text)
		if loc == nil {
			return e.append(text)
		}
		if err := e.append(text[:loc[0]]); err != nil {
			return err
		}
		whole := text[loc[0]:loc[1]]
		inner := strings.TrimSpace(text[loc[2]:loc[3]])
		if err := e.node(whole, inner); err != nil {
			return err
		}
		text = text[loc[1]:]
	}
}

func (e *expander) node(whole, inner string) error {
	ref, isAsset, err := parseAssetArg(inner)
	if !isAsset {
		v, ok := e.lookup(inner)
		if !ok {
			e.unknown = append(e.unknown, unknownVar{name: inner, where: e.here()})

			return e.append(whole)
		}
		return e.append(v)
	}
	if err != nil {
		return err
	}
	t, err := parseAssetTarget(ref)
	if err != nil {
		return err
	}
	return e.asset(t)
}

func (e *expander) here() string {
	if len(e.stack) == 0 {
		return ""
	}
	return e.stack[len(e.stack)-1]
}

func (e *expander) asset(t assetTarget) error {
	key := t.String()

	if len(e.stack) >= maxAssetDepth {
		return fmt.Errorf("资产嵌套超过 %d 层: %s", maxAssetDepth, chainWith(e.stack, key))
	}

	for _, seen := range e.stack {
		if seen == key {
			return fmt.Errorf("资产循环引用: %s", chainWith(e.stack, key))
		}
	}

	var tree pack.Tree
	var where string
	if t.own {
		tree, where = e.own, "本 pack"
	} else {
		tr, ok := e.deps[t.id]
		if !ok {
			if e.unresolvedDeps {
				e.assets = append(e.assets, key)
				return nil
			}
			return fmt.Errorf("资产 %s 来自 pack %s，但本 pack 没有 include 它"+
				"（在 pack.yaml 的 includes 里加上 %s@<精确版本>；引用自己的文件请用 ./）",
				key, t.id, t.id)
		}
		tree, where = tr, "pack "+t.id
	}
	content, ok := tree[t.path]
	if !ok {
		return fmt.Errorf("%s 里没有 %s；它有：%s", where, t.path, treeFileList(tree))
	}
	if len(content) > maxAssetBytes {
		return fmt.Errorf("资产 %s 有 %d 字节，超过单文件上限 %d", key, len(content), maxAssetBytes)
	}
	if bytes.IndexByte(content, 0) >= 0 {
		return fmt.Errorf("资产 %s 含 NUL 字节，看起来是二进制文件；只能内联文本", key)
	}

	e.assets = append(e.assets, key)
	e.stack = append(e.stack, key)
	err := e.expand(string(content))
	e.stack = e.stack[:len(e.stack)-1]
	return err
}

func (e *expander) append(s string) error {
	if e.out.Len()+len(s) > maxExpandedBytes {
		return fmt.Errorf("展开后超过总量上限 %d 字节（递归展开必须有总量上界，"+
			"否则一个很小的 pack 就能指数级膨胀）", maxExpandedBytes)
	}
	e.out.WriteString(s)
	return nil
}

func chainWith(stack []string, next string) string {
	parts := make([]string, 0, len(stack)+1)
	parts = append(parts, stack...)
	parts = append(parts, next)
	return strings.Join(parts, " → ")
}

func treeFileList(tree pack.Tree) string {
	names := make([]string, 0, len(tree))
	for n := range tree {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func formatUnknown(unknown []unknownVar) error {
	seen := map[string]bool{}
	var parts []string
	for _, u := range unknown {
		key := u.name + "|" + u.where
		if seen[key] {
			continue
		}
		seen[key] = true
		if u.where == "" {
			parts = append(parts, u.name)
		} else {
			parts = append(parts, fmt.Sprintf("%s（来自 %s）", u.name, u.where))
		}
	}
	sort.Strings(parts)
	return fmt.Errorf("模板引用了未提供的变量: %s", strings.Join(parts, ", "))
}

func AssetRefs(body string) []string {
	var out []string
	for _, m := range varRe.FindAllStringSubmatch(body, -1) {
		if ref, isAsset, err := parseAssetArg(strings.TrimSpace(m[1])); isAsset && err == nil {
			out = append(out, ref)
		}
	}
	return out
}

type collector struct{ seen map[string]bool }

func (c *collector) get(name string) (string, bool) {
	c.seen[name] = true
	return "", true
}

func AssetClosure(text string, own pack.Tree, deps map[string]pack.Tree) (assets, vars []string, err error) {
	return assetClosure(text, own, deps, false)
}

func assetClosure(text string, own pack.Tree, deps map[string]pack.Tree, unresolvedDeps bool) (assets, vars []string, err error) {
	c := &collector{seen: map[string]bool{}}
	e := &expander{
		lookup:         c.get,
		own:            own,
		deps:           deps,
		unresolvedDeps: unresolvedDeps,
	}
	if err := e.expand(text); err != nil {
		return nil, nil, err
	}
	for name := range c.seen {
		if !builtinVars[name] {
			vars = append(vars, name)
		}
	}
	sort.Strings(vars)
	return e.assets, vars, nil
}

func UndeclaredAssetVars(text string, own pack.Tree, requires []string, deps map[string]pack.Tree) ([]string, error) {
	if deps == nil {
		return nil, nil
	}
	_, vars, err := AssetClosure(text, own, deps)
	if err != nil {
		return nil, err
	}
	declared := map[string]bool{}
	for _, r := range requires {
		declared[r] = true
	}
	var out []string
	for _, v := range vars {
		if !declared[v] {
			out = append(out, v)
		}
	}
	return out, nil
}

func CheckAssets(body string, own pack.Tree, includes []string, deps map[string]pack.Tree) error {
	declared := map[string]bool{}
	for _, inc := range includes {

		if c, err := pack.ParseInclude(inc); err == nil {
			declared[c.ID] = true
		}
	}

	seen := map[string]bool{}
	for _, m := range varRe.FindAllStringSubmatch(body, -1) {
		ref, isAsset, err := parseAssetArg(strings.TrimSpace(m[1]))
		if !isAsset {
			continue
		}
		if err != nil {
			return err
		}
		if seen[ref] {
			continue
		}
		seen[ref] = true
		t, err := parseAssetTarget(ref)
		if err != nil {
			return err
		}
		if t.own {
			continue
		}
		if !declared[t.id] {
			return fmt.Errorf("资产 %q 来自 pack %s，但它不在 includes 里 —— 加上 %s@<精确版本>",
				ref, t.id, t.id)
		}
	}

	assets, _, err := assetClosure(body, own, deps, deps == nil)
	if err != nil {
		return err
	}
	for _, key := range assets {
		t, err := parseAssetTarget(key)
		if err != nil {
			return err
		}
		if t.own {
			continue
		}
		if !declared[t.id] {
			return fmt.Errorf("资产闭包里用到了 pack %s 的 %s，但它不在 includes 里 —— 加上 %s@<精确版本>",
				t.id, key, t.id)
		}
	}
	return nil
}
