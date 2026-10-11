package main

import (
	"io"
	"os"
	"strings"
)

func main() {
	raw, _ := io.ReadAll(os.Stdin)
	env := string(raw)
	turn := field(env, "turn")

	probe := "MISSING"
	if b, err := os.ReadFile("/index.prompt"); err == nil {
		probe = strings.TrimSpace(string(b))
	}

	write := "blocked"
	if err := os.WriteFile("/evil.txt", []byte("x"), 0o644); err == nil {
		write = "ALLOWED"
	}

	switch turn {
	case "ws-fail":
		_, _ = os.Stderr.WriteString("wasmstub: 故意失败\n")
		os.Exit(1)

	case "ws-warn":
		_, _ = os.Stderr.WriteString("wasmstub: 一条诊断\n")

	case "ws-flood":
		var b strings.Builder
		for i := 0; i < 5000; i++ {
			b.WriteByte('y')
		}
		_, _ = os.Stdout.WriteString(b.String())
		return

	case "ws-spin":

		n := 0
		for {
			n = spin(n)
			if n%20000000 == 0 {
				_, _ = os.Stderr.WriteString("wasmstub: 仍在自旋\n")
			}
		}
	}

	var b strings.Builder
	b.WriteString("PACK=" + field(env, "packId") + "@" + field(env, "version") + "\n")
	b.WriteString("NICK=" + field(env, "chat.user.nickname") + "\n")
	b.WriteString("TURN=" + turn + "\n")
	b.WriteString("PROBE=" + probe + "\n")
	b.WriteString("WRITE=" + write + "\n")
	_, _ = os.Stdout.WriteString(b.String())
}

//go:noinline
func spin(n int) int { return n + 1 }

func field(s, key string) string {
	needle := `"` + key + `":"`
	i := strings.Index(s, needle)
	if i < 0 {
		return ""
	}
	rest := s[i+len(needle):]
	if j := strings.IndexByte(rest, '"'); j >= 0 {
		return rest[:j]
	}
	return ""
}
