package render

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

//go:embed sdk/php/sdk.php
var phpSDK []byte

const SDKRelPath = "_ph/sdk.php"

func phpEntryArgs(limits Limits) []string {
	args := []string{
		"-n",
		"-d", "display_errors=stderr",
		"-d", "log_errors=0",
		"-d", "error_reporting=E_ALL",
	}
	if limits.MemoryBytes > 0 {
		args = append(args, "-d", fmt.Sprintf("memory_limit=%dM", limits.MemoryBytes>>20))
	}
	return args
}

func FindPHP(explicit, home string) (string, string) {
	candidates := []string{}
	if explicit != "" {
		candidates = append(candidates, explicit)
	}
	if home != "" {
		candidates = append(candidates,
			filepath.Join(home, "tools", "php", "php.exe"),
			filepath.Join(home, "tools", "php", "php"),
			filepath.Join(home, "tools", "php", "bin", "php"),
		)
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, ""
		}
	}
	if p, err := exec.LookPath("php"); err == nil {
		return p, ""
	}
	if explicit != "" {
		return "", fmt.Sprintf("指定的 php 不存在: %s", explicit)
	}
	return "", "未找到 php（可放在 " + filepath.Join(home, "tools", "php") + " 或加入 PATH）"
}

type phpViewBuilder struct {
	root string
}

func (b phpViewBuilder) Build(req Request) (string, func(), error) {
	if b.root == "" {
		return "", nil, fmt.Errorf("未配置视图根目录")
	}

	root, err := filepath.Abs(b.root)
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp(root, "view-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	if err := writeTree(dir, req.Tree); err != nil {
		cleanup()
		return "", nil, err
	}

	if err := MaterializeVendor(dir, req.Deps); err != nil {
		cleanup()
		return "", nil, err
	}
	sdkAbs := filepath.Join(dir, filepath.FromSlash(SDKRelPath))
	if err := os.MkdirAll(filepath.Dir(sdkAbs), 0o755); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := os.WriteFile(sdkAbs, phpSDK, 0o644); err != nil {
		cleanup()
		return "", nil, err
	}
	return dir, cleanup, nil
}

func NewPHPRuntime(bin, viewRoot string, limits Limits) *ProcessRuntime {
	l := limits.Normalized()
	return NewProcessRuntime("php", bin, phpEntryArgs(l), l, phpViewBuilder{root: viewRoot})
}
