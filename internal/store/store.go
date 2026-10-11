package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yeswearebot/prompt-hub/internal/pack"
)

const installedMarker = ".installed"

type Source string

const (
	SourceStore Source = "store"

	SourceEmbed Source = "embed"
)

type Resolved struct {
	Dir      string
	TreeHash string
	Source   Source
}

type Resolver struct {
	Store *Store

	Embed []string
}

func EmbedDirName(id, version string) string { return id + "@" + version }

func (r *Resolver) Resolve(id, version string) (Resolved, error) {
	if dir, hash, err := r.Store.Resolve(id, version); err == nil {
		return Resolved{Dir: dir, TreeHash: hash, Source: SourceStore}, nil
	} else {
		var notInstalled *NotInstalledError
		if !errors.As(err, &notInstalled) {
			return Resolved{}, err
		}
	}
	for _, root := range r.Embed {
		dir := filepath.Join(root, EmbedDirName(id, version))
		st, err := os.Stat(dir)
		if err != nil || !st.IsDir() {
			continue
		}
		loaded, err := pack.Load(dir)
		if err != nil {
			continue
		}
		return Resolved{Dir: dir, TreeHash: loaded.Tree.Hash(), Source: SourceEmbed}, nil
	}
	return Resolved{}, &NotInstalledError{ID: id, Version: version, EmbedDirs: len(r.Embed)}
}

func (r *Resolver) All() ([]string, error) {
	seen := map[string]bool{}
	out, err := r.Store.All()
	if err != nil {
		return nil, err
	}
	for _, ref := range out {
		seen[ref] = true
	}
	for _, root := range r.Embed {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if _, err := pack.ParseRef(e.Name()); err != nil {
				continue
			}
			if !seen[e.Name()] {
				seen[e.Name()] = true
				out = append(out, e.Name())
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func (r *Resolver) ResolveClosure(root *pack.LoadResult) (map[string]pack.Tree, error) {
	return r.resolveClosure(root, map[string]bool{})
}

func (r *Resolver) resolveClosure(root *pack.LoadResult, visited map[string]bool) (map[string]pack.Tree, error) {
	lock, err := pack.LockFromTree(root.Tree)
	if err != nil {
		return nil, err
	}
	refs, err := root.Manifest.ExactRefs(lock)
	if err != nil {
		return nil, err
	}

	out := map[string]pack.Tree{}
	for _, ref := range refs {
		if visited[ref.String()] {
			continue
		}
		visited[ref.String()] = true

		resolved, err := r.Resolve(ref.ID, ref.Version)
		if err != nil {
			return nil, fmt.Errorf("依赖 %s 不可用: %w", ref, err)
		}
		loaded, err := pack.Load(resolved.Dir)
		if err != nil {
			return nil, fmt.Errorf("依赖 %s 加载失败: %w", ref, err)
		}
		if loaded.Manifest.Kind != "library" {
			return nil, fmt.Errorf("依赖 %s 是 %s pack；只有 library pack 能被 include",
				ref, loaded.Manifest.Kind)
		}
		if _, dup := out[ref.ID]; dup {
			return nil, fmt.Errorf("依赖 %s 出现了两个不同版本，无法确定用哪一个", ref.ID)
		}
		out[ref.ID] = loaded.Tree

		sub, err := r.resolveClosure(loaded, visited)
		if err != nil {
			return nil, err
		}
		for id, tree := range sub {
			if _, dup := out[id]; dup {
				return nil, fmt.Errorf("依赖 %s 被多个版本引入，无法确定用哪一个", id)
			}
			out[id] = tree
		}
	}
	return out, nil
}

func (r *Resolver) Versions(id string) []string {
	seen := map[string]bool{}
	if entries, err := os.ReadDir(filepath.Join(r.Store.Root, id)); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				seen[e.Name()] = true
			}
		}
	}
	want := id + "@"
	for _, root := range r.Embed {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || !strings.HasPrefix(e.Name(), want) {
				continue
			}
			if _, err := pack.ParseRef(e.Name()); err == nil {
				seen[strings.TrimPrefix(e.Name(), want)] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

type Store struct {
	Root string
}

func New(root string) *Store { return &Store{Root: root} }

func (s *Store) Dir(id, version, treeHash string) string {
	return filepath.Join(s.Root, id, version, treeHash)
}

func (s *Store) Install(res *pack.LoadResult) (dir string, err error) {
	m := res.Manifest
	hash := res.Tree.Hash()

	verDir := filepath.Join(s.Root, m.PackID, m.Version)
	if entries, rerr := os.ReadDir(verDir); rerr == nil {
		for _, e := range entries {
			if e.IsDir() && e.Name() != hash {
				return "", &VersionConflictError{
					ID: m.PackID, Version: m.Version,
					Existing: e.Name(), Incoming: hash,
				}
			}
		}
	}

	dir = s.Dir(m.PackID, m.Version, hash)
	if _, statErr := os.Stat(filepath.Join(dir, installedMarker)); statErr == nil {
		return dir, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for rel, content := range res.Tree {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(abs, content, 0o644); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, installedMarker), []byte(hash+"\n"), 0o644); err != nil {
		return "", err
	}
	return dir, nil
}

func (s *Store) Resolve(id, version string) (dir, treeHash string, err error) {
	verDir := filepath.Join(s.Root, id, version)
	entries, err := os.ReadDir(verDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", &NotInstalledError{ID: id, Version: version}
		}
		return "", "", err
	}
	var hashes []string
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "sha256-") {
			continue
		}

		if _, err := os.Stat(filepath.Join(verDir, e.Name(), installedMarker)); err != nil {
			continue
		}
		hashes = append(hashes, e.Name())
	}
	if len(hashes) == 0 {
		return "", "", &NotInstalledError{ID: id, Version: version}
	}
	if len(hashes) > 1 {
		sort.Strings(hashes)
		return "", "", fmt.Errorf("store 中 %s@%s 有多个 tree hash（%s），需要 ph gc 清理",
			id, version, strings.Join(hashes, ", "))
	}
	return s.Dir(id, version, hashes[0]), hashes[0], nil
}

func (s *Store) Installed(id string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.Root, id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *Store) All() ([]string, error) {
	ids, err := os.ReadDir(s.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, id := range ids {
		if !id.IsDir() {
			continue
		}
		vers, _ := s.Installed(id.Name())
		for _, v := range vers {
			out = append(out, id.Name()+"@"+v)
		}
	}
	return out, nil
}

func (s *Store) Remove(id, version string) error {
	dir := filepath.Join(s.Root, id, version)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return &NotInstalledError{ID: id, Version: version}
		}
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if entries, err := os.ReadDir(filepath.Join(s.Root, id)); err == nil && len(entries) == 0 {
		_ = os.Remove(filepath.Join(s.Root, id))
	}
	return nil
}

func (s *Store) GC() ([]string, error) {
	var removed []string
	ids, err := os.ReadDir(s.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	for _, id := range ids {
		if !id.IsDir() {
			continue
		}
		idDir := filepath.Join(s.Root, id.Name())
		versions, _ := os.ReadDir(idDir)
		for _, v := range versions {
			if !v.IsDir() {
				continue
			}
			verDir := filepath.Join(idDir, v.Name())
			trees, _ := os.ReadDir(verDir)
			live := 0
			for _, t := range trees {
				if !t.IsDir() {
					continue
				}
				treeDir := filepath.Join(verDir, t.Name())
				if _, err := os.Stat(filepath.Join(treeDir, installedMarker)); err == nil {
					live++
					continue
				}
				if err := os.RemoveAll(treeDir); err == nil {
					removed = append(removed, treeDir)
				}
			}
			if live == 0 {
				if entries, _ := os.ReadDir(verDir); len(entries) == 0 {
					if err := os.Remove(verDir); err == nil {
						removed = append(removed, verDir)
					}
				}
			}
		}
		if entries, _ := os.ReadDir(idDir); len(entries) == 0 {
			if err := os.Remove(idDir); err == nil {
				removed = append(removed, idDir)
			}
		}
	}
	return removed, nil
}

type VersionConflictError struct {
	ID, Version        string
	Existing, Incoming string
}

func (e *VersionConflictError) Error() string {
	return fmt.Sprintf("拒绝安装 %s@%s：同版本已存在不同内容（已有 %s，本次 %s）。"+
		"同版本重发会静默改变所有使用者的行为，必须改用新版本号。",
		e.ID, e.Version, e.Existing, e.Incoming)
}

type NotInstalledError struct {
	ID, Version string

	EmbedDirs int
}

func (e *NotInstalledError) Error() string {
	if e.EmbedDirs > 0 {
		return fmt.Sprintf("%s@%s 在 store 与 %d 个内嵌层里都没有。用 ph install 装它",
			e.ID, e.Version, e.EmbedDirs)
	}
	return fmt.Sprintf("%s@%s 未安装。先执行 ph install", e.ID, e.Version)
}
