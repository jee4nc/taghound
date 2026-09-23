package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Deploy represents a deployment event to a specific environment.
type Deploy struct {
	Branch      string    `json:"branch"`
	Commit      string    `json:"commit"`
	ShortSha    string    `json:"shortSha"`
	DeployedAt  time.Time `json:"deployedAt"`
	PipelineURL string    `json:"pipelineUrl"`
	PipelineNum int       `json:"pipelineNum,omitempty"`
	EnvName     string    `json:"envName"`
	State       string    `json:"state"`
}

// EnvironmentQuery describes which environment to look up.
type EnvironmentQuery struct {
	CountryCode string // e.g. "CL"
	EnvName     string // e.g. "prd-push-config-cl"
	Tier        string // "prod" or "qa"
}

// DeploymentProvider abstracts CI/CD providers.
type DeploymentProvider interface {
	LastSuccessfulDeploy(ctx context.Context, q EnvironmentQuery) (*Deploy, error)
	Name() string
}

// --- Cache ---

type deployCacheEntry struct {
	FetchedAt time.Time          `json:"fetchedAt"`
	Deploys   map[string]*Deploy `json:"deploys"` // keyed by env name
}

func cacheFilePath(repo RepoInfo) (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("could not determine cache dir: %w", err)
	}
	sum := sha1.Sum([]byte(repo.Host + "/" + repo.Workspace + "/" + repo.Slug))
	name := "deployments-" + hex.EncodeToString(sum[:8]) + ".json"
	return filepath.Join(dir, "taghound", name), nil
}

// loadDeployCache returns a non-expired cache entry, or nil if missing/expired.
func loadDeployCache(repo RepoInfo, ttl time.Duration) (*deployCacheEntry, error) {
	path, err := cacheFilePath(repo)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var e deployCacheEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, nil // treat corrupt cache as miss
	}
	if ttl > 0 && time.Since(e.FetchedAt) > ttl {
		return nil, nil
	}
	if e.Deploys == nil {
		e.Deploys = make(map[string]*Deploy)
	}
	return &e, nil
}

func saveDeployCache(repo RepoInfo, entry *deployCacheEntry) error {
	path, err := cacheFilePath(repo)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
