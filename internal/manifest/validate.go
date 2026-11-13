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

var resets = []string{"daily", "weekly", "monthly"}

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
		pc := pol.Providers[name]
		info, ok := provider.Lookup(pc.Kind)
		if !ok {
			add("policy.yaml", "providers.%s: unknown kind %q (known: %s)", name, pc.Kind, strings.Join(provider.Names(), ", "))
			continue
		}
		for _, opt := range slices.Sorted(maps.Keys(pc.Options.Type)) {
			if !slices.Contains(info.Options, opt) {
				known := strings.Join(append(slices.Clone(CommonProviderOptions), info.Options...), ", ")
				add("policy.yaml", "providers.%s.options.%s: unknown option for %s (known: %s)", name, opt, pc.Kind, known)
			}
		}
		seen := map[string]bool{}
		for i, id := range pc.Options.IgnoreUnmanaged {
			switch {
			case strings.TrimSpace(id) == "":
				add("policy.yaml", "providers.%s.options.%s[%d]: empty key id", name, OptIgnoreUnmanaged, i)
			case seen[id]:
				add("policy.yaml", "providers.%s.options.%s: key id %s is listed twice", name, OptIgnoreUnmanaged, id)
			}
			seen[id] = true
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
	if pol.MaxDeletes != nil && *pol.MaxDeletes < 0 {
		add("policy.yaml", "max_deletes: must not be negative")
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
		if info, ok := provider.Lookup(pol.ProviderKind(k.Provider)); ok && len(k.Scopes) > 0 {
			if !info.Caps.Scopes {
				add(p, "scopes: %s keys have no scopes", k.Provider)
			} else if info.CheckScopes != nil {
				if err := info.CheckScopes(k.Scopes); err != nil {
					add(p, "scopes: %v", err)
				}
			}
		}
		if info, ok := provider.Lookup(pol.ProviderKind(k.Provider)); ok {
			if k.Disabled && !info.Caps.Disable {
				add(p, "disabled: %s keys cannot be disabled; delete the manifest to revoke the key", k.Provider)
			}
			if info.Caps.MaxExpiry > 0 && k.TTL.Duration > info.Caps.MaxExpiry {
				add(p, "ttl: more than %s, the longest expiry of %s keys", values.Days(info.Caps.MaxExpiry), k.Provider)
			}
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
		if k.TrackOnly() {
			validateTracked(k, add)
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
	if len(k.Secrets) == 0 && !k.TrackOnly() {
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
		c.Spec.Validate(func(format string, args ...any) { add(p, where+format, args...) })
		ps, ok := c.Spec.(PushSpec)
		if !ok {
			continue
		}
		// Two keys must not write the same push target.
		// The compare is in lower case. GitHub secret names are case-insensitive.
		id := "consumer:" + strings.ToLower(c.Type+"/"+ps.TargetID())
		if prev, ok := secretPaths[id]; ok {
			add(p, "%s: %s target %s is already written by %s", where, c.Type, ps.TargetID(), prev)
		}
		secretPaths[id] = k.Name
	}
}

// validateTracked checks a key with track_only: true. orkeys does not change a tracked key, so the fields that
// change the key or deliver it are errors. Spend of a tracked key is monthly: DeepInfra reports only the current month.
func validateTracked(k *Key, add func(string, string, ...any)) {
	p := k.Path
	if k.Import == nil {
		add(p, "import: required with track_only: true")
	}
	if len(k.Secrets) > 0 {
		add(p, "secret: not allowed with track_only: true; orkeys does not write a tracked key")
	}
	if len(k.Consumers) > 0 {
		add(p, "consumers: not allowed with track_only: true; orkeys does not deliver a tracked key")
	}
	if k.Disabled {
		add(p, "disabled: not allowed with track_only: true; orkeys does not change a tracked key")
	}
	if k.Budget.ResetOrDefault() != DefaultReset {
		add(p, "budget.reset: track_only: true allows only monthly")
	}
}
