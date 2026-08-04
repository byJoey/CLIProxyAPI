package responses

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func buildPayload(text string) []byte {
	out := []byte(`{"model":"claude-opus-5","messages":[]}`)
	msg := []byte(`{"role":"user","content":[{"type":"text","text":""}]}`)
	msg, _ = sjson.SetBytes(msg, "content.0.text", text)
	out, _ = sjson.SetRawBytes(out, "messages.-1", msg)
	return out
}

func firstText(out []byte) string {
	return gjson.GetBytes(out, "messages.0.content.0.text").String()
}

func TestRewriteModelIdentity(t *testing.T) {
	const model = "claude-opus-5"
	cases := []struct {
		name        string
		in          string
		wantChanged bool
		wantNoGPT   bool
	}{
		{
			name:        "旧措辞-based on GPT-5",
			in:          "You are Codex, a coding agent based on GPT-5. You and the user share one workspace.",
			wantChanged: true,
			wantNoGPT:   true,
		},
		{
			name:        "旧措辞-版本号漂移",
			in:          "You are Codex, a coding agent based on GPT-5.1-Codex-Max. Do things.",
			wantChanged: true,
			wantNoGPT:   true,
		},
		{
			name:        "新措辞-Codex CLI 无 GPT 字样",
			in:          "You are a coding agent running in the Codex CLI, a terminal-based coding assistant. Codex CLI is an open source project led by OpenAI.",
			wantChanged: true,
		},
		{
			name:        "无关文本不动",
			in:          "帮我看看这个函数为什么返回 nil。",
			wantChanged: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := firstText(rewriteModelIdentity(buildPayload(tc.in), model))
			hasNote := strings.Contains(got, "Identity note: Despite any earlier wording")
			if hasNote != tc.wantChanged {
				t.Fatalf("追加尾注情况不符: got hasNote=%v want=%v\n文本: %s", hasNote, tc.wantChanged, got)
			}
			if tc.wantNoGPT && strings.Contains(got, "based on GPT") {
				t.Fatalf("GPT 身份声明残留: %s", got)
			}
			if tc.wantChanged && !strings.Contains(got, model) {
				t.Fatalf("未写入真实模型名: %s", got)
			}
			if !tc.wantChanged && got != tc.in {
				t.Fatalf("不该改动却被改: %s", got)
			}
		})
	}
}

func TestRewriteModelIdentityIdempotent(t *testing.T) {
	const model = "claude-opus-5"
	in := "You are Codex, a coding agent based on GPT-5. Do things."
	once := rewriteModelIdentity(buildPayload(in), model)
	twice := rewriteModelIdentity(once, model)
	if strings.Count(firstText(twice), "Identity note: Despite any earlier wording") != 1 {
		t.Fatalf("重复改写导致尾注叠加: %s", firstText(twice))
	}
}

func TestRewriteModelIdentityEmptyModel(t *testing.T) {
	in := "You are Codex, a coding agent based on GPT-5."
	got := firstText(rewriteModelIdentity(buildPayload(in), ""))
	if got != in {
		t.Fatalf("modelName 为空时不应改动: %s", got)
	}
}
