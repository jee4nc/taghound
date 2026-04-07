# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

TagHound is a single-file Go CLI tool that tracks Git releases by reading branches and tags with semantic versioning. Zero external dependencies — uses only Go stdlib. All logic lives in `main.go` (~660 lines) with tests in `main_test.go`.

## Commands

```bash
make test          # Run tests with race detector: go test -v -race ./...
make build         # Cross-compile for darwin/linux/windows (amd64+arm64)
make run           # Run locally with version injected
make install       # Build and install to /usr/local/bin/taghound
go test -run TestSemverLess -v  # Run a single test
```

## Architecture

**Single-file design** — `main.go` is organized into logical sections:

- **Config system** (`~/.config/taghound/config.json`): Profiles define branch/tag prefix pairs (e.g., `release-` branches + `v` tags). Supports multiple profiles with `config set/use/list/show/delete` subcommands.
- **Dynamic regex generation**: `buildBranchPattern()` and `buildTagPattern()` create regexes from profile prefixes using `regexp.QuoteMeta()` for safe escaping.
- **Git operations**: All Git interaction via `os/exec` subprocess calls (`gitCheck`, `gitFetch`, `findReleaseBranches`, `findReleaseTags`, `getRefInfo`).
- **Tracker** (`runTracker`): Orchestrates fetching, filtering, sorting, grouping tags by major.minor, and formatted output. `--dirty` flag shows orphan tags.

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
