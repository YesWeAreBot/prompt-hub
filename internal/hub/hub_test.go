package hub

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeswearebot/prompt-hub/internal/pack"
)

func archiveFor(t *testing.T, id, version, body string) []byte {
	t.Helper()
	tree := pack.Tree{
		"pack.yaml":    []byte(fmt.Sprintf("packId: %s\nversion: %s\nentry: index.prompt\n", id, version)),
		"index.prompt": []byte(body),
	}
	b, err := pack.WriteArchive(tree)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newTestHub(t *testing.T, token string) (*httptest.Server, *Registry) {
	t.Helper()
	root, err := os.MkdirTemp(".", "ph-hub-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	reg := NewRegistry(root)
	srv := httptest.NewServer((&Server{
		Reg: reg, PublishToken: token,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}).Handler())
	t.Cleanup(srv.Close)
	return srv, reg
}

func TestHubPublishIndexFetch(t *testing.T) {
	srv, reg := newTestHub(t, "")
	client := NewClient(srv.URL, "")

	archive := archiveFor(t, "demo", "1.0.0", "你好")
	if _, err := client.Publish(archiveTree(t, archive)); err != nil {
		t.Fatal(err)
	}

	idx, err := client.Index()
	if err != nil {
		t.Fatal(err)
	}
	if len(idx) != 1 || idx[0].Ref() != "demo@1.0.0" {
		t.Fatalf("索引不对: %+v", idx)
	}

	meta, got, err := client.Fetch("demo", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if meta.TreeHash == "" || meta.Manifest.PackID != "demo" {
		t.Fatalf("元数据不对: %+v", meta)
	}
	tree, err := pack.ReadArchive(got)
	if err != nil {
		t.Fatal(err)
	}
	if tree.Hash() != meta.TreeHash {
		t.Fatalf("取回的归档与元数据不一致")
	}

	if _, err := reg.Publish(archive); err != nil {
		t.Fatalf("重复发布同样内容应幂等: %v", err)
	}
	idx, _ = client.Index()
	if len(idx) != 1 {
		t.Fatalf("幂等发布后索引变了: %d 项", len(idx))
	}
}

func archiveTree(t *testing.T, archive []byte) string {
	t.Helper()
	tree, err := pack.ReadArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(".", "ph-pub-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	for p, b := range tree {
		abs := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestHubRejectsSameVersionDifferentContent(t *testing.T) {
	srv, _ := newTestHub(t, "")
	client := NewClient(srv.URL, "")

	first := archiveFor(t, "demo", "1.0.0", "第一版")
	if _, err := client.Publish(archiveTree(t, first)); err != nil {
		t.Fatal(err)
	}

	second := archiveFor(t, "demo", "1.0.0", "第二版")
	_, err := client.Publish(archiveTree(t, second))
	if err == nil {
		t.Fatal("同版本不同内容必须被拒")
	}
	if !strings.Contains(err.Error(), "同版本已存在不同内容") {
		t.Fatalf("错误信息不对: %v", err)
	}

	resp, err := http.Post(srv.URL+"/v1/packs", "application/octet-stream", bytes.NewReader(second))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("状态 %d，期望 409", resp.StatusCode)
	}
}

func TestFetchDetectsTamperedBlob(t *testing.T) {
	srv, reg := newTestHub(t, "")
	client := NewClient(srv.URL, "")

	good := archiveFor(t, "demo", "1.0.0", "原始内容")
	if _, err := client.Publish(archiveTree(t, good)); err != nil {
		t.Fatal(err)
	}
	meta, _, err := client.Fetch("demo", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}

	evil := archiveFor(t, "demo", "1.0.0", "被替换的内容")
	if err := os.WriteFile(reg.blobPath(meta.TreeHash), evil, 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err = client.Fetch("demo", "1.0.0")
	if err == nil {
		t.Fatal("被篡改的 blob 必须被拒")
	}
	if !strings.Contains(err.Error(), "内容校验失败") {
		t.Fatalf("错误信息不对: %v", err)
	}
}

func TestHubPublishRequiresToken(t *testing.T) {
	srv, _ := newTestHub(t, "s3cret")

	archive := archiveFor(t, "demo", "1.0.0", "x")
	if _, err := NewClient(srv.URL, "").Publish(archiveTree(t, archive)); err == nil {
		t.Fatal("没有 token 的发布必须被拒")
	}
	if _, err := NewClient(srv.URL, "s3cret").Publish(archiveTree(t, archiveFor(t, "demo", "1.0.0", "x"))); err != nil {
		t.Fatalf("带正确 token 应当成功: %v", err)
	}
}

func TestHubRejectsBadArchive(t *testing.T) {
	srv, _ := newTestHub(t, "")
	resp, err := http.Post(srv.URL+"/v1/packs", "application/octet-stream", strings.NewReader("这不是归档"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("状态 %d，期望 400", resp.StatusCode)
	}
}

func TestLatestPicksHighestVersion(t *testing.T) {
	srv, _ := newTestHub(t, "")
	client := NewClient(srv.URL, "")
	for _, v := range []string{"1.9.0", "1.10.0", "1.2.0"} {
		if _, err := client.Publish(archiveTree(t, archiveFor(t, "demo", v, v))); err != nil {
			t.Fatal(err)
		}
	}

	latest, err := client.Latest("demo")
	if err != nil {
		t.Fatal(err)
	}
	if latest.Manifest.Version != "1.10.0" {
		t.Fatalf("Latest 得到 %s，期望 1.10.0", latest.Manifest.Version)
	}
}

func archiveWith(t *testing.T, manifestYaml string, files map[string]string) []byte {
	t.Helper()
	tree := pack.Tree{pack.ManifestName: []byte(manifestYaml)}
	for name, content := range files {
		tree[name] = []byte(content)
	}
	b, err := pack.WriteArchive(tree)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPublishRecordsMissingDeps(t *testing.T) {
	srv, _ := newTestHub(t, "")
	client := NewClient(srv.URL, "")

	top := archiveWith(t,
		"packId: top\nversion: 1.0.0\nentry: i.prompt\nruntime: template\nincludes:\n  - lib@1.0.0\n",
		map[string]string{"i.prompt": "x"})
	meta, err := client.Publish(archiveTree(t, top))
	if err != nil {
		t.Fatalf("缺依赖不该阻止发布: %v", err)
	}
	if len(meta.MissingDeps) != 1 || meta.MissingDeps[0] != "lib@1.0.0" {
		t.Fatalf("MissingDeps = %v，期望 [lib@1.0.0]", meta.MissingDeps)
	}

	got, err := client.Get("top", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.MissingDeps) != 1 {
		t.Fatalf("读回来的元数据丢了 MissingDeps: %+v", got.MissingDeps)
	}

	lib := archiveWith(t, "packId: lib\nversion: 1.0.0\nkind: library\nexports:\n  - l\n",
		map[string]string{"l.php": "<?php\n"})
	if _, err := client.Publish(archiveTree(t, lib)); err != nil {
		t.Fatal(err)
	}

	old, err := client.Get("top", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(old.MissingDeps) != 0 {
		t.Fatalf("依赖已发布，旧版本也不该再报缺: %v", old.MissingDeps)
	}

	top2 := archiveWith(t,
		"packId: top\nversion: 1.1.0\nentry: i.prompt\nruntime: template\nincludes:\n  - lib@1.0.0\n",
		map[string]string{"i.prompt": "x"})
	meta2, err := client.Publish(archiveTree(t, top2))
	if err != nil {
		t.Fatal(err)
	}
	if len(meta2.MissingDeps) != 0 {
		t.Fatalf("依赖已存在，不该再报缺: %v", meta2.MissingDeps)
	}
}

func TestPublishRejectsIncludingContentPack(t *testing.T) {
	srv, _ := newTestHub(t, "")
	client := NewClient(srv.URL, "")

	other := archiveWith(t, "packId: other\nversion: 1.0.0\nentry: i.prompt\n",
		map[string]string{"i.prompt": "y"})
	if _, err := client.Publish(archiveTree(t, other)); err != nil {
		t.Fatal(err)
	}

	bad := archiveWith(t,
		"packId: top\nversion: 1.0.0\nentry: i.prompt\nruntime: template\nincludes:\n  - other@1.0.0\n",
		map[string]string{"i.prompt": "x"})
	_, err := client.Publish(archiveTree(t, bad))
	if err == nil {
		t.Fatal("include 一个 content pack 必须被拒")
	}
	if !strings.Contains(err.Error(), "library") {
		t.Fatalf("错误信息应说明只有 library pack 能被 include: %v", err)
	}
}

func TestCapabilityDiffWidening(t *testing.T) {
	base := pack.Manifest{
		PackID: "demo", Version: "1.0.0", Runtime: "template", Profile: "chat/1",
		Requires: []string{"chat.user.nickname"}, OutputMax: 1000,
	}

	same := base
	same.Version = "1.0.1"
	if d := DiffCapabilities(base, same); d.Widening() {
		t.Fatalf("内容迭代不该算扩大能力: %+v", d)
	}

	more := base
	more.Version = "1.1.0"
	more.Requires = []string{"chat.user.nickname", "chat.memory.recent"}
	d := DiffCapabilities(base, more)
	if !d.Widening() {
		t.Fatal("新增必需字段必须算扩大能力")
	}
	if len(d.AddedRequires) != 1 || d.AddedRequires[0] != "chat.memory.recent" {
		t.Fatalf("差异不对: %+v", d.AddedRequires)
	}
	if !strings.Contains(d.String(), "chat.memory.recent") {
		t.Fatalf("输出里应能看到新增字段: %s", d.String())
	}

	net := base
	net.Version = "1.1.0"
	net.Egress = []string{"api.example.com"}
	if d := DiffCapabilities(base, net); !d.Widening() {
		t.Fatal("新增出网目标必须算扩大能力")
	}

	big := base
	big.Version = "1.1.0"
	big.OutputMax = 4000
	if d := DiffCapabilities(base, big); !d.Widening() {
		t.Fatal("输出预算变大必须算扩大能力")
	}

	rt := base
	rt.Version = "1.1.0"
	rt.Runtime = "php"
	if d := DiffCapabilities(base, rt); !d.Widening() {
		t.Fatal("换运行时必须算扩大能力")
	}

	less := more
	less.Version = "1.2.0"
	less.Requires = []string{"chat.user.nickname"}
	less.OutputMax = 500
	if d := DiffCapabilities(more, less); d.Widening() {
		t.Fatalf("收敛方向不该算扩大能力: %+v", d)
	}
}
