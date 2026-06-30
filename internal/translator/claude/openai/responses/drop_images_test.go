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

func TestStripOldImages_KeepsAllImagesBelowHardLine(t *testing.T) {
	out := []byte(`{"messages":[]}`)
	for i := 0; i < 8; i++ {
		m := []byte(`{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"QUJD"}}]}`)
		out, _ = sjson.SetRawBytes(out, "messages.-1", m)
	}

	got := stripOldImages(out)

	for i := 0; i < 8; i++ {
		if ty := gjson.GetBytes(got, "messages."+string(rune('0'+i))+".content.0.type").String(); ty != "image" {
			t.Fatalf("image in message %d should be kept below hard line, got type=%s", i, ty)
		}
	}
}

func TestStripOldImages_RemovesAllImagesAboveHardLine(t *testing.T) {
	big := strings.Repeat("A", maxClaudePayloadBytesWithImages/2)
	out := []byte(`{"messages":[]}`)

	userImage := []byte(`{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":""}}]}`)
	userImage, _ = sjson.SetBytes(userImage, "content.0.source.data", big)
	out, _ = sjson.SetRawBytes(out, "messages.-1", userImage)

	toolResultImage := []byte(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":""}}]}]}`)
	toolResultImage, _ = sjson.SetBytes(toolResultImage, "content.0.content.0.source.data", big)
	out, _ = sjson.SetRawBytes(out, "messages.-1", toolResultImage)

	got := stripOldImages(out)

	if ty := gjson.GetBytes(got, "messages.0.content.0.type").String(); ty != "text" {
		t.Fatalf("top-level image should be replaced above hard line, got type=%s", ty)
	}
	if txt := gjson.GetBytes(got, "messages.0.content.0.text").String(); txt != "[image omitted]" {
		t.Fatalf("top-level image placeholder = %q", txt)
	}
	if ty := gjson.GetBytes(got, "messages.1.content.0.content.0.type").String(); ty != "text" {
		t.Fatalf("nested tool_result image should be replaced above hard line, got type=%s", ty)
	}
	if strings.Contains(string(got), big) {
		t.Fatalf("image source data should not remain after hard-line stripping")
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
