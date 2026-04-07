package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ANSI colors
const (
	Reset   = "\033[0m"
	Bold    = "\033[1m"
	Red     = "\033[31m"
	Green   = "\033[32m"
	Yellow  = "\033[33m"
	Blue    = "\033[34m"
	Magenta = "\033[35m"
	Cyan    = "\033[36m"
	White   = "\033[37m"
	Gray    = "\033[90m"
)

// Version is set at build time via ldflags.
var Version = "dev"

// --- Types ---

type semver struct {
	Major int
	Minor int
	Patch int
}

func (s semver) String() string {
	return fmt.Sprintf("%d.%d.%d", s.Major, s.Minor, s.Patch)
}

func (s semver) Less(other semver) bool {
	if s.Major != other.Major {
		return s.Major < other.Major
	}
	if s.Minor != other.Minor {
		return s.Minor < other.Minor
	}
	return s.Patch < other.Patch
}

type releaseInfo struct {
	Name    string
	Version semver
	Commit  string
	Date    string
	Author  string
	Message string
	Source  string // "branch" or "tag"
}

type Profile struct {
	BranchPrefix string `json:"branch_prefix"`
	TagPrefix    string `json:"tag_prefix"`
}

type Config struct {
	Active   string             `json:"active"`
	Profiles map[string]Profile `json:"profiles"`
}

// --- Main ---

func main() {
	args := os.Args[1:]
	var dirtyMode bool
	var profileOverride string
	var positional []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-v", "--version":
			fmt.Printf("taghound %s\n", Version)
			return
		case "-h", "--help":
			printUsage()
			return
		case "--dirty":
			dirtyMode = true
		case "--profile":
			if i+1 >= len(args) {
				fatal("--profile requires a profile name")
			}
			i++
			profileOverride = args[i]
		default:
			positional = append(positional, args[i])
		}
	}

	if len(positional) > 0 && positional[0] == "config" {
		if err := handleConfig(positional[1:]); err != nil {
			fatal(err.Error())
		}
		return
	}

	if err := runTracker(profileOverride, dirtyMode); err != nil {
		fatal(err.Error())
	}
}

func printUsage() {
	fmt.Printf(`%s%sTagHound%s — Release Tracker

%sUsage:%s
  taghound                       Show releases (branches and tags)
  taghound --dirty               Include orphan tags (no matching branch)
  taghound --profile <name>      Use a specific profile for this run
  taghound config <command>      Manage configuration profiles

%sConfig commands:%s
  config list                    List all profiles
  config show                    Show active profile
  config set <name> --branch <prefix> --tag <prefix>
                                 Create or update a profile
  config use <name>              Switch active profile
  config delete <name>           Delete a profile (cannot delete 'default')

%sOptions:%s
  -h, --help                     Show this help
  -v, --version                  Show version
  --dirty                        Include tags without a matching branch
  --profile <name>               Use a temporary profile (does not change active)

%sExamples:%s
  taghound                                            # uses active profile
  taghound config set deploy --branch deploy- --tag release-
  taghound config use deploy
  taghound --profile default --dirty                   # temporary override
`, Bold, Cyan, Reset, Bold, Reset, Bold, Reset, Bold, Reset, Bold, Reset)
}

// --- Config I/O ---

func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", "taghound", "config.json"), nil
}

func defaultConfig() Config {
	return Config{
		Active: "default",
		Profiles: map[string]Profile{
			"default": {BranchPrefix: "release-", TagPrefix: "v"},
		},
	}
}

func loadConfig() (Config, error) {
	path, err := configPath()
	if err != nil {
		return defaultConfig(), nil // fallback to defaults if home dir unavailable
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return defaultConfig(), nil
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		warn("Corrupt config, using defaults: " + err.Error())
		return defaultConfig(), nil
	}
	if cfg.Profiles == nil {
		cfg.Profiles = make(map[string]Profile)
	}
	// Ensure default profile always exists
	if _, ok := cfg.Profiles["default"]; !ok {
		cfg.Profiles["default"] = Profile{BranchPrefix: "release-", TagPrefix: "v"}
	}
	if cfg.Active == "" {
		cfg.Active = "default"
	}
	return cfg, nil
}

func saveConfig(cfg Config) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("could not create config directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("could not serialize config: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("could not save config: %w", err)
	}
	return nil
}

// --- Config Commands ---

func handleConfig(args []string) error {
	if len(args) == 0 {
		fmt.Printf("  %s%s⚠️  Use 'taghound config <command>'. Run 'taghound -h' for options.%s\n", Bold, Yellow, Reset)
		return nil
	}

	switch args[0] {
	case "list":
		return cmdConfigList()
	case "show":
		return cmdConfigShow()
	case "set":
		return cmdConfigSet(args[1:])
	case "use":
		return cmdConfigUse(args[1:])
	case "delete":
		return cmdConfigDelete(args[1:])
	default:
		return fmt.Errorf("unknown config command: '%s'. Run 'taghound -h' for options", args[0])
	}
}

func cmdConfigList() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	fmt.Println()
	fmt.Printf("  %s%sTagHound Profiles%s\n", Bold, Cyan, Reset)
	fmt.Printf("  %s%s%s\n", Gray, strings.Repeat("─", 40), Reset)

	// Sort profile names for consistent output
	names := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		p := cfg.Profiles[name]
		marker := "  "
		nameColor := White
		if name == cfg.Active {
			marker = "▶ "
			nameColor = Green
		}
		fmt.Printf("  %s%s%s%s%s  %sbranch:%s %s%s%s  %stag:%s %s%s%s\n",
			marker, Bold, nameColor, name, Reset,
			Gray, Reset, Cyan, p.BranchPrefix, Reset,
			Gray, Reset, Cyan, p.TagPrefix, Reset)
	}
	fmt.Println()
	return nil
}

func cmdConfigShow() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	p, ok := cfg.Profiles[cfg.Active]
	if !ok {
		return fmt.Errorf("active profile '%s' not found in config", cfg.Active)
	}
	fmt.Println()
	fmt.Printf("  %s%sActive profile: %s%s\n", Bold, Cyan, cfg.Active, Reset)
	fmt.Printf("  %s%s%s\n", Gray, strings.Repeat("─", 40), Reset)
	fmt.Printf("  %sBranch prefix:%s  %s%s%s\n", Gray, Reset, Cyan, p.BranchPrefix, Reset)
	fmt.Printf("  %sTag prefix:%s     %s%s%s\n", Gray, Reset, Cyan, p.TagPrefix, Reset)
	fmt.Printf("  %sBranch regex:%s   %s%s%s\n", Gray, Reset, Gray, buildBranchPattern(p.BranchPrefix), Reset)
	fmt.Printf("  %sTag regex:%s      %s%s%s\n", Gray, Reset, Gray, buildTagPattern(p.TagPrefix), Reset)
	fmt.Println()
	return nil
}

func cmdConfigSet(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: taghound config set <name> --branch <prefix> --tag <prefix>")
	}

	name := args[0]
	remaining := args[1:]

	var branchPrefix, tagPrefix string
	for i := 0; i < len(remaining); i++ {
		switch remaining[i] {
		case "--branch":
			if i+1 >= len(remaining) {
				return fmt.Errorf("--branch requires a value")
			}
			i++
			branchPrefix = remaining[i]
		case "--tag":
			if i+1 >= len(remaining) {
				return fmt.Errorf("--tag requires a value")
			}
			i++
			tagPrefix = remaining[i]
		default:
			return fmt.Errorf("unknown option: '%s'", remaining[i])
		}
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	existing, exists := cfg.Profiles[name]
	if exists {
		// Merge: only overwrite provided flags
		if branchPrefix != "" {
			existing.BranchPrefix = branchPrefix
		}
		if tagPrefix != "" {
			existing.TagPrefix = tagPrefix
		}
		cfg.Profiles[name] = existing
	} else {
		if branchPrefix == "" || tagPrefix == "" {
			return fmt.Errorf("both --branch <prefix> and --tag <prefix> are required to create a new profile")
		}
		cfg.Profiles[name] = Profile{BranchPrefix: branchPrefix, TagPrefix: tagPrefix}
	}

	if err := saveConfig(cfg); err != nil {
		return err
	}
	p := cfg.Profiles[name]
	if exists {
		fmt.Printf("  %s%s✅ Profile '%s' updated%s\n", Bold, Green, name, Reset)
	} else {
		fmt.Printf("  %s%s✅ Profile '%s' created%s\n", Bold, Green, name, Reset)
	}
	fmt.Printf("     %sbranch:%s %s%s%s  %stag:%s %s%s%s\n",
		Gray, Reset, Cyan, p.BranchPrefix, Reset,
		Gray, Reset, Cyan, p.TagPrefix, Reset)
	return nil
}

func cmdConfigUse(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: taghound config use <name>")
	}
	name := args[0]
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	if _, ok := cfg.Profiles[name]; !ok {
		return fmt.Errorf("profile '%s' does not exist. Run 'taghound config list' to see available profiles", name)
	}

	cfg.Active = name
	if err := saveConfig(cfg); err != nil {
		return err
	}
	fmt.Printf("  %s%s✅ Active profile: %s%s\n", Bold, Green, name, Reset)
	return nil
}

func cmdConfigDelete(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: taghound config delete <name>")
	}
	name := args[0]

	if name == "default" {
		return fmt.Errorf("cannot delete the 'default' profile")
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if _, ok := cfg.Profiles[name]; !ok {
		return fmt.Errorf("profile '%s' does not exist", name)
	}

	delete(cfg.Profiles, name)
	if cfg.Active == name {
		cfg.Active = "default"
	}
	if err := saveConfig(cfg); err != nil {
		return err
	}
	fmt.Printf("  %s%s✅ Profile '%s' deleted%s\n", Bold, Green, name, Reset)
	return nil
}

// --- Dynamic Regex ---

func buildBranchPattern(prefix string) string {
	return fmt.Sprintf(`^(?:origin/)?%s(\d+)\.(\d+)$`, regexp.QuoteMeta(prefix))
}

func buildTagPattern(prefix string) string {
	return fmt.Sprintf(`^%s(\d+)\.(\d+)\.(\d+)$`, regexp.QuoteMeta(prefix))
}

func buildBranchRegex(prefix string) *regexp.Regexp {
	return regexp.MustCompile(buildBranchPattern(prefix))
}

func buildTagRegex(prefix string) *regexp.Regexp {
	return regexp.MustCompile(buildTagPattern(prefix))
}

func buildTagSearchGlob(prefix string) string {
	return prefix + "*"
}

// --- Profile Resolution ---

func resolveProfile(profileOverride string) (Profile, error) {
	cfg, err := loadConfig()
	if err != nil {
		return Profile{}, err
	}
	name := cfg.Active
	if profileOverride != "" {
		name = profileOverride
	}
	p, ok := cfg.Profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("profile '%s' not found. Run 'taghound config list' to see available profiles", name)
	}
	return p, nil
}

// --- Tracker ---

func runTracker(profileOverride string, dirtyMode bool) error {
	if err := gitCheck(); err != nil {
		return fmt.Errorf("not inside a Git repository")
	}

	profile, err := resolveProfile(profileOverride)
	if err != nil {
		return err
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

	if len(branches) == 0 && len(tags) == 0 {
		return fmt.Errorf("no branches (%s) or tags (%s) found with the active profile",
			buildBranchPattern(profile.BranchPrefix), buildTagPattern(profile.TagPrefix))
	}

	sortReleases(branches)
	sortReleases(tags)

	tagsByVersion := make(map[string][]releaseInfo)
	for _, t := range tags {
		key := fmt.Sprintf("%d.%d", t.Version.Major, t.Version.Minor)
		tagsByVersion[key] = append(tagsByVersion[key], t)
	}

	// Print header
	fmt.Println()
	fmt.Printf("%s%s  TagHound — Release Tracker%s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s%s\n", Gray, strings.Repeat("─", 55), Reset)

	if len(branches) > 0 {
		latest := branches[len(branches)-1]
		fmt.Println()
		fmt.Printf("%s%s  🚀 Latest release on origin%s\n", Bold, Green, Reset)
		fmt.Printf("%s%s%s\n", Gray, strings.Repeat("─", 55), Reset)
		printBranchWithTag(latest, tagsByVersion)
	}

	if len(branches) > 1 {
		fmt.Println()
		fmt.Printf("%s%s  📦 Other releases on origin%s\n", Bold, Blue, Reset)
		fmt.Printf("%s%s%s\n", Gray, strings.Repeat("─", 55), Reset)
		for i := len(branches) - 2; i >= 0; i-- {
			printBranchWithTag(branches[i], tagsByVersion)
		}
	}

	if dirtyMode {
		branchVersions := make(map[string]bool)
		for _, b := range branches {
			key := fmt.Sprintf("%d.%d", b.Version.Major, b.Version.Minor)
			branchVersions[key] = true
		}
		var orphanTags []releaseInfo
		for _, t := range tags {
			key := fmt.Sprintf("%d.%d", t.Version.Major, t.Version.Minor)
			if !branchVersions[key] {
				orphanTags = append(orphanTags, t)
			}
		}
		if len(orphanTags) > 0 {
			fmt.Println()
			fmt.Printf("%s%s  🏷️  Tags without a matching branch (dirty)%s\n", Bold, Yellow, Reset)
			fmt.Printf("%s%s%s\n", Gray, strings.Repeat("─", 55), Reset)
			for i := len(orphanTags) - 1; i >= 0; i-- {
				t := orphanTags[i]
				fmt.Printf("     %s%s%s\n", Gray, t.Name, Reset)
			}
		} else {
			fmt.Println()
			fmt.Printf("  %s%s✓ All tags match a release branch%s\n", Bold, Green, Reset)
		}
	}

	fmt.Println()
	return nil
}

// --- Output ---

func printBranchWithTag(b releaseInfo, tagsByVersion map[string][]releaseInfo) {
	key := fmt.Sprintf("%d.%d", b.Version.Major, b.Version.Minor)
	matchingTags := tagsByVersion[key]

	if len(matchingTags) > 0 {
		sortReleases(matchingTags)
	}

	latestTagLabel := fmt.Sprintf("%s(no tag)%s", Yellow, Reset)
	if len(matchingTags) > 0 {
		latest := matchingTags[len(matchingTags)-1]
		latestTagLabel = fmt.Sprintf("%s%s🏷️  %s%s", Bold, Green, latest.Name, Reset)
	}

	fmt.Printf("\n  🌿 %s%s%s%s  →  %s\n", Bold, Magenta, b.Name, Reset, latestTagLabel)
	fmt.Printf("     %sLast commit:%s  %s%s%s  %s%s%s  %s%s%s\n",
		Gray, Reset,
		Yellow, b.Commit, Reset,
		Cyan, b.Author, Reset,
		White, b.Date, Reset)

	msg := strings.TrimSpace(b.Message)
	if msg != "" {
		if idx := strings.Index(msg, "\n"); idx > 0 {
			msg = msg[:idx]
		}
		fmt.Printf("     %sMessage:%s    %s%s%s\n", Gray, Reset, White, msg, Reset)
	}

	if len(matchingTags) > 0 {
		fmt.Printf("     %sTags:%s       ", Gray, Reset)
		for i := len(matchingTags) - 1; i >= 0; i-- {
			t := matchingTags[i]
			if i == len(matchingTags)-1 {
				fmt.Printf("%s%s%s%s", Bold, Green, t.Name, Reset)
			} else {
				fmt.Printf("%s%s%s", Gray, t.Name, Reset)
			}
			if i > 0 {
				fmt.Printf("%s, %s", Gray, Reset)
			}
		}
		fmt.Println()
	}
}

// --- Git ---

func gitCheck() error {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Stderr = nil
	return cmd.Run()
}

func gitFetch() error {
	cmd := exec.Command("git", "fetch", "--tags", "--prune")
	cmd.Stderr = nil
	cmd.Stdout = nil
	return cmd.Run()
}

func findReleaseBranches(branchRe *regexp.Regexp) []releaseInfo {
	out, err := gitOutput("branch", "-r", "--format=%(refname:short)")
	if err != nil {
		return nil
	}

	var releases []releaseInfo
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := branchRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		major, _ := strconv.Atoi(m[1])
		minor, _ := strconv.Atoi(m[2])

		ri := getRefInfo(line, "branch")
		ri.Version = semver{Major: major, Minor: minor}
		releases = append(releases, ri)
	}
	return releases
}

func findReleaseTags(tagRe *regexp.Regexp, tagGlob string) []releaseInfo {
	out, err := gitOutput("tag", "-l", tagGlob)
	if err != nil {
		return nil
	}

	var releases []releaseInfo
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := tagRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		major, _ := strconv.Atoi(m[1])
		minor, _ := strconv.Atoi(m[2])
		patch, _ := strconv.Atoi(m[3])

		releases = append(releases, releaseInfo{
			Name:    line,
			Version: semver{Major: major, Minor: minor, Patch: patch},
			Source:  "tag",
		})
	}
	return releases
}

func getRefInfo(ref, source string) releaseInfo {
	const sep = "§§§"
	format := fmt.Sprintf("%%h%s%%ai%s%%an%s%%s", sep, sep, sep)
	out, _ := gitOutput("log", "-1", "--format="+format, ref)

	parts := strings.SplitN(out, sep, 4)
	var commit, date, author, message string
	if len(parts) == 4 {
		commit = strings.TrimSpace(parts[0])
		date = strings.TrimSpace(parts[1])
		author = strings.TrimSpace(parts[2])
		message = strings.TrimSpace(parts[3])
	}

	if len(date) >= 10 {
		date = date[:10]
	}

	return releaseInfo{
		Name:    ref,
		Commit:  commit,
		Date:    date,
		Author:  author,
		Message: message,
		Source:  source,
	}
}

func gitOutput(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// --- Helpers ---

func sortReleases(releases []releaseInfo) {
	sort.Slice(releases, func(i, j int) bool {
		return releases[i].Version.Less(releases[j].Version)
	})
}

func info(msg string) {
	fmt.Printf("  %s%s ℹ️  %s%s\n", Bold, Blue, msg, Reset)
}

func warn(msg string) {
	fmt.Printf("  %s%s ⚠️  %s%s\n", Bold, Yellow, msg, Reset)
}

func fatal(msg string) {
	fmt.Printf("  %s%s ❌ %s%s\n", Bold, Red, msg, Reset)
	os.Exit(1)
}
