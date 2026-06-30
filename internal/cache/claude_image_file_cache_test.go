package cache

import "testing"

func TestClaudeImageFileCacheStoreGet(t *testing.T) {
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
	ClearClaudeImageFileCache()
	defer ClearClaudeImageFileCache()

	SetClaudeImageFileID("acct1", "sha-a", "")
	if _, ok := GetClaudeImageFileID("acct1", "sha-a"); ok {
		t.Fatal("empty file_id should not be stored")
	}
}
