package render

import (
	"strings"
	"testing"
	"time"
)

func TestPHPMemoryLimitMatchesSandbox(t *testing.T) {
	args := phpEntryArgs(Limits{MemoryBytes: 128 << 20})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "memory_limit=128M") {
		t.Fatalf("缺少 memory_limit: %v", args)
	}

	none := strings.Join(phpEntryArgs(Limits{}), " ")
	if strings.Contains(none, "memory_limit") {
		t.Fatalf("未设内存上限时不应传 memory_limit: %v", none)
	}
}

func TestPHPIsDeterministic(t *testing.T) {
	args := phpEntryArgs(Limits{WallTimeout: time.Second})
	if len(args) == 0 || args[0] != "-n" {
		t.Fatalf("必须以 -n 开头（跳过 php.ini）: %v", args)
	}
}
