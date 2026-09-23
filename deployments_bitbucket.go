package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	bitbucketDefaultBaseURL = "https://api.bitbucket.org/2.0"
	defaultLookback         = 200
)

// BitbucketProvider implements DeploymentProvider against Bitbucket Cloud.
type BitbucketProvider struct {
	BaseURL    string
	Workspace  string
	Slug       string
	Token      string
	Username   string
	Lookback   int
	HTTPClient *http.Client

	envMu    sync.Mutex
	envUUIDs map[string]string // lowercased env name -> uuid, loaded once
}

// NewBitbucketProvider builds a provider with sensible defaults.
func NewBitbucketProvider(repo RepoInfo, token, username string, lookback int) *BitbucketProvider {
	if lookback <= 0 {
		lookback = defaultLookback
	}
	return &BitbucketProvider{
		BaseURL:    bitbucketDefaultBaseURL,
		Workspace:  repo.Workspace,
		Slug:       repo.Slug,
		Token:      token,
		Username:   username,
		Lookback:   lookback,
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (b *BitbucketProvider) Name() string { return "bitbucket" }

// --- Bitbucket API response types ---

type bbPage struct {
	Values  []json.RawMessage `json:"values"`
	Next    string            `json:"next,omitempty"`
	PageLen int               `json:"pagelen,omitempty"`
	Size    int               `json:"size,omitempty"`
	Page    int               `json:"page,omitempty"`
}

type bbStateStatus struct {
	Name string `json:"name"`
}

type bbState struct {
	Type        string        `json:"type"` // e.g. deployment_state_completed, deployment_state_undeployed
	Name        string        `json:"name"`
	Status      bbStateStatus `json:"status"`
	CompletedOn time.Time     `json:"completed_on"`
}

type bbCommit struct {
	Hash string `json:"hash"`
}

type bbEnvironment struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

type bbPipelineRef struct {
	UUID        string   `json:"uuid"`
	BuildNumber int      `json:"build_number"`
	Commit      bbCommit `json:"commit"`
}

type bbDeployable struct {
	Pipeline bbPipelineRef `json:"pipeline"`
	Commit   bbCommit      `json:"commit"`
}

type bbRelease struct {
	URL    string   `json:"url"`
	Commit bbCommit `json:"commit"`
}

type bbDeployment struct {
	UUID           string        `json:"uuid"`
	State          bbState       `json:"state"`
	Environment    bbEnvironment `json:"environment"`
	Release        bbRelease     `json:"release"`
	Deployable     bbDeployable  `json:"deployable"`
	LastUpdateTime time.Time     `json:"last_update_time"`
}

type bbPipelineTarget struct {
	RefName string   `json:"ref_name"`
	RefType string   `json:"ref_type"`
	Commit  bbCommit `json:"commit"`
}

type bbPipeline struct {
	UUID        string           `json:"uuid"`
	BuildNumber int              `json:"build_number"`
	Target      bbPipelineTarget `json:"target"`
	CreatedOn   time.Time        `json:"created_on"`
	CompletedOn time.Time        `json:"completed_on"`
	State       bbState          `json:"state"`
	Links       struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
}

// --- Public API ---

// LookupDeploy returns the last successful deploy of an environment, whatever
// branch it came from, plus how many pipelines stopped before deploying to it.
func (b *BitbucketProvider) LookupDeploy(ctx context.Context, q EnvironmentQuery) (*DeployLookup, error) {
	if b.Workspace == "" || b.Slug == "" {
		return nil, fmt.Errorf("bitbucket: workspace and repo slug required")
	}
	if q.EnvName == "" {
		return nil, fmt.Errorf("bitbucket: EnvName is required")
	}

	envUUID, err := b.environmentUUID(ctx, q.EnvName)
	if err != nil {
		return nil, err
	}
	if envUUID == "" {
		return &DeployLookup{EnvMissing: true}, nil
	}

	deployment, undeployed, err := b.findLatestSuccessful(ctx, envUUID)
	if err != nil {
		return nil, err
	}
	if deployment == nil {
		return &DeployLookup{Undeployed: undeployed}, nil
	}

	pipeline, err := b.resolvePipeline(ctx, deployment)
	if err != nil {
		return nil, err
	}
	pipelineURL := pipeline.Links.HTML.Href
	buildNum := pipeline.BuildNumber
	if buildNum == 0 {
		buildNum = deployment.Deployable.Pipeline.BuildNumber
	}

	commit := deployment.Deployable.Commit.Hash
	if commit == "" {
		commit = deployment.Release.Commit.Hash
	}
	short := commit
	if len(short) > 7 {
		short = short[:7]
	}
	if pipelineURL == "" {
		pipelineURL = deployment.Release.URL
	}
	deployedAt := deployment.State.CompletedOn
	if deployedAt.IsZero() {
		deployedAt = deployment.LastUpdateTime
	}

	return &DeployLookup{Last: &Deploy{
		Branch:      pipeline.Target.RefName,
		Commit:      commit,
		ShortSha:    short,
		DeployedAt:  deployedAt,
		PipelineURL: pipelineURL,
		PipelineNum: buildNum,
		EnvName:     deployment.Environment.Name,
		State:       deployment.State.Status.Name,
	}}, nil
}

// BitbucketEnvironment is a deployment environment defined in the repository.
type BitbucketEnvironment struct {
	UUID string
	Name string
	Type string // Test, Staging or Production
}

// ListEnvironments returns the repository's deployment environments.
func (b *BitbucketProvider) ListEnvironments(ctx context.Context) ([]BitbucketEnvironment, error) {
	if b.Workspace == "" || b.Slug == "" {
		return nil, fmt.Errorf("bitbucket: workspace and repo slug required")
	}
	u := fmt.Sprintf("%s/repositories/%s/%s/environments/?pagelen=100",
		b.BaseURL,
		url.PathEscape(b.Workspace),
		url.PathEscape(b.Slug))

	var envs []BitbucketEnvironment
	for page := 0; page < 5 && u != ""; page++ {
		var body bbPage
		if err := b.doJSON(ctx, u, &body); err != nil {
			return nil, err
		}
		for _, raw := range body.Values {
			var e struct {
				UUID            string `json:"uuid"`
				Name            string `json:"name"`
				EnvironmentType struct {
					Name string `json:"name"`
				} `json:"environment_type"`
			}
			if err := json.Unmarshal(raw, &e); err != nil {
				continue
			}
			envs = append(envs, BitbucketEnvironment{UUID: e.UUID, Name: e.Name, Type: e.EnvironmentType.Name})
		}
		u = body.Next
	}
	return envs, nil
}

// --- Internal HTTP helpers ---

// environmentUUID maps an environment name to its UUID ("" if it doesn't exist).
// The deployments API only filters by UUID, so the list is loaded once per run.
func (b *BitbucketProvider) environmentUUID(ctx context.Context, name string) (string, error) {
	b.envMu.Lock()
	defer b.envMu.Unlock()
	if b.envUUIDs == nil {
		envs, err := b.ListEnvironments(ctx)
		if err != nil {
			return "", err
		}
		b.envUUIDs = make(map[string]string, len(envs))
		for _, e := range envs {
			b.envUUIDs[strings.ToLower(e.Name)] = e.UUID
		}
	}
	return b.envUUIDs[strings.ToLower(name)], nil
}

// findLatestSuccessful walks an environment's deployments newest first and
// returns the first successful one. undeployed counts pipelines that created
// the deployment but never ran the step (e.g. a manual PROD step).
func (b *BitbucketProvider) findLatestSuccessful(ctx context.Context, envUUID string) (latest *bbDeployment, undeployed int, err error) {
	u := fmt.Sprintf("%s/repositories/%s/%s/deployments/?%s",
		b.BaseURL,
		url.PathEscape(b.Workspace),
		url.PathEscape(b.Slug),
		url.Values{
			"environment": {envUUID},
			"sort":        {"-state.completed_on"},
			"pagelen":     {"100"},
		}.Encode())

	seen := 0
	for u != "" && seen < b.Lookback {
		var body bbPage
		if err := b.doJSON(ctx, u, &body); err != nil {
			return nil, 0, err
		}
		for _, raw := range body.Values {
			seen++
			if seen > b.Lookback {
				break
			}
			var d bbDeployment
			if err := json.Unmarshal(raw, &d); err != nil {
				continue
			}
			if d.State.Type == "deployment_state_undeployed" {
				undeployed++
				continue
			}
			if strings.EqualFold(d.State.Status.Name, "SUCCESSFUL") {
				return &d, undeployed, nil
			}
		}
		u = body.Next
	}
	return nil, undeployed, nil
}

// resolvePipeline fetches the pipeline behind a deployment, which carries the
// branch it ran on and its build number. Returns a zero value if unknown.
func (b *BitbucketProvider) resolvePipeline(ctx context.Context, d *bbDeployment) (bbPipeline, error) {
	uuid := d.Deployable.Pipeline.UUID
	if uuid == "" {
		return bbPipeline{}, nil
	}
	u := fmt.Sprintf("%s/repositories/%s/%s/pipelines/%s",
		b.BaseURL,
		url.PathEscape(b.Workspace),
		url.PathEscape(b.Slug),
		url.PathEscape(uuid))

	var p bbPipeline
	if err := b.doJSON(ctx, u, &p); err != nil {
		return bbPipeline{}, err
	}
	return p, nil
}

func (b *BitbucketProvider) doJSON(ctx context.Context, rawURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	auth, err := b.authHeader()
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "taghound/"+Version)

	resp, err := b.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("bitbucket request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return b.httpError(resp.StatusCode, body)
	}

	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// bbErrorBody is Bitbucket's error payload. Detail is either a string or,
// for missing token scopes, an object with the required/granted scopes.
type bbErrorBody struct {
	Error struct {
		Message string          `json:"message"`
		Detail  json.RawMessage `json:"detail"`
	} `json:"error"`
}

// httpError turns a non-2xx response into a short, actionable message.
func (b *BitbucketProvider) httpError(status int, body []byte) error {
	var eb bbErrorBody
	_ = json.Unmarshal(body, &eb)
	msg := eb.Error.Message

	var scopes struct {
		Required []string `json:"required"`
	}
	if status == http.StatusForbidden && json.Unmarshal(eb.Error.Detail, &scopes) == nil && len(scopes.Required) > 0 {
		return fmt.Errorf("bitbucket: token is missing scope %s — create a new token that includes it", strings.Join(scopes.Required, ", "))
	}

	switch status {
	case http.StatusUnauthorized:
		hint := "check the token"
		if b.Username == "" {
			hint += "; API tokens and app passwords also need BITBUCKET_USERNAME (your Atlassian email or username)"
		}
		return fmt.Errorf("bitbucket: authentication failed (401): %s", hint)
	case http.StatusNotFound:
		return fmt.Errorf("bitbucket: %s/%s not found or not accessible with this token (404)", b.Workspace, b.Slug)
	}

	if msg == "" {
		msg = truncate(strings.TrimSpace(string(body)), 200)
	}
	return fmt.Errorf("bitbucket: request failed (%d): %s", status, msg)
}

func (b *BitbucketProvider) authHeader() (string, error) {
	if b.Username != "" && b.Token != "" {
		creds := base64.StdEncoding.EncodeToString([]byte(b.Username + ":" + b.Token))
		return "Basic " + creds, nil
	}
	if b.Token != "" {
		return "Bearer " + b.Token, nil
	}
	return "", fmt.Errorf("bitbucket: no credentials configured (set BITBUCKET_TOKEN)")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
