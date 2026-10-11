package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yeswearebot/prompt-hub/internal/pack"
)

func libTree() map[string]pack.Tree {
	big := strings.Repeat("x", 100<<10)
	return map[string]pack.Tree{
		"cats-role": {

			"persona.md": []byte("【人设】傲娇猫娘\n{{asset 'cats-role/tone.md'}}"),
			"tone.md":    []byte("语气：{{chat.user.nickname}} 专属\n"),

			"deep.md":    []byte("L1 {{asset 'cats-role/deeper.md'}}"),
			"deeper.md":  []byte("L2 {{asset 'cats-role/deepest.md'}}"),
			"deepest.md": []byte("L3"),

			"cyc-a.md": []byte("A {{asset 'cats-role/cyc-b.md'}}"),
			"cyc-b.md": []byte("B {{asset 'cats-role/cyc-a.md'}}"),
			"self.md":  []byte("自己 {{asset 'cats-role/self.md'}}"),

			"binary.dat": {0x00, 0x01, 0x02},
			"huge.txt":   []byte(strings.Repeat("x", maxAssetBytes+1)),

			"bomb0.md": []byte("{{asset 'cats-role/bomb1.md'}}{{asset 'cats-role/bomb1.md'}}"),
			"bomb1.md": []byte("{{asset 'cats-role/bomb2.md'}}{{asset 'cats-role/bomb2.md'}}"),
			"bomb2.md": []byte("{{asset 'cats-role/bomb3.md'}}{{asset 'cats-role/bomb3.md'}}"),
			"bomb3.md": []byte("{{asset 'cats-role/bomb4.md'}}{{asset 'cats-role/bomb4.md'}}"),
			"bomb4.md": []byte(big),
		},
	}
}

func expand(body string, ctx map[string]string, deps map[string]pack.Tree) (string, error) {
	return expandOwn(body, ctx, nil, deps)
}

func expandOwn(body string, ctx map[string]string, own pack.Tree, deps map[string]pack.Tree) (string, error) {
	vars := map[string]string{"turn": "ev", "now": "t", "nowUnix": "0", "packId": "p", "version": "1"}
	for k, v := range ctx {
		vars[k] = v
	}
	out, unknown, err := expandTemplate(body, vars, own, deps)
	if err != nil {
		return "", err
	}
	if len(unknown) > 0 {
		return "", formatUnknown(unknown)
	}
	return out, nil
}

func TestParseAssetArg(t *testing.T) {

	for _, s := range []string{"chat.user.nickname", "assetx", "asset-foo"} {
		if _, isAsset, _ := parseAssetArg(s); isAsset {
			t.Errorf("%q 不该被当成资产引用", s)
		}
	}
	for _, s := range []string{"asset 'a/b.md'", `asset "a/b.md"`, "asset   'a/b.md'"} {
		ref, isAsset, err := parseAssetArg(s)
		if !isAsset || err != nil || ref != "a/b.md" {
			t.Errorf("%q 解析失败: ref=%q isAsset=%v err=%v", s, ref, isAsset, err)
		}
	}

	for _, s := range []string{"asset", "asset a/b.md", "asset 'a/b.md"} {
		if _, isAsset, err := parseAssetArg(s); !isAsset || err == nil {
			t.Errorf("%q 应当报「写法不对」: isAsset=%v err=%v", s, isAsset, err)
		}
	}
}

func TestAssetRecursion(t *testing.T) {
	got, err := expand("{{asset 'cats-role/persona.md'}}",
		map[string]string{"chat.user.nickname": "小兔"}, libTree())
	if err != nil {
		t.Fatal(err)
	}

	want := "【人设】傲娇猫娘\n语气：小兔 专属\n"
	if got != want {
		t.Fatalf("递归展开结果不对：\n得到 %q\n期望 %q", got, want)
	}
}

func TestAssetRecursionDepth(t *testing.T) {
	got, err := expand("{{asset 'cats-role/deep.md'}}", nil, libTree())
	if err != nil {
		t.Fatal(err)
	}
	if got != "L1 L2 L3" {
		t.Fatalf("多层嵌套结果不对: %q", got)
	}
}

func TestAssetCycleIsReported(t *testing.T) {
	for _, ref := range []string{"cats-role/cyc-a.md", "cats-role/self.md"} {
		_, err := expand("{{asset '"+ref+"'}}", nil, libTree())
		if err == nil {
			t.Fatalf("%s 应当报循环引用", ref)
		}
		if !strings.Contains(err.Error(), "循环引用") {
			t.Fatalf("应报循环引用: %v", err)
		}
		if !strings.Contains(err.Error(), "→") {
			t.Fatalf("应给出完整的链: %v", err)
		}
	}
}

func TestAssetExpansionBombIsBounded(t *testing.T) {
	_, err := expand("{{asset 'cats-role/bomb0.md'}}", nil, libTree())
	if err == nil {
		t.Fatal("指数膨胀必须被拦住")
	}
	if !strings.Contains(err.Error(), "总量上限") {
		t.Fatalf("应报总量上限: %v", err)
	}
}

func TestAssetErrors(t *testing.T) {
	deps := libTree()

	if _, err := expand("{{asset 'other/x.md'}}", nil, deps); err == nil ||
		!strings.Contains(err.Error(), "includes") {
		t.Fatalf("应提示在 includes 里声明: %v", err)
	}

	if _, err := expand("{{asset 'cats-role/nope.md'}}", nil, deps); err == nil ||
		!strings.Contains(err.Error(), "persona.md") {
		t.Fatalf("应列出可用的文件: %v", err)
	}

	if _, err := expand("{{asset 'cats-role/binary.dat'}}", nil, deps); err == nil ||
		!strings.Contains(err.Error(), "二进制") {
		t.Fatalf("二进制资产应被拒: %v", err)
	}

	if _, err := expand("{{asset 'cats-role/huge.txt'}}", nil, deps); err == nil ||
		!strings.Contains(err.Error(), "上限") {
		t.Fatalf("超大资产应被拒: %v", err)
	}

	for _, bad := range []string{"cats-role/../secret", "cats-role/a/../../b", "cats-role/", "/x.md"} {
		if _, err := expand("{{asset '"+bad+"'}}", nil, deps); err == nil {
			t.Errorf("路径 %q 应被拒", bad)
		}
	}
}

func TestUnknownVarInsideAssetNamesItsSource(t *testing.T) {
	_, err := expand("{{asset 'cats-role/persona.md'}}", nil, libTree())
	if err == nil {
		t.Fatal("应当报未知变量")
	}
	if !strings.Contains(err.Error(), "chat.user.nickname") {
		t.Fatalf("应点名变量: %v", err)
	}
	if !strings.Contains(err.Error(), "cats-role/tone.md") {
		t.Fatalf("应说明它来自哪个资产: %v", err)
	}
}

func TestAssetRefs(t *testing.T) {
	body := "{{asset 'cats-role/persona.md'}} {{chat.user.nickname}} {{asset \"cats-role/tone.md\"}}\n"
	refs := AssetRefs(body)
	if len(refs) != 2 || refs[0] != "cats-role/persona.md" || refs[1] != "cats-role/tone.md" {
		t.Fatalf("应取出两个直接引用: %v", refs)
	}
}

func TestAssetClosureAndUndeclaredVars(t *testing.T) {

	assets, vars, err := AssetClosure("{{asset 'cats-role/persona.md'}}", nil, libTree())
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 2 {
		t.Fatalf("闭包应含两个资产: %v", assets)
	}
	if len(vars) != 1 || vars[0] != "chat.user.nickname" {
		t.Fatalf("闭包变量不对: %v", vars)
	}

	if _, vars, _ := AssetClosure("{{turn}} {{asset 'cats-role/tone.md'}}", nil, libTree()); len(vars) != 1 {
		t.Fatalf("内建变量应被排除: %v", vars)
	}

	if bad, err := UndeclaredAssetVars("{{asset 'cats-role/persona.md'}}", nil,
		[]string{"chat.user.nickname"}, libTree()); err != nil || len(bad) != 0 {
		t.Fatalf("已声明不该报: %v %v", bad, err)
	}
	bad, err := UndeclaredAssetVars("{{asset 'cats-role/persona.md'}}", nil, nil, libTree())
	if err != nil || len(bad) != 1 || bad[0] != "chat.user.nickname" {
		t.Fatalf("未声明应被点出: %v %v", bad, err)
	}
}

func TestCheckAssets(t *testing.T) {
	body := "{{asset 'cats-role/persona.md'}}\n"
	deps := libTree()

	if err := CheckAssets(body, nil, []string{"cats-role@1.0.0"}, deps); err != nil {
		t.Fatalf("应当通过: %v", err)
	}
	if err := CheckAssets(body, nil, nil, deps); err == nil || !strings.Contains(err.Error(), "includes") {
		t.Fatalf("应报缺 includes: %v", err)
	}
	if err := CheckAssets("{{asset 'cats-role/nope.md'}}", nil, []string{"cats-role@1.0.0"}, deps); err == nil ||
		!strings.Contains(err.Error(), "persona.md") {
		t.Fatalf("应点名可用文件: %v", err)
	}
	if err := CheckAssets("{{asset cats-role/persona.md}}", nil, []string{"cats-role@1.0.0"}, deps); err == nil {
		t.Fatal("缺引号应当报错")
	}

	if err := CheckAssets(body, nil, nil, nil); err == nil {
		t.Fatal("deps 为 nil 时也应查 includes 声明")
	}
	if err := CheckAssets(body, nil, []string{"cats-role@1.0.0"}, nil); err != nil {
		t.Fatalf("deps 为 nil 时不该报文件不存在: %v", err)
	}
}

func ownTree() pack.Tree {
	return pack.Tree{
		"index.prompt":     []byte("{{asset './partials/head.md'}}正文"),
		"partials/head.md": []byte("【头】{{chat.user.nickname}}\n"),
		"partials/body.md": []byte("【体】{{asset './partials/head.md'}}"),
		"selfloop.md":      []byte("{{asset './selfloop.md'}}"),
	}
}

func TestOwnAssetReference(t *testing.T) {

	got, err := expandOwn("{{asset './partials/head.md'}}",
		map[string]string{"chat.user.nickname": "小兔"}, ownTree(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "【头】小兔\n" {
		t.Fatalf("结果不对: %q", got)
	}
}

func TestOwnAssetRecursion(t *testing.T) {

	got, err := expandOwn("{{asset './partials/body.md'}}",
		map[string]string{"chat.user.nickname": "小兔"}, ownTree(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "【体】【头】小兔\n" {
		t.Fatalf("自己的文件之间没能递归: %q", got)
	}
}

func TestOwnAssetErrors(t *testing.T) {

	_, err := expandOwn("{{asset './nope.md'}}", nil, ownTree(), nil)
	if err == nil || !strings.Contains(err.Error(), "本 pack") ||
		!strings.Contains(err.Error(), "partials/head.md") {
		t.Fatalf("错误信息应说明是「本 pack」并列出文件: %v", err)
	}

	if _, err := expandOwn("{{asset './selfloop.md'}}", nil, ownTree(), nil); err == nil ||
		!strings.Contains(err.Error(), "循环引用") {
		t.Fatalf("应报循环引用: %v", err)
	}

	for _, bad := range []string{"../secret", "/etc/passwd", "./../x", "./", "a/../../b"} {
		if _, err := expandOwn("{{asset '"+bad+"'}}", nil, ownTree(), nil); err == nil {
			t.Errorf("%q 应被拒", bad)
		}
	}
}

func TestCheckAssetsVerifiesOwnRefsWithoutDeps(t *testing.T) {
	own := ownTree()
	if err := CheckAssets("{{asset './partials/head.md'}}", own, nil, nil); err != nil {
		t.Fatalf("引用自己的文件不该要求 includes: %v", err)
	}
	if err := CheckAssets("{{asset './nope.md'}}", own, nil, nil); err == nil {
		t.Fatal("自己的文件不存在，即使依赖没装也要查出来")
	}

	if err := CheckAssets("{{asset './partials/body.md'}}", own, nil, nil); err != nil {
		t.Fatalf("递归进去的自己文件应当通过: %v", err)
	}
}

func TestTemplateRuntimeInlinesAsset(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.prompt"),
		[]byte("{{asset 'cats-role/persona.md'}}你好 {{chat.user.nickname}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := Request{
		Dir: dir,
		Manifest: &pack.Manifest{
			PackID: "demo", Version: "1.0.0", Entry: "index.prompt", Runtime: "template",
			OutputMax: 8000, Requires: []string{"chat.user.nickname"},
		},
		Tree:    ownTree(),
		Deps:    libTree(),
		Now:     time.Unix(0, 0),
		Context: map[string]string{"chat.user.nickname": "小兔"},
	}
	res, err := TemplateRuntime{}.Render(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Body, "傲娇猫娘") || !strings.Contains(res.Body, "你好 小兔") {
		t.Fatalf("资产没被内联: %q", res.Body)
	}

	if !strings.Contains(res.Body, "语气：小兔 专属") {
		t.Fatalf("嵌套资产没被展开: %q", res.Body)
	}
}

func TestBadAssetSyntaxIsNotReportedAsUnknownVariable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.prompt"),
		[]byte("{{asset cats-role/persona.md}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := Request{
		Dir: dir,
		Manifest: &pack.Manifest{
			PackID: "demo", Version: "1.0.0", Entry: "index.prompt", Runtime: "template",
			OutputMax: 8000,
		},
		Deps: libTree(),
	}
	_, err := TemplateRuntime{}.Render(req)
	if err == nil {
		t.Fatal("应当报错")
	}
	if strings.Contains(err.Error(), "未提供的变量") {
		t.Fatalf("应报写法错误，而不是未知变量: %v", err)
	}
	if !strings.Contains(err.Error(), "引号") {
		t.Fatalf("错误信息应说明要加引号: %v", err)
	}
}
