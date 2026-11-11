package manifest

import (
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	// ConsumerESO reads the key through an ExternalSecret.
	ConsumerESO = "eso"
	// ConsumerDirect reads the key from the secrets store.
	ConsumerDirect = "direct"
	// ConsumerGitHub is a GitHub Actions secret that orkeys writes.
	ConsumerGitHub = "github"
)

// ConsumerSpec is the type-specific part of a consumer in a key manifest.
type ConsumerSpec interface {
	// Validate reports problems with add. add puts the field path of the consumer before the message.
	// So a message starts with "." (a field) or ":" (the consumer).
	Validate(add func(format string, args ...any))
}

// PushSpec is a consumer that orkeys writes the key to, for example a GitHub Actions secret.
// orkeys records each push in the state. It deletes the target when the consumer or the key goes away.
type PushSpec interface {
	ConsumerSpec
	// TargetID is the stable name of the target. orkeys stores it in the state.
	// After the manifest is gone, the driver deletes the target by this ID.
	TargetID() string
}

// NotifySpec is a consumer that reads the key from the secrets store.
// At rotation, orkeys tells it to read again: an ExternalSecret sync, a rollout restart.
type NotifySpec interface {
	ConsumerSpec
	// Workloads returns the workloads that must restart to read the new key.
	Workloads() []string
}

// Consumer is one entry of consumers in a key manifest.
type Consumer struct {
	Type string
	Spec ConsumerSpec
}

var consumerTypes = map[string]func() ConsumerSpec{
	ConsumerESO:    func() ConsumerSpec { return &ESOConsumer{} },
	ConsumerDirect: func() ConsumerSpec { return &DirectConsumer{} },
	ConsumerGitHub: func() ConsumerSpec { return &GitHubConsumer{} },
}

// RegisterConsumer adds a consumer type. newSpec returns a pointer to a struct with yaml tags.
// Call it from an init function, before a manifest loads. The engine also needs a driver for the type.
func RegisterConsumer(typ string, newSpec func() ConsumerSpec) {
	if _, dup := consumerTypes[typ]; dup {
		panic("manifest: consumer type " + typ + " registered twice")
	}
	consumerTypes[typ] = newSpec
}

// ConsumerTypes returns the registered consumer types, sorted.
func ConsumerTypes() []string { return slices.Sorted(maps.Keys(consumerTypes)) }

// UnmarshalYAML reads type, then decodes the other fields into the spec of that type.
// A field that the type does not have is an error.
func (c *Consumer) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: consumer: want a mapping", n.Line)
	}
	rest := &yaml.Node{Kind: yaml.MappingNode, Tag: n.Tag, Line: n.Line, Column: n.Column}
	for i := 0; i < len(n.Content); i += 2 {
		if k, v := n.Content[i], n.Content[i+1]; k.Value == "type" {
			c.Type = v.Value
		} else {
			rest.Content = append(rest.Content, k, v)
		}
	}
	newSpec, ok := consumerTypes[c.Type]
	if !ok {
		return fmt.Errorf("line %d: consumer type %q: want one of %s", n.Line, c.Type, strings.Join(ConsumerTypes(), ", "))
	}
	spec := newSpec()
	known := yamlFields(spec)
	for i := 0; i < len(rest.Content); i += 2 {
		if k := rest.Content[i]; !slices.Contains(known, k.Value) {
			return fmt.Errorf("line %d: consumer type %s has no field %s (known: type, %s)", k.Line, c.Type, k.Value, strings.Join(known, ", "))
		}
	}
	if err := rest.Decode(spec); err != nil {
		return err
	}
	c.Spec = spec
	return nil
}

// MarshalYAML writes type and the fields of the spec in one mapping.
func (c Consumer) MarshalYAML() (any, error) {
	var n yaml.Node
	if err := n.Encode(c.Spec); err != nil {
		return nil, err
	}
	typ := []*yaml.Node{{Kind: yaml.ScalarNode, Value: "type"}, {Kind: yaml.ScalarNode, Value: c.Type}}
	n.Content = append(typ, n.Content...)
	return &n, nil
}

// yamlFields returns the yaml field names of the struct that v points to, with inline structs expanded.
func yamlFields(v any) []string {
	var out []string
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
			switch {
			case name == "-":
			case strings.Contains(opts, "inline"):
				walk(f.Type)
			case name != "":
				out = append(out, name)
			case f.IsExported():
				out = append(out, strings.ToLower(f.Name))
			}
		}
	}
	walk(reflect.TypeOf(v).Elem())
	return out
}

var restartKinds = []string{"deployment", "deploy", "statefulset", "sts", "daemonset", "ds", "cronjob", "cj"}

// KubeTarget is the Kubernetes part of the eso and direct consumers.
type KubeTarget struct {
	Cluster   string   `yaml:"cluster"`
	Namespace string   `yaml:"namespace"`
	Restart   []string `yaml:"restart,omitempty"`
}

// Workloads returns the workloads to restart.
func (t *KubeTarget) Workloads() []string { return t.Restart }

func (t *KubeTarget) validate(add func(string, ...any)) {
	if t.Cluster == "" || t.Namespace == "" {
		add(": cluster and namespace are required")
	}
	for _, r := range t.Restart {
		kind, name, ok := strings.Cut(r, "/")
		if !ok || name == "" || !slices.Contains(restartKinds, strings.ToLower(kind)) {
			add(".restart: %q is not <deployment|statefulset|daemonset|cronjob>/<name>", r)
		}
	}
}

// ESOConsumer reads the key through an ExternalSecret. At rotation, orkeys forces a sync and restarts the workloads.
type ESOConsumer struct {
	KubeTarget     `yaml:",inline"`
	ExternalSecret string `yaml:"externalsecret"`
}

// Validate checks the fields.
func (c *ESOConsumer) Validate(add func(string, ...any)) {
	if c.ExternalSecret == "" {
		add(".externalsecret: required for type eso")
	}
	c.validate(add)
}

// DirectConsumer reads the key from the secrets store itself. At rotation, orkeys restarts the workloads.
type DirectConsumer struct {
	KubeTarget `yaml:",inline"`
}

// Validate checks the fields.
func (c *DirectConsumer) Validate(add func(string, ...any)) { c.validate(add) }

// GitHubConsumer is a GitHub Actions secret: Repo or Org, an optional Environment (repo only), and Name.
type GitHubConsumer struct {
	Repo        string `yaml:"repo,omitempty"`
	Org         string `yaml:"org,omitempty"`
	Environment string `yaml:"environment,omitempty"`
	Name        string `yaml:"name"`
	Visibility  string `yaml:"visibility,omitempty"`
}

var (
	githubSecretName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	githubRepo       = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)
	githubOrg        = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
)

// Validate checks the fields.
func (c *GitHubConsumer) Validate(add func(string, ...any)) {
	switch {
	case c.Name == "":
		add(".name: required for type github")
	case !githubSecretName.MatchString(c.Name) || strings.HasPrefix(strings.ToUpper(c.Name), "GITHUB_"):
		add(".name: %q is not a valid GitHub secret name (letters, digits, _; no GITHUB_ prefix)", c.Name)
	}
	switch {
	case (c.Repo == "") == (c.Org == ""):
		add(": set exactly one of repo or org")
	case c.Repo != "" && !githubRepo.MatchString(c.Repo):
		add(".repo: want <owner>/<repo>")
	case c.Org != "" && !githubOrg.MatchString(c.Org):
		add(".org: %q is not a GitHub org name", c.Org)
	}
	if c.Environment != "" && c.Repo == "" {
		add(".environment: only with repo")
	}
	if strings.Contains(c.Environment, ":") {
		add(".environment: must not contain ':'")
	}
	if c.Visibility != "" {
		if c.Org == "" {
			add(".visibility: only with org")
		} else if c.Visibility != "all" && c.Visibility != "private" {
			add(".visibility: want all or private")
		}
	}
}

// Target returns the GitHub secret of the consumer.
func (c *GitHubConsumer) Target() GitHubSecret {
	if c.Org != "" {
		g := GitHubSecret{Org: c.Org, Name: c.Name, Visibility: c.Visibility}
		if g.Visibility == "" {
			g.Visibility = "private"
		}
		return g
	}
	return GitHubSecret{Repo: c.Repo, Environment: c.Environment, Name: c.Name}
}

// TargetID returns the ID of the GitHub secret.
func (c *GitHubConsumer) TargetID() string { return c.Target().ID() }

// GitHubSecret is one GitHub Actions secret that orkeys writes. Set Org, or Repo (owner/name) with an
// optional Environment.
type GitHubSecret struct {
	Org         string
	Repo        string
	Environment string
	Name        string
	Visibility  string
}

// ID is the stable name of the target, stored in the state: org:<org>:<NAME>, repo:<owner/repo>:<NAME>
// or env:<owner/repo>:<environment>:<NAME>.
func (g GitHubSecret) ID() string {
	switch {
	case g.Org != "":
		return "org:" + g.Org + ":" + g.Name
	case g.Environment != "":
		return "env:" + g.Repo + ":" + g.Environment + ":" + g.Name
	}
	return "repo:" + g.Repo + ":" + g.Name
}

func (g GitHubSecret) String() string { return g.ID() }

// ParseGitHubSecret reads an ID. The secret name is the last field; it cannot contain ':'.
func ParseGitHubSecret(id string) (GitHubSecret, error) {
	kind, rest, _ := strings.Cut(id, ":")
	i := strings.LastIndex(rest, ":")
	if i <= 0 || i == len(rest)-1 {
		return GitHubSecret{}, fmt.Errorf("bad github secret id %q", id)
	}
	scope, name := rest[:i], rest[i+1:]
	switch kind {
	case "org":
		return GitHubSecret{Org: scope, Name: name}, nil
	case "repo":
		return GitHubSecret{Repo: scope, Name: name}, nil
	case "env":
		repo, env, ok := strings.Cut(scope, ":")
		if ok && repo != "" && env != "" {
			return GitHubSecret{Repo: repo, Environment: env, Name: name}, nil
		}
	}
	return GitHubSecret{}, fmt.Errorf("bad github secret id %q", id)
}

// PushTarget is one push consumer of a key.
type PushTarget struct {
	Type string
	Spec PushSpec
}

// Key returns the name of the target in the state: <type>/<target ID>.
func (t PushTarget) Key() string { return t.Type + "/" + t.Spec.TargetID() }

// PushTargets returns the push consumers of the key.
func (k *Key) PushTargets() []PushTarget {
	var out []PushTarget
	for _, c := range k.Consumers {
		if ps, ok := c.Spec.(PushSpec); ok {
			out = append(out, PushTarget{Type: c.Type, Spec: ps})
		}
	}
	return out
}
