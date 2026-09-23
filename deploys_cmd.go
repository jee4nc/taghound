package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	defaultAuthEnv     = "BITBUCKET_TOKEN"
	defaultUsernameEnv = "BITBUCKET_USERNAME"
	defaultCacheTTL    = 5 * time.Minute
	deploysTimeout     = 60 * time.Second
)

// deployedRef describes where a deployed commit sits in the local release history.
type deployedRef struct {
	Found   bool   // commit exists in the local clone
	Branch  string // release branch the commit belongs to, if known
	Line    semver // major.minor release line (Patch unused)
	HasLine bool
	Tag     string // highest release tag reachable from the commit
	TagVer  semver
	Ahead   int // commits after Tag
}

// releaseMatcher recognizes the profile's release branches and tags.
type releaseMatcher struct {
	branchRe *regexp.Regexp
	tagRe    *regexp.Regexp
	tagGlob  string
	lines    map[string]bool // major.minor of existing release branches; nil accepts any tag
}

// isReleaseTag reports whether v belongs to a known release line, so stray
// tags like v3950.3950.1 (made from a feature branch) are ignored.
func (m releaseMatcher) isReleaseTag(v semver) bool {
	return m.lines == nil || m.lines[lineKey(v)]
}

type deployStatus int

const (
	statusUnknown deployStatus = iota
	statusLatest
	statusUntagged
	statusBehind
	statusFeature
)

type deployResult struct {
	Lookup *DeployLookup
	Err    error
}

// deployQueries expands the configured countries into one query per environment.
func deployQueries(countries []CountryConfig, repo RepoInfo) []EnvironmentQuery {
	var out []EnvironmentQuery
	for _, c := range countries {
		if c.QAEnv != "" {
			out = append(out, EnvironmentQuery{CountryCode: c.Code, EnvName: expandEnvName(c.QAEnv, c.Code, repo), Tier: "qa"})
		}
		if c.ProdEnv != "" {
			out = append(out, EnvironmentQuery{CountryCode: c.Code, EnvName: expandEnvName(c.ProdEnv, c.Code, repo), Tier: "prod"})
		}
	}
	return out
}

// expandEnvName fills {slug}, {workspace} and {country} so one config works
// across every repo that follows the same environment naming convention.
func expandEnvName(tmpl, country string, repo RepoInfo) string {
	return strings.NewReplacer(
		"{slug}", repo.Slug,
		"{workspace}", repo.Workspace,
		"{country}", strings.ToLower(country),
		"{COUNTRY}", strings.ToUpper(country),
	).Replace(tmpl)
}

// fetchDeploys returns the last successful deploy per environment name,
// serving from cache when fresh and querying the provider concurrently otherwise.
// cachedAt is non-zero when at least one result came from cache.
func fetchDeploys(ctx context.Context, provider DeploymentProvider, repo RepoInfo, queries []EnvironmentQuery, ttl time.Duration, refresh bool) (results map[string]deployResult, cachedAt time.Time) {
	results = make(map[string]deployResult, len(queries))

	var cache *deployCacheEntry
	if !refresh {
		cache, _ = loadDeployCache(repo, ttl)
	}

	var missing []EnvironmentQuery
	for _, q := range queries {
		if cache != nil {
			if l, ok := cache.Deploys[q.EnvName]; ok && l != nil {
				results[q.EnvName] = deployResult{Lookup: l}
				cachedAt = cache.FetchedAt
				continue
			}
		}
		missing = append(missing, q)
	}
	if len(missing) == 0 {
		return results, cachedAt
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, q := range missing {
		wg.Go(func() {
			l, err := provider.LookupDeploy(ctx, q)
			mu.Lock()
			results[q.EnvName] = deployResult{Lookup: l, Err: err}
			mu.Unlock()
		})
	}
	wg.Wait()

	// Keep the original FetchedAt on partial refreshes so stale entries still expire.
	if cache == nil {
		cache = &deployCacheEntry{FetchedAt: time.Now(), Deploys: make(map[string]*DeployLookup)}
	}
	for _, q := range missing {
		if r := results[q.EnvName]; r.Err == nil {
			cache.Deploys[q.EnvName] = r.Lookup
		}
	}
	if err := saveDeployCache(repo, cache); err != nil {
		warn("Could not save deployments cache: " + err.Error())
	}
	return results, cachedAt
}

// resolveDeployedRef locates a deployed commit within the local release branches and tags.
func resolveDeployedRef(d *Deploy, m releaseMatcher) deployedRef {
	r := deployedRef{Found: gitCommitExists(d.Commit)}

	// Prefer the ref the pipeline ran on: it's what the team actually deployed.
	if v, ok := parseVersion(m.branchRe, d.Branch); ok {
		r.Branch, r.Line, r.HasLine = d.Branch, v, true
	} else if v, ok := parseVersion(m.tagRe, d.Branch); ok && m.isReleaseTag(v) {
		r.Line, r.HasLine = semver{Major: v.Major, Minor: v.Minor}, true
	}
	if !r.Found {
		return r
	}

	// A commit deployed from a non-release branch (e.g. a feature branch in QA)
	// only counts as a release if the pipeline ran on a tag or release branch.
	if !r.HasLine && isReleaseLikeRef(d.Branch, m) {
		if b, ok := oldestReleaseBranchContaining(d.Commit, m.branchRe); ok {
			r.Branch, r.Line, r.HasLine = b.Name, b.Version, true
		}
	}

	// Prefer a tag from the deployed release line: release-1.19 may have
	// v1.20.x merged in, but v1.19.x is what describes it.
	sameLine := func(v semver) bool { return m.isReleaseTag(v) && lineKey(v) == lineKey(r.Line) }
	t, ok := releaseInfo{}, false
	if r.HasLine {
		t, ok = highestMergedTag(d.Commit, m.tagRe, m.tagGlob, sameLine)
	}
	if !ok {
		t, ok = highestMergedTag(d.Commit, m.tagRe, m.tagGlob, m.isReleaseTag)
	}
	if ok {
		r.Tag, r.TagVer = t.Name, t.Version
		r.Ahead = countCommits(t.Name, d.Commit)
		if !r.HasLine && isReleaseLikeRef(d.Branch, m) {
			r.Line, r.HasLine = semver{Major: t.Version.Major, Minor: t.Version.Minor}, true
		}
	}
	return r
}

// isReleaseLikeRef reports whether ref is empty (unknown) or matches the
// profile's release branch/tag patterns.
func isReleaseLikeRef(ref string, m releaseMatcher) bool {
	if ref == "" {
		return true
	}
	_, isBranch := parseVersion(m.branchRe, ref)
	_, isTag := parseVersion(m.tagRe, ref)
	return isBranch || isTag
}

// classifyDeploy compares a deployed ref against the newest release branch and
// the newest tag of the deployed release line.
func classifyDeploy(r deployedRef, latestBranch, lineTag *releaseInfo) (deployStatus, string) {
	if r.HasLine && latestBranch != nil && r.Line.Less(latestBranch.Version) {
		return statusBehind, "newer release " + latestBranch.Name + " not deployed"
	}
	if !r.HasLine {
		return statusFeature, "not a release branch"
	}
	if !r.Found {
		return statusUnknown, "commit not found locally"
	}
	if lineTag != nil && (r.Tag == "" || r.TagVer.Less(lineTag.Version)) {
		return statusBehind, lineTag.Name + " not deployed"
	}
	if r.Ahead > 0 {
		return statusUntagged, fmt.Sprintf("%d commits not tagged", r.Ahead)
	}
	if r.Tag == "" {
		return statusUntagged, "no release tag"
	}
	return statusLatest, "latest"
}

// --- Command ---

func runDeploys(args []string, profileOverride string) error {
	refresh, listEnvs := false, false
	for _, a := range args {
		switch a {
		case "-r", "--refresh":
			refresh = true
		case "--envs":
			listEnvs = true
		default:
			return fmt.Errorf("unknown deploys option: '%s'. Run 'taghound -h' for options", a)
		}
	}

	if err := gitCheck(); err != nil {
		return fmt.Errorf("not inside a Git repository")
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	bb := cfg.Bitbucket
	if bb == nil {
		bb = &BitbucketConfig{}
	}
	if !listEnvs && len(bb.Countries) == 0 {
		return fmt.Errorf("no environments configured. Run 'taghound deploys --envs' to see them, then 'taghound config country set <code> --prod <env> [--qa <env>]'")
	}

	profile, err := resolveProfile(profileOverride)
	if err != nil {
		return err
	}

	repo, err := detectRepoInfo()
	if err != nil {
		return err
	}
	if !repo.IsBitbucket() {
		return fmt.Errorf("deploys only supports Bitbucket Cloud remotes (origin is %s)", repo.Host)
	}

	authEnv := bb.AuthEnv
	if authEnv == "" {
		authEnv = defaultAuthEnv
	}
	token := os.Getenv(authEnv)
	if token == "" {
		return fmt.Errorf("missing Bitbucket token: set %s", authEnv)
	}
	username := bb.Username
	if username == "" {
		username = os.Getenv(defaultUsernameEnv)
	}
	if listEnvs {
		return printEnvironments(NewBitbucketProvider(repo, token, username, bb.Lookback), repo)
	}

	ttl := defaultCacheTTL
	if bb.CacheTTLSeconds > 0 {
		ttl = time.Duration(bb.CacheTTLSeconds) * time.Second
	}

	branchRe := buildBranchRegex(profile.BranchPrefix)
	tagRe := buildTagRegex(profile.TagPrefix)
	tagGlob := buildTagSearchGlob(profile.TagPrefix)

	info("Syncing with remote...")
	if err := gitFetch(); err != nil {
		warn("Could not fetch from remote: " + err.Error())
	}

	branches := findReleaseBranches(branchRe)
	tags := findReleaseTags(tagRe, tagGlob)
	sortReleases(branches)
	sortReleases(tags)

	var latestBranch *releaseInfo
	if len(branches) > 0 {
		latestBranch = &branches[len(branches)-1]
	}
	matcher := releaseMatcher{branchRe: branchRe, tagRe: tagRe, tagGlob: tagGlob}
	if len(branches) > 0 {
		matcher.lines = make(map[string]bool, len(branches))
		for _, b := range branches {
			matcher.lines[lineKey(b.Version)] = true
		}
	}
	tagsByLine := make(map[string][]releaseInfo)
	for _, t := range tags {
		if !matcher.isReleaseTag(t.Version) {
			continue
		}
		key := lineKey(t.Version)
		tagsByLine[key] = append(tagsByLine[key], t)
	}

	info("Fetching deployments from Bitbucket...")
	ctx, cancel := context.WithTimeout(context.Background(), deploysTimeout)
	defer cancel()
	provider := NewBitbucketProvider(repo, token, username, bb.Lookback)
	results, cachedAt := fetchDeploys(ctx, provider, repo, deployQueries(bb.Countries, repo), ttl, refresh)

	fmt.Println()
	fmt.Printf("%s%s  TagHound — Deployments%s  %s%s/%s/%s%s\n", Bold, Cyan, Reset, Gray, repo.Host, repo.Workspace, repo.Slug, Reset)
	fmt.Printf("%s%s%s\n", Gray, strings.Repeat("─", 55), Reset)
	if latestBranch != nil {
		latestLabel := latestBranch.Name
		if lt := tagsByLine[lineKey(latestBranch.Version)]; len(lt) > 0 {
			latestLabel += "  →  🏷️  " + lt[len(lt)-1].Name
		}
		fmt.Printf("\n  %sLatest release:%s  %s%s%s%s\n", Gray, Reset, Bold, Magenta, latestLabel, Reset)
	}

	for _, c := range bb.Countries {
		queries := deployQueries([]CountryConfig{c}, repo)
		if allEnvsMissing(queries, results) {
			continue // this repo doesn't deploy to that country
		}
		fmt.Printf("\n  %s%s%s%s\n", Bold, White, strings.ToUpper(c.Code), Reset)
		for _, q := range queries {
			printDeployRow(q, results[q.EnvName], matcher, latestBranch, tagsByLine)
		}
	}

	if !cachedAt.IsZero() {
		fmt.Printf("\n  %sCached %s ago — use --refresh to update%s\n", Gray, time.Since(cachedAt).Round(time.Second), Reset)
	}
	fmt.Println()
	return nil
}

// allEnvsMissing reports whether none of a country's environments exist in the repo.
func allEnvsMissing(queries []EnvironmentQuery, results map[string]deployResult) bool {
	for _, q := range queries {
		r := results[q.EnvName]
		if r.Err != nil || r.Lookup == nil || !r.Lookup.EnvMissing {
			return false
		}
	}
	return len(queries) > 0
}

func printDeployRow(q EnvironmentQuery, res deployResult, m releaseMatcher, latestBranch *releaseInfo, tagsByLine map[string][]releaseInfo) {
	tier := fmt.Sprintf("%-5s", strings.ToUpper(q.Tier))
	prefix := fmt.Sprintf("     %s%s%s ", Cyan, tier, Reset)

	if res.Err != nil {
		fmt.Printf("%s%s✗ %s%s\n", prefix, Red, res.Err.Error(), Reset)
		return
	}
	l := res.Lookup
	switch {
	case l == nil || l.EnvMissing:
		fmt.Printf("%s%senvironment %s not defined in this repo%s\n", prefix, Gray, q.EnvName, Reset)
		return
	case l.Last == nil && l.Undeployed > 0:
		fmt.Printf("%s%snever deployed%s %s— %d pipelines stopped before this step%s\n", prefix, Yellow, Reset, Gray, l.Undeployed, Reset)
		return
	case l.Last == nil:
		fmt.Printf("%s%snever deployed%s\n", prefix, Gray, Reset)
		return
	}

	d := l.Last
	r := resolveDeployedRef(d, m)
	var lineTag *releaseInfo
	if r.HasLine {
		if lt := tagsByLine[lineKey(r.Line)]; len(lt) > 0 {
			lineTag = &lt[len(lt)-1]
		}
	}
	status, detail := classifyDeploy(r, latestBranch, lineTag)

	ref := d.Branch
	if r.Branch != "" {
		ref = r.Branch
	}
	if ref == "" {
		ref = "?"
	}
	tagLabel := "(no tag)"
	if r.Tag != "" {
		tagLabel = r.Tag
		if r.Ahead > 0 {
			tagLabel += fmt.Sprintf("+%d", r.Ahead)
		}
	}

	icon, color, refColor := "?", Gray, Magenta
	switch status {
	case statusLatest:
		icon, color = "✓", Green
	case statusUntagged:
		icon, color = "●", Magenta
	case statusBehind:
		icon, color = "⚠", Yellow
	case statusFeature:
		// Feature branches are expected in QA, but PROD should only get releases.
		icon, color, refColor = "●", Cyan, Cyan
		if q.Tier == "prod" {
			icon, color, refColor = "✗", Red, Red
			detail = "not a release branch deployed to PROD"
		}
	}

	fmt.Printf("%s%s%-22s%s %s%-12s%s %s%s%s  %s%s%s\n",
		prefix,
		Bold+refColor, ref, Reset,
		Green, tagLabel, Reset,
		Yellow, d.ShortSha, Reset,
		White, d.DeployedAt.Local().Format("2006-01-02 15:04"), Reset)
	fmt.Printf("           %s%s %s%s", Bold+color, icon, detail, Reset)
	if d.PipelineNum > 0 {
		fmt.Printf("  %spipeline #%d%s", Gray, d.PipelineNum, Reset)
	}
	fmt.Println()
}

func printEnvironments(provider *BitbucketProvider, repo RepoInfo) error {
	ctx, cancel := context.WithTimeout(context.Background(), deploysTimeout)
	defer cancel()
	envs, err := provider.ListEnvironments(ctx)
	if err != nil {
		return err
	}
	fmt.Println()
	fmt.Printf("  %s%sBitbucket environments%s  %s%s/%s%s\n", Bold, Cyan, Reset, Gray, repo.Workspace, repo.Slug, Reset)
	fmt.Printf("  %s%s%s\n", Gray, strings.Repeat("─", 40), Reset)
	if len(envs) == 0 {
		fmt.Printf("  %sNo environments found%s\n", Gray, Reset)
	}
	for _, e := range envs {
		fmt.Printf("  %s%-12s%s %s\n", Gray, e.Type, Reset, e.Name)
	}
	fmt.Println()
	fmt.Printf("  %sUse {slug} and {country} to reuse one config across repos, e.g.:%s\n", Gray, Reset)
	fmt.Printf("  %staghound config country set CL --prod 'prd-{slug}-{country}'%s\n\n", Cyan, Reset)
	return nil
}

func lineKey(v semver) string {
	return fmt.Sprintf("%d.%d", v.Major, v.Minor)
}

// --- Config: countries ---

func cmdConfigCountry(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: taghound config country <set|delete> <code> ...")
	}
	switch args[0] {
	case "set":
		return cmdConfigCountrySet(args[1:])
	case "delete":
		return cmdConfigCountryDelete(args[1:])
	default:
		return fmt.Errorf("unknown country command: '%s'. Run 'taghound -h' for options", args[0])
	}
}

func cmdConfigCountrySet(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: taghound config country set <code> --prod <env> [--qa <env>]")
	}
	code := strings.ToUpper(args[0])
	var prodEnv, qaEnv string
	remaining := args[1:]
	for i := 0; i < len(remaining); i++ {
		switch remaining[i] {
		case "--prod":
			if i+1 >= len(remaining) {
				return fmt.Errorf("--prod requires a value")
			}
			i++
			prodEnv = remaining[i]
		case "--qa":
			if i+1 >= len(remaining) {
				return fmt.Errorf("--qa requires a value")
			}
			i++
			qaEnv = remaining[i]
		default:
			return fmt.Errorf("unknown option: '%s'", remaining[i])
		}
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if cfg.Bitbucket == nil {
		cfg.Bitbucket = &BitbucketConfig{}
	}

	idx := -1
	for i, c := range cfg.Bitbucket.Countries {
		if strings.EqualFold(c.Code, code) {
			idx = i
			break
		}
	}
	if idx >= 0 {
		// Merge: only overwrite provided flags
		c := &cfg.Bitbucket.Countries[idx]
		if prodEnv != "" {
			c.ProdEnv = prodEnv
		}
		if qaEnv != "" {
			c.QAEnv = qaEnv
		}
	} else {
		if prodEnv == "" {
			return fmt.Errorf("--prod <env> is required to add a new country")
		}
		cfg.Bitbucket.Countries = append(cfg.Bitbucket.Countries, CountryConfig{Code: code, QAEnv: qaEnv, ProdEnv: prodEnv})
		idx = len(cfg.Bitbucket.Countries) - 1
	}

	if err := saveConfig(cfg); err != nil {
		return err
	}
	c := cfg.Bitbucket.Countries[idx]
	fmt.Printf("  %s%s✅ Country '%s' saved%s\n", Bold, Green, code, Reset)
	fmt.Printf("     %sprod:%s %s%s%s  %sqa:%s %s%s%s\n",
		Gray, Reset, Cyan, c.ProdEnv, Reset,
		Gray, Reset, Cyan, orDash(c.QAEnv), Reset)
	return nil
}

func cmdConfigCountryDelete(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: taghound config country delete <code>")
	}
	code := strings.ToUpper(args[0])
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if cfg.Bitbucket != nil {
		for i, c := range cfg.Bitbucket.Countries {
			if strings.EqualFold(c.Code, code) {
				cfg.Bitbucket.Countries = append(cfg.Bitbucket.Countries[:i], cfg.Bitbucket.Countries[i+1:]...)
				if err := saveConfig(cfg); err != nil {
					return err
				}
				fmt.Printf("  %s%s✅ Country '%s' deleted%s\n", Bold, Green, code, Reset)
				return nil
			}
		}
	}
	return fmt.Errorf("country '%s' is not configured", code)
}

func printBitbucketConfig(bb *BitbucketConfig) {
	if bb == nil || len(bb.Countries) == 0 {
		return
	}
	authEnv := bb.AuthEnv
	if authEnv == "" {
		authEnv = defaultAuthEnv
	}
	fmt.Println()
	fmt.Printf("  %s%sDeploy environments (Bitbucket)%s\n", Bold, Cyan, Reset)
	fmt.Printf("  %s%s%s\n", Gray, strings.Repeat("─", 40), Reset)
	fmt.Printf("  %sToken env:%s      %s%s%s\n", Gray, Reset, Cyan, authEnv, Reset)
	for _, c := range bb.Countries {
		fmt.Printf("  %s%-4s%s %sprod:%s %s%s%s  %sqa:%s %s%s%s\n",
			Bold, strings.ToUpper(c.Code), Reset,
			Gray, Reset, Cyan, c.ProdEnv, Reset,
			Gray, Reset, Cyan, orDash(c.QAEnv), Reset)
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
