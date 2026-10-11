package pack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"
)

const ArchiveExt = ".pack"

func WriteArchive(t Tree) ([]byte, error) {
	if len(t) == 0 {
		return nil, fmt.Errorf("空 pack 树")
	}
	paths := make([]string, 0, len(t))
	for p := range t {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	tw := tar.NewWriter(gz)

	for _, p := range paths {
		clean := path.Clean(p)
		if err := validArchivePath(clean); err != nil {
			return nil, err
		}
		content := t[p]
		hdr := &tar.Header{
			Name:     clean,
			Mode:     0o644,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
			ModTime:  time.Unix(0, 0).UTC(),
			Uid:      0,
			Gid:      0,
			Format:   tar.FormatPAX,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(content); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func ReadArchive(data []byte) (Tree, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("不是合法的 gzip 归档: %w", err)
	}
	defer gz.Close()

	tree := Tree{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("读取归档失败: %w", err)
		}
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("归档含不支持的类型 %v: %s", hdr.Typeflag, hdr.Name)
		}
		clean := path.Clean(strings.TrimPrefix(hdr.Name, "./"))
		if err := validArchivePath(clean); err != nil {
			return nil, err
		}
		if int64(len(tree)) > 0 {
			if _, dup := tree[clean]; dup {
				return nil, fmt.Errorf("归档含重复路径: %s", clean)
			}
		}
		b, err := io.ReadAll(io.LimitReader(tr, maxArchiveEntry))
		if err != nil {
			return nil, err
		}
		if int64(len(b)) != hdr.Size {
			return nil, fmt.Errorf("条目 %s 长度不符（声明 %d，实际 %d）", clean, hdr.Size, len(b))
		}
		tree[clean] = b
	}
	if len(tree) == 0 {
		return nil, fmt.Errorf("归档里没有任何文件")
	}
	return tree, nil
}

const maxArchiveEntry = 64 << 20

func validArchivePath(p string) error {
	if p == "" || p == "." {
		return fmt.Errorf("归档含空路径")
	}
	if path.IsAbs(p) || strings.HasPrefix(p, "/") {
		return fmt.Errorf("归档含绝对路径: %s", p)
	}
	if p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../") {
		return fmt.Errorf("归档含越界路径: %s", p)
	}
	if strings.ContainsRune(p, 0) {
		return fmt.Errorf("归档路径含 NUL")
	}
	return nil
}
