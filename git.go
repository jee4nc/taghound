package main

import (
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

type RepoInfo struct {
	Host      string
	Workspace string
	Slug      string
}

var scpSyntaxRe = regexp.MustCompile(`^(?:[^@/:]+@)?([^:/]+):(.+)$`)

// parseRemoteURL parses a git remote URL into host/workspace/slug.
// Supports SCP-like (git@host:ws/repo.git) and URL (https://host/ws/repo.git).
func parseRemoteURL(raw string) (RepoInfo, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return RepoInfo{}, fmt.Errorf("empty remote URL")
	}

	if !strings.Contains(raw, "://") {
		m := scpSyntaxRe.FindStringSubmatch(raw)
		if m == nil {
			return RepoInfo{}, fmt.Errorf("unrecognized remote URL: %s", raw)
		}
		return splitRepoPath(m[1], m[2])
	}

	u, err := url.Parse(raw)
	if err != nil {
		return RepoInfo{}, fmt.Errorf("invalid remote URL: %w", err)
	}
	if u.Host == "" {
		return RepoInfo{}, fmt.Errorf("remote URL missing host: %s", raw)
	}
	return splitRepoPath(u.Host, strings.TrimPrefix(u.Path, "/"))
}

func splitRepoPath(host, path string) (RepoInfo, error) {
	path = strings.TrimSuffix(path, ".git")
	path = strings.TrimSpace(path)
	if path == "" {
		return RepoInfo{}, fmt.Errorf("empty repo path")
	}
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return RepoInfo{}, fmt.Errorf("unexpected repo path: %s", path)
	}
	ws := parts[0]
	slug := parts[len(parts)-1]
	if ws == "" || slug == "" {
		return RepoInfo{}, fmt.Errorf("incomplete repo path: %s", path)
	}
	return RepoInfo{
		Host:      strings.ToLower(host),
		Workspace: ws,
		Slug:      slug,
	}, nil
}

// IsBitbucket reports whether the repo host is a Bitbucket Cloud host.
func (r RepoInfo) IsBitbucket() bool {
	return strings.HasSuffix(r.Host, "bitbucket.org")
}

// getGitRemoteURL runs `git remote get-url <name>` and returns the URL.
func getGitRemoteURL(name string) (string, error) {
	if name == "" {
		name = "origin"
	}
	out, err := gitOutput("remote", "get-url", name)
	if err != nil {
		return "", fmt.Errorf("could not get remote url for '%s': %w", name, err)
	}
	return strings.TrimSpace(out), nil
}

// detectRepoInfo reads `origin` remote and parses it.
func detectRepoInfo() (RepoInfo, error) {
	raw, err := getGitRemoteURL("origin")
	if err != nil {
		return RepoInfo{}, err
	}
	return parseRemoteURL(raw)
}

// gitCommitExists reports whether sha is a commit in the local clone.
func gitCommitExists(sha string) bool {
	if sha == "" {
		return false
	}
	return exec.Command("git", "cat-file", "-e", sha+"^{commit}").Run() == nil
}

// highestMergedTag returns the highest release tag reachable from commit.
// accept filters out tags that don't belong to a release line.
func highestMergedTag(commit string, tagRe *regexp.Regexp, tagGlob string, accept func(semver) bool) (releaseInfo, bool) {
	out, err := gitOutput("tag", "--merged", commit, "-l", tagGlob)
	if err != nil {
		return releaseInfo{}, false
	}
	var best releaseInfo
	found := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		v, ok := parseVersion(tagRe, line)
		if !ok || (accept != nil && !accept(v)) {
			continue
		}
		if !found || best.Version.Less(v) {
			best = releaseInfo{Name: line, Version: v, Source: "tag"}
			found = true
		}
	}
	return best, found
}

// oldestReleaseBranchContaining returns the lowest-versioned remote release
// branch that contains commit, i.e. the release line the commit belongs to.
func oldestReleaseBranchContaining(commit string, branchRe *regexp.Regexp) (releaseInfo, bool) {
	out, err := gitOutput("branch", "-r", "--contains", commit, "--format=%(refname:short)")
	if err != nil {
		return releaseInfo{}, false
	}
	var best releaseInfo
	found := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		v, ok := parseVersion(branchRe, line)
		if !ok {
			continue
		}
		if !found || v.Less(best.Version) {
			best = releaseInfo{Name: line, Version: v, Source: "branch"}
			found = true
		}
	}
	return best, found
}

// countCommits returns the number of commits in from..to.
func countCommits(from, to string) int {
	out, err := gitOutput("rev-list", "--count", from+".."+to)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(out)
	return n
}
