package manifest

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/hatedabamboo/orkeys/internal/provider"
	"github.com/hatedabamboo/orkeys/internal/values"
)

const defaultNaming = `^[a-z0-9][a-z0-9._-]*$`

const (
	SourceManifest = "manifest"
	SourceRequest  = "request"
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
	Naming     string                   `yaml:"naming"`
	Kinds      map[string]*KindPolicy   `yaml:"kinds"`
	Providers  Providers                `yaml:"providers"`
	State      string                   `yaml:"state"`
	Secrets    string                   `yaml:"secrets"`
	Envs       []string                 `yaml:"envs"`
	NamePrefix *string                  `yaml:"name_prefix"`
	GitHub     GitHubConfig             `yaml:"github"`
	Clusters   map[string]ClusterConfig `yaml:"clusters"`
	// TrackDisabled makes plan enforce the disabled status of manifest keys both ways:
	// it also enables keys that are disabled at the provider but not in the manifest.
	TrackDisabled bool `yaml:"track_disabled"`
	// MaxDeletes is the most keys that one apply revokes because their manifests are gone. Nil means
	// DefaultMaxDeletes. apply --allow-deletes raises it for one run.
	MaxDeletes *int `yaml:"max_deletes"`
}

// ReportUnmanaged reports whether plan and the exporter show keys of provider name that have no state.
func (p *Policy) ReportUnmanaged(name string) bool {
	r := p.Providers[name].Options.ReportUnmanaged
	return r == nil || *r
}

// IgnoresUnmanaged reports whether k of provider name is left out of the unmanaged keys.
func (p *Policy) IgnoresUnmanaged(name string, k provider.Key) bool {
	o := p.Providers[name].Options
	if slices.Contains(o.IgnoreUnmanaged, k.ID) {
		return true
	}
	ignoreDisabled := !p.TrackDisabled
	if o.IgnoreDisabled != nil {
		ignoreDisabled = *o.IgnoreDisabled
	}
	return k.Disabled && ignoreDisabled
}

// DefaultMaxDeletes is the max_deletes value when policy.yaml does not set it.
const DefaultMaxDeletes = 3

// DeleteLimit returns max_deletes, or DefaultMaxDeletes when it is not set.
func (p *Policy) DeleteLimit() int {
	if p.MaxDeletes == nil {
		return DefaultMaxDeletes
	}
	return *p.MaxDeletes
}

type GitHubConfig struct {
	Org     string `yaml:"org"`
	APIURL  string `yaml:"api_url"`
	KeysURL string `yaml:"keys_url"`
}

type KindPolicy struct {
	Source    string   `yaml:"source"`
	TTL       Duration `yaml:"ttl"`
	Providers []string `yaml:"providers"`
	Budget    Budget   `yaml:"budget"`
	MaxLimit  float64  `yaml:"max_limit"`
	// Deprecated: ignored. The alert channel follows Source. Remove in the next release.
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

// ChannelFor returns the team channel for alert events of a key of kind kp:
// channels.notifications for source: request, channels.alerts for source: manifest.
func (t *Team) ChannelFor(kp *KindPolicy) string {
	if kp != nil && kp.Source == SourceRequest {
		return t.Channels.Notifications
	}
	return t.Channels.Alerts
}

// Providers maps a provider name to its config. In policy.yaml it is a list with a name and a kind per entry,
// or a map from name to config, where the kind defaults to the name.
type Providers map[string]ProviderConfig

var providerFields = []string{"name", "kind", "management_key", "workspace", "base_url", "options"}

func (ps *Providers) UnmarshalYAML(n *yaml.Node) error {
	out := Providers{}
	add := func(name string, line int, pc ProviderConfig) error {
		if name == "" {
			return fmt.Errorf("line %d: providers: name is required", line)
		}
		if _, dup := out[name]; dup {
			return fmt.Errorf("line %d: providers: duplicate name %q", line, name)
		}
		pc.Name = name
		if pc.Kind == "" {
			pc.Kind = name
		}
		out[name] = pc
		return nil
	}
	decode := func(v *yaml.Node) (ProviderConfig, error) {
		var pc ProviderConfig
		if v.Kind != yaml.MappingNode {
			return pc, fmt.Errorf("line %d: providers: want a mapping", v.Line)
		}
		// Node.Decode does not inherit KnownFields, so check the field names here.
		for i := 0; i < len(v.Content); i += 2 {
			if k := v.Content[i]; !slices.Contains(providerFields, k.Value) {
				return pc, fmt.Errorf("line %d: providers: field %s not found (known: %s)", k.Line, k.Value, strings.Join(providerFields, ", "))
			}
		}
		return pc, v.Decode(&pc)
	}
	switch n.Kind {
	case yaml.SequenceNode:
		for _, item := range n.Content {
			pc, err := decode(item)
			if err != nil {
				return err
			}
			if err := add(pc.Name, item.Line, pc); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			pc, err := decode(v)
			if err != nil {
				return err
			}
			if pc.Name != "" && pc.Name != k.Value {
				return fmt.Errorf("line %d: providers.%s: name %q differs from the key", v.Line, k.Value, pc.Name)
			}
			if err := add(k.Value, k.Line, pc); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("line %d: providers: want a list or a mapping", n.Line)
	}
	*ps = out
	return nil
}

// ProviderKind returns the provider type (openrouter, deepinfra) of a configured provider name.
// An unknown name is returned as is, so a key with provider: openrouter still finds its type.
func (p *Policy) ProviderKind(name string) string {
	if pc, ok := p.Providers[name]; ok && pc.Kind != "" {
		return pc.Kind
	}
	return name
}

type ProviderConfig struct {
	// Name is the provider name used in manifests, state and metrics. Kind is its type.
	Name          string          `yaml:"name"`
	Kind          string          `yaml:"kind"`
	ManagementKey string          `yaml:"management_key"`
	Workspace     string          `yaml:"workspace"`
	BaseURL       string          `yaml:"base_url"`
	Options       ProviderOptions `yaml:"options,omitempty"`
}

// Names of the provider options that all provider types have.
const (
	OptReportUnmanaged = "report_unmanaged"
	OptIgnoreDisabled  = "ignore_disabled"
	OptIgnoreUnmanaged = "ignore_unmanaged"
	OptTrackOnly       = "track_only"
)

// CommonProviderOptions are the option names of all provider types.
var CommonProviderOptions = []string{OptReportUnmanaged, OptIgnoreDisabled, OptIgnoreUnmanaged, OptTrackOnly}

// ProviderOptions are providers[].options. The common options set the report of keys without state and the
// default of track_only. Type holds
// the string options of one provider type, for example project for openai. The provider type lists the names it knows.
type ProviderOptions struct {
	// ReportUnmanaged is false to hide keys without state in plan and in orkeys_unmanaged_keys. Nil means true.
	ReportUnmanaged *bool
	// IgnoreDisabled skips disabled keys without state. Nil means the opposite of track_disabled.
	IgnoreDisabled *bool
	// IgnoreUnmanaged are key IDs that are never reported. Key names are not unique at the providers,
	// so only an ID identifies a key.
	IgnoreUnmanaged []string
	// TrackOnly is the default of the key field track_only for keys of this provider.
	TrackOnly bool
	Type      map[string]string
}

func (o *ProviderOptions) UnmarshalYAML(n *yaml.Node) error {
	if n.Tag == "!!null" {
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: options: want a mapping", n.Line)
	}
	out := ProviderOptions{}
	for i := 0; i < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		var err error
		switch k.Value {
		case OptReportUnmanaged:
			err = v.Decode(&out.ReportUnmanaged)
		case OptIgnoreDisabled:
			err = v.Decode(&out.IgnoreDisabled)
		case OptIgnoreUnmanaged:
			err = v.Decode(&out.IgnoreUnmanaged)
		case OptTrackOnly:
			err = v.Decode(&out.TrackOnly)
		default:
			if v.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: options.%s: want a string", v.Line, k.Value)
			}
			if out.Type == nil {
				out.Type = map[string]string{}
			}
			out.Type[k.Value] = v.Value
		}
		if err != nil {
			return fmt.Errorf("line %d: options.%s: %w", v.Line, k.Value, err)
		}
	}
	*o = out
	return nil
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
	Name        string   `yaml:"name"`
	Kind        string   `yaml:"kind"`
	Provider    string   `yaml:"provider"`
	Env         string   `yaml:"env,omitempty"`
	Service     string   `yaml:"service,omitempty"`
	Description string   `yaml:"description"`
	Scopes      []string `yaml:"scopes,omitempty"`
	Import      *Import  `yaml:"import,omitempty"`
	Disabled    bool     `yaml:"disabled,omitempty"`
	// Track: orkeys keeps state and metrics of the imported key and does not change the key. Nil means the
	// provider option track_only. Load sets it from the provider.
	Track     *bool      `yaml:"track_only,omitempty"`
	Budget    Budget     `yaml:"budget"`
	TTL       Duration   `yaml:"ttl"`
	Secrets   Secrets    `yaml:"secret,omitempty"`
	Consumers []Consumer `yaml:"consumers,omitempty"`

	Team string `yaml:"-"`
	Path string `yaml:"-"`
}

// TrackOnly reports whether orkeys only tracks the key: it saves the state and exports metrics, and it does not
// set the limit, write secrets, sync consumers, rotate or revoke the key.
func (k *Key) TrackOnly() bool { return k.Track != nil && *k.Track }

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

// Secrets is the list of places in the secrets store that hold the key. In a manifest, secret is
// one mapping or a list of mappings. orkeys writes the same key to all places.
type Secrets []Secret

var secretFields = []string{"path", "field"}

func (ss *Secrets) UnmarshalYAML(n *yaml.Node) error {
	decode := func(v *yaml.Node) (Secret, error) {
		var s Secret
		if v.Kind != yaml.MappingNode {
			return s, fmt.Errorf("line %d: secret: want a mapping with path and field", v.Line)
		}
		// Node.Decode does not inherit KnownFields, so check the field names here.
		for i := 0; i < len(v.Content); i += 2 {
			if k := v.Content[i]; !slices.Contains(secretFields, k.Value) {
				return s, fmt.Errorf("line %d: secret: field %s not found (known: %s)", k.Line, k.Value, strings.Join(secretFields, ", "))
			}
		}
		return s, v.Decode(&s)
	}
	switch n.Kind {
	case yaml.MappingNode:
		s, err := decode(n)
		if err != nil {
			return err
		}
		*ss = Secrets{s}
		return nil
	case yaml.SequenceNode:
		out := make(Secrets, 0, len(n.Content))
		for _, item := range n.Content {
			s, err := decode(item)
			if err != nil {
				return err
			}
			out = append(out, s)
		}
		*ss = out
		return nil
	}
	return fmt.Errorf("line %d: secret: want a mapping or a list", n.Line)
}

func (p *Policy) NamePrefixFor(env, team string) string {
	if p.NamePrefix == nil {
		return env + "-" + team + "-"
	}
	return strings.NewReplacer("{env}", env, "{team}", team).Replace(*p.NamePrefix)
}

// DefaultSecretField is the secret field of a key when the manifest does not set field.
const DefaultSecretField = "api_key"

// SecretTargets returns the places of the key in the secrets store, with the default field set.
func (k *Key) SecretTargets() []Secret {
	out := make([]Secret, len(k.Secrets))
	for i, s := range k.Secrets {
		if s.Field == "" {
			s.Field = DefaultSecretField
		}
		out[i] = s
	}
	return out
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
