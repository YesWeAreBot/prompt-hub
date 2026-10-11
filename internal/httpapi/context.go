package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxContextBytes = 1 << 20

func (s *Server) fetchContext(parent context.Context, need []string, app string) (map[string]string, error) {
	if len(need) == 0 {
		return nil, nil
	}
	endpoint, token, err := s.contextSourceFor(app)
	if err != nil {
		return nil, err
	}
	if endpoint == "" {
		return nil, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("context 端点地址不合法: %w", err)
	}

	if !isLoopbackHost(u.Hostname()) {
		return nil, fmt.Errorf("context 端点必须是回环地址，收到 %q", u.Hostname())
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("context 端点只支持 http/https，收到 %q", u.Scheme)
	}

	body, err := json.Marshal(map[string]any{"fields": need})
	if err != nil {
		return nil, err
	}
	timeout := s.cfg.ContextTimeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("回调宿主失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {

		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("回调宿主返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var out struct {
		Values map[string]string `json:"values"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxContextBytes)).Decode(&out); err != nil {
		return nil, fmt.Errorf("解析回调响应失败: %w", err)
	}
	return out.Values, nil
}

func isLoopbackHost(host string) bool {
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func mergeContext(inline, fetched map[string]string) map[string]string {
	if len(fetched) == 0 {
		return inline
	}
	for k, v := range fetched {
		if _, ok := inline[k]; !ok {
			inline[k] = v
		}
	}
	return inline
}
