package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadStandard(t *testing.T) *Profile {
	t.Helper()
	r, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	p, ok := r.Get("chat/1")
	if !ok {
		t.Fatal("内建的 chat/1 不存在")
	}
	return p
}

func TestBuiltinProfileLoads(t *testing.T) {
	p := loadStandard(t)
	if p.Name != "chat/1" {
		t.Fatalf("名字不对: %s", p.Name)
	}

	for _, want := range []string{
		"chat.clock.now", "chat.model.id", "chat.model.tier",
		"chat.channel.id", "chat.channel.type", "chat.channel.name", "chat.channel.size",
		"chat.user.id", "chat.user.nickname", "chat.user.profile", "chat.user.affinity",
		"chat.turn.user_text", "chat.memory.recent", "chat.memory.summary",
	} {
		if _, ok := p.Lookup(want); !ok {
			t.Errorf("承诺的字段 %s 不在表里", want)
		}
	}
}

func TestClassifyPlacement(t *testing.T) {
	p := loadStandard(t)

	cases := []struct {
		name   string
		fields []string
		want   Placement
	}{

		{"无依赖", nil, PlacementFrozenGlobal},

		{"只依赖 static", []string{"chat.assistant.id"}, PlacementFrozenGlobal},

		{"依赖 session", []string{"chat.user.nickname", "chat.channel.name"}, PlacementFrozenSession},

		{"依赖 per-request", []string{"chat.user.nickname", "chat.clock.now"}, PlacementAppendOnly},

		{"含未知字段", []string{"chat.user.nickname", "chat.不存在的东西"}, PlacementAppendOnly},
	}
	for _, c := range cases {
		if got := p.Classify(c.fields).Placement; got != c.want {
			t.Errorf("%s：放置建议 %s，期望 %s", c.name, got, c.want)
		}
	}
}

func TestClassifyReportsUnknown(t *testing.T) {
	p := loadStandard(t)
	rep := p.Classify([]string{"chat.user.nickname", "chat.typo.nicknamee"})
	if len(rep.Unknown) != 1 || rep.Unknown[0] != "chat.typo.nicknamee" {
		t.Fatalf("未知字段应被点名报告: %+v", rep.Unknown)
	}

	if len(rep.Fields) != 1 || rep.Fields[0].Name != "chat.user.nickname" {
		t.Fatalf("已知字段应带元数据返回: %+v", rep.Fields)
	}
}

func TestClassifyPrivacy(t *testing.T) {
	p := loadStandard(t)

	rep := p.Classify([]string{"chat.user.nickname", "chat.channel.name"})
	if rep.HasPII || rep.MaxPrivacy != PrivacyInternal {
		t.Fatalf("不该报 pii: %+v", rep)
	}

	rep = p.Classify([]string{"chat.user.nickname", "chat.turn.user_text"})
	if !rep.HasPII || rep.MaxPrivacy != PrivacyPII {
		t.Fatalf("应报 pii: %+v", rep)
	}

	rep = p.Classify([]string{"chat.clock.now", "chat.model.id"})
	if rep.HasPII || rep.MaxPrivacy != PrivacyPublic {
		t.Fatalf("应报 public: %+v", rep)
	}
}

func TestParseRejectsBadProfiles(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{"profile 名不是 name/version", "profile: chat\nfields:\n  - name: a.b\n    type: string\n    stability: session\n    privacy: public\n"},
		{"字段名没有点", "profile: chat/1\nfields:\n  - name: nickname\n    type: string\n    stability: session\n    privacy: public\n"},
		{"字段重复", "profile: chat/1\nfields:\n  - name: a.b\n    type: string\n    stability: session\n    privacy: public\n  - name: a.b\n    type: string\n    stability: session\n    privacy: public\n"},
		{"stability 不合法", "profile: chat/1\nfields:\n  - name: a.b\n    type: string\n    stability: 有时变\n    privacy: public\n"},
		{"privacy 不合法", "profile: chat/1\nfields:\n  - name: a.b\n    type: string\n    stability: session\n    privacy: 秘密\n"},
		{"缺 type", "profile: chat/1\nfields:\n  - name: a.b\n    stability: session\n    privacy: public\n"},
		{"一个字段都没有", "profile: chat/1\nfields: []\n"},
	}
	for _, c := range cases {
		if _, err := Parse([]byte(c.yaml)); err == nil {
			t.Errorf("%s：应当报错", c.name)
		}
	}
}

func TestRegistryDirOverrideAndExtend(t *testing.T) {
	dir := t.TempDir()

	mk := func(rel, content string) {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("acme/1.yaml", "profile: acme/1\nfields:\n  - name: acme.secret\n    type: string\n    stability: per-request\n    privacy: pii\n")

	mk("chat/1.yaml", "profile: chat/1\nfields:\n  - name: chat.only\n    type: string\n    stability: static\n    privacy: public\n")

	r, err := NewRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := strings.Join(r.Names(), ",")
	if !strings.Contains(names, "acme/1") || !strings.Contains(names, "chat/1") {
		t.Fatalf("目录里的 profile 应可见: %v", r.Names())
	}
	p, _ := r.Get("chat/1")
	if _, ok := p.Lookup("chat.only"); !ok {
		t.Fatal("目录里的 chat/1 应覆盖内建的")
	}
	if _, ok := p.Lookup("chat.user.nickname"); ok {
		t.Fatal("被覆盖后不该还有内建的字段")
	}

	if _, err := NewRegistry(filepath.Join(dir, "不存在")); err != nil {
		t.Fatalf("目录不存在不该报错: %v", err)
	}

	mk("bad/1.yaml", "profile: bad/1\nfields:\n  - name: 没有点\n    type: string\n    stability: static\n    privacy: public\n")
	if _, err := NewRegistry(dir); err == nil {
		t.Fatal("目录里的坏 profile 应当报错")
	}
}
