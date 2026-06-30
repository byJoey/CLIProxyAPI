package cache

import (
	"path/filepath"
	"testing"
)

func TestClaudeImageFileCacheStoreGet(t *testing.T) {
	t.Setenv("CLAUDE_IMAGE_FILE_CACHE_PATH", filepath.Join(t.TempDir(), "cache.jsonl"))
	ClearClaudeImageFileCache()
	defer ClearClaudeImageFileCache()

	if _, ok := GetClaudeImageFileID("acct1", "sha-a"); ok {
		t.Fatal("expected miss before set")
	}

	SetClaudeImageFileID("acct1", "sha-a", "file_1")
	if id, ok := GetClaudeImageFileID("acct1", "sha-a"); !ok || id != "file_1" {
		t.Fatalf("get = %q,%v, want file_1,true", id, ok)
	}

	// 账号隔离:同一 sha 在不同账号下不串用。
	if _, ok := GetClaudeImageFileID("acct2", "sha-a"); ok {
		t.Fatal("expected account isolation miss")
	}
}

func TestClaudeImageFileCacheIgnoresEmptyID(t *testing.T) {
	t.Setenv("CLAUDE_IMAGE_FILE_CACHE_PATH", filepath.Join(t.TempDir(), "cache.jsonl"))
	ClearClaudeImageFileCache()
	defer ClearClaudeImageFileCache()

	SetClaudeImageFileID("acct1", "sha-a", "")
	if _, ok := GetClaudeImageFileID("acct1", "sha-a"); ok {
		t.Fatal("empty file_id should not be stored")
	}
}

// 验证磁盘持久化:写入后清空内存(模拟重启),从磁盘重新加载应恢复映射。
func TestClaudeImageFileCachePersistsAcrossReload(t *testing.T) {
	t.Setenv("CLAUDE_IMAGE_FILE_CACHE_PATH", filepath.Join(t.TempDir(), "cache.jsonl"))
	ClearClaudeImageFileCache()
	defer ClearClaudeImageFileCache()

	SetClaudeImageFileID("acct1", "sha-persist", "file_persist")

	// 模拟进程重启:清空内存并从磁盘加载。
	reloadClaudeImageFileCacheForTest()

	if id, ok := GetClaudeImageFileID("acct1", "sha-persist"); !ok || id != "file_persist" {
		t.Fatalf("after reload get = %q,%v, want file_persist,true", id, ok)
	}
}
