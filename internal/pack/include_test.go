package pack

import (
	"strings"
	"testing"
)

func TestParseInclude(t *testing.T) {

	c, err := ParseInclude("cats-role@1.2.3")
	if err != nil || c.IsRange() || c.Exact != "1.2.3" || c.ID != "cats-role" {
		t.Fatalf("精确版本解析失败: %+v %v", c, err)
	}

	for _, tc := range []struct{ spec, op, base string }{
		{"^1.1.0", "^", "1.1.0"},
		{"~1.2.0", "~", "1.2.0"},
		{"^1.2", "^", "1.2"},
		{"~1.2", "~", "1.2"},
	} {
		c, err := ParseInclude("cats-role@" + tc.spec)
		if err != nil || !c.IsRange() || c.Op != tc.op || c.Base != tc.base {
			t.Errorf("%s 解析失败: %+v %v", tc.spec, c, err)
		}
	}

	for _, bad := range []string{
		"cats-role",
		"cats-role@",
		"@1.0.0",
		"Cats-Role@1.0.0",
		"cats-role@*",
		"cats-role@>=1.0.0",
		"cats-role@1.0",
		"cats-role@^1",
		"cats-role@^1.0.0||2",
	} {
		if _, err := ParseInclude(bad); err == nil {
			t.Errorf("%q 应当被拒", bad)
		}
	}
}

func TestIncludeSatisfies(t *testing.T) {
	cases := []struct {
		spec    string
		version string
		want    bool
	}{

		{"^1.1.0", "1.1.0", true},
		{"^1.1.0", "1.9.9", true},
		{"^1.1.0", "1.0.9", false},
		{"^1.1.0", "2.0.0", false},

		{"^1.1.0", "1.2.0-rc1", false},

		{"^1.2", "1.2.0", true},
		{"^1.2", "1.2.5", true},
		{"^1.2", "1.1.9", false},
		{"~1.2", "1.2.0", true},

		{"~1.1.0", "1.1.0", true},
		{"~1.1.0", "1.1.9", true},
		{"~1.1.0", "1.2.0", false},

		{"^0.2.0", "0.2.0", true},
		{"^0.2.0", "0.2.5", true},
		{"^0.2.0", "0.3.0", false},

		{"^0.0.3", "0.0.3", true},
		{"^0.0.3", "0.0.4", false},

		{"1.2.3", "1.2.3", true},
		{"1.2.3", "1.2.4", false},
	}
	for _, c := range cases {
		got, err := ParseInclude("x@" + c.spec)
		if err != nil {
			t.Fatal(err)
		}
		if got.Satisfies(c.version) != c.want {
			t.Errorf("%s 对 %s 应为 %v", c.spec, c.version, c.want)
		}
	}
}

func TestLockRoundTripAndOrder(t *testing.T) {
	l := &Lock{}

	l.Set("zeta", "1.0.0", "sha256-zzz")
	l.Set("alpha", "2.0.0", "sha256-aaa")

	raw, err := l.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	if strings.Index(out, "alpha") > strings.Index(out, "zeta") {
		t.Fatalf("条目应当按 id 稳定排序（否则每次 lock 都产生无意义 diff）:\n%s", out)
	}
	if !strings.Contains(out, "不要手改") {
		t.Fatalf("应当带上说明: %s", out)
	}

	back, err := ParseLock(raw)
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := back.Get("alpha"); !ok || e.TreeHash != "sha256-aaa" {
		t.Fatalf("往返丢了内容: %+v", back.Entries)
	}

	back.Set("alpha", "3.0.0", "sha256-bbb")
	n := 0
	for _, e := range back.Entries {
		if strings.HasPrefix(e.Ref, "alpha@") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("同一个 id 应当只有一条: %+v", back.Entries)
	}
	if e, _ := back.Get("alpha"); e.Ref != "alpha@3.0.0" {
		t.Fatalf("Set 应当覆盖: %+v", e)
	}

	if _, err := ParseLock([]byte("entries:\n  - ref: 没有at\n")); err == nil {
		t.Fatal("坏条目应当报错")
	}

	empty, err := LockFromTree(Tree{})
	if err != nil || len(empty.Entries) != 0 {
		t.Fatalf("没有锁应当返回空锁: %v %+v", err, empty)
	}
}

func TestExactRefs(t *testing.T) {

	mExact := &Manifest{
		PackID: "demo", Version: "1.0.0", Entry: "i.prompt",
		Includes: []string{"other@2.0.0"},
	}
	refs, err := mExact.ExactRefs(&Lock{})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].String() != "other@2.0.0" {
		t.Fatalf("精确引用应当直接通过: %v", refs)
	}

	m := &Manifest{
		PackID: "demo", Version: "1.0.0", Entry: "i.prompt",
		Includes: []string{"lib@^1.1.0", "other@2.0.0"},
	}

	if _, err := m.ExactRefs(&Lock{}); err == nil || !strings.Contains(err.Error(), "ph lock") {
		t.Fatalf("范围没有锁条目应当报错并给出命令: %v", err)
	}

	l := &Lock{}
	l.Set("lib", "1.2.0", "sha256-x")
	l.Set("other", "2.0.0", "sha256-y")
	refs, err = m.ExactRefs(l)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{refs[0].String(), refs[1].String()}
	if got[0] != "lib@1.2.0" || got[1] != "other@2.0.0" {
		t.Fatalf("解析结果不对: %v", got)
	}

	l.Set("lib", "2.5.0", "sha256-z")
	if _, err := m.ExactRefs(l); err == nil || !strings.Contains(err.Error(), "--update") {
		t.Fatalf("锁条目不满足范围应当报错: %v", err)
	}
}
