# Changelog

All notable changes to this project are listed here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
This project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Inventory mode: `validate`, `plan`, `apply`, `rotate`, `request`, `issued`, `import`, `hygiene`, `unlock` and
  `exporter` commands. See README and `examples/inventory`.
- `orkeys deepinfra` (`di`): list and get DeepInfra tokens with limit and spend. Writes need a dashboard login at
  DeepInfra and fail with API keys (see README).
- State in Vault or S3, secrets in Vault or AWS Secrets Manager.
- State lock: S3 conditional writes or Vault check-and-set.
- Key kinds defined in `policy.yaml`: any name, `source: manifest` (rotated) or `source: request` (self-service,
  expires). Each kind sets TTL, default and maximum limit, limit period and allowed providers.
- `import: {id, name}` in a manifest adopts an existing provider key; `orkeys import` prints such manifests.
- One `budget: {reset, limit}` per key: the limit at the provider and its period. Spend metrics follow the period.
- Self-service keys with per-person limits, encrypted with age to GitHub SSH keys.
- Structured events (text or one JSON document with `-o json`) instead of notifications.
- Prometheus exporter; it can read `policy.yaml` with `--policy`.
- Vault AppRole and Kubernetes auth.
- Vault TLS from `VAULT_CLIENT_CERT`, `VAULT_CLIENT_KEY` (client certificate, mTLS) and `VAULT_CACERT` (CA bundle).
  It applies to all Vault requests, the login and the exporter.
- Dockerfile.
- Team members from GitHub teams. A `team.yaml` without `members` uses the GitHub team with the same slug, or
  `github_team: <slug>`. `request`, `hygiene` and `plan` read it with `GITHUB_TOKEN` (`read:org`).
- `orkeys request --team <name>` chooses the team of a person who is in more than one team.
- `disabled: true` in a key manifest disables the key at the provider and stops its rotation. `track_disabled: true`
  in `policy.yaml` also enables keys that are disabled at the provider but not in the manifest. New events:
  `key_disabled`, `key_enabled`. With `track_disabled: false`, `plan` and `import` ignore disabled provider keys
  without a manifest.
- More than one provider of the same type: `providers` in `policy.yaml` takes a list of `{name, kind, ...}`.
  Manifests, kinds, state and metrics use the name. The map form still works; its kind defaults to the name. The env
  override for a name with `-` uses `_`: `ORKEYS_OPENROUTER_PRIVATE_KEY` (#16).

### Changed

- OpenRouter key commands moved under `orkeys openrouter` (`or`): `orkeys list` is now `orkeys or list`.
- `request` fails when the person is in more than one team and `--team` is not set. Before, the first team in
  alphabetical order was used.
- Alerts go to a team channel by kind `source`: `request` to `channels.notifications`, `manifest` to
  `channels.alerts`. `plan` shows a `channels.alerts` change for existing keys of request kinds that sent alerts to
  `channels.alerts`. The state field and the exporter label `alert_channel` do not change.

### Deprecated

- `kinds.<kind>.alert_channel` in `policy.yaml`. orkeys ignores it and prints a warning. The next release rejects it
  as an unknown field.

### Removed

- Interactive mode. orkeys is now a CLI and an exporter daemon only.

## [0.1.0] - 2026-11-02

### Added

- CLI commands: `list`, `get`, `create`, `update`, `enable`, `disable`, `delete`, `completion`.
- Interactive mode. It runs inline in the console and redraws one fixed region.
- Output formats `table`, `wide` and `json` (`-o`, or the `--json` shorthand). Interactive mode switches between table and JSON with `j`.
- Key lookup by full hash, unique hash prefix or exact name.
- Pagination across all pages, and filters for disabled keys and workspace.
- Expiry input as a duration (`30d`), a date or RFC 3339.
- GET requests retry on HTTP 429 and 5xx.

[Unreleased]: https://github.com/hatedabamboo/orkeys/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/hatedabamboo/orkeys/releases/tag/v0.1.0
