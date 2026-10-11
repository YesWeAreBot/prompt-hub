package pack

import (
	"bytes"
	"strings"
	"testing"
)

func sampleTree() Tree {
	return Tree{
		"pack.yaml":     []byte("packId: demo\nversion: 1.0.0\nentry: index.prompt\n"),
		"index.prompt":  []byte("你好，{{chat.user.nickname}}"),
		"assets/a.png":  bytes.Repeat([]byte{0x89, 0x50}, 500),
		"lib/helper.go": []byte("package main"),
	}
}

func TestArchiveRoundTrip(t *testing.T) {
	tree := sampleTree()
	archive, err := WriteArchive(tree)
	if err != nil {
		t.Fatal(err)
	}
	if len(archive) == 0 {
		t.Fatal("归档为空")
	}

	back, err := ReadArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != len(tree) {
		t.Fatalf("文件数 %d，期望 %d", len(back), len(tree))
	}
	for p, want := range tree {
		got, ok := back[p]
		if !ok {
			t.Fatalf("缺少 %s", p)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s 内容不一致", p)
		}
	}

	if got, want := back.Hash(), tree.Hash(); got != want {
		t.Fatalf("tree hash 变了：%s != %s", got, want)
	}
}

func TestArchiveIsDeterministic(t *testing.T) {
	tree := sampleTree()
	a, err := WriteArchive(tree)
	if err != nil {
		t.Fatal(err)
	}

	shuffled := Tree{}
	for k, v := range map[string][]byte{
		"lib/helper.go": tree["lib/helper.go"],
		"index.prompt":  tree["index.prompt"],
		"assets/a.png":  tree["assets/a.png"],
		"pack.yaml":     tree["pack.yaml"],
	} {
		shuffled[k] = v
	}
	b, err := WriteArchive(shuffled)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("同一棵树打出了不同的归档")
	}
}

func TestArchiveRejectsHostilePaths(t *testing.T) {
	cases := []string{"../evil", "/abs/evil", "a/../../b", ".."}
	for _, name := range cases {
		tree := Tree{name: []byte("x")}
		if _, err := WriteArchive(tree); err == nil {
			t.Errorf("写归档就应拒绝恶意路径 %q", name)
		}
	}
}

func TestReadArchiveRejectsGarbage(t *testing.T) {
	if _, err := ReadArchive([]byte("这不是归档")); err == nil {
		t.Fatal("垃圾输入应当报错")
	}
	if _, err := ReadArchive(nil); err == nil {
		t.Fatal("空输入应当报错")
	}
}

func TestTreeManifest(t *testing.T) {
	m, err := sampleTree().Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if m.PackID != "demo" || m.Version != "1.0.0" || m.Entry != "index.prompt" {
		t.Fatalf("解析结果不对: %+v", m)
	}
	if m.Runtime != "template" || m.Profile != "chat/1" {
		t.Fatalf("默认值没填: %+v", m)
	}
	if _, err := (Tree{"x": []byte("y")}).Manifest(); err == nil {
		t.Fatal("没有 pack.yaml 时应报错")
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "1.0.1", -1},
		{"1.9.0", "1.10.0", -1},
		{"2.0.0", "1.99.99", 1},
		{"1.0.0-rc1", "1.0.0", -1},
		{"1.0.0-rc1", "1.0.0-rc2", -1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d，期望 %d", c.a, c.b, got, c.want)
		}
	}
}

func TestValidArchivePath(t *testing.T) {
	bad := []string{"", ".", "/a", "../a", "a/../../b", "a\x00b"}
	for _, p := range bad {
		if err := validArchivePath(p); err == nil {
			t.Errorf("应拒绝 %q", p)
		}
	}
	good := []string{"a", "a/b", "a/b/c.txt", "./a/b"}
	for _, p := range good {
		if err := validArchivePath(p); err != nil {
			t.Errorf("应接受 %q: %v", p, err)
		}
	}
}

func TestParseRefRejectsRanges(t *testing.T) {
	if _, err := ParseRef("demo@^1.0"); err == nil || !strings.Contains(err.Error(), "精确版本") {
		t.Fatalf("范围版本应被拒: %v", err)
	}
	if _, err := ParseRef("demo"); err == nil {
		t.Fatal("缺版本应被拒")
	}
	r, err := ParseRef("demo@1.2.3")
	if err != nil || r.ID != "demo" || r.Version != "1.2.3" {
		t.Fatalf("解析失败: %v %+v", err, r)
	}
}

func TestStripHeaderComment(t *testing.T) {
	src := []byte("/* Pack-Id: demo\n * Version: 1.0.0\n * Requires: chat.user.nickname\n */\n你好 {{chat.user.nickname}}\n")
	body, stripped := StripHeaderComment(src)
	if !stripped {
		t.Fatal("应识别出头部注释")
	}
	if strings.Contains(string(body), "Pack-Id") {
		t.Fatalf("注释没剥干净: %q", body)
	}
	if !strings.HasPrefix(string(body), "你好") {
		t.Fatalf("正文开头不对: %q", body)
	}

	if _, s := StripHeaderComment([]byte("/* 这只是一段说明 */\n正文\n")); s {
		t.Error("不像 manifest 的注释不应被剥")
	}

	if _, s := StripHeaderComment([]byte("正文")); s {
		t.Error("无注释时不应剥")
	}

	if (Tree{"a.prompt": src}).Hash() == (Tree{"a.prompt": body}).Hash() {
		t.Error("hash 应当基于原始字节，而不是剥过注释的正文")
	}
}
