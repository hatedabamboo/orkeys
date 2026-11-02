# orkeys

[![Release](https://img.shields.io/github/v/release/hatedabamboo/orkeys)](https://github.com/hatedabamboo/orkeys/releases)
[![Go Reference](https://pkg.go.dev/badge/github.com/hatedabamboo/orkeys.svg)](https://pkg.go.dev/github.com/hatedabamboo/orkeys)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

orkeys (pronounced "ORKies") manages [OpenRouter](https://openrouter.ai) API keys from the terminal. It uses the
[Management API](https://openrouter.ai/docs/guides/overview/auth/management-api-keys).
Use the CLI commands in scripts, or use interactive mode by hand.

- List, show, create, update, enable, disable and delete keys.
- Set credit limits, limit resets (daily, weekly or monthly), BYOK accounting and expiry.
- Output as a table, a wide table or JSON.
- Refer to a key by its full hash, a unique hash prefix or its exact name.
- Interactive mode runs inline in the console. It does not take over the screen.

## Install

**Go** (1.24 or later):

```sh
go install github.com/hatedabamboo/orkeys@latest
```

**Binaries:** download an archive for Linux, macOS or Windows from
[Releases](https://github.com/hatedabamboo/orkeys/releases), then put `orkeys` on your `PATH`.

**From source:**

```sh
git clone https://github.com/hatedabamboo/orkeys && cd orkeys
make install
```

## Configuration

orkeys needs a management key. To create one, go to
[openrouter.ai/settings/management-keys](https://openrouter.ai/settings/management-keys).
A management key can only manage keys. It cannot call models.

| Variable | Required | Purpose |
|---|---|---|
| `OPENROUTER_MANAGEMENT_API_KEY` | yes | Management API key |
| `OPENROUTER_API_BASE` | no | API base URL. Default: `https://openrouter.ai/api/v1` |
| `OPENROUTER_WORKSPACE_ID` | no | Workspace for list, create and lookup. Same as `--workspace` |

```sh
export OPENROUTER_MANAGEMENT_API_KEY=sk-or-v1-...
```

## Interactive mode

Run `orkeys` with no command in a terminal, or run `orkeys interactive` (alias `i`).

The app runs inline in the console, not full screen. It uses one region and redraws that region in place.
The region grows to fit the tallest screen so far, up to the terminal height, and does not shrink.
Long output, such as the key list, scrolls inside the region. When you quit, the app erases the region.

The menu has these actions: list, show, create, edit, enable/disable, delete, reload, quit.

| Key | Where | Action |
|---|---|---|
| `/` | key pickers | filter |
| `x`, `space` | delete picker | select a key (`ctrl+a` selects all) |
| `j` | list, show, create result | switch between table and JSON |
| `c` | create result | copy the new secret to the clipboard |
| `y` | show | copy the key hash |
| `esc` | anywhere | go back; at the main menu, quit |
| `ctrl+c` | anywhere | quit |

The secret of a new key shows only once. Quitting erases it from the screen, so copy it first.

## CLI

```sh
orkeys list                          # all pages, active keys
orkeys list -d -o wide               # with disabled keys, all columns
orkeys list --json | jq '.[] | select(.limit == null) | .name'
orkeys get ci
orkeys create --name ci --limit 20 --limit-reset monthly --expires 90d
KEY=$(orkeys create --name tmp --expires 12h -q)   # print only the secret
orkeys update ci --limit none --limit-reset none --name ci-old
orkeys disable ci-old 3f2a9c
orkeys enable ci-old
orkeys delete ci-old -y
```

Run `orkeys <command> --help` to see all flags.

### Output

`-o table` (default), `-o wide` or `-o json`. `--json` is a shorthand for `-o json`.
JSON output holds the full API objects. `create --json` includes the secret in `key`.

### Value formats

| Flag | Format |
|---|---|
| `--limit` | USD amount, or `none` to remove the limit |
| `--limit-reset` | `daily`, `weekly`, `monthly` or `none`. Resets happen at midnight UTC |
| `--expires` | duration (`30d`, `2w`, `12h`), date (`2027-12-31`, midnight UTC) or RFC 3339 |

### Behavior

- `delete` asks for confirmation. Without a terminal it refuses to run unless you pass `--yes`.
- `delete`, `enable` and `disable` take more than one key. `delete` finds all the keys before it deletes any.
- GET requests retry on HTTP 429 and 5xx, and use `Retry-After`. Writes do not retry.
- Exit code is `0` on success and `1` on any error.

### Shell completion

```sh
orkeys completion zsh  > "${fpath[1]}/_orkeys"
orkeys completion bash > /etc/bash_completion.d/orkeys
orkeys completion fish > ~/.config/fish/completions/orkeys.fish
```

## Development

```sh
make            # build ./orkeys
make check      # gofmt check, go vet, tests
make lint       # golangci-lint
make release    # cross-compile into dist/
make snapshot   # local GoReleaser build, no publish
make help       # all targets
```

Layout:

| Path | Contents |
|---|---|
| `internal/api` | HTTP client for the Management API |
| `internal/cli` | cobra commands and interactive mode |
| `internal/values` | input parsing and formatting |

### Releasing

1. Move the `Unreleased` entries in `CHANGELOG.md` under a new version.
2. Tag and push: `git tag v0.1.0 && git push origin v0.1.0`.
3. The Release workflow runs GoReleaser and creates a **draft** GitHub release with the archives and checksums.
4. Check the draft, then publish it.

## License

[MIT](LICENSE)
