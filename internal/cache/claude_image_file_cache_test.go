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

func TestClaudeImageOrphanQueueEnqueueDrain(t *testing.T) {
	// 登记两个孤儿,drain 应一次性取出并清空。
	enqueueClaudeImageOrphan("acctA", "file_x")
	enqueueClaudeImageOrphan("acctA", "file_y")
	enqueueClaudeImageOrphan("acctB", "file_z")

	got := DrainClaudeImageOrphans("acctA")
	if len(got) != 2 || got[0] != "file_x" || got[1] != "file_y" {
		t.Fatalf("drain acctA = %v, want [file_x file_y]", got)
	}
	// 再次 drain 应为空(已清空)。
	if again := DrainClaudeImageOrphans("acctA"); len(again) != 0 {
		t.Fatalf("second drain = %v, want empty", again)
	}
	// 账号隔离:acctB 不受影响。
	if b := DrainClaudeImageOrphans("acctB"); len(b) != 1 || b[0] != "file_z" {
		t.Fatalf("drain acctB = %v, want [file_z]", b)
	}
}

func TestAccountKeyFromCacheKey(t *testing.T) {
	// account 部分含点和邮箱,sha256 为 hex,用最后一个 | 拆分。
	acct, ok := accountKeyFromCacheKey("claude-user@x.com.json|abcdef123456")
	if !ok || acct != "claude-user@x.com.json" {
		t.Fatalf("account = %q,%v", acct, ok)
	}
	if _, ok := accountKeyFromCacheKey("no-separator"); ok {
		t.Fatal("expected failure when no separator")
	}
	if _, ok := accountKeyFromCacheKey("|onlysha"); ok {
		t.Fatal("expected failure when account empty")
	}
}
