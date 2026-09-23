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
	"time"
)

const bitbucketDefaultBaseURL = "https://api.bitbucket.org/2.0"

// BitbucketProvider implements DeploymentProvider against Bitbucket Cloud.
type BitbucketProvider struct {
	BaseURL    string
	Workspace  string
	Slug       string
	Token      string
	Username   string
	Lookback   int
	HTTPClient *http.Client
}

// NewBitbucketProvider builds a provider with sensible defaults.
func NewBitbucketProvider(repo RepoInfo, token, username string, lookback int) *BitbucketProvider {
	if lookback <= 0 {
		lookback = 40
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
	Type   string        `json:"type"`
	Name   string        `json:"name"`
	Status bbStateStatus `json:"status"`
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

func (b *BitbucketProvider) LastSuccessfulDeploy(ctx context.Context, q EnvironmentQuery) (*Deploy, error) {
	if b.Workspace == "" || b.Slug == "" {
		return nil, fmt.Errorf("bitbucket: workspace and repo slug required")
	}
	if q.EnvName == "" {
		return nil, fmt.Errorf("bitbucket: EnvName is required")
	}

	deployment, err := b.findLatestSuccessful(ctx, q.EnvName)
	if err != nil {
		return nil, err
	}
	if deployment == nil {
		return nil, nil
	}

	branch, pipelineURL, err := b.resolvePipeline(ctx, deployment)
	if err != nil {
		return nil, err
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

	return &Deploy{
		Branch:      branch,
		Commit:      commit,
		ShortSha:    short,
		DeployedAt:  deployment.LastUpdateTime,
		PipelineURL: pipelineURL,
		PipelineNum: deployment.Deployable.Pipeline.BuildNumber,
		EnvName:     deployment.Environment.Name,
		State:       deployment.State.Status.Name,
	}, nil
}

// BitbucketEnvironment is a deployment environment defined in the repository.
type BitbucketEnvironment struct {
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
				Name            string `json:"name"`
				EnvironmentType struct {
					Name string `json:"name"`
				} `json:"environment_type"`
			}
			if err := json.Unmarshal(raw, &e); err != nil {
				continue
			}
			envs = append(envs, BitbucketEnvironment{Name: e.Name, Type: e.EnvironmentType.Name})
		}
		u = body.Next
	}
	return envs, nil
}

// --- Internal HTTP helpers ---

func (b *BitbucketProvider) findLatestSuccessful(ctx context.Context, envName string) (*bbDeployment, error) {
	u := fmt.Sprintf("%s/repositories/%s/%s/deployments/?sort=-last_update_time&pagelen=50",
		b.BaseURL,
		url.PathEscape(b.Workspace),
		url.PathEscape(b.Slug))

	seen := 0
	maxPages := 5
	for page := 0; page < maxPages && u != "" && seen < b.Lookback; page++ {
		var body bbPage
		if err := b.doJSON(ctx, u, &body); err != nil {
			return nil, err
		}
		for _, raw := range body.Values {
			seen++
			if seen > b.Lookback {
				return nil, nil
			}
			var d bbDeployment
			if err := json.Unmarshal(raw, &d); err != nil {
				continue
			}
			if !strings.EqualFold(d.Environment.Name, envName) {
				continue
			}
			if !strings.EqualFold(d.State.Status.Name, "SUCCESSFUL") {
				continue
			}
			return &d, nil
		}
		u = body.Next
	}
	return nil, nil
}

func (b *BitbucketProvider) resolvePipeline(ctx context.Context, d *bbDeployment) (branch, htmlURL string, err error) {
	uuid := d.Deployable.Pipeline.UUID
	if uuid == "" {
		return "", "", nil
	}
	u := fmt.Sprintf("%s/repositories/%s/%s/pipelines/%s",
		b.BaseURL,
		url.PathEscape(b.Workspace),
		url.PathEscape(b.Slug),
		url.PathEscape(uuid))

	var p bbPipeline
	if err := b.doJSON(ctx, u, &p); err != nil {
		return "", "", err
	}
	return p.Target.RefName, p.Links.HTML.Href, nil
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
