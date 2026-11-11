package manifest

import (
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hatedabamboo/orkeys/internal/provider"
	"github.com/hatedabamboo/orkeys/internal/values"
)

const hour = time.Hour

var (
	resets       = []string{"daily", "weekly", "monthly"}
	restartKinds = []string{"deployment", "deploy", "statefulset", "sts", "daemonset", "ds", "cronjob", "cj"}
)

type Problem struct {
	Path string
	Msg  string
}

func (p Problem) String() string { return p.Path + ": " + p.Msg }

// Warnings returns problems that do not stop a command.
func (inv *Inventory) Warnings() []Problem {
	var ps []Problem
	for _, name := range slices.Sorted(maps.Keys(inv.Policy.Kinds)) {
		if kp := inv.Policy.Kinds[name]; kp != nil && kp.AlertChannel != "" {
			ps = append(ps, Problem{Path: "policy.yaml", Msg: fmt.Sprintf(
				"kinds.%s.alert_channel: deprecated and ignored; alerts of source: request kinds go to channels.notifications, of source: manifest kinds to channels.alerts", name)})
		}
	}
	return ps
}

func (inv *Inventory) Validate() []Problem {
	var ps []Problem
	add := func(path, format string, args ...any) {
		ps = append(ps, Problem{Path: path, Msg: fmt.Sprintf(format, args...)})
	}

	pol := &inv.Policy
	naming, err := regexp.Compile(pol.Naming)
	if err != nil {
		add("policy.yaml", "naming: invalid regex: %v", err)
		naming = regexp.MustCompile(defaultNaming)
	}
	if len(pol.Providers) == 0 {
		add("policy.yaml", "providers: at least one provider is required")
	}
	for _, name := range slices.Sorted(maps.Keys(pol.Providers)) {
		if !providerName.MatchString(name) {
			add("policy.yaml", "providers.%s: name must match %s", name, providerName)
		}
		if kind := pol.Providers[name].Kind; !knownProvider(kind) {
			add("policy.yaml", "providers.%s: unknown kind %q (known: %s)", name, kind, strings.Join(provider.Names(), ", "))
		}
	}
	if len(pol.Kinds) == 0 {
		add("policy.yaml", "kinds: at least one kind is required")
	}
	var reserved []string
	for _, name := range slices.Sorted(maps.Keys(pol.Kinds)) {
		validateKind(pol, name, add)
		if kp := pol.Kinds[name]; kp != nil && kp.Source == SourceRequest {
			reserved = append(reserved, name+"-")
		}
	}
	switch {
	case strings.HasPrefix(pol.State, "vault://") && strings.Count(strings.Trim(strings.TrimPrefix(pol.State, "vault://"), "/"), "/") >= 1:
	case strings.HasPrefix(pol.State, "s3://") && len(strings.TrimPrefix(pol.State, "s3://")) > 0:
	default:
		add("policy.yaml", "state: want vault://<mount>/<path> or s3://<bucket>/<prefix>")
	}
	if pol.Secrets != "vault" && pol.Secrets != "aws" {
		add("policy.yaml", "secrets: want vault or aws")
	}
	if len(pol.Envs) == 0 {
		add("policy.yaml", "envs: at least one env is required")
	}

	for dir, t := range inv.Teams {
		path := filepath.Join("teams", dir, "team.yaml")
		if t.Team != dir {
			add(path, "team: %q must match the directory name %q", t.Team, dir)
		}
		switch {
		case len(t.Members) > 0 && t.GitHubTeam != "":
			add(path, "members and github_team: set only one")
		case t.GitHubTeam != "" && pol.GitHub.Org == "":
			add(path, "github_team: needs github.org in policy.yaml")
		case t.GitHubTeam != "" && !teamSlug.MatchString(t.GitHubTeam):
			add(path, "github_team: %q is not a GitHub team slug", t.GitHubTeam)
		}
		for field, ch := range map[string]string{"notifications": t.Channels.Notifications, "alerts": t.Channels.Alerts} {
			if strings.ContainsAny(ch, " \t\n") {
				add(path, "channels.%s: %q has whitespace", field, ch)
			}
		}
	}

	names := map[string]string{}
	secretPaths := map[string]string{}
	imports := map[string]string{}
	for _, k := range inv.Keys {
		p := k.Path
		if prev, ok := names[k.Name]; ok {
			add(p, "name %q is already used in %s", k.Name, prev)
		}
		names[k.Name] = p

		if !naming.MatchString(k.Name) {
			add(p, "name %q does not match policy naming %s", k.Name, pol.Naming)
		}
		for _, r := range reserved {
			if strings.HasPrefix(k.Name, r) {
				add(p, "name: prefix %s is reserved for requested %s keys", r, strings.TrimSuffix(r, "-"))
			}
		}
		if base := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(p), ".yaml"), ".yml"); base != k.Name {
			add(p, "file name must be %s.yaml", k.Name)
		}
		if inv.Teams[k.Team] == nil {
			add(p, "teams/%s/team.yaml is missing", k.Team)
		}
		if _, ok := pol.Providers[k.Provider]; !ok {
			add(p, "provider %q is not in policy.yaml providers", k.Provider)
		}
		if strings.TrimSpace(k.Description) == "" {
			add(p, "description: required")
		}
		kp := pol.Kind(k.Kind)
		switch {
		case kp == nil:
			add(p, "kind: %q is not in policy.yaml kinds", k.Kind)
		case kp.Source != SourceManifest:
			add(p, "kind: %s keys are issued with orkeys request, not with manifests", k.Kind)
		}
		if k.Budget.Limit <= 0 {
			add(p, "budget.limit: must be more than 0 (or set kinds.%s.budget.limit)", k.Kind)
		}
		if kp != nil && kp.MaxLimit > 0 && k.Budget.Limit > kp.MaxLimit {
			add(p, "budget.limit: more than kinds.%s.max_limit %g", k.Kind, kp.MaxLimit)
		}
		if k.Budget.Reset != "" && !slices.Contains(resets, k.Budget.Reset) {
			add(p, "budget.reset: want daily, weekly or monthly")
		}
		if info, ok := provider.Lookup(pol.ProviderKind(k.Provider)); ok && !info.Caps.LimitReset && k.Budget.Reset != "" && k.Budget.Reset != DefaultReset {
			add(p, "budget.reset: %s supports only monthly limits", k.Provider)
		}
		if kp != nil && !kp.AllowsProvider(k.Provider) {
			add(p, "provider: %s keys allow only %s", k.Kind, strings.Join(kp.Providers, ", "))
		}
		if k.TTL.Duration == 0 {
			add(p, "ttl: required")
		}
		if info, ok := provider.Lookup(pol.ProviderKind(k.Provider)); ok && len(k.Scopes) > 0 && !info.Caps.Scopes {
			add(p, "scopes: %s keys have no scopes", k.Provider)
		}
		if info, ok := provider.Lookup(pol.ProviderKind(k.Provider)); ok {
			if k.Disabled && !info.Caps.Disable {
				add(p, "disabled: %s keys cannot be disabled; delete the manifest to revoke the key", k.Provider)
			}
			if info.Caps.MaxExpiry > 0 && k.TTL.Duration > info.Caps.MaxExpiry {
				add(p, "ttl: more than %s, the longest expiry of %s keys", values.Days(info.Caps.MaxExpiry), k.Provider)
			}
		}
		if slices.Contains(k.Scopes, "unrestricted") {
			add(p, "scopes: unrestricted is not allowed")
		}

		if k.Import != nil {
			if k.Import.ID == "" || k.Import.Name == "" {
				add(p, "import: id and name are required")
			}
			id := k.Provider + "/" + k.Import.ID
			if prev, ok := imports[id]; ok && k.Import.ID != "" {
				add(p, "import.id: already imported by %s", prev)
			}
			imports[id] = k.Name
		}
		validateManifestKey(inv, k, kp, add, secretPaths)
	}

	slices.SortFunc(ps, func(a, b Problem) int { return strings.Compare(a.String(), b.String()) })
	return ps
}

var teamSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)

var kindName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// providerName is also used in env var names, with "-" as "_".
var providerName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func knownProvider(kind string) bool {
	_, ok := provider.Lookup(kind)
	return ok
}

func validateKind(pol *Policy, name string, add func(string, string, ...any)) {
	at := "kinds." + name
	kp := pol.Kinds[name]
	if kp == nil {
		add("policy.yaml", "%s: empty", at)
		return
	}
	if !kindName.MatchString(name) {
		add("policy.yaml", "%s: name must match %s", at, kindName)
	}
	if kp.TTL.Duration == 0 {
		add("policy.yaml", "%s.ttl: required", at)
	}
	if kp.MaxLimit < 0 || kp.Budget.Limit < 0 || kp.MaxTotalLimit < 0 {
		add("policy.yaml", "%s: limits must not be negative", at)
	}
	if kp.MaxLimit > 0 && kp.Budget.Limit > kp.MaxLimit {
		add("policy.yaml", "%s.budget.limit: more than max_limit", at)
	}
	if !slices.Contains(resets, kp.Budget.Reset) {
		add("policy.yaml", "%s.budget.reset: want daily, weekly or monthly", at)
	}
	for _, p := range kp.Providers {
		if _, ok := pol.Providers[p]; !ok {
			add("policy.yaml", "%s.providers: %q is not in providers", at, p)
		}
	}
	switch kp.Source {
	case SourceManifest:
		if kp.NotifyBefore.Duration > 0 || kp.MaxActive > 0 || kp.MaxTotalLimit > 0 {
			add("policy.yaml", "%s: notify_before, max_active and max_total_limit are only for source: request", at)
		}
		if kp.TTL.Duration > 0 && kp.TTL.Duration <= kp.RotateBefore.Duration {
			add("policy.yaml", "%s.ttl: must be longer than rotate_before", at)
		}
	case SourceRequest:
		if kp.RotateBefore.Duration > 0 || kp.RevokeGrace.Duration > 0 {
			add("policy.yaml", "%s: rotate_before and revoke_grace are only for source: manifest", at)
		}
		if kp.MaxLimit <= 0 {
			add("policy.yaml", "%s.max_limit: required for source: request", at)
		}
	default:
		add("policy.yaml", "%s.source: want manifest or request", at)
	}
}

func validateManifestKey(inv *Inventory, k *Key, kp *KindPolicy, add func(string, string, ...any), secretPaths map[string]string) {
	p, pol := k.Path, &inv.Policy
	if !slices.Contains(pol.Envs, k.Env) {
		add(p, "env: want one of %s", strings.Join(pol.Envs, ", "))
	} else if want := pol.NamePrefixFor(k.Env, k.Team); !strings.HasPrefix(k.Name, want) {
		add(p, "name: must start with %q (policy name_prefix)", want)
	}
	if kp != nil && kp.Source == SourceManifest {
		if k.TTL.Duration > kp.TTL.Duration {
			add(p, "ttl: more than kinds.%s.ttl %s", k.Kind, values.Days(kp.TTL.Duration))
		}
		if k.TTL.Duration > 0 && k.TTL.Duration <= kp.RotateBefore.Duration {
			add(p, "ttl: must be longer than kinds.%s.rotate_before", k.Kind)
		}
	}
	if len(k.Secrets) == 0 {
		add(p, "secret.path: required")
	}
	own := map[string]bool{}
	for i, sec := range k.SecretTargets() {
		where := "secret"
		if len(k.Secrets) > 1 {
			where = fmt.Sprintf("secret[%d]", i)
		}
		sp := sec.Path
		if sp == "" {
			add(p, "%s.path: required", where)
			continue
		}
		if pol.Secrets == "vault" && (strings.Count(sp, "/") < 1 || strings.HasPrefix(sp, "/")) {
			add(p, "%s.path: want <mount>/<path> for Vault", where)
		}
		if statePath, ok := strings.CutPrefix(pol.State, "vault://"); ok && pol.Secrets == "vault" &&
			strings.HasPrefix(sp+"/", strings.Trim(statePath, "/")+"/") {
			add(p, "%s.path: must not be under the state path %s", where, statePath)
		}
		if own[sp+"#"+sec.Field] {
			add(p, "%s: %s#%s is listed twice", where, sp, sec.Field)
			continue
		}
		own[sp+"#"+sec.Field] = true
		if prev, ok := secretPaths[sp]; ok && prev != k.Name {
			add(p, "%s.path: already used by %s", where, prev)
		}
		secretPaths[sp] = k.Name
	}
	for i, c := range k.Consumers {
		where := fmt.Sprintf("consumers[%d]", i)
		if c.Type == ConsumerGitHub {
			validateGitHubConsumer(k, c, where, add, secretPaths)
			continue
		}
		if c.Repo != "" || c.Org != "" || c.Environment != "" || c.Name != "" || c.Visibility != "" {
			add(p, "%s: repo, org, environment, name and visibility are only for type github", where)
		}
		switch c.Type {
		case ConsumerESO:
			if c.ExternalSecret == "" {
				add(p, "%s.externalsecret: required for type eso", where)
			}
		case ConsumerDirect:
			if c.ExternalSecret != "" {
				add(p, "%s.externalsecret: only for type eso", where)
			}
		default:
			add(p, "%s.type: want eso, direct or github", where)
		}
		if c.Cluster == "" || c.Namespace == "" {
			add(p, "%s: cluster and namespace are required", where)
		}
		for _, r := range c.Restart {
			kind, name, ok := strings.Cut(r, "/")
			if !ok || name == "" || !slices.Contains(restartKinds, strings.ToLower(kind)) {
				add(p, "%s.restart: %q is not <deployment|statefulset|daemonset|cronjob>/<name>", where, r)
			}
		}
	}
}

var (
	githubSecretName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	githubRepo       = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)
	githubOrg        = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
)

func validateGitHubConsumer(k *Key, c Consumer, where string, add func(string, string, ...any), seen map[string]string) {
	p := k.Path
	if c.Cluster != "" || c.Namespace != "" || c.ExternalSecret != "" || len(c.Restart) > 0 {
		add(p, "%s: cluster, namespace, externalsecret and restart are not for type github", where)
	}
	switch {
	case c.Name == "":
		add(p, "%s.name: required for type github", where)
	case !githubSecretName.MatchString(c.Name) || strings.HasPrefix(strings.ToUpper(c.Name), "GITHUB_"):
		add(p, "%s.name: %q is not a valid GitHub secret name (letters, digits, _; no GITHUB_ prefix)", where, c.Name)
	}
	switch {
	case (c.Repo == "") == (c.Org == ""):
		add(p, "%s: set exactly one of repo or org", where)
	case c.Repo != "" && !githubRepo.MatchString(c.Repo):
		add(p, "%s.repo: want <owner>/<repo>", where)
	case c.Org != "" && !githubOrg.MatchString(c.Org):
		add(p, "%s.org: %q is not a GitHub org name", where, c.Org)
	}
	if c.Environment != "" && c.Repo == "" {
		add(p, "%s.environment: only with repo", where)
	}
	if strings.Contains(c.Environment, ":") {
		add(p, "%s.environment: must not contain ':'", where)
	}
	if c.Visibility != "" {
		if c.Org == "" {
			add(p, "%s.visibility: only with org", where)
		} else if c.Visibility != "all" && c.Visibility != "private" {
			add(p, "%s.visibility: want all or private", where)
		}
	}
	if c.Name == "" || (c.Repo == "") == (c.Org == "") {
		return
	}
	g := GitHubSecret{Org: c.Org, Repo: c.Repo, Environment: c.Environment, Name: c.Name}
	// GitHub secret names are case-insensitive.
	id := "github:" + strings.ToLower(g.ID())
	if prev, ok := seen[id]; ok {
		add(p, "%s: GitHub secret %s is already written by %s", where, g.ID(), prev)
	}
	seen[id] = k.Name
}
