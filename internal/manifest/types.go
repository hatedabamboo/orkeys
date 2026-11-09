package manifest

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/hatedabamboo/orkeys/internal/values"
)

const defaultNaming = `^[a-z0-9][a-z0-9._-]*$`

const (
	SourceManifest = "manifest"
	SourceRequest  = "request"
)

const (
	ChannelAlerts        = "alerts"
	ChannelNotifications = "notifications"
)

func (p *Policy) Kind(name string) *KindPolicy { return p.Kinds[name] }

// Requested reports whether keys of the kind are issued with orkeys request.
// A kind missing from the policy counts as requested when it has an owner, so its keys still expire.
func (p *Policy) Requested(kind, owner string) bool {
	if kp := p.Kinds[kind]; kp != nil {
		return kp.Source == SourceRequest
	}
	return owner != ""
}

const (
	ConsumerESO    = "eso"
	ConsumerDirect = "direct"
)

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := values.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalYAML() (any, error) { return values.Days(d.Duration), nil }

type Policy struct {
	Naming     string                    `yaml:"naming"`
	Kinds      map[string]*KindPolicy    `yaml:"kinds"`
	Providers  map[string]ProviderConfig `yaml:"providers"`
	State      string                    `yaml:"state"`
	Secrets    string                    `yaml:"secrets"`
	Envs       []string                  `yaml:"envs"`
	NamePrefix *string                   `yaml:"name_prefix"`
	GitHub     GitHubConfig              `yaml:"github"`
	Clusters   map[string]ClusterConfig  `yaml:"clusters"`
}

type GitHubConfig struct {
	Org     string `yaml:"org"`
	APIURL  string `yaml:"api_url"`
	KeysURL string `yaml:"keys_url"`
}

type KindPolicy struct {
	Source        string   `yaml:"source"`
	TTL           Duration `yaml:"ttl"`
	Providers     []string `yaml:"providers"`
	Budget        Budget   `yaml:"budget"`
	MaxLimit      float64  `yaml:"max_limit"`
	AlertChannel  string   `yaml:"alert_channel"`
	RotateBefore  Duration `yaml:"rotate_before"`
	RevokeGrace   Duration `yaml:"revoke_grace"`
	NotifyBefore  Duration `yaml:"notify_before"`
	MaxActive     int      `yaml:"max_active"`
	MaxTotalLimit float64  `yaml:"max_total_limit"`
}

func (kp *KindPolicy) AllowsProvider(name string) bool {
	return len(kp.Providers) == 0 || slices.Contains(kp.Providers, name)
}

func (t *Team) ChannelFor(kp *KindPolicy) string {
	if kp != nil && kp.AlertChannel == ChannelNotifications {
		return t.Channels.Notifications
	}
	return t.Channels.Alerts
}

type ProviderConfig struct {
	ManagementKey string `yaml:"management_key"`
	Workspace     string `yaml:"workspace"`
	BaseURL       string `yaml:"base_url"`
}

type ClusterConfig struct {
	Context string `yaml:"context"`
}

type Team struct {
	Team     string `yaml:"team"`
	Channels struct {
		Notifications string `yaml:"notifications"`
		Alerts        string `yaml:"alerts"`
	} `yaml:"channels"`
	Members    []string `yaml:"members"`
	GitHubTeam string   `yaml:"github_team"`

	Dir string `yaml:"-"`
}

// GitHubSlug returns the slug of the GitHub team that holds the members, or "" when the members are static.
// Without members and github_team, the slug is the team name if org is set.
func (t *Team) GitHubSlug(org string) string {
	switch {
	case len(t.Members) > 0:
		return ""
	case t.GitHubTeam != "":
		return t.GitHubTeam
	case org != "":
		return t.Team
	}
	return ""
}

func (t *Team) HasMember(login string) bool {
	for _, m := range t.Members {
		if strings.EqualFold(m, login) {
			return true
		}
	}
	return false
}

type Key struct {
	Name        string     `yaml:"name"`
	Kind        string     `yaml:"kind"`
	Provider    string     `yaml:"provider"`
	Env         string     `yaml:"env,omitempty"`
	Service     string     `yaml:"service,omitempty"`
	Description string     `yaml:"description"`
	Scopes      []string   `yaml:"scopes,omitempty"`
	Import      *Import    `yaml:"import,omitempty"`
	Budget      Budget     `yaml:"budget"`
	TTL         Duration   `yaml:"ttl"`
	Secret      *Secret    `yaml:"secret,omitempty"`
	Consumers   []Consumer `yaml:"consumers,omitempty"`

	Team string `yaml:"-"`
	Path string `yaml:"-"`
}

// Budget is the limit set at the provider; the provider blocks the key when spend in the reset period reaches it.
type Budget struct {
	Reset string  `yaml:"reset,omitempty"`
	Limit float64 `yaml:"limit,omitempty"`
}

const DefaultReset = "monthly"

// Import adopts an existing provider key instead of creating one. Name is the key's current name at the provider.
type Import struct {
	ID   string `yaml:"id"`
	Name string `yaml:"name"`
}

type Secret struct {
	Path  string `yaml:"path"`
	Field string `yaml:"field,omitempty"`
}

type Consumer struct {
	Type           string   `yaml:"type"`
	Cluster        string   `yaml:"cluster"`
	Namespace      string   `yaml:"namespace"`
	ExternalSecret string   `yaml:"externalsecret,omitempty"`
	Restart        []string `yaml:"restart,omitempty"`
}

func (p *Policy) NamePrefixFor(env, team string) string {
	if p.NamePrefix == nil {
		return env + "-" + team + "-"
	}
	return strings.NewReplacer("{env}", env, "{team}", team).Replace(*p.NamePrefix)
}

func (k *Key) SecretField() string {
	if k.Secret != nil && k.Secret.Field != "" {
		return k.Secret.Field
	}
	return "api_key"
}

func (p *Policy) RequestKinds() []string {
	var out []string
	for _, name := range slices.Sorted(maps.Keys(p.Kinds)) {
		if p.Kinds[name] != nil && p.Kinds[name].Source == SourceRequest {
			out = append(out, name)
		}
	}
	return out
}

func (b Budget) ResetOrDefault() string {
	if b.Reset == "" {
		return DefaultReset
	}
	return b.Reset
}
