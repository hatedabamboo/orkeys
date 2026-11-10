package provider

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type Caps struct {
	NativeExpiry bool
	PerKeyLimit  bool
	LimitReset   bool
	LastUsed     bool
	Scopes       bool
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
	// UsageUSD only grows while the key is used: total usage where the provider has it, else spend this month.
	UsageUSD *float64
}

type Spec struct {
	Name       string
	LimitUSD   *float64
	LimitReset string
	ExpiresAt  *time.Time
	Scopes     []string
}

type Provider interface {
	Name() string
	Caps() Caps
	List(ctx context.Context) ([]Key, error)
	Create(ctx context.Context, s Spec) (Key, string, error)
	SetLimit(ctx context.Context, id string, limitUSD *float64, reset string) error
	Disable(ctx context.Context, id string) error
	// Enable undoes Disable. Where Disable sets the limit to 0, Enable does nothing; the caller sets the limit.
	Enable(ctx context.Context, id string) error
	Delete(ctx context.Context, id string) error
	SelfID() string
}

type Config struct {
	Key       string
	SelfID    string
	BaseURL   string
	Workspace string
	Timeout   time.Duration
}

type Info struct {
	Name  string
	Short string
	Caps  Caps
	new   func(Config) Provider
}

var registry = []Info{
	{Name: "openrouter", Short: "or", Caps: openRouterCaps, new: func(c Config) Provider { return newOpenRouter(c) }},
	{Name: "deepinfra", Short: "di", Caps: deepInfraCaps, new: func(c Config) Provider { return newDeepInfra(c) }},
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

func New(name string, c Config) (Provider, error) {
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	info, ok := Lookup(name)
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (known: %s)", name, strings.Join(Names(), ", "))
	}
	return info.new(c), nil
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
