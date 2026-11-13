package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadValid(t *testing.T) {
	inv, err := Load("testdata/repo")
	if err != nil {
		t.Fatal(err)
	}
	if ps := inv.Validate(); len(ps) != 0 {
		t.Fatalf("problems: %v", ps)
	}
	if len(inv.Keys) != 2 || inv.Teams["chat-core"] == nil {
		t.Fatalf("loaded %d keys", len(inv.Keys))
	}
	k := inv.Key("prod-chat-core-dialog")
	if k.Team != "chat-core" || k.TTL.Duration != 90*24*time.Hour {
		t.Fatalf("key: %+v", k)
	}
	if k.Budget.Limit != 200 || k.Budget.Reset != "monthly" || len(k.SecretTargets()) != 1 || k.SecretTargets()[0].Field != "api_key" || false {
		t.Fatal("defaults")
	}
	if inv.Policy.Kind("local").Budget.Limit != 20 || inv.Policy.Kind("personal").MaxActive != 2 {
		t.Fatal("self-service defaults")
	}
	if !inv.Teams["chat-core"].HasMember("ASmith") || inv.Teams["chat-core"].GitHubSlug("acme") != "" {
		t.Fatal("static members")
	}
}

func copyRepo(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.Walk("testdata/repo", func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("testdata/repo", p)
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestValidateProblems(t *testing.T) {
	cases := map[string]struct{ file, body, want string }{
		"ttl over max": {"teams/chat-core/prod-chat-core-x.yaml", `
name: prod-chat-core-x
kind: production
provider: openrouter
env: prod
service: x
description: d
budget: {limit: 10}
ttl: 120d
secret: {path: kv/llm/x}
`, "ttl: more than kinds.production.ttl 90d"},
		"wrong prefix": {"teams/chat-core/prod-other-x.yaml", `
name: prod-other-x
kind: production
provider: openrouter
env: prod
service: x
description: d
budget: {limit: 10}
ttl: 30d
secret: {path: kv/llm/y}
`, `must start with "prod-chat-core-"`},
		"file name": {"teams/chat-core/wrong.yaml", `
name: prod-chat-core-y
kind: production
provider: openrouter
env: prod
service: y
description: d
budget: {limit: 10}
ttl: 30d
secret: {path: kv/llm/z}
`, "file name must be"},
		"personal manifest": {"teams/chat-core/personal-chat-core-jdoe-x.yaml", `
name: personal-chat-core-jdoe-x
kind: personal
provider: openrouter
description: d
budget: {limit: 10}
ttl: 7d
`, "kind: personal keys are issued with orkeys request"},
		"unknown kind": {"teams/chat-core/prod-chat-core-u.yaml", `
name: prod-chat-core-u
kind: batch
provider: openrouter
env: prod
description: d
budget: {limit: 10}
ttl: 30d
secret: {path: kv/llm/u}
`, `kind: "batch" is not in policy.yaml kinds`},
		"duplicate secret path": {"teams/chat-core/prod-chat-core-dup.yaml", `
name: prod-chat-core-dup
kind: production
provider: openrouter
env: prod
service: dup
description: d
budget: {limit: 10}
ttl: 30d
secret: {path: kv/llm/prod/chat-core/dialog}
`, "already used by"},
		"scopes on openrouter": {"teams/chat-core/prod-chat-core-s.yaml", `
name: prod-chat-core-s
kind: production
provider: openrouter
env: prod
service: s
scopes: [inference]
description: d
budget: {limit: 10}
ttl: 30d
secret: {path: kv/llm/s}
`, "openrouter keys have no scopes"},
		"disabled on openai": {"teams/chat-core/prod-chat-core-oa.yaml", `
name: prod-chat-core-oa
kind: production
provider: openai
env: prod
service: oa
description: d
budget: {limit: 10}
ttl: 30d
disabled: true
secret: {path: kv/llm/oa}
`, "disabled: openai keys cannot be disabled"},
		"ttl over openai max": {"teams/chat-core/prod-chat-core-ob.yaml", `
name: prod-chat-core-ob
kind: production
provider: openai
env: prod
service: ob
description: d
budget: {limit: 10}
ttl: 400d
secret: {path: kv/llm/ob}
`, "ttl: more than 365d, the longest expiry of openai keys"},
		"import without name": {"teams/chat-core/prod-chat-core-i.yaml", `
name: prod-chat-core-i
kind: production
provider: openrouter
env: prod
description: d
import: {id: abc}
secret: {path: kv/llm/i}
`, "import: id and name are required"},
		"bad consumer": {"teams/chat-core/prod-chat-core-c.yaml", `
name: prod-chat-core-c
kind: production
provider: openrouter
env: prod
service: c
description: d
budget: {limit: 10}
ttl: 30d
secret: {path: kv/llm/c}
consumers:
  - {type: eso, cluster: a, namespace: b, restart: [pod/x]}
`, "externalsecret: required"}, "github no repo or org": {"teams/chat-core/prod-chat-core-c.yaml", `
name: prod-chat-core-c
kind: production
provider: openrouter
env: prod
service: c
description: d
budget: {limit: 10}
ttl: 30d
secret: {path: kv/llm/c}
consumers:
  - {type: github, name: X}
`, "set exactly one of repo or org"},
		"github bad name": {"teams/chat-core/prod-chat-core-c.yaml", `
name: prod-chat-core-c
kind: production
provider: openrouter
env: prod
service: c
description: d
budget: {limit: 10}
ttl: 30d
secret: {path: kv/llm/c}
consumers:
  - {type: github, repo: a/b, name: GITHUB_X}
`, "not a valid GitHub secret name"},
		"github env with org": {"teams/chat-core/prod-chat-core-c.yaml", `
name: prod-chat-core-c
kind: production
provider: openrouter
env: prod
service: c
description: d
budget: {limit: 10}
ttl: 30d
secret: {path: kv/llm/c}
consumers:
  - {type: github, org: a, environment: prod, name: X}
`, "environment: only with repo"},
		"github visibility with repo": {"teams/chat-core/prod-chat-core-c.yaml", `
name: prod-chat-core-c
kind: production
provider: openrouter
env: prod
service: c
description: d
budget: {limit: 10}
ttl: 30d
secret: {path: kv/llm/c}
consumers:
  - {type: github, repo: a/b, visibility: all, name: X}
`, "visibility: only with org"},
		"github with cluster": {"teams/chat-core/prod-chat-core-c.yaml", `
name: prod-chat-core-c
kind: production
provider: openrouter
env: prod
service: c
description: d
budget: {limit: 10}
ttl: 30d
secret: {path: kv/llm/c}
consumers:
  - {type: github, repo: a/b, name: X, cluster: c}
`, "consumer type github has no field cluster"},
		"eso with repo": {"teams/chat-core/prod-chat-core-c.yaml", `
name: prod-chat-core-c
kind: production
provider: openrouter
env: prod
service: c
description: d
budget: {limit: 10}
ttl: 30d
secret: {path: kv/llm/c}
consumers:
  - {type: eso, cluster: a, namespace: b, externalsecret: e, repo: a/b}
`, "consumer type eso has no field repo"},
		"unknown consumer type": {"teams/chat-core/prod-chat-core-c.yaml", `
name: prod-chat-core-c
kind: production
provider: openrouter
env: prod
description: d
secret: {path: kv/llm/c}
consumers:
  - {type: vercel, project: x}
`, "consumer type \"vercel\": want one of direct, eso, github"},
		"github duplicate": {"teams/chat-core/prod-chat-core-c.yaml", `
name: prod-chat-core-c
kind: production
provider: openrouter
env: prod
service: c
description: d
budget: {limit: 10}
ttl: 30d
secret: {path: kv/llm/c}
consumers:
  - {type: github, repo: a/b, name: X}
  - {type: github, repo: a/b, name: x}
`, "already written by prod-chat-core-c"},
		"secret listed twice": {"teams/chat-core/prod-chat-core-d.yaml", `
name: prod-chat-core-d
kind: production
provider: openrouter
env: prod
description: d
secret:
  - {path: kv/llm/d}
  - {path: kv/llm/d, field: api_key}
`, "secret[1]: kv/llm/d#api_key is listed twice"},
		"secret list path used by other key": {"teams/chat-core/prod-chat-core-d.yaml", `
name: prod-chat-core-d
kind: production
provider: openrouter
env: prod
description: d
secret:
  - {path: kv/llm/d}
  - {path: kv/llm/prod/chat-core/dialog}
`, "path: already used by prod-chat-core-"},
		"secret list no path": {"teams/chat-core/prod-chat-core-d.yaml", `
name: prod-chat-core-d
kind: production
provider: openrouter
env: prod
description: d
secret:
  - {path: kv/llm/d}
  - {field: x}
`, "secret[1].path: required"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			root := copyRepo(t)
			if err := os.WriteFile(filepath.Join(root, c.file), []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			// A field that the consumer type does not have fails the load, not the validation.
			var all []string
			if inv, err := Load(root); err != nil {
				all = append(all, err.Error())
			} else {
				for _, p := range inv.Validate() {
					all = append(all, p.String())
				}
			}
			if !strings.Contains(strings.Join(all, "\n"), c.want) {
				t.Fatalf("want %q in:\n%s", c.want, strings.Join(all, "\n"))
			}
		})
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	root := copyRepo(t)
	f := filepath.Join(root, "teams/chat-core/team.yaml")
	b, _ := os.ReadFile(f)
	_ = os.WriteFile(f, append(b, []byte("slak: typo\n")...), 0o644)
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "slak") {
		t.Fatalf("want unknown field error, got %v", err)
	}
}

func TestPolicyStores(t *testing.T) {
	cases := map[string]string{
		"state: gcs://b/p\n":                   "state: want vault://",
		"state: vault://infra\n":               "state: want vault://",
		"secrets: gcp\n":                       "secrets: want vault or aws",
		"state: s3://bucket/p\nsecrets: aws\n": "",
	}
	for extra, want := range cases {
		root := copyRepo(t)
		p := filepath.Join(root, "policy.yaml")
		b, _ := os.ReadFile(p)
		s := strings.Replace(string(b), "state: vault://kv/orkeys/state\nsecrets: vault\n", extra, 1)
		_ = os.WriteFile(p, []byte(s), 0o644)
		inv, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		var all []string
		for _, pr := range inv.Validate() {
			all = append(all, pr.String())
		}
		got := strings.Join(all, "\n")
		if want == "" && got != "" {
			t.Errorf("%q: unexpected problems:\n%s", extra, got)
		}
		if want != "" && !strings.Contains(got, want) {
			t.Errorf("%q: want %q in:\n%s", extra, want, got)
		}
	}
}

func TestConfigurableNaming(t *testing.T) {
	key := `
name: %s
kind: production
provider: openrouter
env: %s
description: d
budget: {limit: 10}
ttl: 30d
secret: {path: kv/llm/%s}
`
	cases := []struct {
		policy, name, env, want string
	}{
		{"envs: [live, test]\n", "live-chat-core-x", "live", ""},
		{"envs: [live, test]\n", "prod-chat-core-x", "prod", "env: want one of live, test"},
		{"name_prefix: \"\"\n", "anything-goes", "prod", ""},
		{"name_prefix: \"{team}.{env}.\"\n", "chat-core.prod.x", "prod", ""},
		{"name_prefix: \"{team}.{env}.\"\n", "prod-chat-core-x", "prod", `must start with "chat-core.prod."`},
	}
	for _, c := range cases {
		root := copyRepo(t)
		pp := filepath.Join(root, "policy.yaml")
		b, _ := os.ReadFile(pp)
		s := string(b)
		s = strings.Replace(s, s[:strings.Index(s, "\n")+1], "", 1)
		_ = os.WriteFile(pp, []byte(s+c.policy), 0o644)
		_ = os.WriteFile(filepath.Join(root, "teams/chat-core", c.name+".yaml"), []byte(fmt.Sprintf(key, c.name, c.env, c.name)), 0o644)
		inv, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		var all []string
		for _, p := range inv.Validate() {
			if strings.Contains(p.Path, c.name) {
				all = append(all, p.String())
			}
		}
		got := strings.Join(all, "\n")
		if (c.want == "" && got != "") || (c.want != "" && !strings.Contains(got, c.want)) {
			t.Errorf("%q %s: want %q, got:\n%s", c.policy, c.name, c.want, got)
		}
	}
}

func TestChannelsOptional(t *testing.T) {
	root := copyRepo(t)
	_ = os.WriteFile(filepath.Join(root, "teams/chat-core/team.yaml"), []byte("team: chat-core\nmembers: [jdoe]\n"), 0o644)
	inv, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if ps := inv.Validate(); len(ps) != 0 {
		t.Fatalf("problems: %v", ps)
	}
}

func TestExampleInventoryIsValid(t *testing.T) {
	inv, err := Load("../../examples/inventory")
	if err != nil {
		t.Fatal(err)
	}
	if ps := inv.Validate(); len(ps) != 0 {
		t.Fatalf("examples/inventory has problems: %v", ps)
	}
	if len(inv.Keys) != 2 || inv.Teams["search"] == nil {
		t.Fatalf("loaded %d keys", len(inv.Keys))
	}
}

func TestBudgetReset(t *testing.T) {
	key := `
name: prod-chat-core-h
kind: production
provider: %s
env: prod
description: d
budget: {reset: %s, limit: 400}
ttl: 30d
secret: {path: kv/llm/h}
`
	for _, c := range []struct {
		provider, reset, want string
	}{
		{"openrouter", "daily", ""},
		{"openrouter", "weekly", ""},
		{"openrouter", "hourly", "budget.reset: want daily, weekly or monthly"},
		{"deepinfra", "monthly", ""},
		{"deepinfra", "daily", "deepinfra supports only monthly limits"},
	} {
		root := copyRepo(t)
		_ = os.WriteFile(filepath.Join(root, "teams/chat-core/prod-chat-core-h.yaml"), []byte(fmt.Sprintf(key, c.provider, c.reset)), 0o644)
		inv, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		ps := fmt.Sprint(inv.Validate())
		if c.want == "" && ps != "[]" || c.want != "" && !strings.Contains(ps, c.want) {
			t.Errorf("%s %s: want %q, got %s", c.provider, c.reset, c.want, ps)
		}
	}
}

func withKinds(t *testing.T, kinds string) string {
	t.Helper()
	root := copyRepo(t)
	p := filepath.Join(root, "policy.yaml")
	b, _ := os.ReadFile(p)
	if err := os.WriteFile(p, []byte(strings.Replace(string(b), "kinds:\n", "kinds:\n"+kinds, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestKindPolicyProblems(t *testing.T) {
	cases := map[string]struct{ kind, want string }{
		"no ttl":           {"  batch: {max_limit: 5}\n", "kinds.batch.ttl: required"},
		"bad source":       {"  batch: {source: cron, ttl: 7d}\n", "kinds.batch.source: want manifest or request"},
		"request no max":   {"  guest: {source: request, ttl: 7d}\n", "kinds.guest.max_limit: required for source: request"},
		"request rotation": {"  guest: {source: request, ttl: 7d, max_limit: 5, rotate_before: 1d}\n", "only for source: manifest"},
		"manifest notify":  {"  batch: {ttl: 30d, notify_before: 1d}\n", "only for source: request"},
		"ttl under rotate": {"  batch: {ttl: 2d}\n", "kinds.batch.ttl: must be longer than rotate_before"},
		"default over max": {"  batch: {ttl: 30d, budget: {limit: 9}, max_limit: 5}\n", "kinds.batch.budget.limit: more than max_limit"},
		"bad reset":        {"  batch: {ttl: 30d, budget: {reset: yearly}}\n", "kinds.batch.budget.reset: want daily, weekly or monthly"},
		"unknown provider": {"  batch: {ttl: 30d, providers: [openai]}\n", `kinds.batch.providers: "openai" is not in providers`},
		"bad name":         {"  Batch_1: {ttl: 30d}\n", "kinds.Batch_1: name must match"},
		"request track":    {"  guest: {source: request, ttl: 7d, max_limit: 5, track_only: true}\n", "kinds.guest.track_only: only for source: manifest"},
		"track weekly":     {"  batch: {ttl: 30d, track_only: true, budget: {reset: weekly}}\n", "kinds.batch.budget.reset: track_only: true allows only monthly"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			inv, err := Load(withKinds(t, c.kind))
			if err != nil {
				t.Fatal(err)
			}
			var all []string
			for _, p := range inv.Validate() {
				all = append(all, p.String())
			}
			if !strings.Contains(strings.Join(all, "\n"), c.want) {
				t.Fatalf("want %q in:\n%s", c.want, strings.Join(all, "\n"))
			}
		})
	}
}

func TestCustomManifestKind(t *testing.T) {
	root := withKinds(t, "  batch: {ttl: 30d, rotate_before: 3d, budget: {reset: weekly, limit: 5}, max_limit: 50, providers: [openrouter]}\n")
	key := `
name: prod-chat-core-batch
kind: batch
provider: %s
env: prod
description: nightly batch
secret: {path: kv/llm/batch}
%s`
	write := func(p, extra string) *Inventory {
		_ = os.WriteFile(filepath.Join(root, "teams/chat-core/prod-chat-core-batch.yaml"), []byte(fmt.Sprintf(key, p, extra)), 0o644)
		inv, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		return inv
	}

	inv := write("openrouter", "")
	if ps := inv.Validate(); len(ps) != 0 {
		t.Fatalf("problems: %v", ps)
	}
	k := inv.Key("prod-chat-core-batch")
	if k.TTL.Duration != 30*24*hour || k.Budget.Limit != 5 || k.Budget.Reset != "weekly" {
		t.Fatalf("defaults not applied: ttl %s budget %+v", k.TTL.Duration, k.Budget)
	}

	for extra, want := range map[string]string{
		"budget: {limit: 60}": "budget.limit: more than kinds.batch.max_limit 50",
		"ttl: 40d":            "ttl: more than kinds.batch.ttl 30d",
	} {
		if ps := fmt.Sprint(write("openrouter", extra).Validate()); !strings.Contains(ps, want) {
			t.Errorf("%s: want %q in %s", extra, want, ps)
		}
	}
	if ps := fmt.Sprint(write("deepinfra", "").Validate()); !strings.Contains(ps, "batch keys allow only openrouter") {
		t.Errorf("provider not checked: %s", ps)
	}
}

func TestDuplicateImport(t *testing.T) {
	root := copyRepo(t)
	for _, n := range []string{"a", "b"} {
		body := fmt.Sprintf("name: prod-chat-core-%s\nkind: production\nprovider: openrouter\nenv: prod\ndescription: d\nimport: {id: same, name: old}\nsecret: {path: kv/llm/%s}\n", n, n)
		_ = os.WriteFile(filepath.Join(root, "teams/chat-core/prod-chat-core-"+n+".yaml"), []byte(body), 0o644)
	}
	inv, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if ps := fmt.Sprint(inv.Validate()); !strings.Contains(ps, "import.id: already imported by prod-chat-core-a") {
		t.Fatalf("duplicate import not found: %s", ps)
	}
}

func TestGitHubTeam(t *testing.T) {
	cases := []struct{ name, team, org, want, slug string }{
		{"default slug", "team: chat-core\n", "acme", "", "chat-core"},
		{"own slug", "team: chat-core\ngithub_team: chat\n", "acme", "", "chat"},
		{"static", "team: chat-core\nmembers: [jdoe]\n", "acme", "", ""},
		{"no org, no members", "team: chat-core\n", "", "", ""},
		{"both", "team: chat-core\nmembers: [jdoe]\ngithub_team: chat\n", "acme", "members and github_team: set only one", ""},
		{"no org", "team: chat-core\ngithub_team: chat\n", "", "github_team: needs github.org", ""},
		{"bad slug", "team: chat-core\ngithub_team: Chat Core\n", "acme", "not a GitHub team slug", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := copyRepo(t)
			_ = os.WriteFile(filepath.Join(root, "teams/chat-core/team.yaml"), []byte(c.team), 0o644)
			pol, _ := os.ReadFile(filepath.Join(root, "policy.yaml"))
			pol = []byte(strings.Replace(string(pol), "github:\n  org: example\n", "github: {org: \""+c.org+"\"}\n", 1))
			_ = os.WriteFile(filepath.Join(root, "policy.yaml"), pol, 0o644)
			inv, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			var all []string
			for _, p := range inv.Validate() {
				all = append(all, p.String())
			}
			got := strings.Join(all, "\n")
			if (c.want == "" && got != "") || (c.want != "" && !strings.Contains(got, c.want)) {
				t.Fatalf("want %q, got:\n%s", c.want, got)
			}
			if c.want == "" {
				if s := inv.Teams["chat-core"].GitHubSlug(inv.Policy.GitHub.Org); s != c.slug {
					t.Fatalf("slug %q, want %q", s, c.slug)
				}
			}
		})
	}
}

func TestAlertChannelDeprecated(t *testing.T) {
	inv, err := Load(withKinds(t, "  batch: {ttl: 30d, alert_channel: \"#team-notifications\"}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if ps := inv.Validate(); len(ps) != 0 {
		t.Fatalf("problems %v", ps)
	}
	ws := inv.Warnings()
	if len(ws) != 1 || !strings.Contains(ws[0].String(), "kinds.batch.alert_channel: deprecated") {
		t.Fatalf("warnings %v", ws)
	}
	team := &Team{}
	team.Channels.Notifications, team.Channels.Alerts = "#n", "#a"
	if ch := team.ChannelFor(inv.Policy.Kind("batch")); ch != "#a" {
		t.Fatalf("manifest kind: channel %q", ch)
	}
	if ch := team.ChannelFor(inv.Policy.Kind("personal")); ch != "#n" {
		t.Fatalf("request kind: channel %q", ch)
	}
	if ch := team.ChannelFor(nil); ch != "#a" {
		t.Fatalf("unknown kind: channel %q", ch)
	}
}

const providersMap = `providers:
  openrouter:
    management_key: kv/orkeys/providers/openrouter
  deepinfra:
    management_key: kv/orkeys/providers/deepinfra
`

func withProviders(t *testing.T, providers string) string {
	t.Helper()
	root := copyRepo(t)
	p := filepath.Join(root, "policy.yaml")
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), providersMap) {
		t.Fatal("testdata policy.yaml: providers block not found")
	}
	if err := os.WriteFile(p, []byte(strings.Replace(string(b), providersMap, providers, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestProvidersList(t *testing.T) {
	root := withProviders(t, `providers:
  - name: openrouter
    management_key: kv/orkeys/providers/openrouter
  - name: openrouter-private
    kind: openrouter
    management_key: kv/orkeys/providers/openrouter-private
    workspace: ws-private
  - name: deepinfra
    management_key: kv/orkeys/providers/deepinfra
`)
	key := `name: prod-chat-core-private
kind: production
provider: openrouter-private
env: prod
description: Private workspace.
budget: {reset: daily, limit: 10}
ttl: 30d
secret: {path: kv/llm/private}
`
	_ = os.WriteFile(filepath.Join(root, "teams/chat-core/prod-chat-core-private.yaml"), []byte(key), 0o644)
	inv, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if ps := inv.Validate(); len(ps) > 0 {
		t.Fatalf("problems: %v", ps)
	}
	pc := inv.Policy.Providers["openrouter-private"]
	if pc.Kind != "openrouter" || pc.Workspace != "ws-private" || inv.Policy.ProviderKind("deepinfra") != "deepinfra" {
		t.Fatalf("providers: %+v", inv.Policy.Providers)
	}

	// The caps come from the kind: deepinfra-kind providers allow only monthly limits.
	root = withProviders(t, "providers:\n  - {name: di-batch, kind: deepinfra}\n  - {name: openrouter}\n  - {name: deepinfra}\n")
	_ = os.WriteFile(filepath.Join(root, "teams/chat-core/prod-chat-core-private.yaml"),
		[]byte(strings.Replace(key, "openrouter-private", "di-batch", 1)), 0o644)
	if inv, err = Load(root); err != nil {
		t.Fatal(err)
	}
	if ps := fmt.Sprint(inv.Validate()); !strings.Contains(ps, "di-batch supports only monthly limits") {
		t.Fatalf("want monthly-only problem, got %s", ps)
	}
}

func TestProvidersProblems(t *testing.T) {
	cases := map[string]struct{ providers, loadErr, problem string }{
		"no name":        {"providers:\n  - {kind: openrouter}\n", "name is required", ""},
		"duplicate":      {"providers:\n  - {name: openrouter}\n  - {name: openrouter}\n", `duplicate name "openrouter"`, ""},
		"unknown field":  {"providers:\n  - {name: openrouter, mgmt_key: x}\n", "field mgmt_key not found", ""},
		"map name":       {"providers:\n  openrouter: {name: other}\n", "differs from the key", ""},
		"unknown kind":   {"providers:\n  - {name: openrouter}\n  - {name: deepinfra}\n  - {name: llm, kind: mistral}\n", "", `providers.llm: unknown kind "mistral"`},
		"bad name":       {"providers:\n  - {name: openrouter}\n  - {name: deepinfra}\n  - {name: Or_2, kind: openrouter}\n", "", "providers.Or_2: name must match"},
		"unknown option": {"providers:\n  - {name: openrouter, options: {project: x}}\n  - {name: deepinfra}\n", "", "providers.openrouter.options.project: unknown option for openrouter (known: none)"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			inv, err := Load(withProviders(t, c.providers))
			if c.loadErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.loadErr) {
					t.Fatalf("want load error %q, got %v", c.loadErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if ps := fmt.Sprint(inv.Validate()); !strings.Contains(ps, c.problem) {
				t.Fatalf("want %q in %s", c.problem, ps)
			}
		})
	}
}

func TestSecretList(t *testing.T) {
	root := copyRepo(t)
	f := filepath.Join(root, "teams/chat-core/prod-chat-core-dialog.yaml")
	b, _ := os.ReadFile(f)
	body := strings.Replace(string(b), "secret:\n  path: kv/llm/prod/chat-core/dialog\n",
		"secret:\n  - path: kv/llm/prod/chat-core/dialog\n  - path: kv/llm/staging/chat-core/dialog\n    field: OR_KEY\n", 1)
	_ = os.WriteFile(f, []byte(body), 0o644)
	inv, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if ps := inv.Validate(); len(ps) != 0 {
		t.Fatalf("problems: %v", ps)
	}
	got := inv.Key("prod-chat-core-dialog").SecretTargets()
	want := []Secret{{"kv/llm/prod/chat-core/dialog", "api_key"}, {"kv/llm/staging/chat-core/dialog", "OR_KEY"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("targets: %v", got)
	}

	_ = os.WriteFile(f, []byte(strings.Replace(body, "field: OR_KEY", "feild: OR_KEY", 1)), 0o644)
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "feild") {
		t.Fatalf("want unknown field error, got %v", err)
	}
}

func TestTrackOnly(t *testing.T) {
	root := withKinds(t, "  legacy: {ttl: 365d, track_only: true, budget: {limit: 50}}\n")
	key := `
name: prod-chat-core-%s
kind: %s
provider: deepinfra
env: prod
description: legacy service
import: {id: %s, name: old}
%s`
	write := func(name, kind, extra string) {
		_ = os.WriteFile(filepath.Join(root, "teams/chat-core/prod-chat-core-"+name+".yaml"), []byte(fmt.Sprintf(key, name, kind, name, extra)), 0o644)
	}
	write("tracked", "legacy", "")
	write("manual", "production", "track_only: true")
	write("managed", "legacy", "track_only: false\nsecret: {path: kv/llm/managed}")
	inv, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if ps := inv.Validate(); len(ps) != 0 {
		t.Fatalf("problems: %v", ps)
	}
	for name, want := range map[string]bool{"tracked": true, "manual": true, "managed": false, "dialog": false} {
		if got := inv.Key("prod-chat-core-" + name).TrackOnly(); got != want {
			t.Errorf("%s: TrackOnly %v, want %v", name, got, want)
		}
	}

	for extra, want := range map[string]string{
		"secret: {path: kv/llm/x}": "secret: not allowed with track_only: true",
		"disabled: true":           "disabled: not allowed with track_only: true",
		"budget: {reset: weekly}":  "budget.reset: track_only: true allows only monthly",
		"consumers: [{type: direct, cluster: prod-eu, namespace: chat, restart: [deployment/x]}]": "consumers: not allowed with track_only: true",
	} {
		write("tracked", "legacy", extra)
		inv, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		if ps := fmt.Sprint(inv.Validate()); !strings.Contains(ps, want) {
			t.Errorf("%s: want %q in %s", extra, want, ps)
		}
	}
	_ = os.WriteFile(filepath.Join(root, "teams/chat-core/prod-chat-core-tracked.yaml"),
		[]byte("name: prod-chat-core-tracked\nkind: legacy\nprovider: deepinfra\nenv: prod\ndescription: d\n"), 0o644)
	inv, err = Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if ps := fmt.Sprint(inv.Validate()); !strings.Contains(ps, "import: required with track_only: true") || strings.Contains(ps, "secret.path: required") {
		t.Errorf("no import: %s", ps)
	}
}
