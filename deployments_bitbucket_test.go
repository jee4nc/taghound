package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

func deploymentJSON(envName, status, commit, pipelineUUID string, when time.Time) map[string]any {
	state := map[string]any{
		"type":         "deployment_state_completed",
		"name":         "COMPLETED",
		"status":       map[string]any{"name": status},
		"completed_on": when.Format(time.RFC3339),
	}
	if status == "UNDEPLOYED" {
		state = map[string]any{"type": "deployment_state_undeployed", "name": "UNDEPLOYED"}
	}
	return map[string]any{
		"uuid":  "{deploy-" + envName + "}",
		"state": state,
		"environment": map[string]any{
			"uuid": envUUID(envName),
			"name": envName,
		},
		"release": map[string]any{
			"url":    "https://bitbucket.org/ws/repo/addon/pipelines/deployments/1",
			"commit": map[string]any{"hash": commit},
		},
		"deployable": map[string]any{
			"pipeline": map[string]any{"uuid": pipelineUUID},
			"commit":   map[string]any{"hash": commit},
		},
	}
}

func envUUID(name string) string { return "{env-" + name + "}" }

// fakeAPI imitates the real Bitbucket endpoints used by the provider:
// environments, deployments filtered by environment UUID and sorted by
// -state.completed_on (any other sort is a 400, like the real API), and pipelines.
type fakeAPI struct {
	envs        []string
	deployments []map[string]any // newest first
	pipelines   map[string]map[string]any
}

func (f *fakeAPI) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/environments/"):
			var values []any
			for _, n := range f.envs {
				values = append(values, map[string]any{"uuid": envUUID(n), "name": n, "environment_type": map[string]any{"name": "Production"}})
			}
			writeJSON(t, w, map[string]any{"values": values})
		case strings.HasSuffix(r.URL.Path, "/deployments/"):
			if s := r.URL.Query().Get("sort"); s != "" && s != "-state.completed_on" && s != "-state.started_on" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"message":"Invalid sort attribute provided"}`))
				return
			}
			env := r.URL.Query().Get("environment")
			values := []any{}
			for _, d := range f.deployments {
				if d["environment"].(map[string]any)["uuid"] == env {
					values = append(values, d)
				}
			}
			writeJSON(t, w, map[string]any{"values": values})
		case strings.Contains(r.URL.Path, "/pipelines/"):
			uuid := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			p, ok := f.pipelines[uuid]
			if !ok {
				http.NotFound(w, r)
				return
			}
			writeJSON(t, w, p)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
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

func TestBitbucketLookupDeployHappyPath(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	api := &fakeAPI{
		envs: []string{"prd-push-config-cl"},
		deployments: []map[string]any{
			deploymentJSON("prd-push-config-cl", "SUCCESSFUL", "5c02bed1234", "{pipe-1409}", now),
		},
		pipelines: map[string]map[string]any{"{pipe-1409}": pipelineJSON("{pipe-1409}", 1409, "release-1.4", "5c02bed1234")},
	}
	_, p := newFakeBitbucket(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("expected Bearer auth, got %q", r.Header.Get("Authorization"))
		}
		api.handler(t)(w, r)
	})

	l, err := p.LookupDeploy(context.Background(), EnvironmentQuery{CountryCode: "CL", EnvName: "prd-push-config-cl", Tier: "prod"})
	if err != nil {
		t.Fatalf("LookupDeploy: %v", err)
	}
	d := l.Last
	if d == nil {
		t.Fatal("expected deploy, got nil")
	}
	if d.Branch != "release-1.4" || d.Commit != "5c02bed1234" || d.ShortSha != "5c02bed" {
		t.Errorf("unexpected deploy: %+v", d)
	}
	if d.PipelineNum != 1409 {
		t.Errorf("PipelineNum = %d, want 1409 (taken from the pipeline)", d.PipelineNum)
	}
	if d.State != "SUCCESSFUL" || !d.DeployedAt.Equal(now) {
		t.Errorf("State/DeployedAt = %q/%v, want SUCCESSFUL/%v", d.State, d.DeployedAt, now)
	}
}

func TestBitbucketLookupFiltersByEnvironment(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	api := &fakeAPI{
		envs: []string{"qa-push-config-cl", "prd-push-config-cl"},
		deployments: []map[string]any{
			deploymentJSON("qa-push-config-cl", "SUCCESSFUL", "qa1234", "{pipe-qa}", now),
			deploymentJSON("prd-push-config-cl", "SUCCESSFUL", "prd1234", "{pipe-prd}", now.Add(-time.Hour)),
		},
		pipelines: map[string]map[string]any{"{pipe-prd}": pipelineJSON("{pipe-prd}", 7, "release-1.4", "prd1234")},
	}
	_, p := newFakeBitbucket(t, api.handler(t))

	l, err := p.LookupDeploy(context.Background(), EnvironmentQuery{EnvName: "PRD-push-config-cl"})
	if err != nil {
		t.Fatalf("LookupDeploy: %v", err)
	}
	if l.Last == nil || l.Last.Commit != "prd1234" {
		t.Fatalf("expected prd1234, got %+v", l.Last)
	}
}

func TestBitbucketLookupSkipsFailedAndUndeployed(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	api := &fakeAPI{
		envs: []string{"prd-push-config-cl"},
		deployments: []map[string]any{
			deploymentJSON("prd-push-config-cl", "FAILED", "badcommit", "{pipe-bad}", now),
			deploymentJSON("prd-push-config-cl", "SUCCESSFUL", "goodcommit", "{pipe-ok}", now.Add(-time.Hour)),
		},
		pipelines: map[string]map[string]any{"{pipe-ok}": pipelineJSON("{pipe-ok}", 9, "release-1.4", "goodcommit")},
	}
	// Undeployed entries have no completed_on and are listed first by the real API.
	api.deployments = append([]map[string]any{deploymentJSON("prd-push-config-cl", "UNDEPLOYED", "newer", "{pipe-new}", now)}, api.deployments...)
	_, p := newFakeBitbucket(t, api.handler(t))

	l, err := p.LookupDeploy(context.Background(), EnvironmentQuery{EnvName: "prd-push-config-cl"})
	if err != nil {
		t.Fatalf("LookupDeploy: %v", err)
	}
	if l.Last == nil || l.Last.Commit != "goodcommit" {
		t.Fatalf("expected goodcommit, got %+v", l.Last)
	}
}

func TestBitbucketLookupNeverDeployed(t *testing.T) {
	now := time.Now().UTC()
	api := &fakeAPI{
		envs: []string{"prd-push-config-cl"},
		deployments: []map[string]any{
			deploymentJSON("prd-push-config-cl", "UNDEPLOYED", "a", "{p1}", now),
			deploymentJSON("prd-push-config-cl", "UNDEPLOYED", "b", "{p2}", now),
		},
	}
	_, p := newFakeBitbucket(t, api.handler(t))

	l, err := p.LookupDeploy(context.Background(), EnvironmentQuery{EnvName: "prd-push-config-cl"})
	if err != nil {
		t.Fatalf("LookupDeploy: %v", err)
	}
	if l.Last != nil || l.Undeployed != 2 || l.EnvMissing {
		t.Errorf("unexpected lookup: %+v", l)
	}
}

func TestBitbucketLookupMissingEnvironment(t *testing.T) {
	api := &fakeAPI{envs: []string{"prd-push-config-cl"}}
	_, p := newFakeBitbucket(t, api.handler(t))

	l, err := p.LookupDeploy(context.Background(), EnvironmentQuery{EnvName: "prd-push-config-pe"})
	if err != nil {
		t.Fatalf("LookupDeploy: %v", err)
	}
	if !l.EnvMissing {
		t.Errorf("expected EnvMissing, got %+v", l)
	}
}

func TestBitbucketEnvironmentsLoadedOnce(t *testing.T) {
	var envCalls atomic.Int32
	api := &fakeAPI{envs: []string{"a", "b"}}
	_, p := newFakeBitbucket(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/environments/") {
			envCalls.Add(1)
		}
		api.handler(t)(w, r)
	})
	for _, env := range []string{"a", "b", "a"} {
		if _, err := p.LookupDeploy(context.Background(), EnvironmentQuery{EnvName: env}); err != nil {
			t.Fatalf("LookupDeploy(%s): %v", env, err)
		}
	}
	if n := envCalls.Load(); n != 1 {
		t.Errorf("environments fetched %d times, want 1", n)
	}
}

func TestBitbucketLookupPagination(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/environments/"):
			writeJSON(t, w, map[string]any{"values": []any{map[string]any{"uuid": envUUID("prd"), "name": "prd"}}})
		case strings.Contains(r.URL.Path, "/pipelines/"):
			writeJSON(t, w, pipelineJSON("{pipe-2}", 2, "release-1.3", "page2sha"))
		case r.URL.Query().Get("page") == "2":
			writeJSON(t, w, map[string]any{"values": []any{deploymentJSON("prd", "SUCCESSFUL", "page2sha", "{pipe-2}", now)}})
		default:
			writeJSON(t, w, map[string]any{
				"next":   srvURL + "/repositories/ws/repo/deployments/?page=2",
				"values": []any{deploymentJSON("prd", "FAILED", "failsha", "{pipe-1}", now)},
			})
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	p := NewBitbucketProvider(RepoInfo{Workspace: "ws", Slug: "repo"}, "t", "", 0)
	p.BaseURL = srv.URL
	p.HTTPClient = srv.Client()

	l, err := p.LookupDeploy(context.Background(), EnvironmentQuery{EnvName: "prd"})
	if err != nil {
		t.Fatalf("LookupDeploy: %v", err)
	}
	if l.Last == nil || l.Last.Commit != "page2sha" {
		t.Fatalf("expected page2sha, got %+v", l.Last)
	}
}

func TestBitbucketLookbackCap(t *testing.T) {
	now := time.Now().UTC()
	api := &fakeAPI{envs: []string{"prd"}}
	for i := range 10 {
		api.deployments = append(api.deployments, deploymentJSON("prd", "FAILED", fmt.Sprintf("sha%d", i), "{pipe}", now))
	}
	api.deployments = append(api.deployments, deploymentJSON("prd", "SUCCESSFUL", "old", "{pipe-old}", now))
	_, p := newFakeBitbucket(t, api.handler(t))
	p.Lookback = 3

	l, err := p.LookupDeploy(context.Background(), EnvironmentQuery{EnvName: "prd"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if l.Last != nil {
		t.Errorf("expected nil (lookback exhausted), got %+v", l.Last)
	}
}

func TestBitbucketHTTPError(t *testing.T) {
	_, p := newFakeBitbucket(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad token"}}`))
	})
	_, err := p.LookupDeploy(context.Background(), EnvironmentQuery{EnvName: "prd-push-config-cl"})
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
	_, err := p.LookupDeploy(context.Background(), EnvironmentQuery{})
	if err == nil {
		t.Fatal("expected error with missing EnvName")
	}
}

func TestBitbucketMissingWorkspace(t *testing.T) {
	p := NewBitbucketProvider(RepoInfo{}, "t", "", 40)
	_, err := p.LookupDeploy(context.Background(), EnvironmentQuery{EnvName: "x"})
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
	_, err := p.LookupDeploy(ctx, EnvironmentQuery{EnvName: "prd-push-config-cl"})
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
