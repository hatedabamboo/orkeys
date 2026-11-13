# CHANGELOG

## v0.4.3 (2026-11-13)

- (feat) `apply` saves a copy of `policy.yaml` in the state store (`.policy`) when the file changed, also when the
  plan is empty. Without `--policy`, the exporter reads this copy at each poll. You can remove `--provider` and the
  second copy of `policy.yaml` from the exporter deployment. The copy exists after the first `apply` with this version
- (feat) The exporter exports `orkeys_key_track_only`: 1 for a key with `track_only: true`, 0 for a managed key.
  Alert rules use it to tell a tracked key, which a person renews, from a key that `rotate` renews

## v0.4.2 (2026-11-13)

- (change) Breaking: give the AWS role `secretsmanager:UntagResource`, and upgrade all runners together. orkeys
  keeps the secret metadata (Vault custom metadata, AWS tags) per field: `orkeys-key.<field>`,
  `orkeys-key-id.<field>`, `orkeys-team.<field>`. orkeys reads the old per-secret names for a field without its own
  names, and the next write removes them. orkeys up to v0.4.1 does not read the new names
- (feat) Two keys can use the same `secret.path` with different `secret.field`. `validate` reports only a
  `path#field` that two keys use
- (feat) `validate` checks `secret.field`: at most 114 characters, and with `secrets: aws` only letters, digits,
  spaces and `_.:/=+-@`

## v0.4.1 (2026-11-13)

- (change) `orkeys_unmanaged_keys` no longer counts disabled keys when `track_disabled` is `false` (the default), the
  same as `plan`. To count them, set `ignore_disabled: false` in the provider `options` [0f038c0](https://github.com/hatedabamboo/orkeys/commit/0f038c00297cd0234d73ed39435bde17fc2cd14b)
- (feat) `track_only: true` on a key manifest or in `providers[].options`: `apply` saves only the state of the `import`
  key, and the exporter exports its metrics. orkeys does not set its limit, write it to secrets, sync consumers,
  rotate, disable or revoke it. Deleting the manifest removes only the state. `orkeys request` refuses a provider with
  `track_only: true` [8e3ec8a](https://github.com/hatedabamboo/orkeys/commit/8e3ec8a63880d5ba92b01c43e89c7f5bffc523e6) [0f038c0](https://github.com/hatedabamboo/orkeys/commit/0f038c00297cd0234d73ed39435bde17fc2cd14b)
- (feat) `orkeys import --track-only` prints manifests with `track_only: true` and no `secret` [8e3ec8a](https://github.com/hatedabamboo/orkeys/commit/8e3ec8a63880d5ba92b01c43e89c7f5bffc523e6)
- (feat) Provider options `report_unmanaged`, `ignore_disabled` and `ignore_unmanaged` (key IDs) set which keys
  without state `plan`, `orkeys import` and the exporter report. `plan` warns about an ignored ID that is missing or
  managed [0f038c0](https://github.com/hatedabamboo/orkeys/commit/0f038c00297cd0234d73ed39435bde17fc2cd14b)

## v0.4.0 (2026-11-11)

- (change) Breaking: upgrade all runners (CI and laptops) together. Each state now has `schema: 2`, and orkeys refuses
  to run with a state from a newer orkeys. orkeys up to v0.3.0 has no such check: when it writes a state, it drops
  the new fields [4c5f830](https://github.com/hatedabamboo/orkeys/commit/4c5f830d0f3f3cbaf1452a46e8e8dd35b4b3bc48)
- (change) Breaking: the state keeps push consumer targets in `pushed` (`github/<id>`), not in `github_secrets`.
  orkeys converts an old state when it reads it [4c5f830](https://github.com/hatedabamboo/orkeys/commit/4c5f830d0f3f3cbaf1452a46e8e8dd35b4b3bc48)
- (change) Breaking: update filters and routing that use the old names. The plan action `sync-github` is now
  `sync-consumers`. The events `github_synced` and `github_sync_failed` are now `consumers_synced` and
  `consumers_sync_failed` [4c5f830](https://github.com/hatedabamboo/orkeys/commit/4c5f830d0f3f3cbaf1452a46e8e8dd35b4b3bc48)
- (change) Breaking: give the AWS role `secretsmanager:DescribeSecret` and `secretsmanager:RestoreSecret`. When orkeys
  writes to a secret that it scheduled for deletion, it restores the secret and writes only the new field [4c5f830](https://github.com/hatedabamboo/orkeys/commit/4c5f830d0f3f3cbaf1452a46e8e8dd35b4b3bc48)
- (change) Breaking: `apply` refuses a plan that revokes more than `max_deletes` keys (default 3), before it changes
  anything. `apply --allow-deletes <n>` raises the limit for one run [4c5f830](https://github.com/hatedabamboo/orkeys/commit/4c5f830d0f3f3cbaf1452a46e8e8dd35b4b3bc48)
- (change) Breaking: a consumer field of another consumer type (for example `cluster` on a `github` consumer) now
  fails the load, not `validate` [4c5f830](https://github.com/hatedabamboo/orkeys/commit/4c5f830d0f3f3cbaf1452a46e8e8dd35b4b3bc48)
- (change) Breaking: a build from source needs Go 1.26. `go.mod` sets `toolchain go1.26.11` [4c5f830](https://github.com/hatedabamboo/orkeys/commit/4c5f830d0f3f3cbaf1452a46e8e8dd35b4b3bc48)
- (feat) `plan --out <file>` saves the plan with a fingerprint of its actions. `apply --plan <file>` applies only if
  the new plan is the same [4c5f830](https://github.com/hatedabamboo/orkeys/commit/4c5f830d0f3f3cbaf1452a46e8e8dd35b4b3bc48)
- (feat) A run that stops while it writes a new key leaves the state in phase `issuing`. `plan` shows `resume`.
  `apply` and `rotate` finish the change: they copy the new key from a place that has it, or issue another key and
  treat the lost key as an old key [4c5f830](https://github.com/hatedabamboo/orkeys/commit/4c5f830d0f3f3cbaf1452a46e8e8dd35b4b3bc48)
- (feat) `plan` shows `reissue` when the `orkeys-key-id` metadata of a secret names another key than the state [4c5f830](https://github.com/hatedabamboo/orkeys/commit/4c5f830d0f3f3cbaf1452a46e8e8dd35b4b3bc48)
- (feat) `orkeys keys -p <provider>` lists, creates, limits, disables, enables and deletes keys of any provider in
  `policy.yaml` [4c5f830](https://github.com/hatedabamboo/orkeys/commit/4c5f830d0f3f3cbaf1452a46e8e8dd35b4b3bc48)
- (feat) `providers[].options` in `policy.yaml`: settings of one provider type. `openai` has `project`; `workspace`
  still works. Unknown options are errors [4c5f830](https://github.com/hatedabamboo/orkeys/commit/4c5f830d0f3f3cbaf1452a46e8e8dd35b4b3bc48)
- (feat) `--events-file <path>` appends each event as one JSON line, and `--events-webhook <url>` posts each event, at
  the time of the event. A killed run keeps its events. `--log-format json` writes progress logs as JSON [4c5f830](https://github.com/hatedabamboo/orkeys/commit/4c5f830d0f3f3cbaf1452a46e8e8dd35b4b3bc48)
- (feat) Each successful locked run records a heartbeat in the state store. The exporter shows it as
  `orkeys_last_success_timestamp_seconds{operation}`. Alert on it to find a stopped `rotate` schedule [4c5f830](https://github.com/hatedabamboo/orkeys/commit/4c5f830d0f3f3cbaf1452a46e8e8dd35b4b3bc48)
- (fix) State writes are conditional: Vault check-and-set, S3 `If-Match` and `If-None-Match`. A write over a state
  that another process changed fails [4c5f830](https://github.com/hatedabamboo/orkeys/commit/4c5f830d0f3f3cbaf1452a46e8e8dd35b4b3bc48)
- (fix) When lock renewals fail until 2.5 minutes before the lock expires, orkeys treats the lock as lost and stops
  writing state [4c5f830](https://github.com/hatedabamboo/orkeys/commit/4c5f830d0f3f3cbaf1452a46e8e8dd35b4b3bc48)
- (fix) After SIGINT or SIGTERM, the cleanup of a half-done action still runs, for at most 30 seconds per step. `apply`
  and `rotate` stop between actions. A stopped `rotate` does not mark the rotation as failed [4c5f830](https://github.com/hatedabamboo/orkeys/commit/4c5f830d0f3f3cbaf1452a46e8e8dd35b4b3bc48)
- (fix) `rotate` does not revoke an OpenAI key that was used after the switch. When the spend of an old key goes
  down (a new month), `rotate` takes a new baseline and does not revoke [4c5f830](https://github.com/hatedabamboo/orkeys/commit/4c5f830d0f3f3cbaf1452a46e8e8dd35b4b3bc48)
- (fix) Vault secret field writes and deletes use check-and-set. A change by another client is not lost [4c5f830](https://github.com/hatedabamboo/orkeys/commit/4c5f830d0f3f3cbaf1452a46e8e8dd35b4b3bc48)
- (fix) `request` refuses a key name that exists (two requests in one second) before it creates a key [4c5f830](https://github.com/hatedabamboo/orkeys/commit/4c5f830d0f3f3cbaf1452a46e8e8dd35b4b3bc48)
- (security) Go toolchain 1.26.11 fixes standard library vulnerabilities that `govulncheck` reports. Updated
  `golang.org/x/crypto`, `golang.org/x/term` and `filippo.io/age` [4c5f830](https://github.com/hatedabamboo/orkeys/commit/4c5f830d0f3f3cbaf1452a46e8e8dd35b4b3bc48)
- (ci) Pull requests run gofmt, vet, tests with the race detector, golangci-lint and govulncheck. Release binaries have
  `.sha256` files. The example workflows install the release binary and check its checksum [4c5f830](https://github.com/hatedabamboo/orkeys/commit/4c5f830d0f3f3cbaf1452a46e8e8dd35b4b3bc48)
- (docs) README describes apply safety, how to add a provider and how to add a consumer type [4c5f830](https://github.com/hatedabamboo/orkeys/commit/4c5f830d0f3f3cbaf1452a46e8e8dd35b4b3bc48)

## v0.3.0 (2026-11-11)

- (feat) `openai` provider kind and `orkeys openai` (`oa`) commands: `list`, `get`, `create`, `update`, `delete`. They
  use an OpenAI Admin API key and one project (`workspace` in `policy.yaml`, `--project` or `OPENAI_PROJECT_ID`).
  orkeys creates one service account per key and deletes the account with its last key (#23) [ff22011](https://github.com/hatedabamboo/orkeys/commit/ff22011db11d7b9b4e99d43e6c6eba77ac470ee6)
- (feat) OpenAI keys work in inventory mode: `apply`, `rotate` with a native expiry, `request`, `revoke`, `import` and
  `hygiene`. OpenAI has no per-key limit: `budget` is only a metric, and `validate` rejects `disabled: true` and a
  `ttl` over `365d` on OpenAI keys (#23) [ff22011](https://github.com/hatedabamboo/orkeys/commit/ff22011db11d7b9b4e99d43e6c6eba77ac470ee6)
- (feat) `orkeys_key_budget_usd` metric: the manifest `budget.limit` of each key, per period (#23) [ff22011](https://github.com/hatedabamboo/orkeys/commit/ff22011db11d7b9b4e99d43e6c6eba77ac470ee6)
- (change) `orkeys_key_hard_limit_usd` is exported only for providers with a per-key limit (OpenRouter, DeepInfra). To
  alert on OpenAI keys, compare `orkeys_key_spend_usd` with `orkeys_key_budget_usd` (#23) [ff22011](https://github.com/hatedabamboo/orkeys/commit/ff22011db11d7b9b4e99d43e6c6eba77ac470ee6)
- (docs) README shows what each provider supports in provider commands and in inventory mode (#23) [5250e54](https://github.com/hatedabamboo/orkeys/commit/5250e54b93d9cb4902acae3948b37ba6818a87b6) [ff22011](https://github.com/hatedabamboo/orkeys/commit/ff22011db11d7b9b4e99d43e6c6eba77ac470ee6)

## v0.2.6 (2026-11-11)

- (feat) `orkeys revoke <name>...` revokes requested keys before they expire: it deletes the provider key, the secret
  fields and the GitHub secrets, and removes the state. It refuses manifest keys. If the provider key is already
  deleted, it removes the state. Optional `--reason`; `--yes` in scripts (#21)
- (feat) `orkeys state [name]` lists the keys in the state store with source (`manifest`, `request`, `orphan`), owner,
  key ID, limit, expiry and rotation. Filters: `--team`, `--kind`, `--owner`, `--provider`, `--source`. With a name,
  it prints that state entry as JSON. It needs no management keys (#20)

## v0.2.5 (2026-11-11)

- (feat) `github` consumer type: orkeys writes the key to a GitHub Actions secret (repository, environment or
  organization) and deletes it when the consumer or the manifest is removed. Token: `ORKEYS_GITHUB_SECRETS_TOKEN`
  (default `GITHUB_TOKEN`) (#18) [ed04189](https://github.com/hatedabamboo/orkeys/commit/ed04189d90664a1135435fbdb743a271e3d6f71f)
- (feat) `secret` in a key manifest can be a list of places (`path`, `field`). A change to the list reissues the key.
  State with the old `secret_path` and `secret_field` is read as a list of one (#18) [ed04189](https://github.com/hatedabamboo/orkeys/commit/ed04189d90664a1135435fbdb743a271e3d6f71f)

## v0.2.4 (2026-11-10)

- (feat) `disabled: true` in a key manifest disables the key at the provider and stops its rotation.
  `track_disabled: true` in `policy.yaml` enables keys that are disabled at the provider but not in the manifest.
  New events: `key_disabled`, `key_enabled` (#17) [d4573d6](https://github.com/hatedabamboo/orkeys/commit/d4573d6a84aa9b9342f48626d24ee113aeeab982)
- (feat) More than one provider of the same type: `providers` in `policy.yaml` takes a list of `{name, kind, ...}`.
  The map form still works (#16, #17) [847b290](https://github.com/hatedabamboo/orkeys/commit/847b290b762432cdc1b6eb9a315aaf9f5918a3a3)
- (ci) Push correct image tags to the container registry (#15) [ef86b3f](https://github.com/hatedabamboo/orkeys/commit/ef86b3f7fbd2ab358a85255b819131b3807d5501)

## v0.2.3 (2026-11-09)

- (feat) Alerts go to a team channel by kind `source`: `request` to `channels.notifications`, `manifest` to
  `channels.alerts`. `kinds.<kind>.alert_channel` is deprecated and ignored (#14) [e7e1757](https://github.com/hatedabamboo/orkeys/commit/e7e17572f746361d5037929eb65920f007b20438)
- (ci) Add workflow for container releases (#14) [cae504d](https://github.com/hatedabamboo/orkeys/commit/cae504db9bd090b978adb6f9b3d3ba96c85e2436)

## v0.2.2 (2026-11-09)

- (feat) Vault TLS from `VAULT_CLIENT_CERT`, `VAULT_CLIENT_KEY` (mTLS) and `VAULT_CACERT` (#11, #12) [6f21678](https://github.com/hatedabamboo/orkeys/commit/6f2167805833205345213d3608cd1e2842d7cbd3) [6afed9f](https://github.com/hatedabamboo/orkeys/commit/6afed9faac9f772f92c1f1e39bbfb236eab35df7)

## v0.2.1 (2026-11-09)

- (feat) Team members from GitHub teams. `orkeys request --team <name>` chooses the team of a person who is in more
  than one team (#10) [2e6341c](https://github.com/hatedabamboo/orkeys/commit/2e6341cff15ea93b48c1fd847e8f22757f4c4ff2)
- (docs) Update README and add workflow examples (#10) [13fd409](https://github.com/hatedabamboo/orkeys/commit/13fd409b8a1933d7b820d3a5e39c361e4586abd8)

## v0.2.0 (2026-11-07)

- (feat) Inventory mode: `validate`, `plan`, `apply`, `rotate`, `request`, `issued`, `import`, `hygiene`, `unlock`
  and `exporter` commands. State in Vault or S3, secrets in Vault or AWS Secrets Manager, Prometheus exporter,
  `orkeys deepinfra` (`di`), Dockerfile. OpenRouter commands moved under `orkeys openrouter` (`or`). Interactive
  mode removed (#7) [2302aca](https://github.com/hatedabamboo/orkeys/commit/2302acaf0463b420d2b0d212c9978112d8198a9a)

## v0.1.2 (2026-11-02)

- (chore) Bump the charmbracelet group with 3 updates (#2) [ff2bd53](https://github.com/hatedabamboo/orkeys/commit/ff2bd535142614179ca88548b003630e16f1ba61)
- (chore) Update dependencies [cc6ad9b](https://github.com/hatedabamboo/orkeys/commit/cc6ad9b5031647fa738f49c3544efb19cbfb76ca)

## v0.1.1 (2026-11-02)

- (ci) Update workflows and bump GitHub Actions versions [114cc03](https://github.com/hatedabamboo/orkeys/commit/114cc03b54cfd4ae318778f571221c4b4bf14157) [dbf063d](https://github.com/hatedabamboo/orkeys/commit/dbf063d9bf90a903eecfe31a83d0005271fbd7cf)

## v0.1.0 (2026-11-02)

- (release) orkeys goes live: OpenRouter key commands `list`, `get`, `create`, `update`, `enable`, `disable`,
  `delete`, interactive mode, output formats `table`, `wide`, `json` [d4fa176](https://github.com/hatedabamboo/orkeys/commit/d4fa1765b3763248de1ab641e92a0b09bc4a5b4d)
