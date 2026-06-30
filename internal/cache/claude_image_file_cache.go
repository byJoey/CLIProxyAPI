package cache

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// Claude Files API 图片映射缓存:把同一张图片的 sha256 映射到上传后得到的
// file_id,避免每轮请求都重新上传 base64 图片,从而稳定缓存前缀并省 token。
//
// 缓存按 auth(账号)维度隔离,key 为 account|sha256,防止不同账号串用 file_id。
//
// 持久化:映射同时写入磁盘 JSONL,进程重启后惰性加载,避免每次部署/重启后
// 同一张图重新上传。磁盘是内存的镜像,内存始终是权威读取来源。

const (
	// ClaudeImageFileCacheTTL 是单条 file_id 映射的有效期。Anthropic Files API
	// 上传的文件长期可用,这里取较长 TTL,过期只是为了回收资源。
	ClaudeImageFileCacheTTL = 24 * time.Hour

	// claudeImageFileCacheCleanupInterval 控制后台清理过期条目的频率。
	claudeImageFileCacheCleanupInterval = 30 * time.Minute
)

type claudeImageFileEntry struct {
	fileID    string
	expiresAt time.Time
}

// claudeImageFileRecord 是磁盘 JSONL 的单行结构。
type claudeImageFileRecord struct {
	Key       string `json:"key"`
	FileID    string `json:"file_id"`
	ExpiresAt int64  `json:"expires_at"` // Unix 秒
}

var (
	claudeImageFileCache     sync.Map // key string -> claudeImageFileEntry
	claudeImageFileCacheOnce sync.Once

	claudeImageFileDiskMu     sync.Mutex
	claudeImageFileDiskLoaded bool
)

// GetClaudeImageFileID 返回指定账号下某张图片 sha256 对应的 file_id。
func GetClaudeImageFileID(accountKey, sha256Hex string) (string, bool) {
	ensureClaudeImageFileCacheInit()
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

// SetClaudeImageFileID 写入某张图片 sha256 与 file_id 的映射,并追加落盘。
func SetClaudeImageFileID(accountKey, sha256Hex, fileID string) {
	if fileID == "" {
		return
	}
	ensureClaudeImageFileCacheInit()
	key := claudeImageFileCacheKey(accountKey, sha256Hex)
	expiresAt := time.Now().Add(ClaudeImageFileCacheTTL)
	claudeImageFileCache.Store(key, claudeImageFileEntry{
		fileID:    fileID,
		expiresAt: expiresAt,
	})
	appendClaudeImageFileRecord(claudeImageFileRecord{
		Key:       key,
		FileID:    fileID,
		ExpiresAt: expiresAt.Unix(),
	})
}

// ClearClaudeImageFileCache 清空内存缓存,仅用于测试。
func ClearClaudeImageFileCache() {
	claudeImageFileCache.Range(func(k, _ any) bool {
		claudeImageFileCache.Delete(k)
		return true
	})
}

func claudeImageFileCacheKey(accountKey, sha256Hex string) string {
	return accountKey + "|" + sha256Hex
}

// claudeImageFileCachePath 返回磁盘持久化文件路径。优先使用环境变量,
// 否则落到 $HOME/.cliproxyapi 下;无法确定 HOME 时返回空(纯内存模式)。
func claudeImageFileCachePath() string {
	if p := os.Getenv("CLAUDE_IMAGE_FILE_CACHE_PATH"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".cliproxyapi", "claude_image_files.jsonl")
}

func ensureClaudeImageFileCacheInit() {
	claudeImageFileCacheOnce.Do(func() {
		loadClaudeImageFileCacheFromDisk()
		startClaudeImageFileCacheCleanup()
	})
}

// loadClaudeImageFileCacheFromDisk 把磁盘 JSONL 中未过期的映射加载进内存。
func loadClaudeImageFileCacheFromDisk() {
	path := claudeImageFileCachePath()
	if path == "" {
		return
	}
	claudeImageFileDiskMu.Lock()
	defer claudeImageFileDiskMu.Unlock()
	claudeImageFileDiskLoaded = true

	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	now := time.Now()
	loaded := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec claudeImageFileRecord
		if err := json.Unmarshal(line, &rec); err != nil || rec.Key == "" || rec.FileID == "" {
			continue
		}
		expiresAt := time.Unix(rec.ExpiresAt, 0)
		if now.After(expiresAt) {
			continue
		}
		claudeImageFileCache.Store(rec.Key, claudeImageFileEntry{
			fileID:    rec.FileID,
			expiresAt: expiresAt,
		})
		loaded++
	}
	if loaded > 0 {
		log.WithField("entries", loaded).Debug("loaded claude image file_id cache from disk")
	}
}

// appendClaudeImageFileRecord 以追加方式把一条映射写入磁盘。
func appendClaudeImageFileRecord(rec claudeImageFileRecord) {
	path := claudeImageFileCachePath()
	if path == "" {
		return
	}
	claudeImageFileDiskMu.Lock()
	defer claudeImageFileDiskMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.WithError(err).Debug("claude image file cache: mkdir failed")
		return
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.WithError(err).Debug("claude image file cache: open for append failed")
		return
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		log.WithError(err).Debug("claude image file cache: append failed")
	}
}

// rewriteClaudeImageFileCacheDisk 用当前内存中未过期的条目重写磁盘文件,
// 用于清理过期条目、压实文件。
func rewriteClaudeImageFileCacheDisk() {
	path := claudeImageFileCachePath()
	if path == "" {
		return
	}
	claudeImageFileDiskMu.Lock()
	defer claudeImageFileDiskMu.Unlock()

	now := time.Now()
	var buf []byte
	claudeImageFileCache.Range(func(k, v any) bool {
		entry := v.(claudeImageFileEntry)
		if now.After(entry.expiresAt) {
			return true
		}
		rec := claudeImageFileRecord{
			Key:       k.(string),
			FileID:    entry.fileID,
			ExpiresAt: entry.expiresAt.Unix(),
		}
		if data, err := json.Marshal(rec); err == nil {
			buf = append(buf, data...)
			buf = append(buf, '\n')
		}
		return true
	})

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		log.WithError(err).Debug("claude image file cache: rewrite tmp failed")
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.WithError(err).Debug("claude image file cache: rename failed")
	}
}

func startClaudeImageFileCacheCleanup() {
	go func() {
		ticker := time.NewTicker(claudeImageFileCacheCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now()
			removed := false
			claudeImageFileCache.Range(func(k, v any) bool {
				if now.After(v.(claudeImageFileEntry).expiresAt) {
					claudeImageFileCache.Delete(k)
					removed = true
				}
				return true
			})
			if removed {
				rewriteClaudeImageFileCacheDisk()
			}
		}
	}()
}

// reloadClaudeImageFileCacheForTest 重置缓存状态并从磁盘重新加载,仅用于测试,
// 模拟进程重启后的冷启动加载。
func reloadClaudeImageFileCacheForTest() {
	ClearClaudeImageFileCache()
	claudeImageFileDiskMu.Lock()
	claudeImageFileDiskLoaded = false
	claudeImageFileDiskMu.Unlock()
	loadClaudeImageFileCacheFromDisk()
}
