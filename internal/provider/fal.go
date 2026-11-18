package provider

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hatedabamboo/orkeys/internal/fal"
	"github.com/hatedabamboo/orkeys/internal/spend"
)

type falAI struct {
	c      *fal.Client
	selfID string
	now    func() time.Time
}

// The Platform API has no disabled flag and no per-key limit. So the adapter is not a Limiter or a Disabler.
// Revoke is the only way to stop a key.
func newFal(c Config) *falAI {
	self := c.SelfID
	if self == "" {
		// A fal.ai key value is key_id:key_secret, so the management key gives its own ID.
		self = fal.KeyID(c.Key)
	}
	return &falAI{c: fal.New(c.BaseURL, c.Key, c.Timeout), selfID: self, now: time.Now}
}

func (f *falAI) Name() string   { return "fal" }
func (f *falAI) SelfID() string { return f.selfID }

// fal.ai has no per-key limit. LimitReset is true because orkeys computes the spend per day, week and month from
// the usage endpoint, so a budget of any period can be tracked in metrics.
// The API refuses POST /keys with 403 "required 'auth:keys:create'", also for a key with the FULL preset, and the
// dashboard cannot grant that permission. So a person makes the keys (ManualCreate). List, usage and revoke work.
var falCaps = Caps{NativeExpiry: true, LimitReset: true, Scopes: true, MaxExpiry: fal.MaxExpiry, ManualCreate: true}

// falOptions are the options of fal. console is the page where a person makes keys. Reminders show it.
var falOptions = []string{OptConsole}

func (f *falAI) Caps() Caps { return falCaps }

// KeyIDOf returns the key ID of a fal.ai key value: the part before the colon of key_id:key_secret.
func (f *falAI) KeyIDOf(value string) string { return fal.KeyID(value) }

func (f *falAI) List(ctx context.Context) ([]Key, error) {
	keys, err := f.c.ListKeys(ctx)
	if err != nil {
		return nil, err
	}
	now := f.now()
	costs, err := f.c.Usage(ctx, spend.Start(now))
	if err != nil {
		return nil, fmt.Errorf("usage: %w", err)
	}
	byKey := spend.ByKey(costs, now)
	out := make([]Key, 0, len(keys))
	for _, k := range keys {
		s := byKey[k.ID]
		d, w, m := s.Day, s.Week, s.Month
		out = append(out, Key{
			ID:            k.ID,
			Name:          k.Alias,
			CreatedAt:     k.Created(),
			ExpiresAt:     k.Expires(),
			SpendDayUSD:   &d,
			SpendWeekUSD:  &w,
			SpendMonthUSD: &m,
			UsageUSD:      &m,
		})
	}
	return out, nil
}

func (f *falAI) Create(ctx context.Context, s Spec) (Key, string, error) {
	if err := checkFalScopes(s.Scopes); err != nil {
		return Key{}, "", fmt.Errorf("scopes: %w", err)
	}
	r := fal.CreateRequest{Alias: s.Name}
	switch {
	case len(s.Scopes) == 0:
		r.Preset = fal.DefaultPreset
	case isFalPreset(s.Scopes[0]):
		r.Preset = s.Scopes[0]
	default:
		r.Permissions = s.Scopes
	}
	var exp *time.Time
	if s.ExpiresAt != nil {
		t := s.ExpiresAt.UTC().Truncate(time.Second)
		if d := t.Sub(f.now()); d <= 0 || d > fal.MaxExpiry {
			return Key{}, "", fmt.Errorf("expiry %s: fal allows at most %s from now", t.Format(time.RFC3339), fal.MaxExpiry)
		}
		r.ExpiresAt = t.Format(time.RFC3339)
		exp = &t
	}
	k, err := f.c.CreateKey(ctx, r)
	if err != nil {
		return Key{}, "", err
	}
	return Key{ID: k.ID, Name: s.Name, CreatedAt: f.now().UTC(), ExpiresAt: exp}, k.Key, nil
}

// Delete revokes the key. fal.ai keeps a record of the revoked key, but the list does not show it.
func (f *falAI) Delete(ctx context.Context, id string) error {
	err := f.c.RevokeKey(ctx, id)
	if fal.IsNotFound(err) {
		return nil
	}
	return err
}

// isFalPreset reports a preset name. Presets are upper case words. Permissions have colons: models:requests:submit.
func isFalPreset(s string) bool { return !strings.Contains(s, ":") }

// checkFalScopes accepts one preset or a list of permissions. It refuses FULL, the legacy ADMIN scope and the
// auth:keys permissions other than auth:keys:read: a key with them can create or revoke keys.
func checkFalScopes(scopes []string) error {
	for _, s := range scopes {
		if !isFalPreset(s) {
			if strings.HasPrefix(s, "auth:keys:") && s != "auth:keys:read" {
				return fmt.Errorf("permission %s is not allowed", s)
			}
			continue
		}
		switch {
		case s == "FULL" || s == "ADMIN":
			return fmt.Errorf("preset %s is not allowed", s)
		case !slices.Contains(fal.Presets, s):
			return fmt.Errorf("unknown preset %q (known: %s)", s, strings.Join(fal.Presets, ", "))
		case len(scopes) > 1:
			return fmt.Errorf("preset %s: give one preset or a list of permissions, not both", s)
		}
	}
	return nil
}
