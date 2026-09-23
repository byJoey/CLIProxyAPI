package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// claudeFilesAPIBeta 是引用 Files API file_id 所需的 beta 标记。
const claudeFilesAPIBeta = "files-api-2025-04-14"

// claudeImageFileMinBytes 是触发上传的最小图片体积。过小的图片上传收益不大,
// 直接保留 base64,避免为占位级小图浪费一次上传往返。
const claudeImageFileMinBytes = 4 * 1024

// uploadClaudeImagesToFileIDs 扫描翻译后的 Claude 请求体,把其中的 base64 图片块
// 替换为 Files API 的 file_id 引用。返回新的请求体,以及是否发生过替换(用于决定
// 是否补充 files-api beta)。
//
// 设计要点:
//   - 同一张图片按 sha256 命中账号级缓存,只上传一次,后续复用 file_id。
//   - 上传或读取失败时保留原始 base64 块,绝不让请求因为图片优化而失败。
//   - 仅处理 source.type==base64 的 image 块,已是 file 引用的跳过。
func (e *ClaudeExecutor) uploadClaudeImagesToFileIDs(ctx context.Context, auth *cliproxyauth.Auth, body []byte, baseModel string) (out []byte, changed bool) {
	out = body
	accountKey := claudeImageAccountKey(auth)
	// 借本次请求的账号凭证清理过期淘汰登记的孤儿文件(异步、不阻塞)。
	e.cleanupClaudeImageOrphans(auth, accountKey)

	messages := gjson.GetBytes(out, "messages")
	if !messages.IsArray() {
		return out, false
	}

	messages.ForEach(func(mi, message gjson.Result) bool {
		content := message.Get("content")
		if !content.IsArray() {
			return true
		}
		content.ForEach(func(ci, part gjson.Result) bool {
			msgIdx := mi.Int()
			partIdx := ci.Int()

			// 顶层 image 块
			if part.Get("type").String() == "image" {
				if fileID, ok := e.resolveClaudeImageFileID(ctx, auth, accountKey, baseModel, part); ok {
					path := fmt.Sprintf("messages.%d.content.%d.source", msgIdx, partIdx)
					if nb, err := sjson.SetRawBytes(out, path, claudeFileSourceJSON(fileID)); err == nil {
						out = nb
						changed = true
					}
				}
				return true
			}

			// tool_result 内嵌 image 块
			if part.Get("type").String() == "tool_result" {
				nested := part.Get("content")
				if !nested.IsArray() {
					return true
				}
				nested.ForEach(func(ni, npart gjson.Result) bool {
					if npart.Get("type").String() != "image" {
						return true
					}
					if fileID, ok := e.resolveClaudeImageFileID(ctx, auth, accountKey, baseModel, npart); ok {
						path := fmt.Sprintf("messages.%d.content.%d.content.%d.source", msgIdx, partIdx, ni.Int())
						if nb, err := sjson.SetRawBytes(out, path, claudeFileSourceJSON(fileID)); err == nil {
							out = nb
							changed = true
						}
					}
					return true
				})
			}
			return true
		})
		return true
	})

	return out, changed
}

// resolveClaudeImageFileID 解析单个 image 块,返回其 file_id。命中缓存直接返回,
// 否则上传到 Files API 并写缓存。
func (e *ClaudeExecutor) resolveClaudeImageFileID(ctx context.Context, auth *cliproxyauth.Auth, accountKey, baseModel string, imagePart gjson.Result) (string, bool) {
	source := imagePart.Get("source")
	if source.Get("type").String() != "base64" {
		return "", false
	}
	data := source.Get("data").String()
	if data == "" {
		return "", false
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil || len(raw) < claudeImageFileMinBytes {
		return "", false
	}
	mediaType := source.Get("media_type").String()
	if mediaType == "" {
		mediaType = "image/png"
	}

	sum := sha256.Sum256(raw)
	shaHex := hex.EncodeToString(sum[:])
	if fileID, ok := cache.GetClaudeImageFileID(accountKey, shaHex); ok {
		return fileID, true
	}

	fileID, err := e.uploadClaudeImageBytes(ctx, auth, baseModel, raw, mediaType, shaHex)
	if err != nil || fileID == "" {
		// 上传失败时降级保留原始 base64,不让图片优化影响请求成功率。
		log.WithError(err).Debug("claude image file upload failed; keeping inline base64")
		return "", false
	}
	cache.SetClaudeImageFileID(accountKey, shaHex, fileID)
	return fileID, true
}

// uploadClaudeImageBytes 通过 Anthropic Files API 上传图片,返回 file_id。
func (e *ClaudeExecutor) uploadClaudeImageBytes(ctx context.Context, auth *cliproxyauth.Auth, baseModel string, raw []byte, mediaType, shaHex string) (string, error) {
	_, baseURL := claudeCreds(auth)
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	header := make(textproto.MIMEHeader)
	filename := shaHex + claudeImageExtension(mediaType)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	header.Set("Content-Type", mediaType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return "", err
	}
	if _, err = part.Write(raw); err != nil {
		return "", err
	}
	if err = writer.Close(); err != nil {
		return "", err
	}

	url := fmt.Sprintf("%s/v1/files", strings.TrimRight(baseURL, "/"))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf.Bytes()))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", writer.FormDataContentType())
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("anthropic-beta", claudeFilesAPIBeta)
	if err = e.PrepareRequest(httpReq, auth); err != nil {
		return "", err
	}
	httpReq.Header.Del("Accept-Encoding")

	httpResp, err := e.HttpRequest(ctx, auth, httpReq)
	if err != nil {
		return "", err
	}
	defer httpResp.Body.Close()
	decoded, err := decodeResponseBody(httpResp.Body, httpResp.Header.Get("Content-Encoding"))
	if err != nil {
		return "", err
	}
	defer decoded.Close()
	respBody, err := io.ReadAll(decoded)
	if err != nil {
		return "", err
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return "", fmt.Errorf("claude files upload failed: status=%d body=%s", httpResp.StatusCode, string(respBody))
	}
	fileID := gjson.GetBytes(respBody, "id").String()
	if fileID == "" {
		return "", fmt.Errorf("claude files upload missing id: %s", string(respBody))
	}
	return fileID, nil
}

func claudeFileSourceJSON(fileID string) []byte {
	js := []byte(`{"type":"file","file_id":""}`)
	js, _ = sjson.SetBytes(js, "file_id", fileID)
	return js
}

func claudeImageAccountKey(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return "default"
	}
	if auth.ID != "" {
		return auth.ID
	}
	if apiKey, _ := claudeCreds(auth); apiKey != "" {
		sum := sha256.Sum256([]byte(apiKey))
		return hex.EncodeToString(sum[:])[:16]
	}
	return "default"
}

func claudeImageExtension(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".bin"
	}
}

// appendBetaIfMissing 在 beta 列表中去重追加。
func appendBetaIfMissing(betas []string, beta string) []string {
	beta = strings.TrimSpace(beta)
	if beta == "" {
		return betas
	}
	for _, b := range betas {
		if strings.EqualFold(strings.TrimSpace(b), beta) {
			return betas
		}
	}
	return append(betas, beta)
}

// deleteClaudeImageFile 删除 Anthropic Files API 上的一个文件。用于清理过期
// 缓存对应的孤儿文件,避免长期攒在账号下产生存储费。
func (e *ClaudeExecutor) deleteClaudeImageFile(ctx context.Context, auth *cliproxyauth.Auth, fileID string) error {
	if fileID == "" {
		return nil
	}
	_, baseURL := claudeCreds(auth)
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	url := fmt.Sprintf("%s/v1/files/%s", strings.TrimRight(baseURL, "/"), fileID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("anthropic-beta", claudeFilesAPIBeta)
	if err = e.PrepareRequest(httpReq, auth); err != nil {
		return err
	}
	httpReq.Header.Del("Accept-Encoding")
	httpResp, err := e.HttpRequest(ctx, auth, httpReq)
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()
	_, _ = io.Copy(io.Discard, httpResp.Body)
	// 404 视为已删除,幂等。
	if httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
		return nil
	}
	if httpResp.StatusCode == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("claude files delete failed: status=%d", httpResp.StatusCode)
}

// cleanupClaudeImageOrphans 取出该账号下过期淘汰登记的孤儿文件并异步删除。
// 用后台 context,避免阻塞当前请求;失败仅记日志,不影响主流程。
func (e *ClaudeExecutor) cleanupClaudeImageOrphans(auth *cliproxyauth.Auth, accountKey string) {
	orphans := cache.DrainClaudeImageOrphans(accountKey)
	if len(orphans) == 0 {
		return
	}
	go func(ids []string) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, id := range ids {
			if err := e.deleteClaudeImageFile(ctx, auth, id); err != nil {
				log.WithError(err).WithField("file_id", id).Debug("claude orphan image delete failed")
			}
		}
	}(orphans)
}
