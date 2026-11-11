# orkeys

[![Release](https://img.shields.io/github/v/release/hatedabamboo/orkeys)](https://github.com/hatedabamboo/orkeys/releases)
[![Go Reference](https://pkg.go.dev/badge/github.com/hatedabamboo/orkeys.svg)](https://pkg.go.dev/github.com/hatedabamboo/orkeys)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

`orkeys` (pronounced "ORKies") is an IaC for your LLM API keys. `orkeys` manages LLM API keys. It has two modes:

- **Provider commands** manage keys at one provider directly: `orkeys openrouter` (`or`), `orkeys deepinfra` (`di`)
  and `orkeys openai` (`oa`).
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

## Provider support

OpenRouter has full support. DeepInfra is read only: DeepInfra does not let an API key create, limit, disable or
delete tokens (see [DeepInfra](#deepinfra)). Each write to DeepInfra fails with `401`. OpenAI has no per-key limit and
no disabled flag: orkeys creates, rotates and deletes OpenAI keys, and tracks their budget only in metrics (see
[OpenAI](#openai)).

### Support in provider commands

| Feature | OpenRouter (`or`) | DeepInfra (`di`) | OpenAI (`oa`) |
|---|---|---|---|
| List keys (`list`) | yes | yes. No scopes and no creation time | yes. Keys of users and service accounts |
| Show one key (`get`) | yes | yes | yes |
| Create a key (`create`) | yes | no | yes. One service account per key |
| Change the limit (`update --limit`) | yes | no | no per-key limit |
| Limit period (`--limit-reset`) | `daily`, `weekly`, `monthly` | `monthly` only | no per-key limit |
| Expiry (`--expires`) | yes | no | yes. At most 365d |
| Rename (`update --name`) | yes | no | no |
| Change scopes (`update --scopes`) | no scopes | no | yes |
| List possible scopes (`scopes`) | no scopes | yes | no |
| Disable a key (`disable`) | yes | no | no |
| Enable a key (`enable`) | yes | no command | no |
| Delete a key (`delete`) | yes | no | yes |
| Workspaces (`--workspace`) | yes | no | one project per call (`--project`), required |
| Spend shown | day, week, month, total | month | day, week, month |
| Last use shown | yes | no | yes |

### Support in inventory mode

| Feature | OpenRouter | DeepInfra | OpenAI |
|---|---|---|---|
| `validate`, `state` | yes | yes | yes |
| `plan` | yes | yes | yes |
| `apply`: create a key | yes | no | yes |
| `apply`: set the limit | yes | no | no. The budget is only a metric |
| `apply`: `disabled: true` | yes. Sets the disabled flag | no | no. `validate` rejects it |
| `apply`: revoke a deleted manifest | yes | no | yes |
| `budget.reset` | `daily`, `weekly`, `monthly` | `monthly` only | `daily`, `weekly`, `monthly`. Only a metric |
| `scopes` field | no | yes. Used only at create | yes. Used only at create |
| `rotate` | yes. The key also has a native expiry | no | yes. The key also has a native expiry. `ttl` at most 365d |
| `request`, `revoke` | yes | no | yes |
| `import`: print manifests | yes | yes | yes |
| `apply`: adopt an `import` key | yes | no | yes |
| `hygiene`: revoke keys of people who left | yes | no | yes |
| `hygiene`: find unused keys | yes | no. DeepInfra has no last use | yes |
| `exporter`: spend | yes | yes. Spend this month only | yes |
| `exporter`: hard limit | yes | yes | no. Budget only |
| `exporter`: last use | yes | no | yes |
| Old key "still in use" check at revoke | total usage | spend this month | last use (exact), spend this month |

`orkeys keys -p <provider>` lists, creates, limits, disables, enables and deletes keys of any provider in
`policy.yaml` with the same commands for all providers. It does what all adapters can do; the provider commands
above show the fields of one API.

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

### OpenAI

`orkeys openai` (alias `oa`) uses the [Admin API](https://platform.openai.com/docs/api-reference/administration) to
manage the API keys of one project. Create an Admin API key at
[platform.openai.com/settings/organization/admin-keys](https://platform.openai.com/settings/organization/admin-keys).
An Admin API key cannot call models.

| Variable | Required | Purpose |
|---|---|---|
| `OPENAI_ADMIN_KEY` | yes | Admin API key |
| `OPENAI_PROJECT_ID` | yes | Project ID (`proj_...`). Same as `--project` |
| `OPENAI_API_BASE` | no | API base URL. Default: `https://api.openai.com/v1` |

```sh
orkeys oa list                          # keys with spend this month
orkeys oa list -o wide                  # with owner, day and week spend
orkeys oa get ci
orkeys oa create --name ci --expires 90d
orkeys oa create --name reader --scopes api.all.read
KEY=$(orkeys oa create --name tmp --expires 12h -q)   # print only the secret
orkeys oa update ci --expires 30d       # new expiry, counted from now
orkeys oa delete ci -y
```

Limits of the OpenAI Admin API:

- OpenAI has no per-key spend limit. In inventory mode, orkeys keeps `budget` only as a metric
  (`orkeys_key_budget_usd`). Limits per project and per organization are set in the OpenAI dashboard.
- OpenAI has no disabled flag. `delete` is the only way to revoke a key.
- The Admin API cannot create a key for a user. `create` makes a service account with the key name, and then one key
  for it. `delete` deletes the key, and also the service account when the account has no other keys.
- A key cannot be renamed. The expiry is at most 365 days, and `update --expires` counts it from the time of the call.
- Spend comes from the costs endpoint (`/organization/costs`), in 1-day UTC buckets. OpenAI fills them with a delay,
  so the spend of today is not complete. A week starts on Monday.

Commands that take `<key>` accept a key ID or an exact key name.

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

DeepInfra is read only. [Provider support](#provider-support) shows what works for each provider.

To start, copy [`examples/inventory`](examples/inventory) and run `orkeys validate --repo <dir>`.
[`examples/inventory/.github/workflows`](examples/inventory/.github/workflows) has GitHub Actions workflows for
`rotate` (hourly) and `hygiene` (weekly). They log in to Vault with AppRole and write the events to the run summary.

### Commands

| Command | What it does |
|---|---|
| `orkeys validate` | Check the repository against `policy.yaml` and the rules below. Needs no credentials |
| `orkeys plan` | Show the changes. `--markdown` for a PR comment, `--json` for scripts, `--out <file>` to save the plan for `apply --plan` |
| `orkeys apply` | Create, update and revoke keys; write manifest keys to the secrets store. Asks for confirmation; `--yes` in scripts. Refuses more than `max_deletes` revokes without `--allow-deletes <n>`. With `--plan <file>`, applies only if the plan is the same as the saved one |
| `orkeys rotate` | Rotate manifest keys before expiry, sync External Secrets, restart the listed workloads, revoke old keys after the grace period. Expire requested keys. Run it on a schedule (for example hourly) |
| `orkeys rotate --now <key>` | Rotate a manifest key now |
| `orkeys rotate --retry <key>` | Retry a failed rotation |
| `orkeys rotate --only <key>` | Process only these keys |
| `orkeys request --user <login> --kind <kind> --provider <name> --purpose <text>` | Issue a key of a kind with `source: request`, encrypted to the user's GitHub SSH keys. Optional `--limit` and `--ttl`, up to the kind maximum |
| `orkeys issued` | List active requested keys with owner, purpose, spend and expiry. Filters: `--team`, `--owner`, `--kind`. `--markdown` for a table |
| `orkeys revoke <name>...` | **Revokes keys.** Revoke requested keys before they expire: deletes the provider key, the secret fields and the GitHub secrets, and removes the state. Optional `--reason`. Asks for confirmation; `--yes` in scripts |
| `orkeys state [name]` | List the keys in the state store with source, owner, key ID, limit, expiry and rotation. Filters: `--team`, `--kind`, `--owner`, `--provider`, `--source`. With a name, print that state entry as JSON. Needs no management keys |
| `orkeys import` | Print a manifest with an `import` block for each provider key that orkeys does not manage |
| `orkeys hygiene` | **Changes keys** unless you pass `--dry-run`: revokes keys of people who left the GitHub org, lists unused keys |
| `orkeys unlock [id]` | Show the state lock, or remove it by ID after a crashed run |
| `orkeys exporter` | Serve spend, limits, expiry and rotation state as Prometheus metrics |
| `orkeys keys -p <provider> list\|create\|set-limit\|disable\|enable\|delete` | Manage keys of one provider of `policy.yaml` directly. Changes no state: `plan` shows keys made here as unmanaged |

Point orkeys at the repository with `--repo` or `ORKEYS_REPO`.

Global flags for inventory commands:

| Flag | Env | Meaning |
|---|---|---|
| `--events-file <path>` | `ORKEYS_EVENTS_FILE` | Append each event to the file as one JSON line, at the time of the event. A killed run keeps its events |
| `--events-webhook <url>` | `ORKEYS_EVENTS_WEBHOOK` | POST each event as JSON to the URL. orkeys tries 3 times. Errors do not show the URL |
| `--log-format text\|json` | | Progress logs on stderr as plain lines (default) or as JSON records |

#### Apply safety

- `apply` refuses a plan that revokes more than `max_deletes` keys (default 3), before it changes anything. A
  checkout of the wrong branch or a missing `teams/` directory shows every key as removed. Check the plan, then run
  `apply --allow-deletes <n>`. `plan` shows a warning for such a plan.
- `plan --out plan.json` saves the plan with a fingerprint of its actions. `apply --plan plan.json` makes a new plan
  under the lock and stops if it differs from the saved one, so `apply` does only what the reviewer saw.
- When the run gets SIGINT or SIGTERM, orkeys starts no new action. The cleanup of a half-done action (delete a new
  key, restore a secret, save the state) still runs, for at most 30 seconds per step.

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
| `providers[]` | | Provider accounts. A list of `{name, kind, ...}`, or a map from name to config. See [Providers](#providers) |
| `providers[].name` | | Name used in manifests (`provider:`), kinds, state and metrics. Matches `^[a-z][a-z0-9-]*$` |
| `providers[].kind` | the name | Provider type: `openrouter`, `deepinfra` or `openai` |
| `providers[].management_key` | | Secret path with field `key` (and optional `self_id`, `workspace`, `base_url`, and the provider options). Optional if `ORKEYS_<NAME>_KEY` is set |
| `providers[].workspace`, `base_url` | | OpenRouter workspace or OpenAI project ID; API base URL |
| `providers[].options` | | Settings of one provider type. `openai`: `project` (the project ID, required here or in `workspace`). Other types have none. Unknown options are errors |
| `envs` | `[prod, staging, dev]` | Allowed `env` values of manifests |
| `name_prefix` | `{env}-{team}-` | Required start of key names. `{env}` and `{team}` are replaced. `""` turns the check off |
| `naming` | `^[a-z0-9][a-z0-9._-]*$` | Regex that every key name must match |
| `kinds.<name>` | | Key kinds. At least one is required. See below |
| `clusters.<name>.context` | cluster name | kubectl context for a cluster used in `consumers` |
| `github.org` | | Org for owner checks in `hygiene` and for team members from GitHub teams |
| `github.api_url`, `github.keys_url` | `https://api.github.com`, `https://github.com` | For GitHub Enterprise |
| `track_disabled` | `false` | `true`: `plan` also enables manifest keys that are disabled at the provider but not in the manifest. See [Disabling a key](#disabling-a-key) |
| `max_deletes` | `3` | Most keys that one `apply` revokes because their manifests are gone. `apply --allow-deletes <n>` raises it for one run. See [Apply safety](#apply-safety) |

#### Providers

Each entry is one provider account. `kind` sets the provider type, and `name` is how manifests refer to it. Two
entries can have the same kind, for example two OpenRouter accounts or two workspaces of one account:

```yaml
providers:
  - name: openrouter-private
    kind: openrouter
    management_key: secret/orkeys/providers/openrouter-private
  - name: openrouter-public
    kind: openrouter
    management_key: secret/orkeys/providers/openrouter-public
  - name: deepinfra                                    # kind defaults to the name
    management_key: secret/orkeys/providers/deepinfra
  - name: openai
    management_key: secret/orkeys/providers/openai     # an Admin API key
    options:
      project: proj_abc123                             # the project that holds the keys
```

The map form still works. The map key is the name, and the kind defaults to it:

```yaml
providers:
  openrouter:
    management_key: secret/orkeys/providers/openrouter
  openrouter-public:
    kind: openrouter
    management_key: secret/orkeys/providers/openrouter-public
```

The env var override uses the name in upper case, with `-` as `_`: `openrouter-private` reads
`ORKEYS_OPENROUTER_PRIVATE_KEY` and `ORKEYS_OPENROUTER_PRIVATE_SELF_ID`.

Do not point two entries at the same keys: two management keys of one OpenRouter account without different
`workspace` values. Then each entry lists the keys of the other as unmanaged.

The state stores the provider name of each key. Do not rename a provider that has keys: `plan` then shows
"provider or kind changed". To move keys to a new name, add manifests with new key names.

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
    providers: [openrouter]
```

If you remove a kind from the policy while keys of it exist, `plan` and `rotate` keep the requested keys of that kind
and revoke them at expiry. `rotate` fails for manifest keys of that kind until the kind is back or the manifests use
another kind.

#### `teams/<team>/team.yaml`

```yaml
team: search                         # must match the directory name
channels:
  notifications: "#search-notifications"   # info events; alerts of keys of source: request kinds
  alerts: "#search-alerts"                 # alerts of keys of source: manifest kinds
github_team: search-eng             # optional; GitHub team slug if it is not the team name
# members: [alice, bob]              # optional; static GitHub logins instead of a GitHub team
```

| Event level | Key kind `source` | Channel |
|---|---|---|
| `info` | all | `channels.notifications` |
| `alert` | `request` | `channels.notifications` |
| `alert` | `manifest` | `channels.alerts` |

`kinds.<kind>.alert_channel` is deprecated. orkeys ignores it and prints a warning.

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
| `provider` | yes | A provider name from `providers`, allowed by the kind |
| `env` | yes | One of `envs` |
| `service` | no | Free text; exported as a metric label |
| `description` | yes | |
| `scopes` | no | DeepInfra and OpenAI. DeepInfra default: `[inference]`; `unrestricted` is not allowed. OpenAI default: all permissions of the service account; for example `[api.all.read]` |
| `budget.limit` | no | Limit in USD at the provider; the provider blocks the key when spend in the period reaches it. OpenAI has no per-key limit: orkeys only exports the budget as a metric. Default: the kind `budget.limit`. Up to the kind `max_limit` |
| `budget.reset` | no | Limit period: `daily`, `weekly`, `monthly` (DeepInfra: monthly only). Default: the kind `budget.reset` |
| `ttl` | no | `90d`, `2w`, `24h`. Default and maximum: the kind `ttl`. OpenAI: at most `365d` |
| `secret.path`, `secret.field` | path: yes | Where the key goes. Vault: `<mount>/<path>`; AWS: secret name. Field default `api_key`. Other fields in the same secret are kept. `secret` is one mapping or a list of mappings: orkeys writes the same key to each place |
| `consumers[]` | no | `type` (`eso`, `direct` or `github`). For `eso` and `direct`: `cluster`, `namespace`, `externalsecret` (eso), `restart` (`deployment/x`, `statefulset/x`, `daemonset/x`, `cronjob/x`). orkeys restarts only what `restart` lists. For `github`: `repo` (`owner/repo`) or `org`, `environment` (repo only), `name`, `visibility` (org only: `private` (default) or `all`). See [GitHub Actions secrets](#github-actions-secrets). A field of another type is an error |
| `import.id`, `import.name` | no | Adopt an existing provider key instead of creating one. `name` is its current name at the provider. See below |
| `disabled` | no | `true` disables the key at the provider and stops its rotation. Default `false`. Not for OpenAI keys. See below |

```yaml
secret:                             # one key in two places; each rotation updates both
  - path: kv/llm/prod/search/env
    field: api_key
  - path: kv/llm/staging/search/env
    field: OR_API_KEY
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

#### GitHub Actions secrets

Limits:

- orkeys writes a second copy of the key into GitHub. GitHub secrets are write-only, so orkeys cannot read them back
  to check them. "Synced" means "the write succeeded".
- The token can write secrets in all the repos it can access. Give it access only to the repos in the manifests.
- If you control the workflows, prefer a `direct` consumer with no `restart`. Then the workflow reads Vault or AWS
  with GitHub OIDC (`hashicorp/vault-action`, `aws-actions/configure-aws-credentials`), and no copy exists.

A `github` consumer is a GitHub Actions secret. orkeys writes the key to it:

- `apply` writes it when it creates or imports the key.
- `plan` shows `sync-consumers` when the manifest adds a `github` consumer, or when a write failed before. `apply`
  writes the new secrets and deletes the secrets of removed `github` consumers.
- `rotate` writes the new key during the consumer sync. If a write fails, the rotation fails and the old key stays
  active. Fix the cause, then run `orkeys rotate --retry <key>`.
- When you delete the manifest, `apply` deletes the GitHub secrets first, then revokes the key.

```yaml
secret:                             # one key in two places; each rotation updates both
  - path: kv/llm/prod/search/env
    field: api_key
  - path: kv/llm/staging/search/env
    field: OR_API_KEY
consumers:
  - type: github                    # repository secret
    repo: acme/search
    name: OPENROUTER_API_KEY
  - type: github                    # environment secret
    repo: acme/search
    environment: prod
    name: OPENROUTER_API_KEY
  - type: github                    # organization secret, visible to private repos
    org: acme
    name: SEARCH_OPENROUTER_KEY
```

The token is `ORKEYS_GITHUB_SECRETS_TOKEN`, or `GITHUB_TOKEN` if it is not set. It needs "Secrets: read and write"
on the repositories (fine-grained token or GitHub App), and "Organization secrets: read and write" for `org`. The
default `GITHUB_TOKEN` of a workflow cannot write secrets. A workflow that is running during a rotation keeps the old
value until its next run; `revoke_grace` covers this.

If a consumer still uses the old key after `revoke_grace`, its spend grows and orkeys does not revoke the old key
(`old_key_in_use`). So a consumer without `restart` must pick up the new secret by itself.

Add, remove or change a place in `secret` to move a key: orkeys rotates it, writes the new key to all places in the
list, and removes the fields of removed places after the revoke. Delete the manifest to revoke the key; orkeys removes
only its own fields from the secrets.

#### Disabling a key

Set `disabled: true` in the manifest to stop a key and keep its state and secret. `plan` shows `disable`, and `apply`
disables the key at the provider (`key_disabled` event). OpenRouter has a disabled flag. DeepInfra has none, so orkeys
sets the limit to 0. OpenAI has neither, so `validate` rejects `disabled: true` on OpenAI keys; delete the manifest to
revoke the key. A new or imported key with `disabled: true` is disabled right after `apply` creates or adopts it.

A disabled key does not rotate, and `rotate --now` fails for it. `plan` does not change its limit, and `hygiene` does
not report it as unused. An OpenRouter key still has its native expiry; after it expires, `plan` recreates it as a
disabled key.

Remove `disabled: true` (or set `false`) to enable the key again. This works only with `track_disabled: true` in
`policy.yaml`:

| `track_disabled` | Manifest `disabled: true`, key active | Manifest not disabled, key disabled at the provider |
|---|---|---|
| `false` (default) | `disable` | nothing; the key stays disabled |
| `true` | `disable` | `enable`, then the limit is set from `budget` (`key_enabled` event) |

With `track_disabled: true`, a key disabled by hand (`orkeys or disable`) is enabled again on the next `apply`.

`track_disabled` also controls provider keys without a manifest. With `false`, `plan` and `orkeys import` ignore such
keys when they are disabled. With `true`, `plan` reports them as unmanaged and `orkeys import` prints manifests for them.

#### Importing an existing key

A key made outside orkeys (for example in the provider dashboard) can be managed like any other key. orkeys adopts it
instead of creating a new one. orkeys never revokes or disables a key that has no manifest.

1. Run `orkeys import`. It prints a manifest for each provider key without state, with `import.id` and `import.name`
   filled in and the current limit as `budget`.
2. Write the current key value to `secret.path` (field `secret.field`, default `api_key`). With a list of places,
   write it to the first place; `apply` copies it to the others. orkeys cannot read a key value from the provider.
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

orkeys does not send notifications. Commands that change keys (`apply`, `rotate`, `request`, `revoke`, `hygiene`) emit
**events**, and you route them with the tool you already have.

- Text mode (default): each event is one line on stdout, for example
  `[alert] rotation_failed prod-search-embeddings: rotation failed: ...`. Progress logs go to stderr.
- `--events-file` and `--events-webhook` get each event at the time it happens. See the global flags under
  [Commands](#commands). Use them in CI: a run that is killed prints no JSON document.
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

The result key depends on the command: `plan` (apply), `issued` (request), `revoked` (revoke), `findings` (hygiene);
`rotate` has only `events`. `apply -o json` and `revoke -o json` need `--yes`. The exit code is 1 when `error` is set.

| Event `type` | Level | When |
|---|---|---|
| `key_issued` | info | A key was created |
| `key_imported` | info | An existing provider key was adopted |
| `key_revoked` | info | A key was revoked (manifest removed, expired, owner left, `orkeys revoke`) |
| `key_disabled` | info | A key was disabled (`disabled: true` in the manifest) |
| `key_enabled` | info | A disabled key was enabled again (`track_disabled: true`) |
| `rotation_started` | info | A new key was written; consumers sync next. Level `alert` when orkeys issued the key because an earlier run stopped before it wrote its key to the secrets |
| `rotation_synced` | info | Consumers use the new key; the old key will be revoked after `revoke_grace` |
| `old_key_revoked` | info | The old key of a rotation was revoked |
| `expiry_notice` | info | A self-service key expires within `notify_before` |
| `rotation_failed` | alert | A consumer did not sync or restart; the old key stays active |
| `old_key_in_use` | alert | The old key was used after the switch; it is not revoked |
| `consumers_synced` | info | `apply` wrote or deleted push consumer targets (GitHub Actions secrets) |
| `consumers_sync_failed` | alert | A push consumer write or delete failed; the next `apply` tries again |

`level` picks `channel`: `info` uses the team's `channels.notifications`, `alert` uses `channels.alerts`.
`run_url` is set when orkeys runs in GitHub Actions.

### Storage

Inventory mode needs two stores:

- **State** (`state`). One entry per key: provider key ID, expiry, rotation progress, keys waiting for revoke,
  owner and purpose of requested keys. Without it, orkeys cannot link a manifest to a provider key.
  - `vault://<mount>/<path>`: Vault KV v2, one secret per key.
  - `s3://<bucket>/<prefix>`: one JSON object per key, `<prefix>/<key name>.json`.
  - Each write is conditional: Vault check-and-set on the version, S3 `If-Match` on the ETag (`If-None-Match` for a
    new key). A write over a state that changed since it was read fails, and orkeys does not retry it.
  - Each state has a `schema` version. orkeys refuses to run with a state from a newer orkeys: an older build would
    drop the fields that it does not know. Upgrade all runners (CI and laptops) together.
  - `.heartbeat` holds the time of the last successful `apply`, `rotate`, `hygiene`, `request` and `revoke`. The
    exporter shows it as `orkeys_last_success_timestamp_seconds`.
  - Keep the history: turn on S3 bucket versioning, or keep `max_versions` above 1 on the Vault KV mount. It is the
    audit trail and the way back after a bad change.
- **Secrets** (`secrets`). Manifest keys, and by default the provider management keys.
  - `vault`: Vault KV v2. A path is `<mount>/<path>`; fields are secret fields. Manifest keys get custom metadata
    `orkeys-key`, `orkeys-key-id`, `orkeys-team`. Field writes and deletes use check-and-set, so a change by another
    client between the read and the write is not lost.
  - `aws`: AWS Secrets Manager. A path is the secret name; the value is a JSON object, fields are its keys.
    Manifest keys get the same tags. A secret deleted by orkeys keeps a 7-day recovery window. When orkeys writes to
    a secret in that window, it restores the secret and writes only the new field.
  - `plan` reads `orkeys-key-id` of each place. When a place holds another key than the state, `plan` shows
    `reissue`: orkeys writes a new key to all places. orkeys writes the value before the metadata, so the metadata
    always names the key in the field.

S3 and Secrets Manager together let orkeys run with no Vault.

| Works without stores | Needs the stores |
|---|---|
| `validate`, and the provider commands (`orkeys or ...`, `orkeys di ...`) | `plan`, `apply`, `rotate`, `request`, `issued`, `revoke`, `state`, `import`, `hygiene`, `unlock`, `exporter` |

Read-only commands (`plan`, `issued`, `import`) need read access to the state and to the management keys. `state`
needs read access to the state only.

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
| `ORKEYS_GITHUB_SECRETS_TOKEN` | apply, rotate | Writes GitHub Actions secrets for `github` consumers. Default: `GITHUB_TOKEN` |

AWS permissions: `s3:GetObject`, `s3:PutObject`, `s3:DeleteObject` on `<bucket>/<prefix>/*` and `s3:ListBucket` on the
bucket; `secretsmanager:GetSecretValue`, `DescribeSecret`, `CreateSecret`, `PutSecretValue`, `TagResource`, `DeleteSecret`,
`RestoreSecret` on the secrets orkeys reads and writes. The exporter needs only `s3:GetObject`, `s3:ListBucket` and
`GetSecretValue`.

### Locking

`apply`, `rotate`, `request`, `revoke` and `hygiene` (without `--dry-run`) hold a lock on the state, the same way Terraform's S3
backend does with `use_lockfile`:

- **S3:** orkeys creates `<prefix>/.lock` with a conditional write (`If-None-Match: *`). A second writer gets
  `412 Precondition Failed`. Renew and takeover use `If-Match` with the ETag. This is not S3 Object Lock, which is
  write-once retention, not a mutex.
- **Vault:** orkeys creates `<path>/.lock` with check-and-set (`cas=0` creates only if absent; `cas=N` updates only
  version N).

The lock lives 15 minutes and the holder renews it every 5 minutes. A crashed run's lock expires, and the next run
takes it over. If the lock is held, a run waits up to `--lock-wait` (default 5m), then fails with the holder, the
operation and the lock ID. `orkeys unlock` shows the lock; `orkeys unlock <id>` removes it. `apply` holds the lock
across plan, confirmation and apply.

If a holder loses its lock, it stops and writes no more state. The lease also counts as lost when renewals fail (a
store outage) until 2.5 minutes before the lock expires: another run can take an expired lock, so the holder stops
first. After a failed renewal, orkeys tries again every minute.

Limit: release reads the lock and then deletes it. Neither store has a conditional delete here, so a release that
races with a takeover of an expired lock can remove the new holder's lock.

### Rotation

1. When a manifest key is `rotate_before` (3d) from expiry, `rotate` creates a new key, saves the state with phase
   `issuing`, writes the key to all places in `secret`, and saves the state again without the phase. If a write
   fails, orkeys puts the old values back in the places it already wrote, deletes the new key and restores the state.
   If the run stops between the two state writes, the next `plan` shows `resume`, and the next `apply` or `rotate`
   finishes the change: it copies the new key from a place that has it, or, when no place has it, issues another key
   and treats the lost key as an old key.
2. For each `eso` consumer, it annotates the ExternalSecret with `force-sync` and waits until it is Ready with a newer
   `refreshTime`. Then it runs `kubectl rollout restart` and `rollout status` only for the targets in `restart`. A
   consumer with no `restart` is not restarted: use this for workloads that reload the secret by themselves (a mounted
   secret volume, or an app that reads Vault on each request). `direct` consumers (workloads that read the secrets
   store themselves) get no force-sync, only their `restart` targets. CronJobs are not restarted; the next run reads
   the new secret. `github` consumers get the new key written to their GitHub Actions secrets.
3. 15 minutes after the switch, a `rotate` run records the old key's usage (total usage on OpenRouter, spend this
   month on DeepInfra and OpenAI). OpenAI reports spend with a delay, so use a `revoke_grace` of at least 24h for
   OpenAI keys. After `revoke_grace` (24h), `rotate` compares it again. If the usage grew, a consumer still has
   the old key: orkeys does not revoke it and marks the rotation as failed. For OpenAI keys a last use after the
   switch also counts, because OpenAI reports it without delay. If the spend went down (a new month started),
   orkeys records a new baseline and decides on the next run. If no run recorded the usage in between, the run at
   revoke time records it and the next run revokes. Run `rotate` hourly.
4. A failed rotation sends an alert and stops. Fix the cause, then run `orkeys rotate --retry <key>`. The old key stays
   active until then.

OpenRouter and OpenAI keys also get a native expiry at `ttl`. DeepInfra keys have none; orkeys revokes them itself.

### Requested keys

Keys of a kind with `source: request` have no manifest. A team member requests one with a purpose. orkeys checks the
kind limits per person and keeps owner, team and purpose in the state. Alerts go to the team
`channels.notifications`. At expiry, `rotate` revokes the key and deletes its state; there is no renewal. If the kind has
`notify_before`, orkeys emits `expiry_notice` that long before expiry.

To revoke a requested key before expiry, run `orkeys revoke <name>`. Get the name from `orkeys issued`. Do not use
`orkeys or delete` for this: it deletes the provider key and keeps the state, and the key still counts against
`max_active` and `max_total_limit` until it expires. If the provider key is already deleted, `orkeys revoke` removes the
state. `orkeys revoke` refuses manifest keys: delete the manifest or set `disabled: true`.

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
rule compares `orkeys_key_spend_usd` with `orkeys_key_budget_usd`, for example `spend / budget > 0.8`. For OpenAI keys
this rule is the only control of spend: OpenAI does not stop a key at its budget.

| Metric | Meaning |
|---|---|
| `orkeys_key_spend_usd` | Spend in the current limit period (`reset`: today, this week or this month) |
| `orkeys_key_budget_usd` | `budget.limit` from the manifest for the same period |
| `orkeys_key_hard_limit_usd` | Limit at the provider for the same period. Not for providers without a per-key limit (OpenAI) |
| `orkeys_key_expires_timestamp_seconds` | Expiry |
| `orkeys_key_last_used_timestamp_seconds` | Last use (OpenRouter and OpenAI). OpenRouter reports the request time for recently used keys, so treat it as "recently used", not as an exact time |
| `orkeys_key_rotation_state` | 0 idle, 1 consumers not synced, 2 old key waits for revoke, 4 failed |
| `orkeys_key_missing` | 1 if the key is not found at the provider |
| `orkeys_unmanaged_keys{provider}` | Provider keys without state |
| `orkeys_provider_poll_success{provider}` | 1 if the last poll worked |
| `orkeys_provider_last_poll_timestamp_seconds{provider}` | Last successful poll |
| `orkeys_state_last_load_timestamp_seconds` | Last successful state load |
| `orkeys_last_success_timestamp_seconds{operation}` | Last successful `apply`, `rotate`, `hygiene`, `request`, `revoke`. Alert when `rotate` is older than 2h: without it, keys with a native expiry expire at the provider |

The exporter runs all the time with the management keys. Give it its own management keys where the provider allows
it, and keep them in a secret that only the exporter reads.

### Containers

The `Dockerfile` builds a distroless image with only the `orkeys` binary. It is enough for the exporter and for every
command except `rotate` with consumers, which runs `kubectl`. For `rotate`, run orkeys where `kubectl` and the cluster
contexts are available, for example in CI.

## Development

```sh
make            # build ./orkeys
make check      # gofmt check, go vet, tests (GOTEST_FLAGS=-race for the race detector)
make lint       # golangci-lint
make vuln       # govulncheck
make release    # cross-compile into dist/
make snapshot   # local GoReleaser build, no publish
make help       # all targets
```

Layout:

| Path | Contents |
|---|---|
| `internal/openrouter` | HTTP client for the OpenRouter Management API |
| `internal/deepinfra` | HTTP client for DeepInfra API tokens and key limits |
| `internal/openai` | HTTP client for the OpenAI Admin API: project keys, service accounts, costs |
| `internal/httpx` | JSON over HTTP with retries for GET |
| `internal/nullable` | request fields that are absent, null or set |
| `internal/provider` | common interface, optional `Limiter` and `Disabler`, and registry of the providers |
| `internal/provider/providertest` | contract test that every provider adapter passes |
| `internal/manifest` | inventory loading and validation; consumer types in `consumer.go` |
| `internal/state`, `internal/secrets` | state and secret store interfaces |
| `internal/vault`, `internal/s3store`, `internal/awssm`, `internal/backend` | Vault KV v2, S3, Secrets Manager, and the factory that picks them |
| `internal/engine` | plan, apply, rotate, request, issued, revoke, state listing, hygiene, import; consumer drivers in `consumers.go`, recovery in `recover.go` |
| `internal/exporter` | Prometheus metrics |
| `internal/kube`, `internal/deliver`, `internal/github` | kubectl, age delivery, org membership, Actions secrets |
| `internal/cli` | cobra commands: `openrouter.go`, `deepinfra.go`, `openai.go`, `keys.go`, `inventory.go`, `exporter_cmd.go`; wiring in `wire.go`, output in `render.go`, event webhook in `webhook.go` |
| `examples/inventory` | example inventory repository with rotate and hygiene workflows; a test keeps it valid |
| `internal/values` | input parsing and formatting |

### Adding a provider

1. Add an HTTP client package (`internal/<name>`) on `internal/httpx`.
2. Add an adapter in `internal/provider` that implements `Provider`. Implement `Limiter` only if the provider has a
   per-key limit, and `Disabler` only if it can disable keys. Set `Caps` to match: `New` refuses a mismatch.
3. Add an `Info` to `registry`: `Options` for settings beyond `base_url` and `workspace`, `CheckScopes` for scopes
   that orkeys must not request.
4. Add a fake of the API that keeps keys in memory, and run `providertest.Run` against the adapter (see
   `internal/provider/conformance_test.go`).
5. Add the provider to the support tables of this README. A provider command (`orkeys <name> ...`) is optional:
   `orkeys keys -p <name>` works without one.

### Adding a consumer type

1. Add a spec type in `internal/manifest/consumer.go` with `yaml` tags and a `Validate` method, and add it to
   `consumerTypes`. A spec that orkeys writes the key to implements `PushSpec` (`TargetID`); a spec that reads the
   secrets store implements `NotifySpec` (`Workloads`).
2. Add a driver in `internal/engine` that implements `Pusher` or `Notifier`, and register it in
   `internal/cli/wire.go`. Push targets are recorded in the state under `<type>/<target ID>`, so orkeys can delete
   them after the manifest is gone.
3. Add a row to the key manifest table of this README.

### Releasing

1. Bump `VERSION` with `make bump-patch`, `make bump-minor` or `make bump-major`.
2. Add a `## vX.Y.Z (YYYY-MM-DD)` section to `CHANGELOG.md` with the changes and their commits.
3. Merge to `main`. The Release workflow tags the commit with the content of `VERSION`, builds the binaries and
   creates the GitHub release.
