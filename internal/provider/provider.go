package provider

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Caps are the static capabilities of a provider type. Manifest validation reads them without an instance.
// New checks that PerKeyLimit and Disable match the optional interfaces of the instance.
type Caps struct {
	// NativeExpiry: the provider expires a key itself at Spec.ExpiresAt.
	NativeExpiry bool
	// PerKeyLimit: the provider blocks a key at a spend limit. The adapter implements Limiter.
	PerKeyLimit bool
	// LimitReset: a limit can reset daily or weekly, not only monthly.
	LimitReset bool
	// LastUsed: List sets Key.LastUsedAt.
	LastUsed bool
	// LastUsedExact: LastUsedAt is the time of the last request. A last use after a rotation then proves that a
	// consumer has the old key. OpenRouter is not exact. For recently used keys, it reports the list time.
	LastUsedExact bool
	Scopes        bool
	// Disable: the provider can disable a key and enable it again. The adapter implements Disabler.
	Disable bool
	// MaxExpiry is the longest native expiry. Zero means no limit.
	MaxExpiry time.Duration
}

type Key struct {
	ID            string
	Name          string
	Disabled      bool
	CreatedAt     time.Time
	ExpiresAt     *time.Time
	LastUsedAt    *time.Time
	LimitUSD      *float64
	LimitReset    string
	SpendMonthUSD *float64
	SpendWeekUSD  *float64
	SpendDayUSD   *float64
	// UsageUSD grows while the key is used. OpenRouter reports the total usage.
	// DeepInfra and OpenAI report the spend this month. It goes down when a month starts.
	// OpenAI spend comes from the costs endpoint and is hours late. Nil means no usage data.
	UsageUSD *float64
}

type Spec struct {
	Name       string
	LimitUSD   *float64
	LimitReset string
	ExpiresAt  *time.Time
	Scopes     []string
}

// Provider is what every provider adapter does: list, create and delete keys.
type Provider interface {
	Name() string
	Caps() Caps
	List(ctx context.Context) ([]Key, error)
	// Create returns the new key and its secret. The provider shows the secret only once.
	Create(ctx context.Context, s Spec) (Key, string, error)
	// Delete revokes a key. A key that does not exist is not an error.
	Delete(ctx context.Context, id string) error
	// SelfID is the ID of the management key itself, so it does not show as unmanaged.
	SelfID() string
}

// Limiter sets the spend limit of one key. Adapters with Caps.PerKeyLimit implement it.
type Limiter interface {
	SetLimit(ctx context.Context, id string, limitUSD *float64, reset string) error
}

// Disabler disables a key and enables it again. Adapters with Caps.Disable implement it.
type Disabler interface {
	Disable(ctx context.Context, id string) error
	// Enable undoes Disable and sets this limit. With a nil limit, the key keeps its limit or has none.
	Enable(ctx context.Context, id string, limitUSD *float64, reset string) error
}

// AsLimiter returns p as a Limiter, or an error when p has no per-key limit.
func AsLimiter(p Provider) (Limiter, error) {
	if l, ok := p.(Limiter); ok && p.Caps().PerKeyLimit {
		return l, nil
	}
	return nil, fmt.Errorf("%s has no per-key limit", p.Name())
}

// AsDisabler returns p as a Disabler, or an error when p cannot disable keys.
func AsDisabler(p Provider) (Disabler, error) {
	if d, ok := p.(Disabler); ok && p.Caps().Disable {
		return d, nil
	}
	return nil, fmt.Errorf("%s keys cannot be disabled; delete the key instead", p.Name())
}

// Config is the connection of one provider account.
type Config struct {
	Key       string
	SelfID    string
	BaseURL   string
	Workspace string
	// Options are the provider-specific settings that Info.Options lists.
	Options map[string]string
	Timeout time.Duration
}

// Info describes a provider type.
type Info struct {
	Name  string
	Short string
	Caps  Caps
	// Options are the names that policy.yaml providers[].options and the management key secret can set.
	Options []string
	// CheckScopes returns an error for scopes that orkeys must not request. Nil: all scopes are allowed.
	CheckScopes func(scopes []string) error
	new         func(Config) Provider
}

var registry = []Info{
	{Name: "openrouter", Short: "or", Caps: openRouterCaps, new: func(c Config) Provider { return newOpenRouter(c) }},
	{Name: "deepinfra", Short: "di", Caps: deepInfraCaps, CheckScopes: checkDeepInfraScopes, new: func(c Config) Provider { return newDeepInfra(c) }},
	{Name: "openai", Short: "oa", Caps: openAICaps, Options: openAIOptions, new: func(c Config) Provider { return newOpenAI(c) }},
}

func Lookup(name string) (Info, bool) {
	for _, i := range registry {
		if i.Name == name {
			return i, true
		}
	}
	return Info{}, false
}

func Names() []string {
	out := make([]string, len(registry))
	for i, r := range registry {
		out[i] = r.Name
	}
	return out
}

// New returns an adapter of the provider type name.
func New(name string, c Config) (Provider, error) {
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	info, ok := Lookup(name)
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (known: %s)", name, strings.Join(Names(), ", "))
	}
	for k := range c.Options {
		if !slices.Contains(info.Options, k) {
			return nil, fmt.Errorf("%s: unknown option %q (known: %s)", name, k, strings.Join(info.Options, ", "))
		}
	}
	p := info.new(c)
	if err := checkCaps(p); err != nil {
		return nil, err
	}
	return p, nil
}

// checkCaps catches an adapter whose Caps promise an interface that it does not implement, or the reverse.
func checkCaps(p Provider) error {
	_, limiter := p.(Limiter)
	_, disabler := p.(Disabler)
	if c := p.Caps(); c.PerKeyLimit != limiter || c.Disable != disabler {
		return fmt.Errorf("provider %s: Caps PerKeyLimit=%t Disable=%t, but Limiter=%t Disabler=%t", p.Name(), c.PerKeyLimit, c.Disable, limiter, disabler)
	}
	return nil
}

// cleanupTimeout bounds the delete of a half-created key after the caller's context is cancelled.
const cleanupTimeout = 30 * time.Second

// detached returns a context for cleanup calls. It stays valid after ctx is cancelled.
// A SIGTERM during Create must not leave a key or a service account that nothing tracks.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
}

type Set map[string]Provider

func (s Set) Get(name string) (Provider, error) {
	p, ok := s[name]
	if !ok {
		return nil, fmt.Errorf("provider %q is not configured", name)
	}
	return p, nil
}

func Find(keys []Key, id string) *Key {
	for i := range keys {
		if keys[i].ID == id {
			return &keys[i]
		}
	}
	return nil
}

// SpendFor returns the spend in the current period of a limit reset: daily, weekly or monthly.
func (k *Key) SpendFor(reset string) *float64 {
	switch reset {
	case "daily":
		return k.SpendDayUSD
	case "weekly":
		return k.SpendWeekUSD
	}
	return k.SpendMonthUSD
}
