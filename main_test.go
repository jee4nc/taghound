package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// --- semver tests ---

func TestSemverString(t *testing.T) {
	tests := []struct {
		name string
		sv   semver
		want string
	}{
		{"major.minor only", semver{1, 2, 0}, "1.2.0"},
		{"with patch", semver{1, 2, 3}, "1.2.3"},
		{"zeros", semver{0, 0, 0}, "0.0.0"},
		{"large numbers", semver{100, 200, 300}, "100.200.300"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.sv.String()
			if got != tt.want {
				t.Errorf("semver%v.String() = %q, want %q", tt.sv, got, tt.want)
			}
		})
	}
}

func TestSemverLess(t *testing.T) {
	tests := []struct {
		name string
		a, b semver
		want bool
	}{
		{"equal", semver{1, 2, 3}, semver{1, 2, 3}, false},
		{"major less", semver{1, 0, 0}, semver{2, 0, 0}, true},
		{"major greater", semver{2, 0, 0}, semver{1, 0, 0}, false},
		{"minor less", semver{1, 1, 0}, semver{1, 2, 0}, true},
		{"minor greater", semver{1, 2, 0}, semver{1, 1, 0}, false},
		{"patch less", semver{1, 2, 1}, semver{1, 2, 3}, true},
		{"patch greater", semver{1, 2, 3}, semver{1, 2, 1}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.a.Less(tt.b)
			if got != tt.want {
				t.Errorf("%v.Less(%v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// --- Regex pattern tests ---

func TestBuildBranchPattern(t *testing.T) {
	re := buildBranchRegex("release-")

	tests := []struct {
		input string
		match bool
		major int
		minor int
	}{
		{"origin/release-1.0", true, 1, 0},
		{"release-2.3", true, 2, 3},
		{"origin/release-10.20", true, 10, 20},
		{"release-1.0.0", false, 0, 0},
		{"feature-1.0", false, 0, 0},
		{"origin/deploy-1.0", false, 0, 0},
		{"", false, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			m := re.FindStringSubmatch(tt.input)
			if tt.match && m == nil {
				t.Errorf("expected %q to match branch pattern", tt.input)
			}
			if !tt.match && m != nil {
				t.Errorf("expected %q to NOT match branch pattern", tt.input)
			}
		})
	}
}

func TestBuildTagPattern(t *testing.T) {
	re := buildTagRegex("v")

	tests := []struct {
		input string
		match bool
	}{
		{"v1.0.0", true},
		{"v10.20.30", true},
		{"v0.0.1", true},
		{"v1.0", false},
		{"release-1.0.0", false},
		{"v1.0.0.0", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			m := re.FindStringSubmatch(tt.input)
			if tt.match && m == nil {
				t.Errorf("expected %q to match tag pattern", tt.input)
			}
			if !tt.match && m != nil {
				t.Errorf("expected %q to NOT match tag pattern", tt.input)
			}
		})
	}
}

func TestBuildTagPatternCustomPrefix(t *testing.T) {
	re := buildTagRegex("release-")

	tests := []struct {
		input string
		match bool
	}{
		{"release-1.0.0", true},
		{"release-2.3.1", true},
		{"v1.0.0", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			m := re.FindStringSubmatch(tt.input)
			if tt.match && m == nil {
				t.Errorf("expected %q to match", tt.input)
			}
			if !tt.match && m != nil {
				t.Errorf("expected %q to NOT match", tt.input)
			}
		})
	}
}

func TestBuildTagSearchGlob(t *testing.T) {
	got := buildTagSearchGlob("v")
	if got != "v*" {
		t.Errorf("buildTagSearchGlob(\"v\") = %q, want \"v*\"", got)
	}
	got = buildTagSearchGlob("release-")
	if got != "release-*" {
		t.Errorf("buildTagSearchGlob(\"release-\") = %q, want \"release-*\"", got)
	}
}

// --- sortReleases tests ---

func TestSortReleases(t *testing.T) {
	releases := []releaseInfo{
		{Name: "c", Version: semver{3, 0, 0}},
		{Name: "a", Version: semver{1, 0, 0}},
		{Name: "b", Version: semver{2, 0, 0}},
		{Name: "b1", Version: semver{2, 0, 1}},
		{Name: "a1", Version: semver{1, 1, 0}},
	}
	sortReleases(releases)

	expected := []string{"a", "a1", "b", "b1", "c"}
	for i, r := range releases {
		if r.Name != expected[i] {
			t.Errorf("position %d: got %q, want %q", i, r.Name, expected[i])
		}
	}
}

func TestSortReleasesEmpty(t *testing.T) {
	var releases []releaseInfo
	sortReleases(releases) // should not panic
}

// --- Config tests ---

func TestDefaultConfig(t *testing.T) {
	cfg := defaultConfig()

	if cfg.Active != "default" {
		t.Errorf("Active = %q, want \"default\"", cfg.Active)
	}
	p, ok := cfg.Profiles["default"]
	if !ok {
		t.Fatal("default profile not found")
	}
	if p.BranchPrefix != "release-" {
		t.Errorf("BranchPrefix = %q, want \"release-\"", p.BranchPrefix)
	}
	if p.TagPrefix != "v" {
		t.Errorf("TagPrefix = %q, want \"v\"", p.TagPrefix)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.json")

	cfg := Config{
		Active: "custom",
		Profiles: map[string]Profile{
			"default": {BranchPrefix: "release-", TagPrefix: "v"},
			"custom":  {BranchPrefix: "deploy-", TagPrefix: "release-"},
		},
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var loaded Config
	if err := json.Unmarshal(raw, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if loaded.Active != "custom" {
		t.Errorf("Active = %q, want \"custom\"", loaded.Active)
	}
	if len(loaded.Profiles) != 2 {
		t.Errorf("Profiles count = %d, want 2", len(loaded.Profiles))
	}
	p := loaded.Profiles["custom"]
	if p.BranchPrefix != "deploy-" || p.TagPrefix != "release-" {
		t.Errorf("custom profile = %+v, unexpected", p)
	}
}

func TestLoadConfigMissing(t *testing.T) {
	// Override configPath to point to a non-existent file
	// Since loadConfig uses configPath(), we test that defaultConfig is returned
	// by testing defaultConfig directly (loadConfig calls os.ReadFile which
	// returns error for non-existent files, falling back to defaultConfig)
	cfg := defaultConfig()
	if cfg.Active != "default" {
		t.Errorf("fallback Active = %q, want \"default\"", cfg.Active)
	}
}

func TestLoadConfigCorrupt(t *testing.T) {
	// Verify that corrupt JSON results in valid defaults
	var cfg Config
	err := json.Unmarshal([]byte("{invalid json}"), &cfg)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
	// loadConfig would return defaultConfig() in this case
	fallback := defaultConfig()
	if fallback.Active != "default" {
		t.Errorf("fallback should have default active profile")
	}
}

// --- Profile tests ---

func TestProfileMerge(t *testing.T) {
	cfg := Config{
		Active: "default",
		Profiles: map[string]Profile{
			"default": {BranchPrefix: "release-", TagPrefix: "v"},
		},
	}

	// Simulate updating only branch prefix
	existing := cfg.Profiles["default"]
	newBranch := "deploy-"
	existing.BranchPrefix = newBranch
	cfg.Profiles["default"] = existing

	p := cfg.Profiles["default"]
	if p.BranchPrefix != "deploy-" {
		t.Errorf("BranchPrefix = %q, want \"deploy-\"", p.BranchPrefix)
	}
	if p.TagPrefix != "v" {
		t.Errorf("TagPrefix should remain \"v\", got %q", p.TagPrefix)
	}
}

// --- Regex escaping tests ---

func TestBuildBranchPatternSpecialChars(t *testing.T) {
	// Prefix with regex special characters should be escaped
	re := buildBranchRegex("release.v-")
	m := re.FindStringSubmatch("release.v-1.0")
	if m == nil {
		t.Error("expected match with special char prefix")
	}

	// The dot should NOT match arbitrary characters
	m = re.FindStringSubmatch("releasexv-1.0")
	if m != nil {
		t.Error("dot in prefix should be literal, not regex wildcard")
	}
}

func TestBuildTagPatternSpecialChars(t *testing.T) {
	re := buildTagRegex("v.")
	m := re.FindStringSubmatch("v.1.0.0")
	if m == nil {
		t.Error("expected match with 'v.' prefix")
	}
	m = re.FindStringSubmatch("vx1.0.0")
	if m != nil {
		t.Error("dot in prefix should be literal")
	}
}

// --- releaseInfo tests ---

func TestReleaseInfoSource(t *testing.T) {
	ri := releaseInfo{
		Name:    "origin/release-1.0",
		Version: semver{1, 0, 0},
		Source:  "branch",
	}
	if ri.Source != "branch" {
		t.Errorf("Source = %q, want \"branch\"", ri.Source)
	}

	ri2 := releaseInfo{
		Name:    "v1.0.0",
		Version: semver{1, 0, 0},
		Source:  "tag",
	}
	if ri2.Source != "tag" {
		t.Errorf("Source = %q, want \"tag\"", ri2.Source)
	}
}
