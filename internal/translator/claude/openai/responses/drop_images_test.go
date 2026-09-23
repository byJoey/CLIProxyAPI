package responses

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestMarkPayloadTooLargeForLocalCleanup_KeepsPayloadBelowHardLine(t *testing.T) {
	out := []byte(`{"messages":[]}`)
	for i := 0; i < 8; i++ {
		m := []byte(`{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"QUJD"}}]}`)
		out, _ = sjson.SetRawBytes(out, "messages.-1", m)
	}

	got := markPayloadTooLargeForLocalCleanup(out)

	for i := 0; i < 8; i++ {
		if ty := gjson.GetBytes(got, "messages."+string(rune('0'+i))+".content.0.type").String(); ty != "image" {
			t.Fatalf("image in message %d should be kept below hard line, got type=%s", i, ty)
		}
	}
}

func TestMarkPayloadTooLargeForLocalCleanup_ReturnsAgentActionAboveHardLine(t *testing.T) {
	// 非图片文本内容本身超过硬上限时,扣除图片字节后仍应触发 cleanup marker。
	big := strings.Repeat("A", maxClaudePayloadBytesWithImages+1024)
	out := []byte(`{"messages":[]}`)

	userText := []byte(`{"role":"user","content":[{"type":"text","text":""}]}`)
	userText, _ = sjson.SetBytes(userText, "content.0.text", big)
	out, _ = sjson.SetRawBytes(out, "messages.-1", userText)

	got := markPayloadTooLargeForLocalCleanup(out)

	if code := gjson.GetBytes(got, "error.code").String(); code != "codex_local_cleanup_required" {
		t.Fatalf("error.code = %q, want codex_local_cleanup_required; payload=%s", code, got)
	}
	if !gjson.GetBytes(got, "cliproxy_local_cleanup_required").Bool() {
		t.Fatalf("cliproxy_local_cleanup_required marker missing: %s", got)
	}
	if command := gjson.GetBytes(got, "error.param").String(); command != localCleanupCommand {
		t.Fatalf("cleanup command = %q, want %q", command, localCleanupCommand)
	}
	if strings.Contains(string(got), big) {
		t.Fatalf("error marker should not include image source data")
	}
}

func TestCompressBase64PNGImagesToJPEG_CompressesAndIsStable(t *testing.T) {
	pngData := testPNGBase64(t)
	out := []byte(`{"messages":[]}`)

	userImage := []byte(`{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":""}}]}`)
	userImage, _ = sjson.SetBytes(userImage, "content.0.source.data", pngData)
	out, _ = sjson.SetRawBytes(out, "messages.-1", userImage)

	toolResultImage := []byte(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":""}}]}]}`)
	toolResultImage, _ = sjson.SetBytes(toolResultImage, "content.0.content.0.source.data", pngData)
	out, _ = sjson.SetRawBytes(out, "messages.-1", toolResultImage)

	got := compressBase64PNGImagesToJPEG(out)
	gotAgain := compressBase64PNGImagesToJPEG(got)

	if !bytes.Equal(got, gotAgain) {
		t.Fatalf("JPEG compression should be stable after the first pass")
	}
	for _, path := range []string{
		"messages.0.content.0.source",
		"messages.1.content.0.content.0.source",
	} {
		if mt := gjson.GetBytes(got, path+".media_type").String(); mt != "image/jpeg" {
			t.Fatalf("%s media_type = %q, want image/jpeg", path, mt)
		}
		data := gjson.GetBytes(got, path+".data").String()
		if len(data) >= len(pngData) {
			t.Fatalf("%s data length = %d, want smaller than png length %d", path, len(data), len(pngData))
		}
		raw, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			t.Fatalf("%s data is not valid base64: %v", path, err)
		}
		if len(raw) < 2 || raw[0] != 0xff || raw[1] != 0xd8 {
			t.Fatalf("%s data is not a JPEG payload", path)
		}
	}
}

func testPNGBase64(t *testing.T) string {
	t.Helper()
	return testPNGBase64Size(t, 256, 256)
}

func testPNGBase64Size(t *testing.T, w, h int) string {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	state := uint32(1)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			state = state*1664525 + 1013904223
			r := uint8(state >> 24)
			state = state*1664525 + 1013904223
			g := uint8(state >> 24)
			state = state*1664525 + 1013904223
			b := uint8(state >> 24)
			img.SetRGBA(x, y, color.RGBA{
				R: r,
				G: g,
				B: b,
				A: 255,
			})
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestCompressBase64PNGImagesToJPEG_DownscalesLongEdge(t *testing.T) {
	pngData := testPNGBase64Size(t, 3200, 1800)
	out := []byte(`{"messages":[]}`)
	userImage := []byte(`{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":""}}]}`)
	userImage, _ = sjson.SetBytes(userImage, "content.0.source.data", pngData)
	out, _ = sjson.SetRawBytes(out, "messages.-1", userImage)

	got := compressBase64PNGImagesToJPEG(out)
	gotAgain := compressBase64PNGImagesToJPEG(got)
	if !bytes.Equal(got, gotAgain) {
		t.Fatalf("downscaled JPEG compression must be stable after first pass")
	}

	data := gjson.GetBytes(got, "messages.0.content.0.source.data").String()
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		t.Fatalf("decode jpeg base64: %v", err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode jpeg config: %v", err)
	}
	if cfg.Width > maxScreenshotLongEdge || cfg.Height > maxScreenshotLongEdge {
		t.Fatalf("long edge not capped: %dx%d", cfg.Width, cfg.Height)
	}
	if cfg.Width != maxScreenshotLongEdge {
		t.Fatalf("expected width %d, got %d", maxScreenshotLongEdge, cfg.Width)
	}
}

func TestMarkPayloadTooLargeForLocalCleanup_ExcludesImageBytesFromThreshold(t *testing.T) {
	// 构造一张超过硬上限的大图,但非图片内容很小。扣除图片字节后应当不触发
	// cleanup marker,因为图片会在 executor 阶段被替换为 file_id。
	big := strings.Repeat("A", maxClaudePayloadBytesWithImages+2*1024*1024)
	out := []byte(`{"messages":[]}`)
	userImage := []byte(`{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":""}}]}`)
	userImage, _ = sjson.SetBytes(userImage, "content.0.source.data", big)
	out, _ = sjson.SetRawBytes(out, "messages.-1", userImage)

	got := markPayloadTooLargeForLocalCleanup(out)
	if gjson.GetBytes(got, "cliproxy_local_cleanup_required").Bool() {
		t.Fatalf("oversized images must not trigger cleanup; payload head=%s", string(got)[:120])
	}
	if gjson.GetBytes(got, "messages.0.content.0.source.data").String() != big {
		t.Fatalf("payload should be returned unchanged when only images are large")
	}
}

func TestClaudeBase64ImageBytes_CountsNestedToolResult(t *testing.T) {
	out := []byte(`{"messages":[]}`)
	top := []byte(`{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}`)
	out, _ = sjson.SetRawBytes(out, "messages.-1", top)
	tool := []byte(`{"role":"user","content":[{"type":"tool_result","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"BBBBBB"}}]}]}`)
	out, _ = sjson.SetRawBytes(out, "messages.-1", tool)

	if got := claudeBase64ImageBytes(out); got != len("AAAA")+len("BBBBBB") {
		t.Fatalf("image bytes = %d, want %d", got, len("AAAA")+len("BBBBBB"))
	}
}
