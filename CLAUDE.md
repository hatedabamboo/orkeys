# orkeys: instructions for agents

orkeys manages LLM provider API keys (OpenRouter, DeepInfra, OpenAI). It has two modes:

- Provider commands: `orkeys openrouter` (`or`), `orkeys deepinfra` (`di`) and `orkeys openai` (`oa`) list and change
  keys at one provider.
- Inventory mode: a git repository of YAML manifests is the source of truth. orkeys makes the providers, the secrets
  store, Kubernetes and GitHub Actions secrets match it, rotates keys and exports metrics.

`README.md` is the user documentation. Read the part about your task before you change behavior.

## Rules

- Do not run `git commit`, `git push`, `git rebase` or `git tag` unless the user tells you to.
- Do not add yourself as a co-author of a commit.
- Do not run `orkeys apply`, `orkeys rotate`, `orkeys hygiene` (without `--dry-run`), `orkeys request` or
  `orkeys unlock <id>` against real credentials. They create, change and revoke real keys. `validate` and `plan` are
  safe.
- Do not put API keys, management keys or tokens in files, tests, examples or command output.

## Repository structure

| Path | Contents |
|---|---|
| `main.go` | Entry point. Sets `main.version` from ldflags |
| `internal/cli` | Cobra commands: `openrouter.go`, `deepinfra.go`, `openai.go`, `keys.go` (any provider), `inventory.go`, `exporter_cmd.go`. Wiring in `wire.go`, output in `render.go` and `output.go`, event webhook in `webhook.go` |
| `internal/engine` | Inventory logic: `plan.go`, `apply.go`, `rotate.go`, `request.go`, `revoke.go`, `tracked.go`, `hygiene.go`, `import.go`, `teams.go`, `consumers.go` (consumer drivers), `recover.go` (interrupted issues, secret drift), `event.go`. Test fakes in `fakes_test.go` |
| `internal/manifest` | Load and validate `policy.yaml`, `team.yaml` and key manifests. Types in `types.go`, consumer types in `consumer.go`. Test repo in `testdata/repo` |
| `internal/provider` | Common provider interface, optional `Limiter` and `Disabler`, and registry; OpenRouter, DeepInfra and OpenAI adapters. Contract tests in `conformance_test.go` |
| `internal/provider/providertest` | Contract test that every provider adapter passes |
| `internal/nullable` | Request fields that are absent, null or set |
| `internal/openrouter`, `internal/deepinfra`, `internal/openai` | HTTP clients for the provider APIs |
| `internal/httpx` | JSON over HTTP, retries GET on 429 and 5xx. All provider clients use it |
| `internal/state`, `internal/secrets` | State store and secrets store interfaces; state lock, schema version, heartbeats |
| `internal/vault`, `internal/s3store`, `internal/awssm`, `internal/backend` | Vault KV v2, S3, AWS Secrets Manager, and the factory that picks one |
| `internal/kube` | `kubectl` calls: ExternalSecret force-sync, rollout restart |
| `internal/github` | Org and team membership, Actions secrets |
| `internal/deliver` | age encryption of requested keys to GitHub SSH keys |
| `internal/exporter` | Prometheus exporter |
| `internal/values` | Parse and format durations, dates, money |
| `examples/inventory` | Example inventory repository and workflows. A test keeps it valid |
| `VERSION` | Current version. A change to it on `main` makes the tag and the release |
| `CHANGELOG.md` | One section per version |

If you add a package or move code, update this table and the Layout table in `README.md`.

## Build and check

```sh
make            # build ./orkeys
make check      # gofmt check, go vet, go test ./...
make lint       # golangci-lint
make vuln       # govulncheck
```

Run `make check` and `make lint` before you say that a change is done. If a check fails, report the output.

- Imports: standard library, then third party, then `github.com/hatedabamboo/orkeys/...` (goimports `local-prefixes`).
- Errors: wrap with `%w`. Compare with `errors.Is` and `errors.As` (`errorlint`).
- Tests sit next to the code (`*_test.go`). Use the fakes in `internal/engine/fakes_test.go` and `httptest` servers.
  Tests do not use the network or real credentials.
- A new manifest field needs: the type in `internal/manifest/types.go`, a check in `validate.go`, a test in
  `load_test.go`, a row in the README table, and an example if it is common.

## Comments

- Write a doc comment for each exported identifier. Start it with the name: `// Get returns one key by hash.`
- Write a comment only when the code cannot show the reason: a provider quirk, an API limit, an order that matters,
  a safety check. Example: `// Only GET is retried. Writes are not idempotent from our side.`
- Do not write comments that repeat the code, describe the change ("added", "new", "fixed"), or name a ticket or a
  person.
- Do not leave commented-out code or `TODO` without a reason.
- Use short, plain sentences. One sentence per fact. Present tense.
- In YAML examples, put the comment on the same line, after the value. Align the comments in one block to the same
  column. The comment says what the value does, not what the field is called.

## Writing style (README, CHANGELOG, errors, events)

- Use ASD-STE100 style: short sentences, subject, verb, object, active voice, one instruction per sentence.
- Put limits and risks first, then the main point.
- Do not use filler words: "simply", "just", "easily", "rather", "however".
- Error messages start in lower case, have no period at the end, and name the thing that failed:
  `key prod-search-api: secret field api_key is empty`.
- Write field names, commands, paths and values in backticks.

## Changelog

`CHANGELOG.md` has one section per released version, newest first. There is no `Unreleased` section.

```markdown
## v0.2.5 (2026-11-11)

- (feat) `github` consumer type: orkeys writes the key to a GitHub Actions secret (#18) [ed04189](https://github.com/hatedabamboo/orkeys/commit/ed04189d90664a1135435fbdb743a271e3d6f71f)
- (fix) `plan` no longer shows `update` for keys with an equal limit (#19) [abc1234](https://github.com/hatedabamboo/orkeys/commit/<full sha>)
```

- Header: `## vX.Y.Z (YYYY-MM-DD)`. The version is the content of `VERSION`. The date is the release date.
- Add the section in the same PR that changes `VERSION` (`make bump-patch`, `bump-minor` or `bump-major`). The merge
  to `main` makes the tag and the release.
- One bullet per user-visible change. Start with the type in parentheses: `feat`, `fix`, `change`, `remove`,
  `deprecate`, `security`, `docs`, `ci`, `chore`.
- After the type: what changed, from the user's view. Name the command, field or flag. One or two sentences.
  Breaking changes and required user actions go first in the bullet: `(change) Breaking: ...`.
- After the text: the PR number `(#N)`, then a link to each commit: `[short sha](https://github.com/hatedabamboo/orkeys/commit/<full sha>)`.
  Get the SHA with `git log`. If the commit or the PR does not exist yet, ask the user; do not invent a SHA or a number.
- Do not list commits that only bump `VERSION`, fix typos, or update dependencies without a user-visible effect.
- Do not change sections of released versions, except to fix a wrong link or a wrong fact.

## Managing keys with manifests

Use this part when the user asks you to add, change, move, disable or remove a key in an inventory repository. The
inventory repository is usually a separate repository. `examples/inventory` shows its layout. `README.md`, section
"Inventory mode", has the full field reference.

### Before you write a manifest

Read these files in the inventory repository:

1. `policy.yaml`: `envs`, `name_prefix`, `naming`, `kinds` (their `source`, `ttl`, `max_limit`, `providers`),
   `providers` (names), `clusters`.
2. `teams/<team>/team.yaml`: the team must exist. If it does not, ask the user. Do not create a team without the
   user's channels and members.
3. Other manifests of the same team: copy their `secret.path` pattern and consumer style.

Ask the user for facts you cannot find: the team, the env, the provider, the limit, where the key goes
(`secret.path`), and which workloads use it. Do not guess a cluster, namespace or ExternalSecret name.

### Rules for a key manifest

- One key per file: `teams/<team>/<name>.yaml`. `name` is the file name without `.yaml`.
- `name` starts with `name_prefix` (default `{env}-{team}-`, for example `prod-search-`) and matches `naming`.
- `name` must not start with `<kind>-` of a kind with `source: request`.
- `kind` is a kind with `source: manifest` (or no `source`). Request kinds are for `orkeys request`, not for manifests.
- `provider` is a name from `providers` and is allowed by the kind's `providers`.
- `env` is one of `envs`. `description` is required: say what uses the key.
- `budget.limit` is at most the kind `max_limit`. Omit `budget` to use the kind default. DeepInfra allows only
  `reset: monthly`.
- `ttl` is at most the kind `ttl`. Omit it to use the kind `ttl`.
- `secret.path` is where orkeys writes the key. `secret.field` defaults to `api_key`. `secret` can be a list to write
  the same key to more than one place.
- `consumers` lists what reads the key. `restart` lists only workloads that must restart to read the new key.
- Unknown fields are errors. Do not add fields that the README does not list.
- Never put a key value in a manifest.

Template:

```yaml
---
name: prod-search-embeddings                  # must match the file name
kind: production
provider: openrouter
env: prod
service: embeddings                           # free text, metric label
description: Query rewriting for the search API.
budget:                                       # limit at the provider, per period
  reset: monthly
  limit: 1000
secret:                                       # where orkeys writes the key
  path: secret/llm/prod/search/embeddings
  field: api_key
consumers:
  - type: eso                                 # force-sync the ExternalSecret, then restart
    cluster: prod-eu
    namespace: search
    externalsecret: search-llm
    restart: [deployment/search-api]
```

### Effects of a change

Tell the user what `apply` will do before they merge. These changes have effects that are easy to miss:

| Change | Effect at `apply` or `rotate` |
|---|---|
| New file | Creates a new key and writes it to `secret` |
| Delete the file | Revokes the key at the provider and removes its secret fields and GitHub secrets. `apply` refuses more than `max_deletes` (default 3) revokes in one run |
| Rename the file and `name` | Revokes the old key and creates a new one. Consumers get a new key |
| Change `secret` (add, remove or change a place) | Reissues the key and writes it to all places |
| Change `provider` or `kind` of a key with state | `plan` shows "provider or kind changed". Make a new manifest with a new name instead |
| Change `budget` | Sets the new limit at the provider. No new key |
| Add a `github` consumer | Writes the key to the GitHub secret (`sync-consumers`) |
| `disabled: true` | Disables the key at the provider and stops rotation. Keeps state and secret. Prefer this to deleting the file when the user may need the key again |
| `import: {id, name}` | Adopts an existing provider key. The user must first write its value to `secret.path` |

### After you write a manifest

1. Run `orkeys validate --repo <dir>`. It needs no credentials. Fix every error.
2. If the user has credentials set, run `orkeys plan --repo <dir>` and show the output. Check that the actions are the
   ones you expect. A `revoke` that the user did not ask for is a stop: report it and do not continue.
3. Do not run `orkeys apply`. The user or CI applies the change after review.
