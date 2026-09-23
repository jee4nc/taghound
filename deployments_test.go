package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCacheFilePathStable(t *testing.T) {
	repo := RepoInfo{Host: "bitbucket.org", Workspace: "ws", Slug: "repo"}
	p1, err := cacheFilePath(repo)
	if err != nil {
		t.Fatalf("cacheFilePath: %v", err)
	}
	p2, err := cacheFilePath(repo)
	if err != nil {
		t.Fatalf("cacheFilePath second call: %v", err)
	}
	if p1 != p2 {
		t.Errorf("expected stable path, got %q vs %q", p1, p2)
	}
	if filepath.Base(p1) == "" {
		t.Errorf("expected non-empty cache filename")
	}
}

func TestCacheFilePathDiffersPerRepo(t *testing.T) {
	a, _ := cacheFilePath(RepoInfo{Host: "bitbucket.org", Workspace: "ws", Slug: "repoA"})
	b, _ := cacheFilePath(RepoInfo{Host: "bitbucket.org", Workspace: "ws", Slug: "repoB"})
	if a == b {
		t.Errorf("different repos should yield different cache paths")
	}
}

func TestDeployCacheTTL(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	// macOS honors HOME for os.UserCacheDir when XDG not used; set both.
	t.Setenv("HOME", tmp)

	repo := RepoInfo{Host: "bitbucket.org", Workspace: "ws", Slug: "repo-ttl"}

	entry := &deployCacheEntry{
		FetchedAt: time.Now(),
		Deploys: map[string]*DeployLookup{
			"prd-push-config-cl": {Last: &Deploy{Branch: "release-1.4", Commit: "abc"}},
		},
	}
	if err := saveDeployCache(repo, entry); err != nil {
		t.Fatalf("saveDeployCache: %v", err)
	}

	// fresh load should succeed
	loaded, err := loadDeployCache(repo, 5*time.Minute)
	if err != nil {
		t.Fatalf("loadDeployCache fresh: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected cache hit, got nil")
	}
	if loaded.Deploys["prd-push-config-cl"].Last.Branch != "release-1.4" {
		t.Errorf("unexpected branch: %+v", loaded.Deploys)
	}

	// expired TTL should return nil (tiny TTL + brief sleep forces expiry)
	time.Sleep(2 * time.Millisecond)
	expired, err := loadDeployCache(repo, 1*time.Nanosecond)
	if err != nil {
		t.Fatalf("loadDeployCache expired: %v", err)
	}
	if expired != nil {
		t.Errorf("expected cache miss on expired TTL, got %+v", expired)
	}
}

func TestDeployCacheMissing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	t.Setenv("HOME", tmp)

	repo := RepoInfo{Host: "bitbucket.org", Workspace: "ws", Slug: "missing-repo"}
	entry, err := loadDeployCache(repo, time.Minute)
	if err != nil {
		t.Fatalf("unexpected error for missing cache: %v", err)
	}
	if entry != nil {
		t.Errorf("expected nil for missing cache, got %+v", entry)
	}
}

func TestDeployCacheCorrupt(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	t.Setenv("HOME", tmp)

	repo := RepoInfo{Host: "bitbucket.org", Workspace: "ws", Slug: "corrupt-repo"}
	p, err := cacheFilePath(repo)
	if err != nil {
		t.Fatalf("cacheFilePath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}

	entry, err := loadDeployCache(repo, time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry != nil {
		t.Errorf("expected nil for corrupt cache, got %+v", entry)
	}
}

func TestDeployRoundtripJSON(t *testing.T) {
	d := &Deploy{
		Branch:      "release-1.4",
		Commit:      "5c02bed1234567890",
		ShortSha:    "5c02bed",
		DeployedAt:  time.Now().UTC().Truncate(time.Second),
		PipelineURL: "https://bitbucket.org/ws/repo/pipelines/results/1409",
		PipelineNum: 1409,
		EnvName:     "prd-push-config-cl",
		State:       "SUCCESSFUL",
	}
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var loaded Deploy
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if loaded.Branch != d.Branch || loaded.Commit != d.Commit || !loaded.DeployedAt.Equal(d.DeployedAt) {
		t.Errorf("roundtrip mismatch: %+v vs %+v", loaded, d)
	}
}
