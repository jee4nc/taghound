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

### From source

```bash
# Build and install to /usr/local/bin
make install

# Or just build
go build -o taghound .
```

### Pre-built binaries

Download from the [Releases](../../releases) page for your platform:

| Platform         | Binary                        |
|------------------|-------------------------------|
| macOS arm64      | `taghound-darwin-arm64`       |
| macOS amd64      | `taghound-darwin-amd64`       |
| Linux amd64      | `taghound-linux-amd64`        |
| Windows amd64    | `taghound-windows-amd64.exe`  |

## Requirements

- Git installed on your system

## Quick start

```bash
# Run in any Git repository
taghound

# Include orphan tags (tags without a matching branch)
taghound --dirty

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
