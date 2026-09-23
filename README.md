<p align="center">
  <img src="https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go" alt="Go Version">
  <img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="License">
  <img src="https://img.shields.io/badge/zero-dependencies-brightgreen" alt="Zero Dependencies">
</p>

# TagHound

A fast CLI tool that tracks Git releases by reading **branches** and **tags** with semantic versioning. Built for teams managing multiple release versions across repositories.

## What it does

1. Verifies you're inside a Git repository
2. Syncs tags and branches from the remote (`git fetch --tags --prune`)
3. Finds remote **branches** matching your configured prefix (e.g. `release-1.0`)
4. Finds **tags** matching your configured prefix (e.g. `v1.0.0`)
5. Parses and sorts everything by semver (major.minor.patch)
6. Shows the latest release, other active branches, and their associated tags
7. Optionally reveals orphan tags (tags without a matching branch)

## Installation

### Homebrew (macOS / Linux)

```bash
brew install jee4nc/tap/taghound
```

### Scoop (Windows)

```powershell
scoop bucket add taghound https://github.com/jee4nc/scoop-bucket
scoop install taghound
```

### Debian / Ubuntu (.deb)

Download the `.deb` package from the [Releases](../../releases) page:

```bash
sudo dpkg -i taghound_*.deb
```

### Pre-built binaries

Download from the [Releases](../../releases) page for your platform:

| Platform         | Archive                          |
|------------------|----------------------------------|
| macOS arm64      | `taghound_darwin_arm64.tar.gz`   |
| macOS amd64      | `taghound_darwin_amd64.tar.gz`   |
| Linux arm64      | `taghound_linux_arm64.tar.gz`    |
| Linux amd64      | `taghound_linux_amd64.tar.gz`    |
| Windows amd64    | `taghound_windows_amd64.zip`     |
| Windows arm64    | `taghound_windows_arm64.zip`     |

### From source

```bash
# Build and install to /usr/local/bin
make install

# Or just build
go build -o taghound .
```

## Requirements

- Git installed on your system

## Quick start

```bash
# Run in any Git repository
taghound

# Show only orphan tags (tags without a matching branch)
taghound --dirty    # or: taghound -d

# Show what is deployed per country (Bitbucket Pipelines)
taghound deploys

# Show version
taghound --version
```

## Example output

```
  TagHound — Release Tracker
───────────────────────────────────────────────────

  🚀 Latest release on origin
───────────────────────────────────────────────────

  🌿 origin/release-3.0  →  🏷️  v3.0.1
     Last commit:  a1b2c3d  Dev Team  2026-03-25
     Message:    prepare release 3.0
     Tags:       v3.0.1, v3.0.0

  📦 Other releases on origin
───────────────────────────────────────────────────

  🌿 origin/release-2.5  →  🏷️  v2.5.3
     Last commit:  f4e5d6c  Dev Team  2026-03-20
     Message:    hotfix for login
     Tags:       v2.5.3, v2.5.2, v2.5.1, v2.5.0
```

## Profiles

TagHound uses **profiles** to support different branch/tag naming conventions. The default profile matches `release-X.Y` branches and `vX.Y.Z` tags.

```bash
# List all profiles
taghound config list

# Show the active profile
taghound config show

# Create a custom profile
taghound config set deploy --branch deploy- --tag release-

# Switch to it
taghound config use deploy

# Use a profile for a single run (without switching)
taghound --profile default --dirty
```

Configuration is stored at `~/.config/taghound/config.json`.

## Deployments (Bitbucket Cloud)

A new release branch doesn't mean it's live. `taghound deploys` asks Bitbucket Pipelines for the **last successful deploy** of each environment and places that exact commit in your release history — which branch it belongs to, which tag it matches, and whether something newer is waiting.

```bash
export BITBUCKET_TOKEN=...          # repository/workspace access token
# export BITBUCKET_USERNAME=...     # only for app passwords / API tokens (Basic auth)

taghound deploys --envs             # list the environments defined in this repo

# {slug} → repo name, {country} → lowercase code, {COUNTRY} → uppercase, {workspace}
taghound config country set CL --prod 'prd-{slug}-{country}' --qa 'qa-{slug}-{country}'
taghound config country set PE --prod 'prd-{slug}-{country}'

taghound deploys                    # results are cached for 5 minutes
taghound deploys --refresh          # skip the cache
```

```
  TagHound — Deployments  bitbucket.org/acme/push-config
───────────────────────────────────────────────────────

  Latest release:  origin/release-3.1  →  🏷️  v3.1.0

  CL
     QA    release-3.1            v3.1.0       a1b2c3d  2026-09-20 14:02
           ✓ latest  pipeline #1410
     PROD  release-3.0            v3.0.1+2     f4e5d6c  2026-09-10 10:31
           ⚠ newer release origin/release-3.1 not deployed  pipeline #1398
```

The last successful deploy is what's in the environment, whatever branch it came from.

| Status | Meaning |
|--------|---------|
| `✓ latest` | The deployed commit is the newest tag of the newest release line |
| `● N commits not tagged` | Deployed from a release branch that has commits after its last tag |
| `⚠ … not deployed` | A newer release branch, or a newer tag in the same line, exists |
| `● not a release branch` | Deployed from a feature/other branch — normal in QA |
| `✗ not a release branch deployed to PROD` | Same, but in PROD (shown in red) |
| `never deployed — N pipelines stopped before this step` | Pipelines reached the (manual) step but nobody ran it |
| `? commit not found locally` | The deployed commit isn't in your clone (deleted branch, force-push) |

Countries whose environments don't exist in the repo are hidden. Tags outside the release lines of your branches (e.g. `v3950.3950.1` created from a feature branch) are ignored.

Environment names are templates so one global config works for every repo that follows the same convention. Optional settings in `config.json` under `bitbucket`: `auth_env` (token variable name, default `BITBUCKET_TOKEN`), `username`, `lookback` (deployments scanned per environment, default 200) and `cache_ttl_seconds` (default 300).

## Build from source

```bash
# Build for all platforms
make build

# Single platform
make darwin-arm64
make linux-amd64
make windows-amd64

# Run tests
make test
```

Binaries are output to `dist/` with optimized flags (`-s -w`) for minimal size.

## Zero dependencies

TagHound uses only the Go standard library. Git is invoked via `os/exec` -- no `libgit2`, no CGO, no external packages.

## License

[MIT](LICENSE)
