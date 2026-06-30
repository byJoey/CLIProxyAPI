package cache

import (
	"sync"
	"time"
)

// Claude Files API 图片映射缓存:把同一张图片的 sha256 映射到上传后得到的
// file_id,避免每轮请求都重新上传 base64 图片,从而稳定缓存前缀并省 token。
//
// 缓存按 auth(账号)维度隔离,key 为 account|sha256,防止不同账号串用 file_id。

const (
	// ClaudeImageFileCacheTTL 是单条 file_id 映射的有效期。Anthropic Files API
	// 上传的文件长期可用,这里取较长 TTL,过期只是为了回收内存。
	ClaudeImageFileCacheTTL = 24 * time.Hour

	// claudeImageFileCacheCleanupInterval 控制后台清理过期条目的频率。
	claudeImageFileCacheCleanupInterval = 30 * time.Minute
)

type claudeImageFileEntry struct {
	fileID    string
	expiresAt time.Time
}

var (
	claudeImageFileCache     sync.Map // key string -> claudeImageFileEntry
	claudeImageFileCacheOnce sync.Once
)

// GetClaudeImageFileID 返回指定账号下某张图片 sha256 对应的 file_id。
func GetClaudeImageFileID(accountKey, sha256Hex string) (string, bool) {
	key := claudeImageFileCacheKey(accountKey, sha256Hex)
	val, ok := claudeImageFileCache.Load(key)
	if !ok {
		return "", false
	}
	entry := val.(claudeImageFileEntry)
	if time.Now().After(entry.expiresAt) {
		claudeImageFileCache.Delete(key)
		return "", false
	}
	return entry.fileID, true
}

// SetClaudeImageFileID 写入某张图片 sha256 与 file_id 的映射。
func SetClaudeImageFileID(accountKey, sha256Hex, fileID string) {
	if fileID == "" {
		return
	}
	claudeImageFileCacheOnce.Do(startClaudeImageFileCacheCleanup)
	key := claudeImageFileCacheKey(accountKey, sha256Hex)
	claudeImageFileCache.Store(key, claudeImageFileEntry{
		fileID:    fileID,
		expiresAt: time.Now().Add(ClaudeImageFileCacheTTL),
	})
}

// ClearClaudeImageFileCache 清空缓存,仅用于测试。
func ClearClaudeImageFileCache() {
	claudeImageFileCache.Range(func(k, _ any) bool {
		claudeImageFileCache.Delete(k)
		return true
	})
}

func claudeImageFileCacheKey(accountKey, sha256Hex string) string {
	return accountKey + "|" + sha256Hex
}

func startClaudeImageFileCacheCleanup() {
	go func() {
		ticker := time.NewTicker(claudeImageFileCacheCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now()
			claudeImageFileCache.Range(func(k, v any) bool {
				if now.After(v.(claudeImageFileEntry).expiresAt) {
					claudeImageFileCache.Delete(k)
				}
				return true
			})
		}
	}()
}
