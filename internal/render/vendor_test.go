package render

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yeswearebot/prompt-hub/internal/pack"
)

func TestMaterializeVendor(t *testing.T) {
	dir, err := os.MkdirTemp(".", "ph-ven-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	deps := map[string]pack.Tree{
		"cats-role": {
			"index.php":  []byte("<?php function cats_role() { return '喵'; }"),
			"lib/raw.md": []byte("素材"),
		},
		"another": {"a.txt": []byte("a")},
	}
	if err := MaterializeVendor(dir, deps); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "vendor", "cats-role", "index.php"))
	if err != nil {
		t.Fatalf("依赖入口没铺到位: %v", err)
	}
	if string(b) != "<?php function cats_role() { return '喵'; }" {
		t.Fatalf("依赖内容不对: %q", b)
	}

	if _, err := os.Stat(filepath.Join(dir, "vendor", "cats-role", "lib", "raw.md")); err != nil {
		t.Fatalf("依赖的子目录没保留: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "vendor", "another", "a.txt")); err != nil {
		t.Fatalf("第二个依赖没铺: %v", err)
	}

	empty, err := os.MkdirTemp(".", "ph-ven2-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(empty) })
	if err := MaterializeVendor(empty, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(empty, VendorDir)); !os.IsNotExist(err) {
		t.Error("没有依赖时不该建 vendor 目录")
	}
}

func TestMaterializeVendorRejectsHostileIDs(t *testing.T) {
	dir, err := os.MkdirTemp(".", "ph-ven3-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	for _, id := range []string{"../evil", "a/b", `a\b`, "..", ""} {
		tree := pack.Tree{"x.txt": []byte("x")}
		if err := MaterializeVendor(dir, map[string]pack.Tree{id: tree}); err == nil {
			t.Errorf("应拒绝把 %q 当作路径", id)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil")); err == nil {
		t.Fatal("越界目录被真的创建了")
	}
}
