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

type deployStatus int

const (
	statusUnknown deployStatus = iota
	statusLatest
	statusUntagged
	statusBehind
)

type deployResult struct {
	Deploy *Deploy
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
			if d, ok := cache.Deploys[q.EnvName]; ok {
				results[q.EnvName] = deployResult{Deploy: d}
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
			d, err := provider.LastSuccessfulDeploy(ctx, q)
			mu.Lock()
			results[q.EnvName] = deployResult{Deploy: d, Err: err}
			mu.Unlock()
		})
	}
	wg.Wait()

	// Keep the original FetchedAt on partial refreshes so stale entries still expire.
	if cache == nil {
		cache = &deployCacheEntry{FetchedAt: time.Now(), Deploys: make(map[string]*Deploy)}
	}
	for _, q := range missing {
		if r := results[q.EnvName]; r.Err == nil {
			cache.Deploys[q.EnvName] = r.Deploy
		}
	}
	if err := saveDeployCache(repo, cache); err != nil {
		warn("Could not save deployments cache: " + err.Error())
	}
	return results, cachedAt
}

// resolveDeployedRef locates a deployed commit within the local release branches and tags.
func resolveDeployedRef(d *Deploy, branchRe, tagRe *regexp.Regexp, tagGlob string) deployedRef {
	r := deployedRef{Found: gitCommitExists(d.Commit)}

	// Prefer the ref the pipeline ran on: it's what the team actually deployed.
	if v, ok := parseVersion(branchRe, d.Branch); ok {
		r.Branch, r.Line, r.HasLine = d.Branch, v, true
	} else if v, ok := parseVersion(tagRe, d.Branch); ok {
		r.Line, r.HasLine = semver{Major: v.Major, Minor: v.Minor}, true
	}
	if !r.Found {
		return r
	}

	if t, ok := highestMergedTag(d.Commit, tagRe, tagGlob); ok {
		r.Tag, r.TagVer = t.Name, t.Version
		r.Ahead = countCommits(t.Name, d.Commit)
	}

	if !r.HasLine {
		if b, ok := oldestReleaseBranchContaining(d.Commit, branchRe); ok {
			r.Branch, r.Line, r.HasLine = b.Name, b.Version, true
		} else if r.Tag != "" {
			r.Line, r.HasLine = semver{Major: r.TagVer.Major, Minor: r.TagVer.Minor}, true
		}
	}
	return r
}

// classifyDeploy compares a deployed ref against the newest release branch and
// the newest tag of the deployed release line.
func classifyDeploy(r deployedRef, latestBranch, lineTag *releaseInfo) (deployStatus, string) {
	if r.HasLine && latestBranch != nil && r.Line.Less(latestBranch.Version) {
		return statusBehind, "newer release " + latestBranch.Name + " not deployed"
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
	tagsByLine := make(map[string][]releaseInfo)
	for _, t := range tags {
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
		fmt.Printf("\n  %s%s%s%s\n", Bold, White, strings.ToUpper(c.Code), Reset)
		for _, q := range deployQueries([]CountryConfig{c}, repo) {
			printDeployRow(q, results[q.EnvName], branchRe, tagRe, tagGlob, latestBranch, tagsByLine, bb.Lookback)
		}
	}

	if !cachedAt.IsZero() {
		fmt.Printf("\n  %sCached %s ago — use --refresh to update%s\n", Gray, time.Since(cachedAt).Round(time.Second), Reset)
	}
	fmt.Println()
	return nil
}

func printDeployRow(q EnvironmentQuery, res deployResult, branchRe, tagRe *regexp.Regexp, tagGlob string, latestBranch *releaseInfo, tagsByLine map[string][]releaseInfo, lookback int) {
	tier := fmt.Sprintf("%-5s", strings.ToUpper(q.Tier))
	prefix := fmt.Sprintf("     %s%s%s ", Cyan, tier, Reset)

	if res.Err != nil {
		fmt.Printf("%s%s✗ %s%s\n", prefix, Red, res.Err.Error(), Reset)
		return
	}
	if res.Deploy == nil {
		if lookback <= 0 {
			lookback = 40
		}
		fmt.Printf("%s%sno successful deploy to %s in the last %d deployments%s\n", prefix, Gray, q.EnvName, lookback, Reset)
		return
	}

	d := res.Deploy
	r := resolveDeployedRef(d, branchRe, tagRe, tagGlob)
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

	icon, color := "?", Gray
	switch status {
	case statusLatest:
		icon, color = "✓", Green
	case statusUntagged:
		icon, color = "●", Magenta
	case statusBehind:
		icon, color = "⚠", Yellow
	}

	fmt.Printf("%s%s%-22s%s %s%-12s%s %s%s%s  %s%s%s\n",
		prefix,
		Magenta, ref, Reset,
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
