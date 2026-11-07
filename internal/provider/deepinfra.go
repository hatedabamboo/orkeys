package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hatedabamboo/orkeys/internal/deepinfra"
	"github.com/hatedabamboo/orkeys/internal/httpx"
)

var defaultDeepInfraScopes = []string{"inference"}

type deepInfra struct {
	c      *deepinfra.Client
	selfID string
}

func newDeepInfra(c Config) *deepInfra {
	return &deepInfra{c: deepinfra.New(c.BaseURL, c.Key, c.Timeout), selfID: c.SelfID}
}

func (d *deepInfra) Name() string   { return "deepinfra" }
func (d *deepInfra) SelfID() string { return d.selfID }

var deepInfraCaps = Caps{PerKeyLimit: true, Scopes: true}

func (d *deepInfra) Caps() Caps { return deepInfraCaps }

func (d *deepInfra) List(ctx context.Context) ([]Key, error) {
	limits, err := d.c.KeyLimits(ctx)
	if err != nil {
		return nil, fmt.Errorf("key limits: %w", err)
	}
	tokens, err := d.c.ListTokens(ctx)
	if deepinfra.IsForbidden(err) {
		tokens = deepinfra.TokensFromLimits(limits)
	} else if err != nil {
		return nil, err
	}
	byID := make(map[string]deepinfra.KeyLimit, len(limits))
	for _, l := range limits {
		byID[l.TokenID] = l
	}
	out := make([]Key, 0, len(tokens))
	for _, t := range tokens {
		k := Key{ID: t.TokenID, Name: t.Name}
		if t.CreatedAt > 0 {
			k.CreatedAt = t.Created()
		}
		if l, ok := byID[t.TokenID]; ok {
			k.LimitUSD = l.Limit
			k.SpendMonthUSD = l.Spend
			k.UsageUSD = l.Spend
			k.Disabled = l.Limit != nil && *l.Limit == 0
			if l.Limit != nil {
				k.LimitReset = "monthly"
			}
		}
		out = append(out, k)
	}
	return out, nil
}

func (d *deepInfra) Create(ctx context.Context, s Spec) (Key, string, error) {
	scopes := s.Scopes
	if len(scopes) == 0 {
		scopes = defaultDeepInfraScopes
	}
	t, err := d.c.CreateToken(ctx, s.Name, scopes)
	if err != nil {
		return Key{}, "", err
	}
	if s.LimitUSD != nil {
		if err := d.c.SetKeyLimit(ctx, t.TokenID, s.LimitUSD); err != nil {
			if derr := d.c.DeleteToken(ctx, t.TokenID); derr != nil {
				return Key{}, "", fmt.Errorf("set limit: %w; delete new token %s also failed: %w", err, t.TokenID, derr)
			}
			return Key{}, "", fmt.Errorf("set limit (new token deleted): %w", err)
		}
	}
	return Key{ID: t.TokenID, Name: t.Name, CreatedAt: t.Created(), LimitUSD: s.LimitUSD}, t.Secret, nil
}

func (d *deepInfra) SetLimit(ctx context.Context, id string, limit *float64, _ string) error {
	return d.c.SetKeyLimit(ctx, id, limit)
}

func (d *deepInfra) Disable(ctx context.Context, id string) error {
	zero := 0.0
	return d.c.SetKeyLimit(ctx, id, &zero)
}

func (d *deepInfra) Delete(ctx context.Context, id string) error {
	err := d.c.DeleteToken(ctx, id)
	if httpx.StatusOf(err) == http.StatusNotFound {
		return nil
	}
	return err
}
