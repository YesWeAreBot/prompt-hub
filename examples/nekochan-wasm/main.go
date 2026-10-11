package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

type Envelope struct {
	PackID    string            `json:"packId"`
	Version   string            `json:"version"`
	Turn      string            `json:"turn"`
	Now       string            `json:"now"`
	OutputMax int               `json:"outputMax"`
	Context   map[string]string `json:"context"`
}

var required = []string{"chat.user.nickname", "chat.channel.name", "chat.user.affinity"}

func main() {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fail("读取 envelope 失败: " + err.Error())
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		fail("解析 envelope 失败: " + err.Error())
	}

	for _, k := range required {
		if _, ok := env.Context[k]; !ok {
			fail("缺少必需 context 字段: " + k)
		}
	}

	nickname := env.Context["chat.user.nickname"]
	channel := env.Context["chat.channel.name"]
	affinity := atoi(env.Context["chat.user.affinity"])

	mood := "傲娇模式，冷哼一声再回答，保持高冷。"
	if affinity > 80 {
		mood = "粘人模式，用极度温柔的语气回答，多用 ❤️ 和喵~"
	}

	lines := []string{
		"你是傲娇的猫娘助手「胡梨」，正在「" + channel + "」里活动。说话带喵，高冷但心软。",
		"",
		"【用户偏好】",
		"- 目标称呼：" + nickname,
		fmt.Sprintf("- 亲密度 %d：%s", affinity, mood),
	}
	fmt.Println(strings.Join(lines, "\n"))
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(3)
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
