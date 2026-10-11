package ledger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Entry struct {
	Time     time.Time `json:"t"`
	Pack     string    `json:"pack"`
	Version  string    `json:"version"`
	TreeHash string    `json:"treeHash"`
	Turn     string    `json:"turn"`
	Runtime  string    `json:"runtime"`

	Source string `json:"source,omitempty"`

	Placement string `json:"placement,omitempty"`

	MaxPrivacy string   `json:"maxPrivacy,omitempty"`
	Status     string   `json:"status"`
	Degraded   bool     `json:"degraded"`
	BodyHash   string   `json:"bodyHash,omitempty"`
	ETag       string   `json:"etag,omitempty"`
	NotMod     bool     `json:"notModified,omitempty"`
	Ms         int64    `json:"ms"`
	Warnings   []string `json:"warnings,omitempty"`
	Error      string   `json:"error,omitempty"`
}

type Ledger struct {
	mu   sync.Mutex
	path string
}

func Open(path string) (*Ledger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return &Ledger{path: path}, nil
}

func (l *Ledger) Append(e Entry) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

func (l *Ledger) Tail(n int) ([]Entry, error) {
	if l == nil {
		return nil, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	raw, err := os.ReadFile(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var all []Entry
	for _, line := range splitLines(raw) {
		var e Entry
		if json.Unmarshal(line, &e) == nil {
			all = append(all, e)
		}
	}
	if n > 0 && len(all) > n {
		all = all[len(all)-n:]
	}
	return all, nil
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			if i > start {
				out = append(out, b[start:i])
			}
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}
