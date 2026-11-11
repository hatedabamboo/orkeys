package provider

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/hatedabamboo/orkeys/internal/openai"
)

// errOpenAIDisable: the Admin API has no disabled flag and no per-key limit. Delete is the only revoke.
var errOpenAIDisable = errors.New("openai keys cannot be disabled or enabled; delete the key instead")

var errOpenAILimit = errors.New("openai has no per-key limit")

type openAI struct {
	c      *openai.Client
	selfID string
	now    func() time.Time
}

func newOpenAI(c Config) *openAI {
	return &openAI{c: openai.New(c.BaseURL, c.Key, c.Workspace, c.Timeout), selfID: c.SelfID, now: time.Now}
}

func (o *openAI) Name() string   { return "openai" }
func (o *openAI) SelfID() string { return o.selfID }

// OpenAI has no per-key limit. LimitReset is true because orkeys computes the spend per day, week and month from
// the costs endpoint, so a budget of any period can be tracked in metrics.
var openAICaps = Caps{NativeExpiry: true, LimitReset: true, LastUsed: true, Scopes: true, MaxExpiry: openai.MaxExpiry}

func (o *openAI) Caps() Caps { return openAICaps }

func (o *openAI) List(ctx context.Context) ([]Key, error) {
	keys, err := o.c.ListKeys(ctx)
	if err != nil {
		return nil, err
	}
	now := o.now()
	costs, err := o.c.Costs(ctx, openai.SpendStart(now))
	if err != nil {
		return nil, fmt.Errorf("costs: %w", err)
	}
	spend := openai.SpendByKey(costs, now)
	out := make([]Key, 0, len(keys))
	for _, k := range keys {
		s := spend[k.ID]
		d, w, m := s.Day, s.Week, s.Month
		out = append(out, Key{
			ID:            k.ID,
			Name:          k.Name,
			Disabled:      k.OwnerProjectAccess == "inactive",
			CreatedAt:     time.Unix(k.CreatedAt, 0).UTC(),
			ExpiresAt:     unixPtr(k.ExpiresAt),
			LastUsedAt:    unixPtr(k.LastUsedAt),
			SpendDayUSD:   &d,
			SpendWeekUSD:  &w,
			SpendMonthUSD: &m,
			UsageUSD:      &m,
		})
	}
	return out, nil
}

func unixPtr(v *int64) *time.Time {
	if v == nil || *v == 0 {
		return nil
	}
	t := time.Unix(*v, 0).UTC()
	return &t
}

// Create makes one service account per key, so that Delete can remove the account with its key.
// OpenAI creates a default key with the account. That key has no name, scopes or expiry, so orkeys creates a
// second key with them and deletes the default key.
func (o *openAI) Create(ctx context.Context, s Spec) (Key, string, error) {
	ks := openai.KeySpec{Name: s.Name, Scopes: s.Scopes}
	if s.ExpiresAt != nil {
		secs := int64(math.Ceil(s.ExpiresAt.Sub(o.now()).Seconds()))
		if secs < 1 || time.Duration(secs)*time.Second > openai.MaxExpiry {
			return Key{}, "", fmt.Errorf("expiry %s: openai allows 1s to %s", s.ExpiresAt.UTC().Format(time.RFC3339), openai.MaxExpiry)
		}
		ks.ExpiresInSeconds = &secs
	}
	sa, err := o.c.CreateServiceAccount(ctx, s.Name)
	if err != nil {
		return Key{}, "", fmt.Errorf("create service account: %w", err)
	}
	undo := func(err error) (Key, string, error) {
		if derr := o.c.DeleteServiceAccount(ctx, sa.ID); derr != nil {
			return Key{}, "", fmt.Errorf("%w; delete new service account %s also failed: %w", err, sa.ID, derr)
		}
		return Key{}, "", fmt.Errorf("%w (new service account deleted)", err)
	}
	k, err := o.c.CreateServiceAccountKey(ctx, sa.ID, ks)
	if err != nil {
		return undo(fmt.Errorf("create key: %w", err))
	}
	if sa.APIKey != nil && sa.APIKey.ID != "" && sa.APIKey.ID != k.ID {
		if err := o.c.DeleteKey(ctx, sa.APIKey.ID); err != nil && !openai.IsNotFound(err) {
			return undo(fmt.Errorf("delete default key %s: %w", sa.APIKey.ID, err))
		}
	}
	return Key{
		ID:        k.ID,
		Name:      k.Name,
		CreatedAt: time.Unix(k.CreatedAt, 0).UTC(),
		ExpiresAt: unixPtr(k.ExpiresAt),
	}, k.Value, nil
}

func (o *openAI) SetLimit(context.Context, string, *float64, string) error { return errOpenAILimit }

func (o *openAI) Disable(context.Context, string) error { return errOpenAIDisable }

func (o *openAI) Enable(context.Context, string) error { return errOpenAIDisable }

// Delete deletes the key. It also deletes the owning service account when the account has no other keys, so an
// imported key of a shared service account does not take the other keys with it.
func (o *openAI) Delete(ctx context.Context, id string) error {
	keys, err := o.c.ListKeys(ctx)
	if err != nil {
		return err
	}
	var sa string
	found := false
	for _, k := range keys {
		if k.ID == id {
			sa, found = k.ServiceAccountID(), true
		}
	}
	if !found {
		return nil
	}
	if err := o.c.DeleteKey(ctx, id); err != nil && !openai.IsNotFound(err) {
		return err
	}
	if sa == "" {
		return nil
	}
	for _, k := range keys {
		if k.ID != id && k.ServiceAccountID() == sa {
			return nil
		}
	}
	if err := o.c.DeleteServiceAccount(ctx, sa); err != nil && !openai.IsNotFound(err) {
		return fmt.Errorf("delete service account %s: %w", sa, err)
	}
	return nil
}
