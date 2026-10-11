package render

import (
	"sort"
	"strings"
	"time"

	"github.com/yeswearebot/prompt-hub/internal/pack"
)

type Limits struct {
	WallTimeout time.Duration

	MemoryBytes int64

	MaxProcesses int

	AllowUnsandboxed bool
}

func (l Limits) Normalized() Limits {
	if l.WallTimeout <= 0 {
		l.WallTimeout = 5 * time.Second
	}
	if l.MemoryBytes < 0 {
		l.MemoryBytes = 0
	}
	if l.MaxProcesses < 0 {
		l.MaxProcesses = 0
	}
	return l
}

func CheckEgress(m *pack.Manifest, allow []string) []string {
	if m == nil || len(m.Egress) == 0 {
		return nil
	}
	set := make(map[string]bool, len(allow))
	for _, h := range allow {
		h = strings.ToLower(strings.TrimSpace(h))
		if h != "" {
			set[h] = true
		}
	}
	var bad []string
	for _, h := range m.Egress {
		if !set[strings.ToLower(strings.TrimSpace(h))] {
			bad = append(bad, h)
		}
	}
	sort.Strings(bad)
	return bad
}
