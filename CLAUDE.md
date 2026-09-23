# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

TagHound is a Go CLI tool that tracks Git releases by reading branches and tags with semantic versioning, and optionally shows what is deployed per environment via Bitbucket Pipelines. Zero external dependencies — uses only Go stdlib.

## Commands

```bash
make test          # Run tests with race detector: go test -v -race ./...
make build         # Cross-compile for darwin/linux/windows (amd64+arm64)
make run           # Run locally with version injected
make install       # Build and install to /usr/local/bin/taghound
go test -run TestSemverLess -v  # Run a single test
```

## Architecture

- `main.go` — CLI parsing, config system (`~/.config/taghound/config.json`, profiles with branch/tag prefixes), dynamic regex generation (`buildBranchPattern`/`buildTagPattern` + `parseVersion`), Git subprocess helpers and the tracker (`runTracker`, `--dirty` for orphan tags).
- `git.go` — remote URL parsing (`RepoInfo`) and helpers to place a commit in the release history (`highestMergedTag`, `oldestReleaseBranchContaining`, `countCommits`).
- `deployments.go` — `DeploymentProvider` interface, `Deploy` type and the per-repo cache in `os.UserCacheDir()`.
- `deployments_bitbucket.go` — Bitbucket Cloud provider (last successful deploy per environment, environment listing).
- `deploys_cmd.go` — `taghound deploys` and `config country`: expands env name templates (`{slug}`, `{country}`), fetches deploys concurrently, resolves each deployed commit against local branches/tags and classifies it (`classifyDeploy`).

Version is injected at build time via `-X main.Version=$(VERSION)` ldflags.

## Commit Convention

Follow Conventional Commits **with scope**: `type(scope): description`

Types: `feat`, `fix`, `refactor`, `docs`, `style`, `test`, `chore`, `perf`, `ci`
Scopes: `cli`, `config`, `tracker`, `ci`, `build` (match the area of change)

Include a body only when the title alone is insufficient. Use bullet points, max 3-4, focus on "why" not "what".

## Release Process

- `main` is the release branch; features go in topic branches merged via PR
- Each merged PR to `main` auto-creates a patch tag (e.g., `v1.2.3` → `v1.2.4`)
- Minor/major version bumps are done manually (`git tag v1.3.0 && git push --tags`)
- Tags matching `v*` trigger the release workflow (GoReleaser v2)
- Distributed via GitHub releases, Homebrew (`jee4nc/tap/taghound`), Scoop, and .deb/.rpm packages
