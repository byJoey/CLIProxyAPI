package executor

// 本文件为本 fork 自有补丁：
//   1. Codex 超大 payload 的本地清理指令（markPayloadTooLargeForLocalCleanup 的执行端）
//   2. Claude Files API 上传直通（配合 uploadClaudeImagesToFileIDs 把图片换成 file_id）
// 上游无对应实现，单独成文件以免和上游拆分后的 executor 文件冲突。

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const codexLocalCleanupToolCommand = "codex-prune-rollout-images --current-window --request-mb 31 --apply"

func claudeLocalCleanupRequired(body []byte) bool {
	if !gjson.GetBytes(body, "cliproxy_local_cleanup_required").Bool() ||
		strings.TrimSpace(gjson.GetBytes(body, "error.code").String()) != "codex_local_cleanup_required" {
		return false
	}
	return true
}

func codexLocalCleanupToolCallItem() []byte {
	args := fmt.Sprintf(`{"cmd":%q,"yield_time_ms":10000,"max_output_tokens":12000}`, codexLocalCleanupToolCommand)
	item := []byte(`{"id":"fc_local_cleanup_required","type":"function_call","status":"completed","arguments":"","call_id":"call_local_cleanup_required","name":"exec_command"}`)
	item, _ = sjson.SetBytes(item, "arguments", args)
	return item
}

func codexLocalCleanupResponsesPayload() []byte {
	item := codexLocalCleanupToolCallItem()
	payload := []byte(`{"id":"resp_local_cleanup_required","object":"response","created_at":0,"status":"completed","background":false,"error":null,"output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`)
	payload, _ = sjson.SetRawBytes(payload, "output.-1", item)
	return payload
}

func codexLocalCleanupResponsesStream() *cliproxyexecutor.StreamResult {
	item := codexLocalCleanupToolCallItem()
	done := []byte(`{"type":"response.output_item.done","sequence_number":1,"output_index":0,"item":{}}`)
	done, _ = sjson.SetRawBytes(done, "item", item)
	completed := []byte(`{"type":"response.completed","sequence_number":2,"response":{"id":"resp_local_cleanup_required","object":"response","created_at":0,"status":"completed","background":false,"error":null,"output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
	completed, _ = sjson.SetRawBytes(completed, "response.output.-1", item)
	chunks := make(chan cliproxyexecutor.StreamChunk, 2)
	chunks <- cliproxyexecutor.StreamChunk{Payload: done}
	chunks <- cliproxyexecutor.StreamChunk{Payload: completed}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}
}

func (e *ClaudeExecutor) executeFileUpload(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	_, baseURL := claudeCreds(auth)
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	url := fmt.Sprintf("%s/v1/files", strings.TrimRight(baseURL, "/"))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(req.Payload))
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	if contentType := strings.TrimSpace(opts.Headers.Get("Content-Type")); contentType != "" {
		httpReq.Header.Set("Content-Type", contentType)
	}
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("anthropic-beta", "files-api-2025-04-14")
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return cliproxyexecutor.Response{}, err
	}
	httpReq.Header.Del("Accept-Encoding")

	httpResp, err := e.HttpRequest(ctx, auth, httpReq)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	defer httpResp.Body.Close()
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return cliproxyexecutor.Response{}, statusErr{code: httpResp.StatusCode, msg: string(body)}
	}
	return cliproxyexecutor.Response{Payload: body, Headers: httpResp.Header.Clone()}, nil
}
