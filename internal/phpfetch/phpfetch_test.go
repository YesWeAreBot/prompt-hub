package phpfetch

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPickReleasePrefersNewestSeries(t *testing.T) {
	doc := []byte(`{
	  "8.4": {"version":"8.4.26","nts-vs17-x64":{"zip":{"path":"php-8.4.26-nts-Win32-vs17-x64.zip","sha256":"AAAA"}}},
	  "8.10": {"version":"8.10.1","nts-vs17-x64":{"zip":{"path":"php-8.10.1-nts-Win32-vs17-x64.zip","sha256":"BBBB"}}},
	  "9.0": {"version":"9.0.0"}
	}`)
	rel, err := PickRelease(doc)
	if err != nil {
		t.Fatal(err)
	}

	if rel.Version != "8.10.1" {
		t.Fatalf("选中 %s，期望 8.10.1", rel.Version)
	}
	if rel.Variant != "nts-vs17-x64" {
		t.Fatalf("变体 %s", rel.Variant)
	}
	if rel.SHA256 != "bbbb" {
		t.Fatalf("校验值应规范为小写，得到 %s", rel.SHA256)
	}
	if !strings.HasPrefix(rel.Download, releasesBase) {
		t.Fatalf("下载地址不对: %s", rel.Download)
	}
}

func TestPickReleaseFallsBackVariant(t *testing.T) {
	doc := []byte(`{
	  "8.3": {"version":"8.3.35","nts-vs16-x64":{"zip":{"path":"php-8.3.35-nts-Win32-vs16-x64.zip","sha256":"CCCC"}}}
	}`)
	rel, err := PickRelease(doc)
	if err != nil {
		t.Fatal(err)
	}
	if rel.Variant != "nts-vs16-x64" {
		t.Fatalf("应退回到 vs16 变体，得到 %s", rel.Variant)
	}
}

func TestPickReleaseErrorsWithoutUsableVariant(t *testing.T) {
	cases := []string{
		`{}`,
		`{"8.4": {"version":"8.4.26"}}`,
		`{"8.4": {"version":"8.4.26","ts-vs17-x64":{"zip":{"path":"x.zip","sha256":"D"}}}}`,
		`{"8.4": {"version":"8.4.26","nts-vs17-x64":{"zip":{"path":"","sha256":""}}}}`,
		`不是 JSON`,
	}
	for _, c := range cases {
		if _, err := PickRelease([]byte(c)); err == nil {
			t.Errorf("应报错: %s", c)
		}
	}
}

func TestUnzipRejectsTraversal(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("../evil.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	dest, err := os.MkdirTemp(".", "ph-zip-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dest) })

	if _, err := unzip(buf.Bytes(), dest); err == nil {
		t.Fatal("越界条目必须被拒")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "evil.txt")); err == nil {
		t.Fatal("越界文件被真的写出来了")
	}
}

func TestUnzipWritesNormalEntries(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range map[string]string{"php.exe": "binary", "ext/php_pdo.dll": "dll"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	dest, err := os.MkdirTemp(".", "ph-zip-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dest) })

	n, err := unzip(buf.Bytes(), dest)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("解出 %d 个文件，期望 2", n)
	}
	b, err := os.ReadFile(filepath.Join(dest, "ext", "php_pdo.dll"))
	if err != nil || string(b) != "dll" {
		t.Fatalf("子目录里的文件不对: %v %q", err, b)
	}
}
