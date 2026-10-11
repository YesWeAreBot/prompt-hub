package phpfetch

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const releasesURL = "https://downloads.php.net/~windows/releases/releases.json"

const releasesBase = "https://downloads.php.net/~windows/releases/"

var variantPreference = []string{"nts-vs17-x64", "nts-vs16-x64"}

type Release struct {
	Version  string
	Variant  string
	Path     string
	SHA256   string
	Download string
}

type variantDoc struct {
	Zip struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	} `json:"zip"`
}

func PickRelease(doc []byte) (Release, error) {
	var top map[string]map[string]json.RawMessage
	if err := json.Unmarshal(doc, &top); err != nil {
		return Release{}, fmt.Errorf("解析 releases.json 失败: %w", err)
	}
	if len(top) == 0 {
		return Release{}, fmt.Errorf("releases.json 里没有任何版本")
	}

	series := make([]string, 0, len(top))
	for k := range top {
		series = append(series, k)
	}

	sort.Slice(series, func(i, j int) bool { return compareSeries(series[i], series[j]) > 0 })

	for _, s := range series {
		variants := top[s]
		for _, want := range variantPreference {
			raw, ok := variants[want]
			if !ok {
				continue
			}
			var v variantDoc
			if json.Unmarshal(raw, &v) != nil {
				continue
			}
			if v.Zip.Path == "" || v.Zip.SHA256 == "" {
				continue
			}
			version := s
			if vr, ok := variants["version"]; ok {
				var vs string
				if json.Unmarshal(vr, &vs) == nil && vs != "" {
					version = vs
				}
			}
			return Release{
				Version:  version,
				Variant:  want,
				Path:     v.Zip.Path,
				SHA256:   strings.ToLower(v.Zip.SHA256),
				Download: releasesBase + v.Zip.Path,
			}, nil
		}
	}
	return Release{}, fmt.Errorf("releases.json 里没有 %s 变体（需要 %s）",
		strings.Join(variantPreference, " 或 "), runtime.GOARCH)
}

func compareSeries(a, b string) int {
	pa, pb := splitSeries(a), splitSeries(b)
	for i := 0; i < 2; i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func splitSeries(s string) [2]int {
	var out [2]int
	for i, part := range strings.SplitN(s, ".", 2) {
		if i > 1 {
			break
		}
		n, _ := strconv.Atoi(part)
		out[i] = n
	}
	return out
}

func Install(dest string, log io.Writer, client *http.Client) (string, error) {
	if log == nil {
		log = io.Discard
	}
	if runtime.GOOS != "windows" {
		return "", fmt.Errorf("自动获取目前只支持 Windows；其它平台请用发行版包管理器或 " +
			"static-php-cli 装上 php，再把可执行文件放进 $PH_HOME/tools/php/")
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Minute}
	}

	fmt.Fprintf(log, "读取发行版索引 %s\n", releasesURL)
	doc, err := httpGet(client, releasesURL, 8<<20)
	if err != nil {
		return "", err
	}
	rel, err := PickRelease(doc)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(log, "选中 PHP %s（%s，%s）\n", rel.Version, rel.Variant, rel.Path)

	fmt.Fprintf(log, "下载中…\n")
	archive, err := httpGet(client, rel.Download, 256<<20)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(log, "已下载 %d 字节\n", len(archive))

	sum := sha256.Sum256(archive)
	got := hex.EncodeToString(sum[:])
	if got != rel.SHA256 {
		return "", fmt.Errorf("校验失败，已丢弃：官方公布 %s，实际 %s", rel.SHA256, got)
	}
	fmt.Fprintf(log, "sha256 校验通过\n")

	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	n, err := unzip(archive, dest)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(log, "解压 %d 个文件到 %s\n", n, dest)
	return rel.Version, nil
}

func httpGet(client *http.Client, url string, limit int64) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("请求 %s 失败: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("请求 %s 返回 HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, err
	}
	return b, nil
}

func unzip(data []byte, dest string) (int, error) {
	zr, err := zip.NewReader(strings.NewReader(string(data)), int64(len(data)))
	if err != nil {
		return 0, fmt.Errorf("不是合法的 zip: %w", err)
	}
	absDest, err := filepath.Abs(dest)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		clean := filepath.Clean(filepath.FromSlash(f.Name))
		if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			return n, fmt.Errorf("归档含越界路径: %s", f.Name)
		}
		target := filepath.Join(absDest, clean)
		if !strings.HasPrefix(target, absDest+string(os.PathSeparator)) {
			return n, fmt.Errorf("归档条目跑出目标目录: %s", f.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return n, err
		}
		rc, err := f.Open()
		if err != nil {
			return n, err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			rc.Close()
			return n, err
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			rc.Close()
			return n, err
		}
		out.Close()
		rc.Close()
		n++
	}
	return n, nil
}
