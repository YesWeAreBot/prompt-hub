package render

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yeswearebot/prompt-hub/internal/pack"
)

const VendorDir = "vendor"

func writeTree(dst string, tree pack.Tree) error {
	for rel, content := range tree {
		abs := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(abs, content, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func MaterializeVendor(viewDir string, deps map[string]pack.Tree) error {
	if len(deps) == 0 {
		return nil
	}
	ids := make([]string, 0, len(deps))
	for id := range deps {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {

		if id == "" || strings.ContainsAny(id, `/\:`) || strings.Contains(id, "..") {
			return fmt.Errorf("依赖 id %q 不能作为路径", id)
		}
		if err := writeTree(filepath.Join(viewDir, VendorDir, id), deps[id]); err != nil {
			return fmt.Errorf("铺开依赖 %s 失败: %w", id, err)
		}
	}
	return nil
}
