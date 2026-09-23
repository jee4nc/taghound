package main

import "testing"

func TestParseRemoteURL(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantHost  string
		wantWS    string
		wantSlug  string
		wantError bool
	}{
		{"scp syntax", "git@bitbucket.org:ripleylabs/fc-customer-care.git", "bitbucket.org", "ripleylabs", "fc-customer-care", false},
		{"scp no dot-git", "git@bitbucket.org:ripleylabs/fc-customer-care", "bitbucket.org", "ripleylabs", "fc-customer-care", false},
		{"https", "https://bitbucket.org/ripleylabs/fc-customer-care.git", "bitbucket.org", "ripleylabs", "fc-customer-care", false},
		{"https no dot-git", "https://bitbucket.org/ripleylabs/fc-customer-care", "bitbucket.org", "ripleylabs", "fc-customer-care", false},
		{"https with user", "https://jeanc@bitbucket.org/ripleylabs/fc-customer-care.git", "bitbucket.org", "ripleylabs", "fc-customer-care", false},
		{"https token-auth", "https://x-token-auth@bitbucket.org/ripleylabs/fc-customer-care.git", "bitbucket.org", "ripleylabs", "fc-customer-care", false},
		{"ssh url form", "ssh://git@bitbucket.org/ripleylabs/fc-customer-care.git", "bitbucket.org", "ripleylabs", "fc-customer-care", false},
		{"github https", "https://github.com/jee4nc/taghound.git", "github.com", "jee4nc", "taghound", false},
		{"uppercase host", "git@BitBucket.org:ws/repo.git", "bitbucket.org", "ws", "repo", false},
		{"nested path", "https://bitbucket.org/ripleylabs/team/fc-customer-care.git", "bitbucket.org", "ripleylabs", "fc-customer-care", false},
		{"whitespace trimmed", "  git@bitbucket.org:ws/repo.git  \n", "bitbucket.org", "ws", "repo", false},

		{"empty", "", "", "", "", true},
		{"scp no colon", "git@bitbucket.org", "", "", "", true},
		{"no slug", "git@bitbucket.org:ws", "", "", "", true},
		{"trailing slash only", "https://bitbucket.org/", "", "", "", true},
		{"missing host", "https:///ws/repo.git", "", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := parseRemoteURL(tt.input)
			if tt.wantError {
				if err == nil {
					t.Fatalf("expected error, got nil (result=%+v)", info)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if info.Host != tt.wantHost {
				t.Errorf("Host = %q, want %q", info.Host, tt.wantHost)
			}
			if info.Workspace != tt.wantWS {
				t.Errorf("Workspace = %q, want %q", info.Workspace, tt.wantWS)
			}
			if info.Slug != tt.wantSlug {
				t.Errorf("Slug = %q, want %q", info.Slug, tt.wantSlug)
			}
		})
	}
}

func TestIsBitbucket(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"bitbucket.org", true},
		{"api.bitbucket.org", true},
		{"github.com", false},
		{"gitlab.com", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			got := RepoInfo{Host: tt.host}.IsBitbucket()
			if got != tt.want {
				t.Errorf("IsBitbucket(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}
