package main

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// --- helpers ---

type fakeProvider struct {
	calls   atomic.Int32
	deploys map[string]*Deploy
	errs    map[string]error
}

func testMatcher() releaseMatcher {
	return releaseMatcher{
		branchRe: buildBranchRegex("release-"),
		tagRe:    buildTagRegex("v"),
		tagGlob:  "v*",
		lines:    map[string]bool{"1.0": true, "1.1": true},
	}
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) LookupDeploy(_ context.Context, q EnvironmentQuery) (*DeployLookup, error) {
	f.calls.Add(1)
	if err := f.errs[q.EnvName]; err != nil {
		return nil, err
	}
	return &DeployLookup{Last: f.deploys[q.EnvName]}, nil
}

// newTestRepo creates a repo with a bare "origin" and chdirs into it.
//
//	main:        c1 (v1.0.0) ─ c2 (v1.0.1)
//	release-1.0: c1 ─ c2 ─ c3 (untagged) ─ c6 (merge of release-1.1)
//	release-1.1: c1 ─ c2 ─ c4 (v1.1.0)
//	feature/x:   c4 ─ c5 (stray tag v3950.3950.1)
func newTestRepo(t *testing.T) map[string]string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	remote := root + "/remote.git"
	work := root + "/work"

	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(msg string) string {
		run(work, "commit", "--allow-empty", "-q", "-m", msg)
		return run(work, "rev-parse", "HEAD")
	}

	run(root, "init", "-q", "--bare", remote)
	run(root, "init", "-q", "-b", "main", work)
	run(work, "remote", "add", "origin", remote)

	shas := map[string]string{}
	shas["c1"] = commit("c1")
	run(work, "tag", "v1.0.0")
	shas["c2"] = commit("c2")
	run(work, "tag", "v1.0.1")
	run(work, "checkout", "-q", "-b", "release-1.0")
	shas["c3"] = commit("c3")
	run(work, "checkout", "-q", "main")
	run(work, "checkout", "-q", "-b", "release-1.1")
	shas["c4"] = commit("c4")
	run(work, "tag", "v1.1.0")
	run(work, "checkout", "-q", "-b", "feature/x")
	shas["c5"] = commit("c5")
	run(work, "tag", "v3950.3950.1")
	run(work, "checkout", "-q", "release-1.0")
	run(work, "merge", "-q", "--no-ff", "-m", "c6", "release-1.1")
	shas["c6"] = run(work, "rev-parse", "HEAD")
	run(work, "push", "-q", "origin", "main", "release-1.0", "release-1.1", "feature/x", "--tags")
	run(work, "fetch", "-q", "origin")

	t.Chdir(work)
	return shas
}

// --- tests ---

func TestExpandEnvName(t *testing.T) {
	repo := RepoInfo{Host: "bitbucket.org", Workspace: "acme", Slug: "push-config"}
	tests := []struct {
		tmpl, country, want string
	}{
		{"prd-{slug}-{country}", "CL", "prd-push-config-cl"},
		{"{workspace}-{COUNTRY}-prod", "cl", "acme-CL-prod"},
		{"prd-push-config-cl", "CL", "prd-push-config-cl"},
	}
	for _, tt := range tests {
		if got := expandEnvName(tt.tmpl, tt.country, repo); got != tt.want {
			t.Errorf("expandEnvName(%q, %q) = %q, want %q", tt.tmpl, tt.country, got, tt.want)
		}
	}
}

func TestDeployQueries(t *testing.T) {
	repo := RepoInfo{Slug: "svc"}
	qs := deployQueries([]CountryConfig{
		{Code: "CL", QAEnv: "qa-{slug}-{country}", ProdEnv: "prd-{slug}-{country}"},
		{Code: "PE", ProdEnv: "prd-{slug}-{country}"},
	}, repo)
	want := []EnvironmentQuery{
		{CountryCode: "CL", EnvName: "qa-svc-cl", Tier: "qa"},
		{CountryCode: "CL", EnvName: "prd-svc-cl", Tier: "prod"},
		{CountryCode: "PE", EnvName: "prd-svc-pe", Tier: "prod"},
	}
	if len(qs) != len(want) {
		t.Fatalf("got %d queries, want %d: %+v", len(qs), len(want), qs)
	}
	for i := range want {
		if qs[i] != want[i] {
			t.Errorf("query[%d] = %+v, want %+v", i, qs[i], want[i])
		}
	}
}

func TestClassifyDeploy(t *testing.T) {
	rel11 := &releaseInfo{Name: "origin/release-1.1", Version: semver{1, 1, 0}}
	tag101 := &releaseInfo{Name: "v1.0.1", Version: semver{1, 0, 1}}
	tag110 := &releaseInfo{Name: "v1.1.0", Version: semver{1, 1, 0}}

	tests := []struct {
		name       string
		ref        deployedRef
		latest     *releaseInfo
		lineTag    *releaseInfo
		wantStatus deployStatus
		wantDetail string
	}{
		{
			name:       "newer release branch not deployed",
			ref:        deployedRef{Found: true, HasLine: true, Line: semver{1, 0, 0}, Tag: "v1.0.1", TagVer: semver{1, 0, 1}},
			latest:     rel11,
			lineTag:    tag101,
			wantStatus: statusBehind,
			wantDetail: "origin/release-1.1",
		},
		{
			name:       "older tag in same line",
			ref:        deployedRef{Found: true, HasLine: true, Line: semver{1, 1, 0}, Tag: "v1.0.1", TagVer: semver{1, 0, 1}},
			latest:     rel11,
			lineTag:    tag110,
			wantStatus: statusBehind,
			wantDetail: "v1.1.0",
		},
		{
			name:       "latest tag deployed",
			ref:        deployedRef{Found: true, HasLine: true, Line: semver{1, 1, 0}, Tag: "v1.1.0", TagVer: semver{1, 1, 0}},
			latest:     rel11,
			lineTag:    tag110,
			wantStatus: statusLatest,
		},
		{
			name:       "commits after latest tag",
			ref:        deployedRef{Found: true, HasLine: true, Line: semver{1, 1, 0}, Tag: "v1.1.0", TagVer: semver{1, 1, 0}, Ahead: 3},
			latest:     rel11,
			lineTag:    tag110,
			wantStatus: statusUntagged,
			wantDetail: "3 commits",
		},
		{
			name:       "new line without tags yet",
			ref:        deployedRef{Found: true, HasLine: true, Line: semver{1, 1, 0}, Tag: "v1.0.1", TagVer: semver{1, 0, 1}, Ahead: 1},
			latest:     rel11,
			wantStatus: statusUntagged,
		},
		{
			name:       "feature branch",
			ref:        deployedRef{Found: true, Tag: "v1.1.0", TagVer: semver{1, 1, 0}, Ahead: 23},
			latest:     rel11,
			wantStatus: statusFeature,
		},
		{
			name:       "commit not fetched",
			ref:        deployedRef{HasLine: true, Line: semver{1, 1, 0}},
			latest:     rel11,
			wantStatus: statusUnknown,
		},
		{
			name:       "unknown commit on old line is still behind",
			ref:        deployedRef{HasLine: true, Line: semver{1, 0, 0}},
			latest:     rel11,
			wantStatus: statusBehind,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, detail := classifyDeploy(tt.ref, tt.latest, tt.lineTag)
			if status != tt.wantStatus {
				t.Errorf("status = %v, want %v (detail %q)", status, tt.wantStatus, detail)
			}
			if !strings.Contains(detail, tt.wantDetail) {
				t.Errorf("detail = %q, want it to contain %q", detail, tt.wantDetail)
			}
		})
	}
}

func TestResolveDeployedRef(t *testing.T) {
	shas := newTestRepo(t)
	m := testMatcher()

	t.Run("pipeline branch with untagged commits", func(t *testing.T) {
		r := resolveDeployedRef(&Deploy{Commit: shas["c3"], Branch: "release-1.0"}, m)
		if !r.Found || r.Branch != "release-1.0" || r.Line != (semver{1, 0, 0}) {
			t.Fatalf("unexpected ref: %+v", r)
		}
		if r.Tag != "v1.0.1" || r.Ahead != 1 {
			t.Errorf("tag = %q +%d, want v1.0.1 +1", r.Tag, r.Ahead)
		}
	})

	t.Run("tag pipeline", func(t *testing.T) {
		r := resolveDeployedRef(&Deploy{Commit: shas["c4"], Branch: "v1.1.0"}, m)
		if r.Line != (semver{1, 1, 0}) || r.Tag != "v1.1.0" || r.Ahead != 0 {
			t.Errorf("unexpected ref: %+v", r)
		}
	})

	t.Run("prefers the tag of the deployed release line", func(t *testing.T) {
		// c6 on release-1.0 has v1.1.0 merged in, but v1.0.1 describes it.
		r := resolveDeployedRef(&Deploy{Commit: shas["c6"], Branch: "release-1.0"}, m)
		if r.Tag != "v1.0.1" {
			t.Errorf("tag = %q, want v1.0.1", r.Tag)
		}
	})

	t.Run("feature branch is not a release", func(t *testing.T) {
		r := resolveDeployedRef(&Deploy{Commit: shas["c5"], Branch: "feature/x"}, m)
		if r.HasLine || r.Branch != "" {
			t.Errorf("feature deploy should have no release line: %+v", r)
		}
		if r.Tag != "v1.1.0" || r.Ahead != 1 {
			t.Errorf("tag = %q +%d, want v1.1.0 +1 (stray v3950 tag ignored)", r.Tag, r.Ahead)
		}
	})

	t.Run("non-release branch is not a release even if a release contains it", func(t *testing.T) {
		r := resolveDeployedRef(&Deploy{Commit: shas["c3"], Branch: "hotfix/login"}, m)
		if r.HasLine {
			t.Errorf("unexpected release line: %+v", r)
		}
	})

	t.Run("unknown ref falls back to the oldest containing release branch", func(t *testing.T) {
		r := resolveDeployedRef(&Deploy{Commit: shas["c2"]}, m)
		if r.Branch != "origin/release-1.0" || r.Tag != "v1.0.1" {
			t.Errorf("unexpected ref: %+v", r)
		}
	})

	t.Run("unknown commit", func(t *testing.T) {
		r := resolveDeployedRef(&Deploy{Commit: strings.Repeat("a", 40), Branch: "release-1.1"}, m)
		if r.Found || !r.HasLine || r.Tag != "" {
			t.Errorf("unexpected ref: %+v", r)
		}
	})
}

func TestReleaseMatcherIgnoresStrayTags(t *testing.T) {
	m := testMatcher()
	if !m.isReleaseTag(semver{1, 1, 3}) {
		t.Error("v1.1.3 should be a release tag")
	}
	if m.isReleaseTag(semver{3950, 3950, 1}) {
		t.Error("v3950.3950.1 should be ignored")
	}
	if !(releaseMatcher{}).isReleaseTag(semver{3950, 3950, 1}) {
		t.Error("without release branches every tag is accepted")
	}
}

func TestFetchDeploysUsesCache(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	t.Setenv("HOME", tmp)

	repo := RepoInfo{Host: "bitbucket.org", Workspace: "ws", Slug: "fetch-cache"}
	queries := []EnvironmentQuery{
		{CountryCode: "CL", EnvName: "prd-cl", Tier: "prod"},
		{CountryCode: "PE", EnvName: "prd-pe", Tier: "prod"},
	}
	fake := &fakeProvider{
		deploys: map[string]*Deploy{"prd-cl": {Branch: "release-1.0", Commit: "abc"}},
		errs:    map[string]error{"prd-pe": errors.New("boom")},
	}
	ctx := context.Background()

	res, cachedAt := fetchDeploys(ctx, fake, repo, queries, time.Minute, false)
	if !cachedAt.IsZero() {
		t.Errorf("first call should not be cached")
	}
	if res["prd-cl"].Lookup == nil || res["prd-cl"].Lookup.Last == nil || res["prd-pe"].Err == nil {
		t.Fatalf("unexpected results: %+v", res)
	}
	if n := fake.calls.Load(); n != 2 {
		t.Fatalf("calls = %d, want 2", n)
	}

	// Errors are not cached, so only prd-pe is retried.
	res, cachedAt = fetchDeploys(ctx, fake, repo, queries, time.Minute, false)
	if cachedAt.IsZero() {
		t.Errorf("second call should report cache use")
	}
	if n := fake.calls.Load(); n != 3 {
		t.Errorf("calls = %d, want 3", n)
	}
	if l := res["prd-cl"].Lookup; l == nil || l.Last == nil || l.Last.Commit != "abc" {
		t.Errorf("cached deploy missing: %+v", res["prd-cl"])
	}

	// --refresh bypasses the cache entirely.
	fetchDeploys(ctx, fake, repo, queries, time.Minute, true)
	if n := fake.calls.Load(); n != 5 {
		t.Errorf("calls = %d, want 5", n)
	}
}
