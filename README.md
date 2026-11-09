# orkeys

[![Release](https://img.shields.io/github/v/release/hatedabamboo/orkeys)](https://github.com/hatedabamboo/orkeys/releases)
[![Go Reference](https://pkg.go.dev/badge/github.com/hatedabamboo/orkeys.svg)](https://pkg.go.dev/github.com/hatedabamboo/orkeys)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

`orkeys` (pronounced "ORKies") is an IaC for your LLM API keys. `orkeys` manages LLM API keys. It has two modes:

- **Provider commands** manage keys at one provider directly: `orkeys openrouter` (`or`) and `orkeys deepinfra` (`di`).
- **Inventory mode** manages keys of all providers from an inventory repository, with state and secrets in Vault or
  AWS, rotation, alerts and self-service keys (see [Inventory mode](#inventory-mode)).

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

## Provider commands

### OpenRouter

`orkeys openrouter` (alias `or`) uses the
[Management API](https://openrouter.ai/docs/guides/overview/auth/management-api-keys). To create a management key, go to
[openrouter.ai/settings/management-keys](https://openrouter.ai/settings/management-keys).
A management key can only manage keys. It cannot call models.

| Variable | Required | Purpose |
|---|---|---|
| `OPENROUTER_MANAGEMENT_API_KEY` | yes | Management API key |
| `OPENROUTER_API_BASE` | no | API base URL. Default: `https://openrouter.ai/api/v1` |
| `OPENROUTER_WORKSPACE_ID` | no | Workspace for list, create and lookup. Same as `--workspace` |

```sh
orkeys or list                          # all pages, active keys
orkeys or list -d -o wide               # with disabled keys, all columns
orkeys or list --json | jq '.[] | select(.limit == null) | .name'
orkeys or get ci
orkeys or create --name ci --limit 20 --limit-reset monthly --expires 90d
KEY=$(orkeys or create --name tmp --expires 12h -q)   # print only the secret
orkeys or update ci --limit none --limit-reset none --name ci-old
orkeys or disable ci-old 3f2a9c
orkeys or enable ci-old
orkeys or delete ci-old -y
```

Commands that take `<key>` accept a full hash, a unique hash prefix or an exact key name.

### DeepInfra

> **DeepInfra cannot be automated with API keys.** DeepInfra's own rule: "Some actions are never open to an API key,
> whatever its scopes, unrestricted included: managing API keys, changing payment details, setting spending limits and
> topping up all need a dashboard login"
> ([docs](https://docs.deepinfra.com/account/authentication#api-keys)). There is no documented API session for that
> login. So with any API key, orkeys can only **read** DeepInfra: token names, limits and spend. Creating, deleting,
> limiting or disabling tokens fails with `401`, in the `di` commands and in inventory mode.

`orkeys deepinfra` (alias `di`) shows API tokens with their monthly limit and spend.

| Variable | Required | Purpose |
|---|---|---|
| `DEEPINFRA_API_KEY` | yes | API key. Reads need the `billing:read` scope |
| `DEEPINFRA_API_BASE` | no | API base URL. Default: `https://api.deepinfra.com` |

```sh
orkeys di list                          # tokens with limit and spend this month
orkeys di get ci
```

`list` and `get` read `/payment/key-limits`. An API key cannot list tokens (`/v1/api-tokens`), so scopes and creation
times are not shown. Commands that take `<token>` accept a token ID or an exact name.

The write commands (`create`, `update`, `disable`, `delete`, `scopes`) call the token and limit endpoints. They work
only with a credential that DeepInfra allows to manage keys; no API key qualifies today.

What we found about the alternative, scoped JWTs (`POST /v1/scoped-jwt`), tested on 2026-11-06:

- A JWT expires on time and cannot call management endpoints. A model allow-list is enforced.
- `spending_limit` is a total for the JWT's life, and it works only with exactly one model.
- `api_key_name` picks the key that signs and pays. Any key that may mint JWTs can bill any key in the account.
- Only the signing key can read a JWT's spend (`GET /v1/scoped-jwt`).
- Retired model names are redirected to a new model, so a JWT for an old name is refused.

orkeys does not use JWTs. To manage DeepInfra traffic fully, route it through OpenRouter with DeepInfra as the
provider: the keys are then OpenRouter keys.

Run `orkeys <provider> <command> --help` to see all flags.

### Output

`-o table` (default), `-o wide` or `-o json`. `--json` is a shorthand for `-o json`.
JSON output holds the full API objects. `create --json` includes the secret in `key`.

### Value formats

| Flag | Format |
|---|---|
| `--limit` | USD amount, or `none` to remove the limit |
| `--limit-reset` | OpenRouter only: `daily`, `weekly`, `monthly` or `none`. Resets happen at midnight UTC |
| `--expires` | OpenRouter only: duration (`30d`, `2w`, `12h`), date (`2027-12-31`, midnight UTC) or RFC 3339 |

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

## Inventory mode

Inventory mode manages keys of all providers from a git repository of YAML manifests. orkeys makes the
providers, a secrets store and Kubernetes match the manifests, rotates keys before they expire, issues short-lived
self-service keys, and exports spend and expiry as Prometheus metrics.

Providers: OpenRouter (read and write), DeepInfra (read only: spend, limits and the exporter work; creating,
limiting and revoking keys fail, see [DeepInfra](#deepinfra)).

To start, copy [`examples/inventory`](examples/inventory) and run `orkeys validate --repo <dir>`.
[`examples/inventory/.github/workflows`](examples/inventory/.github/workflows) has GitHub Actions workflows for
`rotate` (hourly) and `hygiene` (weekly). They log in to Vault with AppRole and write the events to the run summary.

### Commands

| Command | What it does |
|---|---|
| `orkeys validate` | Check the repository against `policy.yaml` and the rules below. Needs no credentials |
| `orkeys plan` | Show the changes. `--markdown` for a PR comment, `--json` for scripts |
| `orkeys apply` | Create, update and revoke keys; write manifest keys to the secrets store. Asks for confirmation; `--yes` in scripts |
| `orkeys rotate` | Rotate manifest keys before expiry, sync External Secrets, restart the listed workloads, revoke old keys after the grace period. Expire requested keys. Run it on a schedule (for example hourly) |
| `orkeys rotate --now <key>` | Rotate a manifest key now |
| `orkeys rotate --retry <key>` | Retry a failed rotation |
| `orkeys rotate --only <key>` | Process only these keys |
| `orkeys request --user <login> --kind <kind> --provider <name> --purpose <text>` | Issue a key of a kind with `source: request`, encrypted to the user's GitHub SSH keys. Optional `--limit` and `--ttl`, up to the kind maximum |
| `orkeys issued` | List active requested keys with owner, purpose, spend and expiry. Filters: `--team`, `--owner`, `--kind`. `--markdown` for a table |
| `orkeys import` | Print a manifest with an `import` block for each provider key that orkeys does not manage |
| `orkeys hygiene` | **Changes keys** unless you pass `--dry-run`: revokes keys of people who left the GitHub org, lists unused keys |
| `orkeys unlock [id]` | Show the state lock, or remove it by ID after a crashed run |
| `orkeys exporter` | Serve spend, limits, expiry and rotation state as Prometheus metrics |

Point orkeys at the repository with `--repo` or `ORKEYS_REPO`.

### Repository layout

```
policy.yaml                 global settings and rules
teams/<team>/team.yaml      team routing channels; members or a GitHub team
teams/<team>/<key>.yaml     one manifest key per file; file name = key name
```

Unknown fields are errors, so a typo does not pass silently.

#### `policy.yaml`

| Field | Default | Meaning |
|---|---|---|
| `state` | `vault://secret/orkeys/state` | State store: `vault://<mount>/<path>` or `s3://<bucket>/<prefix>` |
| `secrets` | `vault` | Secrets store: `vault` or `aws` (Secrets Manager) |
| `providers.<name>.management_key` | | Secret path with field `key` (and optional `self_id`, `workspace`, `base_url`). Optional if `ORKEYS_<NAME>_KEY` is set |
| `providers.<name>.workspace`, `base_url` | | OpenRouter workspace; API base URL |
| `envs` | `[prod, staging, dev]` | Allowed `env` values of manifests |
| `name_prefix` | `{env}-{team}-` | Required start of key names. `{env}` and `{team}` are replaced. `""` turns the check off |
| `naming` | `^[a-z0-9][a-z0-9._-]*$` | Regex that every key name must match |
| `kinds.<name>` | | Key kinds. At least one is required. See below |
| `clusters.<name>.context` | cluster name | kubectl context for a cluster used in `consumers` |
| `github.org` | | Org for owner checks in `hygiene` and for team members from GitHub teams |
| `github.api_url`, `github.keys_url` | `https://api.github.com`, `https://github.com` | For GitHub Enterprise |

#### Kinds

You name the kinds and set their values. A kind name matches `^[a-z][a-z0-9-]*$`. The `source` of a kind sets how
its keys are made and how they end:

- `manifest`: keys come from manifests in `teams/`. orkeys writes them to the secrets store and rotates them before
  they expire.
- `request`: keys come from `orkeys request`. orkeys encrypts them to the owner. They expire at `ttl` and are not
  renewed. Requested key names are `<kind>-<login>-<time>`, so no manifest name can start with `<kind>-`.

| Field | Source | Default | Meaning |
|---|---|---|---|
| `source` | all | `manifest` | `manifest` or `request` |
| `ttl` | all | required | Manifest keys: the default and the longest allowed `ttl`. Requested keys: the default and the longest `--ttl` |
| `providers` | all | all in `providers` | Providers allowed for this kind |
| `budget.reset` | all | `monthly` | Default limit period: `daily`, `weekly` or `monthly`. Requested keys always use it |
| `budget.limit` | all | `max_limit` | Default limit in USD when the manifest or the request gives none |
| `max_limit` | all | none (required for `request`) | Highest allowed limit |
| `alert_channel` | all | `alerts` | Team channel for `alert` events and the `alert_channel` label: `alerts` or `notifications` |
| `rotate_before` | `manifest` | `3d` | Start rotation this long before expiry. Must be shorter than `ttl` |
| `revoke_grace` | `manifest` | `24h` | Keep the old key this long after consumers switched |
| `notify_before` | `request` | none | Send `expiry_notice` this long before expiry |
| `max_active` | `request` | none | Active keys of this kind per person |
| `max_total_limit` | `request` | none | Sum of limits of this kind per person |

```yaml
kinds:
  production:
    ttl: 90d
    budget: {reset: monthly, limit: 100}
  batch:                          # short-lived job keys, rotated monthly
    ttl: 30d
    rotate_before: 1d
    budget: {reset: daily}
    max_limit: 50
    providers: [openrouter]
  personal:
    source: request
    ttl: 14d
    budget: {limit: 25}
    max_limit: 100
    max_active: 3
    notify_before: 3d
    alert_channel: notifications
    providers: [openrouter]
```

If you remove a kind from the policy while keys of it exist, `plan` and `rotate` keep the requested keys of that kind
and revoke them at expiry. `rotate` fails for manifest keys of that kind until the kind is back or the manifests use
another kind.

#### `teams/<team>/team.yaml`

```yaml
team: search                         # must match the directory name
channels:
  notifications: "#search-notifications"   # info events; alerts of kinds with alert_channel: notifications
  alerts: "#search-alerts"                 # alerts of the other kinds
github_team: search-eng             # optional; GitHub team slug if it is not the team name
# members: [alice, bob]              # optional; static GitHub logins instead of a GitHub team
```

Members of a team may request keys. orkeys reads them from one of two places:

- **GitHub team (default).** Without `members`, orkeys uses the team `github.com/orgs/<github.org>/teams/<slug>`.
  The slug is `github_team`, or `team` if `github_team` is not set. Members of child teams count. Pending invitations
  do not count. This needs `github.org` and `GITHUB_TOKEN` with `read:org`.
- **Static list.** `members: [alice, bob]` sets the GitHub logins in the file. No token is necessary.

`members` and `github_team` together are an error. `validate` does not use the network. `plan` warns about a GitHub
team that does not exist, if `GITHUB_TOKEN` is set.

Channels are optional. orkeys sends nothing itself: it puts the channel on events and metrics, so a downstream
service (Alertmanager, a CI step, a bot) can route them.

#### Key manifest

| Field | Required | Meaning |
|---|---|---|
| `name` | yes | Same as the file name. Must match `naming` and start with `name_prefix` |
| `kind` | yes | A kind with `source: manifest` |
| `provider` | yes | A key of `providers`, allowed by the kind |
| `env` | yes | One of `envs` |
| `service` | no | Free text; exported as a metric label |
| `description` | yes | |
| `scopes` | no | DeepInfra only. Default `[inference]`. `unrestricted` is not allowed |
| `budget.limit` | no | Limit in USD at the provider; the provider blocks the key when spend in the period reaches it. Default: the kind `budget.limit`. Up to the kind `max_limit` |
| `budget.reset` | no | Limit period: `daily`, `weekly`, `monthly` (DeepInfra: monthly only). Default: the kind `budget.reset` |
| `ttl` | no | `90d`, `2w`, `24h`. Default and maximum: the kind `ttl` |
| `secret.path`, `secret.field` | path: yes | Where the key goes. Vault: `<mount>/<path>`; AWS: secret name. Field default `api_key`. Other fields in the same secret are kept |
| `consumers[]` | no | `type` (`eso` or `direct`), `cluster`, `namespace`, `externalsecret` (eso), `restart` (`deployment/x`, `statefulset/x`, `daemonset/x`, `cronjob/x`). orkeys restarts only what `restart` lists |
| `import.id`, `import.name` | no | Adopt an existing provider key instead of creating one. `name` is its current name at the provider. See below |

```yaml
consumers:
  - type: eso                       # force-sync, then restart the API
    cluster: prod-eu
    namespace: search
    externalsecret: search-llm
    restart: [deployment/search-api]
  - type: eso                       # force-sync only; the worker reloads the mounted secret
    cluster: prod-eu
    namespace: search
    externalsecret: search-worker-llm
```

If a consumer still uses the old key after `revoke_grace`, its spend grows and orkeys does not revoke the old key
(`old_key_in_use`). So a consumer without `restart` must pick up the new secret by itself.

Change `secret.path` or `secret.field` to move a key: orkeys rotates it to the new place and removes the old field after
the revoke. Delete the manifest to revoke the key; orkeys removes only its own field from the secret.

#### Importing an existing key

A key made outside orkeys (for example in the provider dashboard) can be managed like any other key. orkeys adopts it
instead of creating a new one. orkeys never revokes or disables a key that has no manifest.

1. Run `orkeys import`. It prints a manifest for each provider key without state, with `import.id` and `import.name`
   filled in and the current limit as `budget`.
2. Write the current key value to `secret.path` (field `secret.field`, default `api_key`). orkeys cannot read a key
   value from the provider.
3. Save the manifest as `teams/<team>/<name>.yaml`, fill in the `TODO` fields and open a PR.

```yaml
name: prod-search-api
kind: production
provider: openrouter
env: prod
description: Search API
import:
  id: 3f2a9c1b7e04d5a6...
  name: search-api          # current name at the provider
budget: {reset: monthly, limit: 500}
secret: {path: secret/llm/prod/search/api}
```

`plan` shows `import` and checks that a key with `import.id` exists, has the name `import.name`, and is not managed yet.
`apply` reads the secret and fails if the field is empty, sets `budget` at the provider, and saves the state. It
creates no key and restarts nothing. The key expires at import time + `ttl`, or earlier if the provider has an earlier
expiry. From then on the key rotates like any other key. After the import, the `import` block has no effect; you can
delete it.

### Output and events

orkeys does not send notifications. Commands that change keys (`apply`, `rotate`, `request`, `hygiene`) emit
**events**, and you route them with the tool you already have.

- Text mode (default): each event is one line on stdout, for example
  `[alert] rotation_failed prod-search-embeddings: rotation failed: ...`. Progress logs go to stderr.
- JSON mode (`-o json` or `--json`): the command prints one JSON document on stdout at the end, also when it fails:

```json
{
  "plan": { "actions": [ ... ] },
  "events": [
    {
      "time": "2026-11-06T12:00:00Z",
      "level": "alert",
      "type": "rotation_failed",
      "key": "prod-search-embeddings",
      "kind": "production",
      "team": "search",
      "provider": "openrouter",
      "channel": "#search-alerts",
      "message": "rotation failed: ...; the old key stays active; after the fix run: orkeys rotate --retry prod-search-embeddings",
      "run_url": "https://github.com/acme/keys/actions/runs/123"
    }
  ],
  "error": "1 key failed"
}
```

The result key depends on the command: `plan` (apply), `issued` (request), `findings` (hygiene); `rotate` has only
`events`. `apply -o json` needs `--yes`. The exit code is 1 when `error` is set.

| Event `type` | Level | When |
|---|---|---|
| `key_issued` | info | A key was created |
| `key_imported` | info | An existing provider key was adopted |
| `key_revoked` | info | A key was revoked (manifest removed, expired, owner left) |
| `rotation_started` | info | A new key was written; consumers sync next |
| `rotation_synced` | info | Consumers use the new key; the old key will be revoked after `revoke_grace` |
| `old_key_revoked` | info | The old key of a rotation was revoked |
| `expiry_notice` | info | A self-service key expires within `notify_before` |
| `rotation_failed` | alert | A consumer did not sync or restart; the old key stays active |
| `old_key_in_use` | alert | The old key was used after the switch; it is not revoked |

`level` picks `channel`: `info` uses the team's `channels.notifications`, `alert` uses `channels.alerts`.
`run_url` is set when orkeys runs in GitHub Actions.

### Storage

Inventory mode needs two stores:

- **State** (`state`). One entry per key: provider key ID, expiry, rotation progress, keys waiting for revoke,
  owner and purpose of requested keys. Without it, orkeys cannot link a manifest to a provider key.
  - `vault://<mount>/<path>`: Vault KV v2, one secret per key.
  - `s3://<bucket>/<prefix>`: one JSON object per key, `<prefix>/<key name>.json`. Turn on bucket versioning to keep
    history.
- **Secrets** (`secrets`). Manifest keys, and by default the provider management keys.
  - `vault`: Vault KV v2. A path is `<mount>/<path>`; fields are secret fields. Manifest keys get custom metadata
    `orkeys-key`, `orkeys-key-id`, `orkeys-team`.
  - `aws`: AWS Secrets Manager. A path is the secret name; the value is a JSON object, fields are its keys.
    Manifest keys get the same tags. A secret deleted by orkeys keeps a 7-day recovery window.

S3 and Secrets Manager together let orkeys run with no Vault.

| Works without stores | Needs the stores |
|---|---|
| `validate`, and the provider commands (`orkeys or ...`, `orkeys di ...`) | `plan`, `apply`, `rotate`, `request`, `issued`, `import`, `hygiene`, `unlock`, `exporter` |

Read-only commands (`plan`, `issued`, `import`) need read access to the state and to the management keys.

### Credentials

| Variable | Used by | Purpose |
|---|---|---|
| `VAULT_ADDR`, `VAULT_NAMESPACE` | Vault stores | Vault address and optional namespace |
| `VAULT_TOKEN` | Vault stores | Vault token. Or `VAULT_ROLE_ID` + `VAULT_SECRET_ID` (AppRole, mount `VAULT_APPROLE_MOUNT`, default `approle`), or `VAULT_K8S_ROLE` (Kubernetes auth, mount `VAULT_K8S_MOUNT`, default `kubernetes`) |
| `VAULT_CLIENT_CERT`, `VAULT_CLIENT_KEY` | Vault stores | PEM client certificate and key for mTLS (for example, Vault behind Teleport app access). Set both or none |
| `VAULT_CACERT` | Vault stores | PEM CA bundle for the Vault server, added to the system CAs |
| `AWS_REGION`, `AWS_PROFILE`, ... | S3 and Secrets Manager | Standard AWS SDK credential chain: env vars, profile, web identity (GitHub OIDC), pod role |
| `ORKEYS_<PROVIDER>_KEY`, `ORKEYS_<PROVIDER>_SELF_ID` | all except validate | Management key and its ID; override `management_key` |
| `GITHUB_TOKEN` | request, hygiene, plan | Team members from GitHub teams and org membership checks (`read:org`). Without it, `request` fails for a GitHub team, `hygiene` skips owner checks, `plan` skips team checks |

AWS permissions: `s3:GetObject`, `s3:PutObject`, `s3:DeleteObject` on `<bucket>/<prefix>/*` and `s3:ListBucket` on the
bucket; `secretsmanager:GetSecretValue`, `CreateSecret`, `PutSecretValue`, `TagResource`, `DeleteSecret` on the
secrets orkeys reads and writes. The exporter needs only `s3:GetObject`, `s3:ListBucket` and `GetSecretValue`.

### Locking

`apply`, `rotate`, `request` and `hygiene` (without `--dry-run`) hold a lock on the state, the same way Terraform's S3
backend does with `use_lockfile`:

- **S3:** orkeys creates `<prefix>/.lock` with a conditional write (`If-None-Match: *`). A second writer gets
  `412 Precondition Failed`. Renew and takeover use `If-Match` with the ETag. This is not S3 Object Lock, which is
  write-once retention, not a mutex.
- **Vault:** orkeys creates `<path>/.lock` with check-and-set (`cas=0` creates only if absent; `cas=N` updates only
  version N).

The lock lives 15 minutes and the holder renews it every 5 minutes. A crashed run's lock expires, and the next run
takes it over. If the lock is held, a run waits up to `--lock-wait` (default 5m), then fails with the holder, the
operation and the lock ID. `orkeys unlock` shows the lock; `orkeys unlock <id>` removes it. If a holder loses its lock,
it stops. `apply` holds the lock across plan, confirmation and apply.

Limit: release reads the lock and then deletes it. Neither store has a conditional delete here, so a release that
races with a takeover of an expired lock can remove the new holder's lock.

### Rotation

1. When a manifest key is `rotate_before` (3d) from expiry, `rotate` creates a new key, saves the state, and writes
   the key to the secrets store. If the write fails, orkeys deletes the new key and restores the state.
2. For each `eso` consumer, it annotates the ExternalSecret with `force-sync` and waits until it is Ready with a newer
   `refreshTime`. Then it runs `kubectl rollout restart` and `rollout status` only for the targets in `restart`. A
   consumer with no `restart` is not restarted: use this for workloads that reload the secret by themselves (a mounted
   secret volume, or an app that reads Vault on each request). `direct` consumers (workloads that read the secrets
   store themselves) get no force-sync, only their `restart` targets. CronJobs are not restarted; the next run reads
   the new secret.
3. 15 minutes after the switch, a `rotate` run records the old key's usage (total usage on OpenRouter, spend this
   month on DeepInfra). After `revoke_grace` (24h), `rotate` compares it again. If the usage grew, a consumer still has
   the old key: orkeys does not revoke it and marks the rotation as failed. If no run recorded the usage in between,
   the run at revoke time records it and the next run revokes. Run `rotate` hourly.
4. A failed rotation sends an alert and stops. Fix the cause, then run `orkeys rotate --retry <key>`. The old key stays
   active until then.

OpenRouter keys also get a native expiry at `ttl`. DeepInfra keys have none; orkeys revokes them itself.

### Requested keys

Keys of a kind with `source: request` have no manifest. A team member requests one with a purpose. orkeys checks the
kind limits per person and keeps owner, team and purpose in the state. Alerts use the team channel from the kind
`alert_channel`. At expiry, `rotate` revokes the key and deletes its state; there is no renewal. If the kind has
`notify_before`, orkeys emits `expiry_notice` that long before expiry.

orkeys finds the team of `--user` in `members` or in the GitHub team. If the user is in more than one team, `request`
fails and lists the teams: give one with `--team <name>`. With `--team`, orkeys checks only that team. A missing
GitHub team or a missing `GITHUB_TOKEN` is an error, not "not a member".

The keys are not stored in the secrets store. orkeys encrypts them with `age` to the owner's SSH keys
(`ssh-ed25519` or `ssh-rsa`) from `<github.keys_url>/<login>.keys` and writes `<name>.age` to `--out`.
Decrypt with `age -d -i ~/.ssh/id_ed25519 <file>`.

### Hygiene

`hygiene` revokes requested keys whose owner is not in `github.org`. GitHub answers 404 both for "not a member"
and for a wrong org name or a token without access, so before any revoke orkeys checks that the org exists and that
at least one team member (from `members` or from the GitHub team) is reported as a member. A GitHub team that does
not exist also stops the check. If not, it revokes nothing and fails. A user who renamed
their GitHub login is reported as gone; they can request a new key. Run `--dry-run` first.

### Exporter

```sh
orkeys exporter --policy /etc/orkeys/policy.yaml          # state, secrets and providers from the policy

orkeys exporter --state s3://my-bucket/orkeys/state --secrets aws \
  --provider openrouter=orkeys/providers/openrouter      # or everything as flags; flags override --policy
```

The exporter polls the providers every `--interval` (default 10m) and serves the cached values. `/metrics` serves the
metrics; `/healthz` returns 200 after the first poll. It needs read access to the state and to the management keys.
Key series have the labels `name`, `team`, `kind`, `env`, `provider`, `service`, `owner`, `notify_channel`,
`alert_channel` and `reset`, so Alertmanager can route by team and channel. orkeys has no alert thresholds: an alert
rule compares `orkeys_key_spend_usd` with `orkeys_key_hard_limit_usd`, for example `spend / limit > 0.8`.

| Metric | Meaning |
|---|---|
| `orkeys_key_spend_usd` | Spend in the current limit period (`reset`: today, this week or this month) |
| `orkeys_key_hard_limit_usd` | Limit at the provider for the same period |
| `orkeys_key_expires_timestamp_seconds` | Expiry |
| `orkeys_key_last_used_timestamp_seconds` | Last use (OpenRouter only). OpenRouter reports the request time for recently used keys, so treat it as "recently used", not as an exact time |
| `orkeys_key_rotation_state` | 0 idle, 1 consumers not synced, 2 old key waits for revoke, 4 failed |
| `orkeys_key_missing` | 1 if the key is not found at the provider |
| `orkeys_unmanaged_keys{provider}` | Provider keys without state |
| `orkeys_provider_poll_success{provider}` | 1 if the last poll worked |
| `orkeys_provider_last_poll_timestamp_seconds{provider}` | Last successful poll |
| `orkeys_state_last_load_timestamp_seconds` | Last successful state load |

### Containers

The `Dockerfile` builds a distroless image with only the `orkeys` binary. It is enough for the exporter and for every
command except `rotate` with consumers, which runs `kubectl`. For `rotate`, run orkeys where `kubectl` and the cluster
contexts are available, for example in CI.

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
| `internal/openrouter` | HTTP client for the OpenRouter Management API |
| `internal/deepinfra` | HTTP client for DeepInfra API tokens and key limits |
| `internal/httpx` | JSON over HTTP with retries for GET |
| `internal/provider` | common interface and registry of the providers |
| `internal/manifest` | inventory loading and validation |
| `internal/state`, `internal/secrets` | state and secret store interfaces |
| `internal/vault`, `internal/s3store`, `internal/awssm`, `internal/backend` | Vault KV v2, S3, Secrets Manager, and the factory that picks them |
| `internal/engine` | plan, apply, rotate, request, issued, hygiene, import, report |
| `internal/exporter` | Prometheus metrics |
| `internal/kube`, `internal/deliver`, `internal/github` | kubectl, age delivery, org membership |
| `internal/cli` | cobra commands: `openrouter.go`, `deepinfra.go`, `inventory.go`, `exporter_cmd.go`; wiring in `wire.go`, output in `render.go` |
| `examples/inventory` | example inventory repository with rotate and hygiene workflows; a test keeps it valid |
| `internal/values` | input parsing and formatting |

### Releasing

1. Move the `Unreleased` entries in `CHANGELOG.md` under a new version.
2. Tag and push: `git tag v0.1.0 && git push origin v0.1.0`.
3. The Release workflow runs GoReleaser and creates a **draft** GitHub release with the archives and checksums.
4. Check the draft, then publish it.

## License

[MIT](LICENSE)
