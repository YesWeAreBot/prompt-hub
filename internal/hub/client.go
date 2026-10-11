package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yeswearebot/prompt-hub/internal/pack"
)

type Client struct {
	BaseURL      string
	PublishToken string
	HTTP         *http.Client
}

func NewClient(baseURL, token string) *Client {
	return &Client{
		BaseURL:      strings.TrimRight(baseURL, "/"),
		PublishToken: token,
		HTTP:         &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *Client) Publish(dir string) (Meta, error) {
	loaded, err := pack.Load(dir)
	if err != nil {
		return Meta{}, err
	}
	if err := loaded.Manifest.Validate(); err != nil {
		return Meta{}, fmt.Errorf("manifest 不合法: %w", err)
	}
	archive, err := pack.WriteArchive(loaded.Tree)
	if err != nil {
		return Meta{}, err
	}

	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/v1/packs", bytes.NewReader(archive))
	if err != nil {
		return Meta{}, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if c.PublishToken != "" {
		req.Header.Set("X-Ph-Publish-Token", c.PublishToken)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Meta{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return Meta{}, fmt.Errorf("hub 拒绝发布（HTTP %d）: %s", resp.StatusCode, firstLine(body))
	}
	var m Meta
	if err := json.Unmarshal(body, &m); err != nil {
		return Meta{}, err
	}
	return m, nil
}

func (c *Client) Index() ([]Meta, error) {
	resp, err := c.HTTP.Get(c.BaseURL + "/v1/index")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, firstLine(body))
	}
	var out struct {
		Packs []Meta `json:"packs"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out.Packs, nil
}

func (c *Client) Get(id, version string) (Meta, error) {
	resp, err := c.HTTP.Get(c.BaseURL + "/v1/packs/" + id + "/" + version)
	if err != nil {
		return Meta{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return Meta{}, fmt.Errorf("%s@%s: HTTP %d: %s", id, version, resp.StatusCode, firstLine(body))
	}
	var m Meta
	if err := json.Unmarshal(body, &m); err != nil {
		return Meta{}, err
	}
	return m, nil
}

func (c *Client) Fetch(id, version string) (Meta, []byte, error) {
	meta, err := c.Get(id, version)
	if err != nil {
		return Meta{}, nil, err
	}
	resp, err := c.HTTP.Get(c.BaseURL + "/v1/blobs/" + meta.TreeHash)
	if err != nil {
		return Meta{}, nil, err
	}
	defer resp.Body.Close()
	archive, err := io.ReadAll(resp.Body)
	if err != nil {
		return Meta{}, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return Meta{}, nil, fmt.Errorf("取归档失败: HTTP %d: %s", resp.StatusCode, firstLine(archive))
	}

	tree, err := pack.ReadArchive(archive)
	if err != nil {
		return Meta{}, nil, fmt.Errorf("归档不可读: %w", err)
	}
	if got := tree.Hash(); got != meta.TreeHash {
		return Meta{}, nil, fmt.Errorf(
			"内容校验失败：元数据声明 %s，实际收到 %s。拒绝安装", meta.TreeHash, got)
	}
	return meta, archive, nil
}

func (c *Client) Latest(id string) (Meta, error) {
	idx, err := c.Index()
	if err != nil {
		return Meta{}, err
	}
	var best Meta
	found := false
	for _, m := range idx {
		if m.Manifest.PackID != id {
			continue
		}
		if !found || pack.CompareVersions(m.Manifest.Version, best.Manifest.Version) > 0 {
			best, found = m, true
		}
	}
	if !found {
		return Meta{}, fmt.Errorf("hub 上没有 %s", id)
	}
	return best, nil
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
