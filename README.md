# orkeys

[![Release](https://img.shields.io/github/v/release/hatedabamboo/orkeys)](https://github.com/hatedabamboo/orkeys/releases)
[![Go Reference](https://pkg.go.dev/badge/github.com/hatedabamboo/orkeys.svg)](https://pkg.go.dev/github.com/hatedabamboo/orkeys)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

`orkeys` (pronounced "ORKies") is an IaC for your LLM API keys. `orkeys` manages LLM API keys. It has two modes:

- **Provider commands** manage keys at one provider directly: `orkeys openrouter` (`or`), `orkeys deepinfra` (`di`),
  `orkeys openai` (`oa`), `orkeys fal` and `orkeys elevenlabs` (`el`).
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
[OpenAI](#openai)). The fal.ai API cannot create keys, and fal.ai has no per-key limit and no disabled flag: a person
makes each fal.ai key, and orkeys delivers, tracks and revokes it (see [fal.ai](#falai)). ElevenLabs has full support
for the keys of one service account. Its limits are in credits: orkeys converts them with a price per credit, and the
limit period is the billing period of the workspace (see [ElevenLabs](#elevenlabs)).

### Support in provider commands

| Feature | OpenRouter (`or`) | DeepInfra (`di`) | OpenAI (`oa`) | fal.ai (`fal`) | ElevenLabs (`el`) |
|---|---|---|---|---|---|
| List keys (`list`) | yes | yes. No scopes and no creation time | yes. Keys of users and service accounts | yes | yes. Keys of one service account. `service-accounts` lists the accounts |
| Show one key (`get`) | yes | yes | yes | yes | yes |
| Create a key (`create`) | yes | no | yes. One service account per key | no. fal.ai refuses it with `403` | yes |
| Change the limit (`update --limit`) | yes | no | no per-key limit | no per-key limit | yes (`update --credit-limit`). In credits |
| Limit period (`--limit-reset`) | `daily`, `weekly`, `monthly` | `monthly` only | no per-key limit | no per-key limit | `monthly` only. The billing period |
| Expiry (`--expires`) | yes | no | yes. At most 365d | yes. At most 365d | yes. 15m to 90d |
| Rename (`update --name`) | yes | no | no | no | yes |
| Change scopes (`update --scopes`) | no scopes | no | yes | no. Preset or permissions only at create | yes |
| List possible scopes (`scopes`) | no scopes | yes | no | no | no |
| Disable a key (`disable`) | yes | no | no | no | yes |
| Enable a key (`enable`) | yes | no command | no | no | yes. Not a key that ElevenLabs found in public |
| Delete a key (`delete`) | yes | no | yes | yes. A revoke cannot be undone | yes |
| Workspaces (`--workspace`) | yes | no | one project per call (`--project`), required | the workspace of the key | one service account per call (`--service-account`), required |
| Spend shown | day, week, month, total | month | day, week, month | day, week, month. An estimate | credits used in the billing period. USD with `ELEVENLABS_USD_PER_CREDIT` |
| Last use shown | yes | no | yes | no | no |

### Support in inventory mode

| Feature | OpenRouter | DeepInfra | OpenAI | fal.ai | ElevenLabs |
|---|---|---|---|---|---|
| `validate`, `state` | yes | yes | yes | yes | yes |
| `plan` | yes | yes | yes | yes | yes |
| `apply`: create a key | yes | no | yes | no. A person makes the key and writes it with `orkeys put` | yes |
| `apply`: set the limit | yes | no | no. The budget is only a metric | no. The budget is only a metric | yes. In credits: `budget.limit` / `usd_per_credit`, rounded down |
| `apply`: `disabled: true` | yes. Sets the disabled flag | no | no. `validate` rejects it | no. `validate` rejects it | yes. Disables the key |
| `apply`: revoke a deleted manifest | yes | no | yes | yes | yes |
| `budget.reset` | `daily`, `weekly`, `monthly` | `monthly` only | `daily`, `weekly`, `monthly`. Only a metric | `daily`, `weekly`, `monthly`. Only a metric | `monthly` only. The billing period |
| `scopes` field | no | yes. Used only at create | yes. Used only at create | no. A person sets the permissions at fal.ai | yes. Used only at create |
| `rotate` | yes. The key also has a native expiry | no | yes. The key also has a native expiry. `ttl` at most 365d | by hand: reminders, then `orkeys put`. `rotate` syncs the consumers and revokes the old key | yes. The key also has a native expiry. `ttl` at most 90d |
| `request`, `revoke` | yes | no | yes | no | yes |
| `import`: print manifests | yes | yes | yes | yes | yes |
| `apply`: adopt an `import` key | yes | no | yes | no. Write the key to `secret`; orkeys reads the key ID from the value | yes |
| `apply`: track a key (`track_only: true`) | yes | yes | yes | yes | yes |
| `hygiene`: revoke keys of people who left | yes | no | yes | no. No requested keys | yes |
| `hygiene`: find unused keys | yes | no. DeepInfra has no last use | yes | no. fal.ai has no last use | no. ElevenLabs has no last use |
| `exporter`: spend | yes | yes. Spend this month only | yes | yes. An estimate | yes. Billing period only, converted from credits |
| `exporter`: hard limit | yes | yes | no. Budget only | no. Budget only | yes |
| `exporter`: last use | yes | no | yes | no | no |
| Old key "still in use" check at revoke | total usage | spend this month | last use (exact), spend this month | spend this month (an estimate) | spend in the billing period |

A provider of type `void` has no API. orkeys tracks the expiry of its keys and delivers the values that a person
enters. See [Keys without an API](#keys-without-an-api).

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

### fal.ai

The fal.ai API cannot create keys. `POST /v1/keys` needs the permission `auth:keys:create`. No key has it, also a key
with the `FULL` preset, and the dashboard cannot grant it. So `orkeys fal create` fails with `403`, and a person makes
each key at [fal.ai/dashboard/keys](https://fal.ai/dashboard/keys). In inventory mode, orkeys does the rest (see
[fal.ai keys in inventory mode](#falai-keys-in-inventory-mode)).

`orkeys fal` uses the [Platform API](https://fal.ai/docs/api-reference/platform-apis) to list and revoke the API keys
of one fal.ai workspace. The management key needs the permissions `auth:keys:read`, `auth:keys:write` (revoke),
`billing:usage:read` and `account:users:read` (spend). A custom key with "Keys: Read, Write" and "Billing: Usage" has
them. A key with the `FULL` preset also works.

| Variable | Required | Purpose |
|---|---|---|
| `FAL_ADMIN_KEY` | yes | Management key, as `key_id:key_secret` |
| `FAL_API_BASE` | no | API base URL. Default: `https://api.fal.ai/v1` |

```sh
orkeys fal list                         # keys with spend this month
orkeys fal list -o wide                 # with day and week spend, creation time, description
orkeys fal get ci
orkeys fal delete ci -y                 # revoke
```

Limits of the fal.ai Platform API:

- A revoke cannot be undone. fal.ai has no disabled flag, so `delete` (revoke) is the only way to stop a key.
- fal.ai has no per-key spend limit. In inventory mode, orkeys keeps `budget` only as a metric
  (`orkeys_key_budget_usd`).
- A key cannot be renamed, and its preset, permissions and expiry cannot change after create.
- The secret of a new key is `key_id:key_secret`. Clients send it as `Authorization: Key <secret>`.
- `scopes` (`--scopes`) is one preset (`API`, `AGENT`, `DEPLOY`, `READONLY`, `BILLING`, `COMPUTE`, `SERVERLESS`) or a
  list of permissions such as `models:requests:submit`. The default is `API`. orkeys refuses `FULL` and
  the `auth:keys:*` permissions except `auth:keys:read`, because a key with them can create or revoke keys.
- The expiry is at most 365 days. The fal.ai dashboard allows 1 hour to 1 year. The API docs give no limit.
- Spend comes from the usage endpoint (`/models/usage`), in 1-day UTC buckets. It is the fal.ai estimate. A week starts
  on Monday. Usage of a dashboard login is not in the spend of any key.
- orkeys finds the ID of the management key from its value (`key_id:key_secret`), so `self_id` is not necessary.
  `delete` refuses to revoke the key in `FAL_ADMIN_KEY`.

#### fal.ai keys in inventory mode

fal.ai keys use the flow of [Keys without an API](#keys-without-an-api): a person makes the key, writes it to the
secret or runs `orkeys put`, and orkeys syncs the consumers and sends the rotation reminders. The fal.ai API adds
these steps:

- The value is `key_id:key_secret`. orkeys reads the key ID from it, so the state holds the fal.ai `key_id`. `put` and
  `plan` refuse a value that is not a fal.ai key, or a key that fal.ai does not list.
- The exporter shows the spend and the budget of the key. `budget` is allowed, as a metric only.
- After a new key reaches the consumers, `rotate` revokes the old key at fal.ai after `revoke_grace`. It uses the
  usage check of [Rotation](#rotation): spend on the old key after the switch stops the revoke. There is no
  `revoke_by_hand` event.
- When the manifest is deleted, `apply` revokes the key at fal.ai.
- `import` and `scopes` are not allowed. Set the permissions and the expiry when you make the key. `issued` is
  allowed.
- `track_only: true` with `import` works as for other providers: orkeys exports the spend and does not change the key.

```yaml
name: prod-media-images
kind: production
provider: fal
env: prod
description: Image generation for the media service.
issued: 2026-11-18                            # date of the key at fal.ai; change it at each rotation
budget: {limit: 300}                          # a metric only; fal.ai does not stop the key
secret:
  path: secret/llm/prod/media/images
consumers:
  - type: eso
    cluster: prod-eu
    namespace: media
    externalsecret: media-llm
    restart: [deployment/media-api]
```

### ElevenLabs

Limits first:

- Service accounts are only for multi-seat workspaces, and only a workspace admin can manage them.
- A new service account has no access to the resources of the workspace (voices, projects, agents). Add it to a
  group, or share the resources with it. Its keys can then use them.
- To create, change and delete keys, orkeys needs two permissions. Without them, a key can only list the service
  accounts and their keys.
  - The management key needs the permission `service_account_write`. Without it, ElevenLabs answers `401` with
    `missing_permissions`.
  - The owner of the management key needs the group permission `service_accounts_manage`. For a key of a service
    account, add that service account to a workspace group with this permission. Without it, ElevenLabs answers `403`
    with `missing_group_permissions`.

orkeys manages the API keys of one service account through the
[service account API](https://elevenlabs.io/docs/api-reference/service-accounts/api-keys/create). The keys of a
service account call the models like personal keys. Their permissions set what they can do.

`orkeys elevenlabs` (alias `el`) manages the keys of one service account directly. It shows limits and usage in
credits. With `ELEVENLABS_USD_PER_CREDIT`, it also shows them in USD.

| Variable | Required | Purpose |
|---|---|---|
| `ELEVENLABS_ADMIN_KEY` | yes | Management key |
| `ELEVENLABS_SERVICE_ACCOUNT` | yes, except for `service-accounts` | `service_account_user_id`. Same as `--service-account` (`-s`) |
| `ELEVENLABS_API_BASE` | no | API base URL. Default: `https://api.elevenlabs.io` |
| `ELEVENLABS_USD_PER_CREDIT` | no | Price of one credit. Adds spend and limit in USD to the output |

```sh
orkeys el service-accounts              # find the service_account_user_id
orkeys el list                          # keys with credits used and the credit limit
orkeys el list -o wide                  # with hint, creation time and permissions
orkeys el get ci                        # also the disable reason and allowed IPs
orkeys el create --name ci --expires 30d --credit-limit 100000
orkeys el create --name stt --scopes speech_to_text
KEY=$(orkeys el create --name tmp --expires 1h -q)   # print only the secret
orkeys el update ci --credit-limit none --expires 60d
orkeys el update ci --name ci-old --scopes text_to_speech
orkeys el disable ci
orkeys el enable ci
orkeys el delete ci -y
```

Commands that take `<key>` accept a key ID or an exact key name. In inventory mode, the same keys are available
with `orkeys keys -p <provider>`.

Set up:

1. Create a service account (ElevenLabs dashboard, or `POST /v1/service-accounts`). Give it access to the resources
   that the keys need. Copy its `service_account_user_id`.
2. Make a management key with the permission `service_account_write`. Its owner needs the group permission
   `service_accounts_manage`. Write the key to the `management_key` secret, or set `ORKEYS_<NAME>_KEY`.
3. Add the provider to `policy.yaml`:

```yaml
providers:
  - name: elevenlabs
    kind: elevenlabs
    management_key: secret/orkeys/providers/elevenlabs
    options:
      service_account: sa_abc123                         # the service account that holds the keys
      usd_per_credit: "0.00018"                          # price of one credit in your plan
```

| Option | Required | Purpose |
|---|---|---|
| `service_account` | yes | `service_account_user_id` of the service account. `workspace` also works |
| `usd_per_credit` | yes | Price of one credit in USD. orkeys uses it to convert `budget.limit` to credits, and the credits used to spend. Example: 500,000 credits for $99 is `0.000198` |

Limits of the ElevenLabs API:

- The key limit (`character_limit`) is in credits. orkeys sets `budget.limit / usd_per_credit` credits, rounded
  down. The spend that orkeys shows is credits used × `usd_per_credit`. It is correct only when the price matches
  your plan. Credits of a usage-based overage can cost more.
- The API docs also call the limit "characters". It counts credits: the same text costs a different number of credits
  with each model.
- The credits used (`character_count`) change about one minute after a request. A request over the limit fails with
  `401` and `quota_exceeded`. A client can take this for a bad key.
- The limit and the spend reset at the start of the billing period of the workspace, not on the 1st of the month. So
  `budget.reset` is `monthly` only. Spend per day and per week is not available.
- ElevenLabs has no last use, so `hygiene` cannot find unused keys.
- The expiry is 15 minutes to 90 days from now. A workspace can refuse keys that never expire. Then set `ttl` on
  every key.
- In inventory mode, orkeys does not rename a key or change its permissions after create. `orkeys el update` can.
- `scopes` is a list of permissions, for example `[text_to_speech, voices_read]`. The default is
  `[text_to_speech, speech_to_speech, speech_to_text, voices_read, models_read]`. orkeys refuses `all`,
  `service_account_write`, `create_user_api_key`, `group_members_manage`, `workspace_members_invite`,
  `workspace_members_remove` and `workspace_write`, because a key with them can make keys or add members.
- orkeys finds the management key in the list from the hash of its value (`hashed_xi_api_key` is its SHA-256), so
  `self_id` is not necessary. The management key does not show as unmanaged, and `orkeys el disable` and `delete`
  refuse it.
- ElevenLabs disables a key that it finds in public (`disable_reason: exposed_publicly`). orkeys refuses to enable
  such a key, also with `track_disabled: true`: `apply` and `orkeys el enable` fail for it. Rotate the key (`orkeys rotate --now <name>`).

Run `orkeys <provider> <command> --help` to see all flags.

### Output

`-o table` (default), `-o wide` or `-o json`. `--json` is a shorthand for `-o json`.
JSON output holds the full API objects. `create --json` includes the secret in `key`.

### Value formats

| Flag | Format |
|---|---|
| `--limit` | USD amount, or `none` to remove the limit |
| `--limit-reset` | OpenRouter only: `daily`, `weekly`, `monthly` or `none`. Resets happen at midnight UTC |
| `--expires` | OpenRouter, OpenAI, fal.ai and ElevenLabs: duration (`30d`, `2w`, `12h`), date (`2027-12-31`, midnight UTC) or RFC 3339 |

### Behavior

- `delete` asks for confirmation. Without a terminal it refuses to run unless you pass `--yes`.
- `delete`, `enable` and `disable` take more than one key. `delete` finds all the keys before it deletes any.
- Writes (create, update, delete) retry only on HTTP 429. A timeout or a 5xx on a write can come after the provider
  did the work, so orkeys does not send the write again.
- Reads (GET) retry on network errors, HTTP 408, 429 and 5xx. Each request has up to 5 tries.
- The wait before a retry comes from `Retry-After`, `x-ratelimit-reset-requests` (OpenAI) or `X-RateLimit-Reset`
  (OpenRouter). Without these headers, the wait starts at 5 seconds for 429 and 1 second for other errors, and
  doubles at each retry. One wait is at most 60 seconds. Each retry writes a `WARN` log record to stderr.
- Exit code is `0` on success and `1` on any error.

### Debug logs

Debug logs show URLs, secret paths, key names and key IDs. Keep them out of public CI logs if these names are
private.

`--debug` (or `ORKEYS_DEBUG=true`) writes debug logs to stderr. It works with all commands. `--log-format json` writes
them as JSON records. The flag has priority over the env.

Debug logs show:

- each HTTP call to a provider, Vault and GitHub: method, URL without the query, status, attempt, time, retries;
- each AWS call: service, operation, time, error;
- each `kubectl` call: arguments, exit code, time; each poll of an ExternalSecret;
- the state lock: acquire, wait, renew, release;
- the inventory files that orkeys reads, and the state and secrets backends;
- the steps of `plan`, `apply`, `rotate`, `request` and `hygiene`: the data of each key, the decision, the secret
  writes, the consumer syncs, the revoke checks;
- the exporter polls and the scrapes.

Debug logs do not show key values, management keys, tokens, request and response bodies, URL queries or the
`--events-webhook` URL.

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
| `orkeys put <key>` | Write a new value of a `void` key from stdin. `--issued <date>` sets the creation date. The next `rotate` syncs the consumers. See [Keys without an API](#keys-without-an-api) |
| `orkeys request --user <login> --kind <kind> --provider <name> --purpose <text>` | Issue a key of a kind with `source: request`, encrypted to the user's GitHub SSH keys. Optional `--limit` and `--ttl`, up to the kind maximum |
| `orkeys issued` | List active requested keys with owner, purpose, spend and expiry. Filters: `--team`, `--owner`, `--kind`. `--markdown` for a table |
| `orkeys revoke <name>...` | **Revokes keys.** Revoke requested keys before they expire: deletes the provider key, the secret fields and the GitHub secrets, and removes the state. Optional `--reason`. Asks for confirmation; `--yes` in scripts |
| `orkeys state [name]` | List the keys in the state store with source, owner, key ID, limit, expiry and rotation. Filters: `--team`, `--kind`, `--owner`, `--provider`, `--source`. With a name, print that state entry as JSON. Needs no management keys |
| `orkeys import` | Print a manifest with an `import` block for each provider key that orkeys does not manage. `--track-only`: print manifests with `track_only: true` and no `secret` |
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
| `--log-format text\|json` | | Progress logs on stderr as plain lines (default) or as JSON records. Debug logs use the same format |
| `--debug` | `ORKEYS_DEBUG` | Write debug logs to stderr. See [Debug logs](#debug-logs) |

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
| `providers[].kind` | the name | Provider type: `openrouter`, `deepinfra`, `openai`, `fal`, `elevenlabs` or `void` (no API, see [Keys without an API](#keys-without-an-api)) |
| `providers[].management_key` | | Secret path with field `key` (and optional `self_id`, `workspace`, `base_url`, and the provider options). Optional if `ORKEYS_<NAME>_KEY` is set |
| `providers[].workspace`, `base_url` | | OpenRouter workspace, OpenAI project ID or ElevenLabs service account; API base URL |
| `providers[].options` | | Settings of the provider. All types: `report_unmanaged`, `ignore_disabled`, `ignore_unmanaged` (see [Keys without a manifest](#keys-without-a-manifest)) and `track_only` (see [Tracking a key](#tracking-a-key)). `openai`: `project` (the project ID, required here or in `workspace`). `elevenlabs`: `service_account` (required here or in `workspace`) and `usd_per_credit` (required), see [ElevenLabs](#elevenlabs). `void`: `console` (the vendor page for keys; events show it). Unknown options are errors |
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
  - name: fal
    management_key: secret/orkeys/providers/fal        # lists, reads spend and revokes; cannot create
  - name: elevenlabs
    management_key: secret/orkeys/providers/elevenlabs
    options:
      service_account: sa_abc123                       # the service account that holds the keys
      usd_per_credit: "0.00018"                        # converts budgets and spend to and from credits
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
| `scopes` | no | DeepInfra, OpenAI, fal.ai and ElevenLabs. DeepInfra default: `[inference]`; `unrestricted` is not allowed. OpenAI default: all permissions of the service account; for example `[api.all.read]`. fal.ai: one preset or a list of permissions, default `[API]`; `FULL` and `auth:keys:*` other than `auth:keys:read` are not allowed. ElevenLabs: a list of permissions, default `[text_to_speech, speech_to_speech, speech_to_text, voices_read, models_read]`; `all` and the permissions that manage keys, members or the workspace are not allowed |
| `budget.limit` | no | Limit in USD at the provider; the provider blocks the key when spend in the period reaches it. OpenAI and fal.ai have no per-key limit: orkeys only exports the budget as a metric. ElevenLabs: converted to credits with `usd_per_credit`. Default: the kind `budget.limit`. Up to the kind `max_limit`. Not allowed for `void` providers |
| `budget.reset` | no | Limit period: `daily`, `weekly`, `monthly` (DeepInfra: monthly only). Default: the kind `budget.reset` |
| `ttl` | no | `90d`, `2w`, `24h`. Default and maximum: the kind `ttl`. OpenAI and fal.ai: at most `365d`. ElevenLabs: at most `90d` |
| `secret.path`, `secret.field` | path: yes, except with `track_only: true` or a `void` provider | Where the key goes. Vault: `<mount>/<path>`; AWS: secret name. Field default `api_key`. Other fields in the same secret are kept. `secret` is one mapping or a list of mappings: orkeys writes the same key to each place. Two keys can share a path, not a path and field |
| `consumers[]` | no | `type` (`eso`, `direct` or `github`). For `eso` and `direct`: `cluster`, `namespace`, `externalsecret` (eso), `restart` (`deployment/x`, `statefulset/x`, `daemonset/x`, `cronjob/x`). orkeys restarts only what `restart` lists. For `github`: `repo` (`owner/repo`) or `org`, `environment` (repo only), `name`, `visibility` (org only: `private` (default) or `all`), `workflows` (repo only: a list of `{workflow, ref, inputs}` to run after each write). See [GitHub Actions secrets](#github-actions-secrets). A field of another type is an error |
| `import.id`, `import.name` | no | Adopt an existing provider key instead of creating one. `name` is its current name at the provider. See below |
| `disabled` | no | `true` disables the key at the provider and stops its rotation. Default `false`. Not for OpenAI and fal.ai keys. See below |
| `issued` | no | `void` keys only. Date of the value in `secret` (`YYYY-MM-DD`). The key expires at `issued` plus `ttl`. A change tells `apply` that the secret holds a new value. See [Keys without an API](#keys-without-an-api) |
| `track_only` | no | `true`: orkeys saves the state of the `import` key and exports its metrics, and does not change the key. Default: the provider option `track_only`. `false` on a key of a provider with `track_only: true` makes a managed key. See [Tracking a key](#tracking-a-key) |

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
- With `workflows`, orkeys starts a `workflow_dispatch` run of each listed workflow in `repo` after each write of
  the secret. See [Workflow runs](#workflow-runs).

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

##### Workflow runs

Limits:

- `workflows` is for `repo` consumers only. Organization secrets have no repository to run a workflow in.
- The workflow must have a `workflow_dispatch` trigger. GitHub rejects `inputs` that the trigger does not declare.
- orkeys starts the run and does not wait for it. A failed run does not fail the rotation. Watch the run in GitHub.
- orkeys does not start runs when you add `workflows` to a consumer that already holds the current key. The first
  runs come with the next write: a rotation, or a change of `secret`.
- If one start fails, the retry starts all workflows of the consumer again, also the ones that started.
- Two `github` consumers with the same workflow start two runs.
- One consumer can list the same `workflow` more than once with another `ref` or other `inputs`. Each entry starts
  one run. Two entries with the same `workflow`, `ref` and `inputs` are an error. An error of a start names its
  entry: `workflows[1]`.

Use `workflows` when a deploy workflow reads the secret and the service gets the key only from a deploy. After each
write of the secret (create, import, rotation, reissue), orkeys starts a run of each workflow in the list:

```yaml
consumers:
  - type: github
    repo: acme/search
    name: OPENROUTER_API_KEY
    workflows:
      - workflow: deploy.yml            # file in .github/workflows, or a workflow ID
        ref: main                       # branch or tag to run on; default: the default branch
        inputs: {environment: prod}     # workflow_dispatch inputs; optional
      - workflow: deploy.yml            # same workflow again: a second run with other inputs
        ref: main
        inputs: {environment: canary}
      - workflow: smoke-test.yml        # runs on the default branch with no inputs
```

If a start fails, the write counts as failed. At `rotate`, the rotation fails and the old key stays active. Fix the
cause, then run `orkeys rotate --retry <key>`: orkeys writes the secret again and starts the workflows again. At
`apply`, the next `plan` shows `sync-consumers`.

The token needs "Actions: read and write" on the repository, in addition to "Secrets: read and write".

#### Disabling a key

Set `disabled: true` in the manifest to stop a key and keep its state and secret. `plan` shows `disable`, and `apply`
disables the key at the provider (`key_disabled` event). OpenRouter and ElevenLabs have a disabled flag. DeepInfra has none, so orkeys
sets the limit to 0. OpenAI and fal.ai have neither, so `validate` rejects `disabled: true` on their keys; delete the
manifest to revoke the key. A new or imported key with `disabled: true` is disabled right after `apply` creates or adopts it.

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

The state saves the manifest value of `disabled`. The exporter uses it in `orkeys_key_disabled` to tell a planned
disable from a disable outside orkeys. A state from orkeys before v0.4.3 has no such field: the first `plan` shows
`update-state` with `disabled false -> true` for each disabled key. With `track_disabled: false`, the removal of
`disabled: true` also gives `update-state`: the key stays disabled, and `orkeys_key_disabled` shows `2`.

`track_disabled` is also the default for provider keys without a manifest. With `false`, `plan`, `orkeys import` and
the exporter ignore such keys when they are disabled. With `true`, they report them. The provider option
`ignore_disabled` replaces this default for one provider. See [Keys without a manifest](#keys-without-a-manifest).

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

#### Tracking a key

Limits:

- orkeys does not change a tracked key. It does not set its limit, write it to the secrets store, sync consumers,
  rotate it, disable it or revoke it. The provider does not stop the key at `budget.limit`.
- `budget.reset` must be `monthly`. DeepInfra reports spend only for the current month.
- `validate` rejects `secret`, `consumers` and `disabled: true` on a tracked key. `import` is required.

Use `track_only: true` for a key that orkeys cannot or must not manage, for example a DeepInfra key or a key that
another tool rotates. orkeys keeps a state entry with the key ID, team, env, service, channels and budget. The
exporter then shows the key metrics with the team labels, so the spend and budget alert rules work for it. It shows
`orkeys_key_track_only 1` for the key. Alert rules use this series to tell a tracked key from a managed key.

1. Run `orkeys import --track-only`. It prints a manifest with `track_only: true` and no `secret` for each provider
   key without state.
2. Save the manifest as `teams/<team>/<name>.yaml`, fill in the `TODO` fields and open a PR.

```yaml
name: prod-search-legacy
kind: production
provider: deepinfra
env: prod
service: legacy
description: Legacy reranker, key made in the DeepInfra dashboard.
import:
  id: 3f2a9c1b7e04d5a6...
  name: reranker            # current name at the provider
track_only: true            # orkeys exports the spend and does not change the key
budget: {limit: 300}        # alert budget per month; not set at the provider
```

To track all keys of a provider, set `track_only: true` in its options. A key of that provider with
`track_only: false` is a managed key. `orkeys request` refuses a provider with `track_only: true`.

```yaml
providers:
  deepinfra:
    management_key: secret/orkeys/providers/deepinfra
    options:
      track_only: true                       # manifests of DeepInfra keys only track them
```

When you set `track_only: true` on a provider that has managed keys, `validate` fails for their manifests: they have
`secret` and no `import`. Set `track_only: false` in each of them. orkeys does not convert a managed key to a tracked
key.

| Change | Effect at `apply` |
|---|---|
| New manifest | `track`: saves the state. Reads no secret and changes nothing at the provider (`key_tracked` event) |
| Change `budget`, `service`, `env` or the team channels | `update-state`: changes only the state |
| Change `import.id` | `track`: the state gets the new key ID. The old key stays at the provider |
| Delete the manifest | `untrack`: removes the state entry. The key stays at the provider (`key_untracked` event). Does not count against `max_deletes` |
| Remove `track_only` | Nothing. `plan` shows a warning. To manage the key, delete the manifest and apply, write the key to `secret.path`, then add the manifest with `import` and without `track_only` |
| Add `track_only: true` to a managed key | Nothing. `plan` shows a warning. Add a key with a new name instead |

`rotate` and `hygiene` skip tracked keys, and `orkeys revoke` refuses them. `orkeys state` shows `tracked` in the
`ROTATION` column. The exporter shows `orkeys_key_hard_limit_usd` from the limit that the provider reports, and no
`orkeys_key_expires_timestamp_seconds` when the provider key has no expiry.

#### Keys without an API

Limits:

- orkeys cannot make, rotate or revoke the key at the vendor. A person does it. orkeys tells the team when.
- orkeys has no spend data. `budget`, `import` and `track_only` are not allowed.
- The key ID is a hash of the key name and the value (`fp-...`). The hash goes to the state and to the secret
  metadata. It is safe for random API keys. It does not protect a short password.
- `plan` reads the secret values of `void` and fal.ai keys. The `plan` job needs read access to them.
- orkeys does not see a new value in the secret by itself. Change `issued` in the manifest, or run `orkeys put`.
- `put` needs write access to the state store and the secrets store. For a fal.ai key, it also needs the fal.ai
  management key.
- The consumers sync in the next `rotate` run, with the credentials of that run.

fal.ai keys use this flow too, because the fal.ai API cannot create keys. orkeys revokes their old keys and shows
their spend. See [fal.ai keys in inventory mode](#falai-keys-in-inventory-mode).

Use a provider of type `void` for a vendor without a key API. orkeys tracks the expiry of the key, writes the value
to `secret`, syncs the consumers and sends reminders. A key without `secret` is only an expiry date: orkeys sends the
reminders and exports the expiry. `consumers` need a `secret`.

The key expires at `issued` plus `ttl`. Without `issued`, it expires at `ttl` after the time when orkeys got the
value.

```yaml
providers:
  - name: elevenlabs
    kind: void                                         # no API and no management key
    options:
      console: https://elevenlabs.io/app/settings/api-keys   # link in the reminders
```

```yaml
name: prod-voice-tts
kind: production
provider: elevenlabs
env: prod
description: TTS for the voice gateway.
issued: 2026-09-01                            # date of the value in secret; change it at each rotation
ttl: 90d                                      # rotation period; default: the kind ttl
secret:
  path: secret/llm/prod/voice/tts
consumers:
  - type: eso                                 # force-sync, then restart, after each new value
    cluster: prod-eu
    namespace: voice
    externalsecret: voice-llm
    restart: [deployment/voice-gw]
```

1. Merge the manifest. `apply` registers the key (`register`). `plan` shows the case:
   - The secret holds a value: orkeys adopts it.
   - The secret is empty: the key waits for a value. `plan` warns, and `rotate` sends a `value_missing` alert once a
     day. Write the key to the secret: the next `plan` shows `adopt-value`. Or run `orkeys put`.
   - The places in `secret` hold different values: `plan` warns and registers nothing. Make the values equal.
2. At `rotate_before`, `rotate` sends `expiry_notice`. After the expiry, it sends `key_expired` once a day.
3. Make a new key at the vendor. Then do one of these:
   - Write the key to the secret and change `issued` in the manifest. `plan` shows `adopt-value` with the new hash.
     The PR records the rotation.
   - Run `orkeys put <key>` and paste the key, or pipe it to stdin. `put` writes it to all places in `secret`.
     If the manifest has `issued`, the event tells you the new date to write there.
4. The next `rotate` syncs the consumers and sends `revoke_by_hand` with the hash of the old key. Revoke the old
   key at the vendor. After `revoke_grace`, orkeys forgets the old hash.

```sh
orkeys put prod-voice-tts                         # issued now
pbpaste | orkeys put prod-voice-tts --issued 2026-09-01
```

For a key without `secret`, `put` reads no value and sets only the dates.

When `issued` changes, `plan` reads the secret:

| Secret holds | `plan` shows |
|---|---|
| A new value | `adopt-value`: new hash, new dates. The old hash waits for `revoke_by_hand` |
| The same value | `set-dates`: only the dates change. Check that you wrote the new key: "same value" in the plan means that you did not |
| Different values in the places | A warning. No action |
| Nothing | A warning. No action |

| Change | Effect at `apply` or `rotate` |
|---|---|
| New manifest | `register`: saves the state. Adopts the value in `secret` if it is set |
| Change `issued` | `adopt-value` or `set-dates`, see above |
| Change `ttl` | `set-dates`: the expiry moves |
| Change `secret` | `move-secret`: copies the value to the new places. The next `rotate` syncs the consumers. The old places stay until `revoke_grace` ends |
| Remove `secret` | `move-secret`: deletes the value. `revoke_by_hand` names the key to revoke |
| `disabled: true` | `update-state`. Stops the reminders. The key stays active at the vendor |
| Delete the manifest | `delete`: deletes the secret fields, the GitHub secrets and the state. Counts against `max_deletes`. `revoke_by_hand` names the key to revoke |
| `rotate --now` | Error. Make the key at the vendor and run `put` |

#### Keys without a manifest

Limits:

- `ignore_unmanaged` takes key IDs only. Key names are not unique at the providers, so a name does not identify a key.
- An ignored key has no state and no metrics. orkeys does not see its spend.
- The management key secret cannot set these options. Set them in `policy.yaml`.

`plan` lists provider keys that have no state, and the exporter counts them in `orkeys_unmanaged_keys`. Use an alert
rule on this metric to find keys made outside orkeys. Use the options of each provider to change what they report:

| Option | Default | Meaning |
|---|---|---|
| `report_unmanaged` | `true` | `false`: `plan` does not show keys of this provider without state, and the exporter does not export `orkeys_unmanaged_keys` for it. `orkeys import` still prints them |
| `ignore_disabled` | the opposite of `track_disabled` | `true`: `plan`, `orkeys import` and the exporter skip keys that are disabled at the provider |
| `ignore_unmanaged` | | Key IDs that `plan`, `orkeys import` and the exporter never report |

```yaml
providers:
  openrouter:
    management_key: secret/orkeys/providers/openrouter
    options:
      ignore_disabled: true                  # skip keys disabled at OpenRouter
      ignore_unmanaged:
        - 3f2a9c1b7e04d5a6...                # CI key of the platform team, managed in Terraform
  openai:
    management_key: secret/orkeys/providers/openai
    options:
      project: proj_abc123                   # the project that holds the keys
      report_unmanaged: false                # other teams make keys in this project
```

The key ID is the OpenRouter key `hash`, the DeepInfra token ID, the OpenAI key ID, the fal.ai `key_id` or the ElevenLabs `key_id`. `orkeys keys -p <provider> list`
and the `plan` output show it. A disabled key is an OpenRouter key with the disabled flag, a DeepInfra key with a
limit of 0, an OpenAI key with inactive project access, or an ElevenLabs key with `is_disabled`.

`plan` shows a warning for an ID in `ignore_unmanaged` that is not at the provider or that has state. Remove such
entries.

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
| `key_tracked` | info | `apply` saved the state of a key with `track_only: true`, or registered a `void` key |
| `key_untracked` | info | `apply` removed the state of a tracked key whose manifest is gone. The key stays at the provider |
| `key_revoked` | info | A key was revoked (manifest removed, expired, owner left, `orkeys revoke`) |
| `key_disabled` | info | A key was disabled (`disabled: true` in the manifest) |
| `key_enabled` | info | A disabled key was enabled again (`track_disabled: true`) |
| `rotation_started` | info | A new key was written; consumers sync next. Level `alert` when orkeys issued the key because an earlier run stopped before it wrote its key to the secrets |
| `rotation_synced` | info | Consumers use the new key; the old key will be revoked after `revoke_grace` |
| `old_key_revoked` | info | The old key of a rotation was revoked |
| `expiry_notice` | info | A self-service key expires within `notify_before`, or a `void` key within `rotate_before` |
| `rotation_failed` | alert | A consumer did not sync or restart; the old key stays active |
| `old_key_in_use` | alert | The old key was used after the switch; it is not revoked |
| `consumers_synced` | info | `apply` wrote or deleted push consumer targets (GitHub Actions secrets) |
| `consumers_sync_failed` | alert | A push consumer write or delete failed; the next `apply` tries again |
| `key_expired` | alert | A `void` key is past its expiry. Once a day until `orkeys put` |
| `value_missing` | alert | A `void` key with `secret` has no value. Once a day until `orkeys put` |
| `revoke_by_hand` | alert | orkeys does not use an old `void` key any more. Revoke it at the vendor |

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
  - `.policy` holds a copy of `policy.yaml` (`.policy.json` in S3). A successful `apply` writes it when the file
    changed, also when the plan is empty. A failed write fails `apply`, after the key changes. The exporter reads
    this copy, so it needs no second copy of the file.
  - Keep the history: turn on S3 bucket versioning, or keep `max_versions` above 1 on the Vault KV mount. It is the
    audit trail and the way back after a bad change.
- **Secrets** (`secrets`). Manifest keys, and by default the provider management keys.
  - `vault`: Vault KV v2. A path is `<mount>/<path>`; fields are secret fields. Manifest keys get custom metadata
    per field: `orkeys-key.<field>`, `orkeys-key-id.<field>`, `orkeys-team.<field>`. Field writes and deletes use
    check-and-set, so a change by another client between the read and the write is not lost.
  - `aws`: AWS Secrets Manager. A path is the secret name; the value is a JSON object, fields are its keys.
    Manifest keys get the same names as tags. A field name has only letters, digits, spaces and `_.:/=+-@`. A secret
    deleted by orkeys keeps a 7-day recovery window. When orkeys writes to a secret in that window, it restores the
    secret and writes only the new field.
  - A field name has at most 114 characters: Vault metadata keys and AWS tag keys have at most 128.
  - Two keys can use the same path with different fields. Each field has its own metadata.
  - orkeys up to v0.4.1 wrote one set of metadata per secret (`orkeys-key-id` without a field). orkeys reads it for
    a field that has no metadata of its own. The next write to the secret removes it.
  - `plan` reads `orkeys-key-id.<field>` of each place. When a place holds another key than the state, `plan` shows
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
| `ORKEYS_GITHUB_SECRETS_TOKEN` | apply, rotate | Writes GitHub Actions secrets for `github` consumers and starts their `workflows`. Default: `GITHUB_TOKEN` |

AWS permissions: `s3:GetObject`, `s3:PutObject`, `s3:DeleteObject` on `<bucket>/<prefix>/*` and `s3:ListBucket` on the
bucket; `secretsmanager:GetSecretValue`, `DescribeSecret`, `CreateSecret`, `PutSecretValue`, `TagResource`, `UntagResource`,
`DeleteSecret`, `RestoreSecret` on the secrets orkeys reads and writes. The exporter needs only `s3:GetObject`, `s3:ListBucket` and
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
   the new secret. `github` consumers get the new key written to their GitHub Actions secrets, and orkeys starts their `workflows`.
3. 15 minutes after the switch, a `rotate` run records the old key's usage (total usage on OpenRouter, spend this
   month on DeepInfra, OpenAI and fal.ai, spend in the billing period on ElevenLabs). OpenAI reports spend with a delay, so use a `revoke_grace` of at least 24h
   for OpenAI keys. fal.ai does not document the delay of its usage estimate: use at least 24h for fal.ai keys too.
   After `revoke_grace` (24h), `rotate` compares it again. If the usage grew, a consumer still has
   the old key: orkeys does not revoke it and marks the rotation as failed. For OpenAI keys a last use after the
   switch also counts, because OpenAI reports it without delay. If the spend went down (a new month started),
   orkeys records a new baseline and decides on the next run. If no run recorded the usage in between, the run at
   revoke time records it and the next run revokes. Run `rotate` hourly.
4. A failed rotation sends an alert and stops. Fix the cause, then run `orkeys rotate --retry <key>`. The old key stays
   active until then. `--retry` syncs the consumers again and starts a new wait: the usage check of step 3 records a
   new baseline. Only spend after the retry counts.

OpenRouter, OpenAI and ElevenLabs keys also get a native expiry at `ttl`. DeepInfra keys have none; orkeys revokes them itself.
A fal.ai key has the expiry that the person set at fal.ai, if any.

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

Limits:

- A provider that is new in the stored `policy.yaml` gets no metrics until the exporter restarts. The exporter makes
  the provider clients at start. It logs the new provider name.
- The stored copy exists only after the first `apply` with orkeys v0.4.3 or later.
- The stored copy cannot change the state store. The exporter always uses `--state` to find it.

```sh
orkeys exporter --state vault://secret/orkeys/state       # secrets and providers from the copy of policy.yaml in the state store

orkeys exporter --policy /etc/orkeys/policy.yaml          # state, secrets and providers from the file

orkeys exporter --state s3://my-bucket/orkeys/state --secrets aws \
  --provider openrouter=orkeys/providers/openrouter      # flags override the policy
```

Without `--policy`, the exporter reads the copy of `policy.yaml` that `apply` saves in the state store (`.policy`).
It reads the copy again at each poll, so a change of the provider `options` (for example `ignore_unmanaged`) needs no
restart. It ignores fields that it does not know: CI can run a newer orkeys than the exporter. When the copy is
missing or broken, the exporter logs the error, keeps the last good policy and stops the update of
`orkeys_policy_last_load_timestamp_seconds`. Without a copy and without flags, the exporter has no providers and does
not start.

The exporter polls the providers every `--interval` (default 10m) and serves the cached values. `/metrics` serves the
metrics; `/healthz` returns 200 after the first poll. It needs read access to the state and to the management keys.
Key series have the labels `name`, `team`, `kind`, `env`, `provider`, `service`, `owner`, `notify_channel`,
`alert_channel` and `reset`, so Alertmanager can route by team and channel. orkeys has no alert thresholds: an alert
rule compares `orkeys_key_spend_usd` with `orkeys_key_budget_usd`, for example `spend / budget > 0.8`. For OpenAI and
fal.ai keys this rule is the only control of spend: these providers do not stop a key at its budget.

| Metric | Meaning |
|---|---|
| `orkeys_key_spend_usd` | Spend in the current limit period (`reset`: today, this week or this month) |
| `orkeys_key_budget_usd` | `budget.limit` from the manifest for the same period. Not for `void` keys |
| `orkeys_key_hard_limit_usd` | Limit that the provider reports for the same period. A limit changed outside orkeys shows here, and differs from `orkeys_key_budget_usd`. No series for a key without a limit at the provider, and for providers without a per-key limit (OpenAI, fal.ai). A key missing at the provider shows `budget.limit` |
| `orkeys_key_usage_usd_total` | Counter of the key usage in USD, as the provider reports it. Use `increase()` or `rate()`. It resets at each rotation; DeepInfra, OpenAI and fal.ai also reset it at the start of each month, ElevenLabs at the start of each billing period. OpenAI data is hours late |
| `orkeys_key_expires_timestamp_seconds` | Expiry. Not for a tracked key without a provider expiry |
| `orkeys_key_last_used_timestamp_seconds` | Last use (OpenRouter and OpenAI). OpenRouter reports the request time for recently used keys, so treat it as "recently used", not as an exact time |
| `orkeys_key_created_timestamp_seconds` | Creation time of the key at the provider. When the provider does not report it: the time when orkeys created or adopted the key |
| `orkeys_key_rotation_state` | 0 idle, 1 consumers not synced, 2 old key waits for revoke, 4 failed |
| `orkeys_key_pending_revoke_timestamp_seconds` | Earliest planned revoke time of an old key after a rotation. `rotate` holds the revoke while the old key is in use. A value far in the past means that a consumer still uses the old key |
| `orkeys_key_missing` | 1 if the key is not found at the provider, or a `void` key has no value yet. A `void` key with a value shows 0: orkeys cannot see it at the vendor |
| `orkeys_key_disabled` | 0 enabled, 1 disabled by the manifest (`disabled: true`), 2 disabled at the provider and not in the manifest. DeepInfra: a limit of 0 is disabled. No series for a missing key |
| `orkeys_key_track_only` | 1 if the key has `track_only: true`: orkeys does not rotate it, and a person must renew it before `orkeys_key_expires_timestamp_seconds`. 0 for a managed key |
| `orkeys_unmanaged_keys{provider}` | Provider keys without state that the provider options do not ignore. Not exported with `report_unmanaged: false`. The exporter reads the options from `--policy` or from the copy in the state store |
| `orkeys_provider_poll_success{provider}` | 1 if the last poll worked |
| `orkeys_provider_last_poll_timestamp_seconds{provider}` | Last successful poll |
| `orkeys_state_last_load_timestamp_seconds` | Last successful state load |
| `orkeys_policy_last_load_timestamp_seconds` | Last successful load of `policy.yaml` from the state store. Not exported with `--policy`. Alert when it is older than 30m |
| `orkeys_exporter_build_info{version}` | Always 1. `version` is the orkeys version of the exporter. CI and the exporter must read the same state and policy format, so compare it with the version in CI |
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
| `internal/fal` | HTTP client for the fal.ai Platform API: keys, usage |
| `internal/elevenlabs` | HTTP client for the API keys of an ElevenLabs service account |
| `internal/spend` | spend per key in the current UTC day, week and month, from daily costs (OpenAI, fal.ai) |
| `internal/httpx` | JSON over HTTP with retries: reads on network errors, 408, 429 and 5xx; writes on 429 |
| `internal/nullable` | request fields that are absent, null or set |
| `internal/provider` | common interface, optional `Limiter` and `Disabler`, and registry of the providers; `void.go` is the provider without an API |
| `internal/provider/providertest` | contract test that every provider adapter passes |
| `internal/manifest` | inventory loading and validation; consumer types in `consumer.go` |
| `internal/state`, `internal/secrets` | state and secret store interfaces |
| `internal/vault`, `internal/s3store`, `internal/awssm`, `internal/backend` | Vault KV v2, S3, Secrets Manager, and the factory that picks them |
| `internal/engine` | plan, apply, rotate, request, issued, revoke, state listing, hygiene, import; consumer drivers in `consumers.go`, recovery in `recover.go`, `void` keys and `put` in `void.go` |
| `internal/exporter` | Prometheus metrics |
| `internal/kube`, `internal/deliver`, `internal/github` | kubectl, age delivery, org membership, Actions secrets |
| `internal/cli` | cobra commands: `openrouter.go`, `deepinfra.go`, `openai.go`, `fal.go`, `elevenlabs.go`, `keys.go`, `inventory.go`, `put.go`, `exporter_cmd.go`; wiring in `wire.go`, output in `render.go`, event webhook in `webhook.go`, log setup in `logging.go` |
| `examples/inventory` | example inventory repository with rotate and hygiene workflows; a test keeps it valid |
| `internal/values` | input parsing and formatting |

### Adding a provider

1. Add an HTTP client package (`internal/<name>`) on `internal/httpx`.
2. Add an adapter in `internal/provider` that implements `Provider`. Implement `Limiter` only if the provider has a
   per-key limit, and `Disabler` only if it can disable keys. Set `Caps` to match: `New` refuses a mismatch.
   If the API cannot create keys, set `Caps.ManualCreate` and implement `KeyIdentifier`: it returns the provider ID
   of a key value. Without it, `New` refuses the adapter.
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
