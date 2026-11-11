package provider

import (
	"context"
	"time"

	"github.com/hatedabamboo/orkeys/internal/openrouter"
)

type openRouter struct {
	c         *openrouter.Client
	workspace string
	selfID    string
}

func newOpenRouter(c Config) *openRouter {
	return &openRouter{
		c:         openrouter.New(c.BaseURL, c.Key, c.Timeout, "orkeys"),
		workspace: c.Workspace,
		selfID:    c.SelfID,
	}
}

func (o *openRouter) Name() string   { return "openrouter" }
func (o *openRouter) SelfID() string { return o.selfID }

var openRouterCaps = Caps{NativeExpiry: true, PerKeyLimit: true, LimitReset: true, LastUsed: true, Disable: true}

func (o *openRouter) Caps() Caps { return openRouterCaps }

func (o *openRouter) List(ctx context.Context) ([]Key, error) {
	keys, err := o.c.ListAll(ctx, openrouter.ListOptions{IncludeDisabled: true, WorkspaceID: o.workspace})
	if err != nil {
		return nil, err
	}
	out := make([]Key, 0, len(keys))
	for _, k := range keys {
		out = append(out, fromOpenRouter(k))
	}
	return out, nil
}

func fromOpenRouter(k openrouter.Key) Key {
	month, week, day, usage := k.UsageMonthly, k.UsageWeekly, k.UsageDaily, k.Usage
	out := Key{
		UsageUSD:      &usage,
		ID:            k.Hash,
		Name:          k.Name,
		Disabled:      k.Disabled,
		CreatedAt:     parseTime(&k.CreatedAt).UTC(),
		ExpiresAt:     parseTimePtr(k.ExpiresAt),
		LastUsedAt:    parseTimePtr(k.LastUsedAt),
		LimitUSD:      k.Limit,
		SpendMonthUSD: &month,
		SpendWeekUSD:  &week,
		SpendDayUSD:   &day,
	}
	if k.LimitReset != nil {
		out.LimitReset = *k.LimitReset
	}
	return out
}

func (o *openRouter) Create(ctx context.Context, s Spec) (Key, string, error) {
	req := openrouter.CreateRequest{Name: s.Name, Limit: s.LimitUSD, WorkspaceID: o.workspace}
	if s.LimitReset != "" && s.LimitUSD != nil {
		r := s.LimitReset
		req.LimitReset = &r
	}
	if s.ExpiresAt != nil {
		e := s.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z")
		req.ExpiresAt = &e
	}
	resp, err := o.c.Create(ctx, req)
	if err != nil {
		return Key{}, "", err
	}
	return fromOpenRouter(resp.Data), resp.Key, nil
}

func (o *openRouter) SetLimit(ctx context.Context, id string, limit *float64, reset string) error {
	req := openrouter.UpdateRequest{Limit: openrouter.Null[float64](), LimitReset: openrouter.Null[string]()}
	if limit != nil {
		req.Limit = openrouter.Value(*limit)
		if reset != "" {
			req.LimitReset = openrouter.Value(reset)
		}
	}
	_, err := o.c.Update(ctx, id, req)
	return err
}

func (o *openRouter) Disable(ctx context.Context, id string) error {
	t := true
	_, err := o.c.Update(ctx, id, openrouter.UpdateRequest{Disabled: &t})
	return err
}

func (o *openRouter) Enable(ctx context.Context, id string) error {
	f := false
	_, err := o.c.Update(ctx, id, openrouter.UpdateRequest{Disabled: &f})
	return err
}

func (o *openRouter) Delete(ctx context.Context, id string) error {
	err := o.c.Delete(ctx, id)
	if openrouter.IsNotFound(err) {
		return nil
	}
	return err
}

func parseTime(s *string) time.Time {
	if t := parseTimePtr(s); t != nil {
		return *t
	}
	return time.Time{}
}

func parseTimePtr(s *string) *time.Time {
	if s == nil || *s == "" {
		return nil
	}
	for _, l := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(l, *s); err == nil {
			t = t.UTC()
			return &t
		}
	}
	return nil
}
