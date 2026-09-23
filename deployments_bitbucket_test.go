package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- helpers ---

func newFakeBitbucket(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *BitbucketProvider) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	p := NewBitbucketProvider(
		RepoInfo{Host: "bitbucket.org", Workspace: "ws", Slug: "repo"},
		"fake-token",
		"",
		40,
	)
	p.BaseURL = srv.URL
	p.HTTPClient = srv.Client()
	return srv, p
}

func deploymentJSON(envName, status, commit, pipelineUUID string, buildNum int, when time.Time) map[string]any {
	return map[string]any{
		"uuid": "{deploy-" + envName + "}",
		"state": map[string]any{
			"type":   "deployment_state_completed",
			"name":   "COMPLETED",
			"status": map[string]any{"name": status},
		},
		"environment": map[string]any{
			"uuid": "{env-" + envName + "}",
			"name": envName,
		},
		"release": map[string]any{
			"url":    "https://bitbucket.org/ws/repo/addon/pipelines/deployments/1",
			"commit": map[string]any{"hash": commit},
		},
		"deployable": map[string]any{
			"pipeline": map[string]any{
				"uuid":         pipelineUUID,
				"build_number": buildNum,
				"commit":       map[string]any{"hash": commit},
			},
			"commit": map[string]any{"hash": commit},
		},
		"last_update_time": when.Format(time.RFC3339),
	}
}

func pipelineJSON(uuid string, buildNum int, refName, commit string) map[string]any {
	return map[string]any{
		"uuid":         uuid,
		"build_number": buildNum,
		"target": map[string]any{
			"ref_name": refName,
			"ref_type": "branch",
			"commit":   map[string]any{"hash": commit},
		},
		"created_on":   time.Now().Format(time.RFC3339),
		"completed_on": time.Now().Format(time.RFC3339),
		"state": map[string]any{
			"type":   "pipeline_state_completed",
			"name":   "COMPLETED",
			"status": map[string]any{"name": "SUCCESSFUL"},
		},
		"links": map[string]any{
			"html": map[string]any{"href": "https://bitbucket.org/ws/repo/pipelines/results/" + fmt.Sprint(buildNum)},
		},
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

// --- tests ---

func TestBitbucketLastSuccessfulDeployHappyPath(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	pipelineUUID := "{pipe-1409}"

	_, p := newFakeBitbucket(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("expected Bearer auth, got %q", r.Header.Get("Authorization"))
		}
		switch {
		case strings.Contains(r.URL.Path, "/deployments/"):
			writeJSON(t, w, map[string]any{
				"values": []any{
					deploymentJSON("prd-push-config-cl", "SUCCESSFUL", "5c02bed1234", pipelineUUID, 1409, now),
				},
			})
		case strings.Contains(r.URL.Path, "/pipelines/"):
			writeJSON(t, w, pipelineJSON(pipelineUUID, 1409, "release-1.4", "5c02bed1234"))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})

	d, err := p.LastSuccessfulDeploy(context.Background(), EnvironmentQuery{
		CountryCode: "CL",
		EnvName:     "prd-push-config-cl",
		Tier:        "prod",
	})
	if err != nil {
		t.Fatalf("LastSuccessfulDeploy: %v", err)
	}
	if d == nil {
		t.Fatal("expected deploy, got nil")
	}
	if d.Branch != "release-1.4" {
		t.Errorf("Branch = %q, want release-1.4", d.Branch)
	}
	if d.Commit != "5c02bed1234" {
		t.Errorf("Commit = %q", d.Commit)
	}
	if d.ShortSha != "5c02bed" {
		t.Errorf("ShortSha = %q, want 5c02bed", d.ShortSha)
	}
	if d.PipelineNum != 1409 {
		t.Errorf("PipelineNum = %d, want 1409", d.PipelineNum)
	}
	if d.State != "SUCCESSFUL" {
		t.Errorf("State = %q", d.State)
	}
	if !d.DeployedAt.Equal(now) {
		t.Errorf("DeployedAt = %v, want %v", d.DeployedAt, now)
	}
}

func TestBitbucketFiltersNonMatchingEnv(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	pipelineUUID := "{pipe-cl}"

	_, p := newFakeBitbucket(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/deployments/"):
			writeJSON(t, w, map[string]any{
				"values": []any{
					deploymentJSON("qa-push-config-cl", "SUCCESSFUL", "aaa1234", "{pipe-qa}", 1410, now),
					deploymentJSON("prd-push-config-pe", "SUCCESSFUL", "bbb1234", "{pipe-pe}", 1411, now),
					deploymentJSON("prd-push-config-cl", "SUCCESSFUL", "ccc1234", pipelineUUID, 1409, now.Add(-1*time.Hour)),
				},
			})
		case strings.Contains(r.URL.Path, "/pipelines/"):
			writeJSON(t, w, pipelineJSON(pipelineUUID, 1409, "release-1.4", "ccc1234"))
		}
	})

	d, err := p.LastSuccessfulDeploy(context.Background(), EnvironmentQuery{
		EnvName: "prd-push-config-cl",
	})
	if err != nil {
		t.Fatalf("LastSuccessfulDeploy: %v", err)
	}
	if d == nil {
		t.Fatal("expected deploy")
	}
	if d.EnvName != "prd-push-config-cl" {
		t.Errorf("EnvName = %q", d.EnvName)
	}
	if d.Commit != "ccc1234" {
		t.Errorf("Commit = %q, want ccc1234", d.Commit)
	}
}

func TestBitbucketSkipsFailedDeploys(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	pipelineUUID := "{pipe-ok}"

	_, p := newFakeBitbucket(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/deployments/"):
			writeJSON(t, w, map[string]any{
				"values": []any{
					deploymentJSON("prd-push-config-cl", "FAILED", "badcommit", "{pipe-bad}", 1410, now),
					deploymentJSON("prd-push-config-cl", "SUCCESSFUL", "goodcommit", pipelineUUID, 1409, now.Add(-1*time.Hour)),
				},
			})
		case strings.Contains(r.URL.Path, "/pipelines/"):
			writeJSON(t, w, pipelineJSON(pipelineUUID, 1409, "release-1.4", "goodcommit"))
		}
	})

	d, err := p.LastSuccessfulDeploy(context.Background(), EnvironmentQuery{EnvName: "prd-push-config-cl"})
	if err != nil {
		t.Fatalf("LastSuccessfulDeploy: %v", err)
	}
	if d == nil || d.Commit != "goodcommit" {
		t.Fatalf("expected goodcommit, got %+v", d)
	}
}

func TestBitbucketPaginationNext(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	pipelineUUID := "{pipe-page2}"

	var srvURL string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/pipelines/"):
			writeJSON(t, w, pipelineJSON(pipelineUUID, 1405, "release-1.3", "page2sha"))
		case strings.Contains(r.URL.RawQuery, "page=2"):
			writeJSON(t, w, map[string]any{
				"values": []any{
					deploymentJSON("prd-push-config-cl", "SUCCESSFUL", "page2sha", pipelineUUID, 1405, now),
				},
			})
		default:
			writeJSON(t, w, map[string]any{
				"next": srvURL + "/repositories/ws/repo/deployments/?page=2",
				"values": []any{
					deploymentJSON("qa-push-config-cl", "SUCCESSFUL", "qacommit", "{qa-pipe}", 1410, now),
				},
			})
		}
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()
	srvURL = srv.URL

	p := NewBitbucketProvider(RepoInfo{Workspace: "ws", Slug: "repo"}, "t", "", 40)
	p.BaseURL = srv.URL
	p.HTTPClient = srv.Client()

	d, err := p.LastSuccessfulDeploy(context.Background(), EnvironmentQuery{EnvName: "prd-push-config-cl"})
	if err != nil {
		t.Fatalf("LastSuccessfulDeploy: %v", err)
	}
	if d == nil || d.Commit != "page2sha" {
		t.Fatalf("expected page2sha, got %+v", d)
	}
}

func TestBitbucketReturnsNilWhenNotFound(t *testing.T) {
	_, p := newFakeBitbucket(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"values": []any{}})
	})
	d, err := p.LastSuccessfulDeploy(context.Background(), EnvironmentQuery{EnvName: "prd-push-config-cl"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != nil {
		t.Errorf("expected nil deploy, got %+v", d)
	}
}

func TestBitbucketLookbackCap(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	var values []any
	for i := range 10 {
		values = append(values, deploymentJSON("other-env", "SUCCESSFUL", fmt.Sprintf("sha%d", i), "{pipe}", 1000+i, now))
	}
	_, p := newFakeBitbucket(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"values": values})
	})
	p.Lookback = 3

	d, err := p.LastSuccessfulDeploy(context.Background(), EnvironmentQuery{EnvName: "prd-push-config-cl"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != nil {
		t.Errorf("expected nil (lookback exhausted), got %+v", d)
	}
}

func TestBitbucketHTTPError(t *testing.T) {
	_, p := newFakeBitbucket(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad token"}}`))
	})
	_, err := p.LastSuccessfulDeploy(context.Background(), EnvironmentQuery{EnvName: "prd-push-config-cl"})
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 in error, got %v", err)
	}
}

func TestBitbucketBasicAuthHeader(t *testing.T) {
	p := NewBitbucketProvider(RepoInfo{Workspace: "ws", Slug: "repo"}, "app-pass", "jeanc", 40)
	h, err := p.authHeader()
	if err != nil {
		t.Fatalf("authHeader: %v", err)
	}
	if !strings.HasPrefix(h, "Basic ") {
		t.Errorf("expected Basic auth, got %q", h)
	}
}

func TestBitbucketBearerAuthHeader(t *testing.T) {
	p := NewBitbucketProvider(RepoInfo{Workspace: "ws", Slug: "repo"}, "bearer-token", "", 40)
	h, err := p.authHeader()
	if err != nil {
		t.Fatalf("authHeader: %v", err)
	}
	if h != "Bearer bearer-token" {
		t.Errorf("expected Bearer bearer-token, got %q", h)
	}
}

func TestBitbucketMissingCreds(t *testing.T) {
	p := NewBitbucketProvider(RepoInfo{Workspace: "ws", Slug: "repo"}, "", "", 40)
	_, err := p.authHeader()
	if err == nil {
		t.Fatal("expected error with missing credentials")
	}
}

func TestBitbucketMissingEnvName(t *testing.T) {
	p := NewBitbucketProvider(RepoInfo{Workspace: "ws", Slug: "repo"}, "t", "", 40)
	_, err := p.LastSuccessfulDeploy(context.Background(), EnvironmentQuery{})
	if err == nil {
		t.Fatal("expected error with missing EnvName")
	}
}

func TestBitbucketMissingWorkspace(t *testing.T) {
	p := NewBitbucketProvider(RepoInfo{}, "t", "", 40)
	_, err := p.LastSuccessfulDeploy(context.Background(), EnvironmentQuery{EnvName: "x"})
	if err == nil {
		t.Fatal("expected error with missing workspace")
	}
}

func TestBitbucketContextCancellation(t *testing.T) {
	_, p := newFakeBitbucket(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		writeJSON(t, w, map[string]any{"values": []any{}})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := p.LastSuccessfulDeploy(ctx, EnvironmentQuery{EnvName: "prd-push-config-cl"})
	if err == nil {
		t.Fatal("expected context error")
	}
}

func TestBitbucketListEnvironments(t *testing.T) {
	_, p := newFakeBitbucket(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/repositories/ws/repo/environments/") {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"values": []map[string]any{
				{"name": "qa-repo-cl", "environment_type": map[string]any{"name": "Staging"}},
				{"name": "prd-repo-cl", "environment_type": map[string]any{"name": "Production"}},
			},
		})
	})
	envs, err := p.ListEnvironments(context.Background())
	if err != nil {
		t.Fatalf("ListEnvironments: %v", err)
	}
	if len(envs) != 2 || envs[1].Name != "prd-repo-cl" || envs[1].Type != "Production" {
		t.Errorf("unexpected envs: %+v", envs)
	}
}

func TestBitbucketHTTPErrorMessages(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		username string
		want     string
		notWant  string
	}{
		{
			name:    "missing scope",
			status:  http.StatusForbidden,
			body:    `{"type":"error","error":{"message":"Your credentials lack one or more required privilege scopes.","detail":{"required":["read:pipeline:bitbucket"],"granted":["read:repository:bitbucket"]}}}`,
			want:    "missing scope read:pipeline:bitbucket",
			notWant: "https://",
		},
		{
			name:   "unauthorized without username hints at it",
			status: http.StatusUnauthorized,
			body:   `{"type":"error","error":{"message":"Unauthorized"}}`,
			want:   "BITBUCKET_USERNAME",
		},
		{
			name:     "unauthorized with username",
			status:   http.StatusUnauthorized,
			username: "me@example.com",
			want:     "authentication failed (401): check the token",
			notWant:  "BITBUCKET_USERNAME",
		},
		{
			name:   "not found",
			status: http.StatusNotFound,
			body:   `{"type":"error","error":{"message":"Repository not found"}}`,
			want:   "ws/repo not found",
		},
		{
			name:   "forbidden with string detail",
			status: http.StatusForbidden,
			body:   `{"type":"error","error":{"message":"Access denied","detail":"You need admin"}}`,
			want:   "request failed (403): Access denied",
		},
		{
			name:   "non-json body",
			status: http.StatusBadGateway,
			body:   `upstream down`,
			want:   "request failed (502): upstream down",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, p := newFakeBitbucket(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			p.Username = tt.username
			_, err := p.ListEnvironments(context.Background())
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err, tt.want)
			}
			if tt.notWant != "" && strings.Contains(err.Error(), tt.notWant) {
				t.Errorf("error = %q, should not contain %q", err, tt.notWant)
			}
		})
	}
}
