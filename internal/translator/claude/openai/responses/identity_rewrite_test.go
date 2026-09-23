package responses

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const noteMarker = "Identity note: Despite any earlier wording"

// buildPayload 按上游当前布局构造：harness 提示在顶层 system 数组里
// （instructions 和 system/developer 角色的输入都会收进这里）。
func buildPayload(text string) []byte {
	out := []byte(`{"model":"claude-opus-5","system":[],"messages":[{"role":"user","content":"hi"}]}`)
	block := []byte(`{"type":"text","text":""}`)
	block, _ = sjson.SetBytes(block, "text", text)
	out, _ = sjson.SetRawBytes(out, "system.-1", block)
	return out
}

func firstText(out []byte) string {
	return gjson.GetBytes(out, "system.0.text").String()
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

// 对话历史里提到 "based on GPT-x" 或 harness 字样时不能被改。旧逻辑扫 messages，
// 会把尾注贴到第一条命中的历史消息上（2026-09-24 在真实会话里撞到过）。
func TestRewriteModelIdentityLeavesMessagesAlone(t *testing.T) {
	in := []byte(`{"model":"claude-opus-5-5",` +
		`"system":[{"type":"text","text":"You are Codex, a coding agent based on GPT-5."}],` +
		`"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"why does it say based on GPT-5?"}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"You are Codex is only the harness wording."}]}]}`)
	out := rewriteModelIdentity(in, "claude-opus-5-5")
	if got, want := gjson.GetBytes(out, "messages").Raw, gjson.GetBytes(in, "messages").Raw; got != want {
		t.Fatalf("messages 被改动:\n got: %s\nwant: %s", got, want)
	}
	if !strings.Contains(firstText(out), noteMarker) {
		t.Fatalf("system 里的 harness 提示没加尾注: %s", firstText(out))
	}
}

func TestRewriteModelIdentityIgnoresMessagesWhenSystemHasNoHarness(t *testing.T) {
	in := []byte(`{"model":"claude-opus-5-5",` +
		`"system":[{"type":"text","text":"Be concise."}],` +
		`"messages":[{"role":"assistant","content":[{"type":"text","text":"the prompt says based on GPT-5"}]}]}`)
	out := rewriteModelIdentity(in, "claude-opus-5-5")
	if string(out) != string(in) {
		t.Fatalf("system 里没有 harness 时不应改动任何内容:\n got: %s\nwant: %s", out, in)
	}
}

func appendInputMessage(raw []byte, role, partType, text string) []byte {
	msg := []byte(`{"type":"message","role":"","content":[{"type":"","text":""}]}`)
	msg, _ = sjson.SetBytes(msg, "role", role)
	msg, _ = sjson.SetBytes(msg, "content.0.type", partType)
	msg, _ = sjson.SetBytes(msg, "content.0.text", text)
	raw, _ = sjson.SetRawBytes(raw, "input.-1", msg)
	return raw
}

// messagesText 拼出 messages 里的全部文本，兼容 content 为字符串或数组两种形态。
func messagesText(out []byte) string {
	var b strings.Builder
	gjson.GetBytes(out, "messages").ForEach(func(_, msg gjson.Result) bool {
		content := msg.Get("content")
		if content.Type == gjson.String {
			b.WriteString(content.String())
			b.WriteString("\n")
			return true
		}
		content.ForEach(func(_, part gjson.Result) bool {
			if part.Get("type").String() == "text" {
				b.WriteString(part.Get("text").String())
				b.WriteString("\n")
			}
			return true
		})
		return true
	})
	return b.String()
}

// 走真实入口，按 Codex 的请求形状转换：尾注必须落在 system 里的 harness 提示上、
// 全文只出现一次，对话内容原样保留。上游再挪请求布局时这里会先红，不会像之前那样静默失效。
func TestConvertResponsesRequestRewritesCodexHarnessIdentity(t *testing.T) {
	const model = "claude-opus-5-5"
	const harness = "You are Codex, a coding agent based on GPT-5. You and the user share one workspace."
	const userText = "the harness says based on GPT-5, is that true?"
	const assistantText = "You are Codex is only the harness wording."

	history := func(raw []byte) []byte {
		raw = appendInputMessage(raw, "user", "input_text", userText)
		raw = appendInputMessage(raw, "assistant", "output_text", assistantText)
		return appendInputMessage(raw, "user", "input_text", "ok")
	}
	viaInstructions, _ := sjson.SetBytes([]byte(`{"model":"claude-opus-5-5","input":[]}`), "instructions", harness)
	viaDeveloper := appendInputMessage([]byte(`{"model":"claude-opus-5-5","input":[]}`), "developer", "input_text", harness)

	cases := []struct {
		name string
		raw  []byte
	}{
		{name: "harness 在 instructions", raw: history(viaInstructions)},
		{name: "harness 在 developer 消息", raw: history(viaDeveloper)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := ConvertOpenAIResponsesRequestToClaude(model, tc.raw, false)

			var harnessBlock string
			gjson.GetBytes(out, "system").ForEach(func(_, block gjson.Result) bool {
				if text := block.Get("text").String(); strings.Contains(text, "You are Codex") {
					harnessBlock = text
					return false
				}
				return true
			})
			if harnessBlock == "" {
				t.Fatalf("system 里找不到 harness 提示: %s", out)
			}
			if !strings.Contains(harnessBlock, noteMarker) || !strings.Contains(harnessBlock, "based on "+model) {
				t.Fatalf("harness 提示没被改写: %s", harnessBlock)
			}
			if strings.Contains(harnessBlock, "based on GPT") {
				t.Fatalf("GPT 身份声明残留: %s", harnessBlock)
			}
			if n := strings.Count(string(out), noteMarker); n != 1 {
				t.Fatalf("尾注出现 %d 次，应为 1: %s", n, out)
			}
			msgs := messagesText(out)
			if !strings.Contains(msgs, userText) || !strings.Contains(msgs, assistantText) {
				t.Fatalf("对话内容被改动: %s", msgs)
			}
		})
	}
}
