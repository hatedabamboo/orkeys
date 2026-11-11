# CHANGELOG

## v0.3.0 (2026-11-11)

- (feat) `openai` provider kind and `orkeys openai` (`oa`) commands: `list`, `get`, `create`, `update`, `delete`. They
  use an OpenAI Admin API key and one project (`workspace` in `policy.yaml`, `--project` or `OPENAI_PROJECT_ID`).
  orkeys creates one service account per key and deletes the account with its last key
- (feat) OpenAI keys work in inventory mode: `apply`, `rotate` with a native expiry, `request`, `revoke`, `import` and
  `hygiene`. OpenAI has no per-key limit: `budget` is only a metric, and `validate` rejects `disabled: true` and a
  `ttl` over `365d` on OpenAI keys
- (feat) `orkeys_key_budget_usd` metric: the manifest `budget.limit` of each key, per period
- (change) `orkeys_key_hard_limit_usd` is exported only for providers with a per-key limit (OpenRouter, DeepInfra). To
  alert on OpenAI keys, compare `orkeys_key_spend_usd` with `orkeys_key_budget_usd`
- (docs) README shows what each provider supports in provider commands and in inventory mode

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
