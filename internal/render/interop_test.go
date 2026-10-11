package render

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yeswearebot/prompt-hub/internal/pack"
)

func jsCLI(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("没有 node，跳过 JS SDK 互操作测试")
	}
	cli, err := filepath.Abs(filepath.Join("..", "..", "sdk", "js", "cli.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cli); err != nil {
		t.Skipf("找不到 JS SDK：%v", err)
	}
	return cli
}

func runNode(t *testing.T, cli string, args ...string) (string, string) {
	t.Helper()
	cmd := exec.Command("node", append([]string{cli}, args...)...)
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("node %s 失败: %v\nstderr: %s", strings.Join(args, " "), err, errBuf.String())
	}
	return out.String(), errBuf.String()
}

func TestJSSDKAgreesOnTreeHash(t *testing.T) {
	cli := jsCLI(t)
	dirs := []string{"nekochan", "live-data", "nekochan-php"}
	for _, name := range dirs {
		dir := filepath.Join("..", "..", "examples", name)
		loaded, err := pack.Load(dir)
		if err != nil {
			t.Fatalf("加载 %s: %v", dir, err)
		}
		want := loaded.Tree.Hash()
		got, _ := runNode(t, cli, "hash", dir)
		got = strings.TrimSpace(got)
		if got != want {
			t.Errorf("%s 的 tree hash 两种实现不一致：\n  Go: %s\n  JS: %s\n"+
				"（检查排序序与 mode 字面量）", name, want, got)
		}
	}
}

func TestJSSDKAgreesOnNonASCIIPaths(t *testing.T) {
	cli := jsCLI(t)
	dir, err := os.MkdirTemp(".", "ph-uni-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	const supplementary = "\U00020000"
	const bmp = "\uE000"

	files := map[string]string{
		"pack.yaml":               "packId: uni\nversion: 1.0.0\nentry: " + supplementary + ".prompt\n",
		supplementary + ".prompt": "补充平面文件名\n",
		bmp + ".prompt":           "BMP 私用区文件名\n",
		"普通.prompt":               "中文文件名\n",
		"z.txt":                   "普通 ASCII\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Skipf("这台机器的文件系统不接受这个文件名（%q）: %v", name, err)
		}
	}

	loaded, err := pack.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := loaded.Tree.Hash()
	got, _ := runNode(t, cli, "hash", dir)
	got = strings.TrimSpace(got)
	if got != want {
		t.Errorf("含非 ASCII 路径时两种实现不一致：\n  Go: %s\n  JS: %s", want, got)
	}
	t.Logf("非 ASCII 路径下一致: %s", want)
}

func TestJSSDKAgreesOnSingleFileHash(t *testing.T) {
	cli := jsCLI(t)
	dir, err := os.MkdirTemp(".", "ph-single-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	file := filepath.Join(dir, "neko.prompt")
	content := "/* Pack-Id: single-demo\n" +
		" * Version: 2.0.0\n" +
		" * Runtime: template\n" +
		" * Requires: chat.user.nickname\n" +
		" */\n" +
		"你好 {{chat.user.nickname}}\n"
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := pack.Load(file)
	if err != nil {
		t.Fatalf("加载单文件 pack 失败: %v", err)
	}
	if !loaded.SingleFile {
		t.Fatal("应识别为单文件形态")
	}
	want := loaded.Tree.Hash()
	got, _ := runNode(t, cli, "hash", file)
	got = strings.TrimSpace(got)
	if got != want {
		t.Errorf("单文件 pack 的 tree hash 两种实现不一致：\n  Go: %s\n  JS: %s\n"+
			"（检查 canonical JSON 的字段顺序与 HTML 转义是否两侧一致）", want, got)
	}

	if _, ok := loaded.Tree[pack.CanonicalManifestName]; !ok {
		t.Errorf("单文件 pack 的虚拟树里应有 %s", pack.CanonicalManifestName)
	}
	t.Logf("单文件 pack 的 hash 一致且可移植: %s", want)
}

func TestJSSDKAgreesOnArchive(t *testing.T) {
	cli := jsCLI(t)
	dir := filepath.Join("..", "..", "examples", "nekochan-asset")
	loaded, err := pack.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := pack.WriteArchive(loaded.Tree)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "demo"+pack.ArchiveExt)
	if err := os.WriteFile(file, archive, 0o644); err != nil {
		t.Fatal(err)
	}

	want := loaded.Tree.Hash()
	got, _ := runNode(t, cli, "hash", file)
	if strings.TrimSpace(got) != want {
		t.Fatalf("JS 从归档算出的 tree hash 与 Go 不一致：\n  Go（目录）: %s\n  JS（归档）: %s",
			want, strings.TrimSpace(got))
	}

	libDir := filepath.Join("..", "..", "examples", "cats-role")
	lib, err := pack.Load(libDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := map[string]string{"chat.user.nickname": "小兔", "chat.channel.name": "辩论队吹水群"}
	now := time.Date(2026, 10, 11, 6, 0, 0, 0, time.UTC)
	goRes, err := TemplateRuntime{}.Render(Request{
		Dir: dir, Manifest: loaded.Manifest, TreeHash: want,
		Tree: loaded.Tree, Deps: map[string]pack.Tree{"cats-role": lib.Tree},
		Turn: "ev_archive", Now: now, Context: ctx,
	})
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"render", file, "--turn", "ev_archive", "--now", now.Format(time.RFC3339)}
	for k, v := range ctx {
		args = append(args, "--ctx", k+"="+v)
	}
	args = append(args, "--dep", "cats-role="+libDir)
	jsOut, _ := runNode(t, cli, args...)
	if jsOut != goRes.Body {
		t.Errorf("从归档渲染的结果不一致：\n--- Go（目录）---\n%s\n--- JS（归档）---\n%s", goRes.Body, jsOut)
	}
	t.Logf("归档跨语言一致：tree hash %s", want)
}

type interopFixture struct {
	Name    string            `json:"name"`
	Turn    string            `json:"turn"`
	Now     string            `json:"now"`
	Context map[string]string `json:"context"`
	Expect  string            `json:"expect"`
}

func TestJSSDKAgreesOnRender(t *testing.T) {
	cli := jsCLI(t)

	libDir := filepath.Join("..", "..", "examples", "cats-role")
	lib, err := pack.Load(libDir)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		dir     string
		deps    map[string]pack.Tree
		depArgs []string
	}{
		{name: "nekochan", dir: filepath.Join("..", "..", "examples", "nekochan")},
		{
			name:    "nekochan-asset",
			dir:     filepath.Join("..", "..", "examples", "nekochan-asset"),
			deps:    map[string]pack.Tree{"cats-role": lib.Tree},
			depArgs: []string{"--dep", "cats-role=" + libDir},
		},
	}

	rt := TemplateRuntime{}
	checked := 0
	for _, c := range cases {
		loaded, err := pack.Load(c.dir)
		if err != nil {
			t.Fatal(err)
		}
		fixtures, err := os.ReadDir(filepath.Join(c.dir, "fixtures"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range fixtures {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(c.dir, "fixtures", f.Name()))
			if err != nil {
				t.Fatal(err)
			}
			var fx interopFixture
			if err := json.Unmarshal(raw, &fx); err != nil {
				t.Fatal(err)
			}
			now := time.Unix(0, 0)
			if fx.Now != "" {
				parsed, err := time.Parse(time.RFC3339, fx.Now)
				if err != nil {
					t.Fatalf("fixture %s 的 now 不合法: %v", f.Name(), err)
				}
				now = parsed
			}

			goRes, err := rt.Render(Request{
				Dir: c.dir, Manifest: loaded.Manifest, TreeHash: loaded.Tree.Hash(),
				Tree: loaded.Tree, Deps: c.deps,
				Turn: fx.Turn, Now: now, Context: fx.Context,
			})
			if err != nil {
				t.Fatalf("Go 渲染 %s/%s 失败: %v", c.name, f.Name(), err)
			}

			args := []string{"render", c.dir, "--turn", fx.Turn}
			if fx.Now != "" {
				args = append(args, "--now", now.Format(time.RFC3339))
			}
			for k, v := range fx.Context {
				args = append(args, "--ctx", k+"="+v)
			}
			args = append(args, c.depArgs...)
			jsOut, _ := runNode(t, cli, args...)

			if jsOut != goRes.Body {
				t.Errorf("[%s] fixture %s 两种实现渲染结果不一致：\n--- Go ---\n%s\n--- JS ---\n%s",
					c.name, f.Name(), goRes.Body, jsOut)
			}
			checked++
		}
	}
	if checked < 2 {
		t.Fatalf("对拍覆盖太少（%d 个），应当至少覆盖普通 pack 与含 {{asset}} 的 pack", checked)
	}
	t.Logf("对拍了 %d 个 fixture（含 {{asset}} 依赖），正文逐字节一致", checked)
}

func TestRegistryHintIsActionable(t *testing.T) {
	r := NewRegistry()
	r.Register(TemplateRuntime{})
	r.SetHint("php", "ph runtime enable php")

	_, err := r.Get("php")
	if err == nil {
		t.Fatal("未注册的档位必须报错")
	}
	if !strings.Contains(err.Error(), "ph runtime enable php") {
		t.Fatalf("错误信息必须包含启用方式，得到: %v", err)
	}
}
