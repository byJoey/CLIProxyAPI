package executor

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestClaudeFileSourceJSON(t *testing.T) {
	got := claudeFileSourceJSON("file_abc123")
	if ty := gjson.GetBytes(got, "type").String(); ty != "file" {
		t.Fatalf("type = %q, want file", ty)
	}
	if id := gjson.GetBytes(got, "file_id").String(); id != "file_abc123" {
		t.Fatalf("file_id = %q, want file_abc123", id)
	}
}

func TestAppendBetaIfMissing(t *testing.T) {
	betas := appendBetaIfMissing(nil, claudeFilesAPIBeta)
	betas = appendBetaIfMissing(betas, claudeFilesAPIBeta)
	betas = appendBetaIfMissing(betas, "other-beta")
	if len(betas) != 2 {
		t.Fatalf("betas = %v, want 2 unique entries", betas)
	}
	if betas[0] != claudeFilesAPIBeta {
		t.Fatalf("betas[0] = %q", betas[0])
	}
}

func TestClaudeImageExtension(t *testing.T) {
	cases := map[string]string{
		"image/png":  ".png",
		"image/jpeg": ".jpg",
		"image/webp": ".webp",
		"image/gif":  ".gif",
		"weird/type": ".bin",
	}
	for media, want := range cases {
		if got := claudeImageExtension(media); got != want {
			t.Fatalf("ext(%q) = %q, want %q", media, got, want)
		}
	}
}

func TestClaudeImageAccountKeyPrefersID(t *testing.T) {
	if got := claudeImageAccountKey(nil); got != "default" {
		t.Fatalf("nil auth key = %q, want default", got)
	}
}

func TestClaudeFileSourceJSONNoRawInjection(t *testing.T) {
	// 确保 file_id 通过 sjson 编码,带引号的恶意值不会破坏 JSON 结构。
	got := claudeFileSourceJSON(`a"b`)
	if !gjson.ValidBytes(got) {
		t.Fatalf("produced invalid JSON: %s", got)
	}
	if id := gjson.GetBytes(got, "file_id").String(); id != `a"b` {
		t.Fatalf("file_id = %q", id)
	}
	if strings.Count(string(got), `"type"`) != 1 {
		t.Fatalf("unexpected structure: %s", got)
	}
}
