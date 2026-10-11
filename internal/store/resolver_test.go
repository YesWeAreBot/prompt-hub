package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeswearebot/prompt-hub/internal/pack"
)

func writePack(t *testing.T, dir, id, version, body string) *pack.LoadResult {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := "packId: " + id + "\nversion: " + version + "\nentry: index.prompt\n"
	if err := os.WriteFile(filepath.Join(dir, pack.ManifestName), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.prompt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := pack.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func newResolverFixture(t *testing.T) (*Resolver, string, string) {
	t.Helper()
	root, err := os.MkdirTemp(".", "ph-res-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	storeRoot := filepath.Join(root, "store")
	embedRoot := filepath.Join(root, "embed")
	if err := os.MkdirAll(embedRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	return &Resolver{Store: New(storeRoot), Embed: []string{embedRoot}}, storeRoot, embedRoot
}

func TestResolverFallsBackToEmbed(t *testing.T) {
	r, _, embedRoot := newResolverFixture(t)
	loaded := writePack(t, filepath.Join(embedRoot, EmbedDirName("demo", "1.0.0")), "demo", "1.0.0", "内嵌默认")

	got, err := r.Resolve("demo", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != SourceEmbed {
		t.Fatalf("来源应为 embed，得到 %s", got.Source)
	}
	if got.TreeHash != loaded.Tree.Hash() {
		t.Fatalf("tree hash 不符：%s != %s", got.TreeHash, loaded.Tree.Hash())
	}
}

func TestResolverPrefersStoreOverEmbed(t *testing.T) {
	r, storeRoot, embedRoot := newResolverFixture(t)

	embedded := writePack(t, filepath.Join(embedRoot, EmbedDirName("demo", "1.0.0")), "demo", "1.0.0", "内嵌默认")

	tmp, err := os.MkdirTemp(".", "ph-src-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	userPack := writePack(t, tmp, "demo", "1.0.0", "用户改过的")
	if _, err := r.Store.Install(userPack); err != nil {
		t.Fatal(err)
	}
	_ = storeRoot

	got, err := r.Resolve("demo", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != SourceStore {
		t.Fatalf("store 里有就必须用 store，得到 %s", got.Source)
	}
	if got.TreeHash == embedded.Tree.Hash() {
		t.Fatal("解析到了内嵌层的内容")
	}
	if got.TreeHash != userPack.Tree.Hash() {
		t.Fatalf("tree hash 不符：%s != %s", got.TreeHash, userPack.Tree.Hash())
	}
}

func TestResolveRejectsIncompleteInstall(t *testing.T) {
	root, err := os.MkdirTemp(".", "ph-inc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	st := New(root)

	dir := filepath.Join(root, "demo", "1.0.0", "sha256-deadbeef")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, pack.ManifestName), []byte("packId: demo\nversion: 1.0.0\nentry: i.prompt\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := st.Resolve("demo", "1.0.0"); err == nil {
		t.Fatal("残缺的安装不能被解析为可用版本")
	}

	if err := os.WriteFile(filepath.Join(dir, ".installed"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, hash, err := st.Resolve("demo", "1.0.0"); err != nil || hash != "sha256-deadbeef" {
		t.Fatalf("补上标记后应可解析: %v %s", err, hash)
	}
}

func TestGCRemovesIncompleteInstalls(t *testing.T) {
	root, err := os.MkdirTemp(".", "ph-gc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	st := New(root)

	src, err := os.MkdirTemp(".", "ph-gcsrc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(src) })
	good := writePack(t, src, "demo", "1.0.0", "好的")
	if _, err := st.Install(good); err != nil {
		t.Fatal(err)
	}

	bad := filepath.Join(root, "demo", "2.0.0", "sha256-cafebabe")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}

	removed, err := st.GC()
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) == 0 {
		t.Fatal("应当清掉残缺的 tree")
	}
	if _, err := os.Stat(bad); !os.IsNotExist(err) {
		t.Fatal("残缺的 tree 应被删除")
	}

	if _, _, err := st.Resolve("demo", "1.0.0"); err != nil {
		t.Fatalf("GC 不该动装好的版本: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, "demo", "2.0.0")); !os.IsNotExist(err) {
		t.Error("空掉的版本目录应被删除")
	}
}

func TestResolverNotFoundMentionsEmbedLayers(t *testing.T) {
	r, _, _ := newResolverFixture(t)
	_, err := r.Resolve("nope", "9.9.9")
	if err == nil {
		t.Fatal("不存在时必须报错")
	}
	var notInstalled *NotInstalledError
	if !asNotInstalled(err, &notInstalled) {
		t.Fatalf("应为 NotInstalledError，得到 %T", err)
	}
	if notInstalled.EmbedDirs != 1 {
		t.Fatalf("应报告查过 1 个内嵌层，得到 %d", notInstalled.EmbedDirs)
	}
}

func TestResolverAllUnionsLayers(t *testing.T) {
	r, _, embedRoot := newResolverFixture(t)
	writePack(t, filepath.Join(embedRoot, EmbedDirName("a", "1.0.0")), "a", "1.0.0", "x")
	writePack(t, filepath.Join(embedRoot, EmbedDirName("b", "2.0.0")), "b", "2.0.0", "y")

	if err := os.MkdirAll(filepath.Join(embedRoot, "不是 pack"), 0o755); err != nil {
		t.Fatal(err)
	}

	tmp, err := os.MkdirTemp(".", "ph-src-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	if _, err := r.Store.Install(writePack(t, tmp, "c", "3.0.0", "z")); err != nil {
		t.Fatal(err)
	}

	all, err := r.All()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a@1.0.0", "b@2.0.0", "c@3.0.0"}
	if len(all) != len(want) {
		t.Fatalf("得到 %v，期望 %v", all, want)
	}
	for i := range want {
		if all[i] != want[i] {
			t.Fatalf("得到 %v，期望 %v", all, want)
		}
	}
}

func writeLibPack(t *testing.T, dir, id, version, marker string) *pack.LoadResult {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := "packId: " + id + "\nversion: " + version + "\nkind: library\nexports:\n  - x\n"
	if err := os.WriteFile(filepath.Join(dir, pack.ManifestName), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.prompt"), []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := pack.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func TestResolveClosureUsesLockForRanges(t *testing.T) {
	r, _, _ := newResolverFixture(t)
	tmp, err := os.MkdirTemp(".", "ph-lib-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })

	lo := writeLibPack(t, filepath.Join(tmp, "lib-1.1.0"), "lib", "1.1.0", "旧")
	if _, err := r.Store.Install(lo); err != nil {
		t.Fatal(err)
	}
	hi := writeLibPack(t, filepath.Join(tmp, "lib-1.2.0"), "lib", "1.2.0", "新")
	if _, err := r.Store.Install(hi); err != nil {
		t.Fatal(err)
	}

	contentDir := filepath.Join(tmp, "content")
	writePack(t, contentDir, "content", "1.0.0", "{{asset 'lib/index.prompt'}}")
	yaml := "packId: content\nversion: 1.0.0\nentry: index.prompt\nruntime: template\n" +
		"includes:\n  - lib@^1.1.0\n"
	if err := os.WriteFile(filepath.Join(contentDir, pack.ManifestName), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := r.ResolveClosure(mustLoad(t, contentDir)); err == nil ||
		!strings.Contains(err.Error(), "ph lock") {
		t.Fatalf("范围没有锁应当报错并给出命令: %v", err)
	}

	l := &pack.Lock{}
	l.Set("lib", "1.1.0", lo.Tree.Hash())
	raw, err := l.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contentDir, pack.LockName), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	deps, err := r.ResolveClosure(mustLoad(t, contentDir))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(deps["lib"]["index.prompt"]); got != "旧" {
		t.Fatalf("应当按锁走 1.1.0，得到 %q", got)
	}

	l2 := &pack.Lock{}
	l2.Set("lib", "2.0.0", "sha256-x")
	raw2, _ := l2.Marshal()
	if err := os.WriteFile(filepath.Join(contentDir, pack.LockName), raw2, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ResolveClosure(mustLoad(t, contentDir)); err == nil ||
		!strings.Contains(err.Error(), "--update") {
		t.Fatalf("锁不满足范围应当报错: %v", err)
	}
}

func TestResolverVersionsUnionsLayers(t *testing.T) {
	r, _, embedRoot := newResolverFixture(t)
	tmp, err := os.MkdirTemp(".", "ph-ver-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })

	if _, err := r.Store.Install(writePack(t, filepath.Join(tmp, "s"), "lib", "1.0.0", "x")); err != nil {
		t.Fatal(err)
	}
	writePack(t, filepath.Join(embedRoot, EmbedDirName("lib", "1.1.0")), "lib", "1.1.0", "y")

	got := strings.Join(r.Versions("lib"), ",")
	if got != "1.0.0,1.1.0" {
		t.Fatalf("应当跨层汇总且有序: %q", got)
	}
	if n := len(r.Versions("nope")); n != 0 {
		t.Fatalf("不存在的 id 应当返回空: %d", n)
	}
}

func mustLoad(t *testing.T, dir string) *pack.LoadResult {
	t.Helper()
	loaded, err := pack.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func asNotInstalled(err error, target **NotInstalledError) bool {
	for err != nil {
		if n, ok := err.(*NotInstalledError); ok {
			*target = n
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
