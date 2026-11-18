package provider

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hatedabamboo/orkeys/internal/elevenlabs"
	"github.com/hatedabamboo/orkeys/internal/httpx"
)

// Options of elevenlabs. service_account is the service_account_user_id that holds the keys. usd_per_credit is the
// price of one credit: ElevenLabs counts limits and usage in credits, orkeys in USD.
const (
	OptServiceAccount = "service_account"
	OptUSDPerCredit   = "usd_per_credit"
)

var elevenLabsOptions = []string{OptServiceAccount, OptUSDPerCredit}

// deniedElevenLabsScopes give control of keys, members or the workspace.
var deniedElevenLabsScopes = []string{
	"all", "service_account_write", "create_user_api_key", "group_members_manage",
	"workspace_members_invite", "workspace_members_remove", "workspace_write",
}

// The key limit (character_limit) is in credits per billing period of the workspace. The period is not the calendar
// month, so LimitReset is false and a budget is monthly. The API has no last use.
var elevenLabsCaps = Caps{NativeExpiry: true, PerKeyLimit: true, Scopes: true, Disable: true, MaxExpiry: elevenlabs.MaxExpiry}

type elevenLabs struct {
	c *elevenlabs.Client
	// selfHash finds the management key in List when the config has no self ID.
	selfHash string
	mu       sync.Mutex
	selfID   string
	// usdPerCredit converts the budget to credits. Zero when cfgErr is set.
	usdPerCredit float64
	cfgErr       error
	now          func() time.Time
}

func newElevenLabs(c Config) *elevenLabs {
	sa := c.Options[OptServiceAccount]
	if sa == "" {
		sa = c.Workspace
	}
	e := &elevenLabs{c: elevenlabs.New(c.BaseURL, c.Key, sa, c.Timeout), selfID: c.SelfID, now: time.Now}
	if c.SelfID == "" && c.Key != "" {
		e.selfHash = elevenlabs.KeyHash(c.Key)
	}
	var errs []error
	if sa == "" {
		errs = append(errs, fmt.Errorf("option %s (or workspace) is not set", OptServiceAccount))
	}
	price, err := strconv.ParseFloat(strings.TrimSpace(c.Options[OptUSDPerCredit]), 64)
	switch {
	case c.Options[OptUSDPerCredit] == "":
		errs = append(errs, fmt.Errorf("option %s is not set", OptUSDPerCredit))
	case err != nil || price <= 0 || math.IsInf(price, 0) || math.IsNaN(price):
		errs = append(errs, fmt.Errorf("option %s: %q is not a positive number", OptUSDPerCredit, c.Options[OptUSDPerCredit]))
	default:
		e.usdPerCredit = price
	}
	if len(errs) > 0 {
		e.cfgErr = fmt.Errorf("elevenlabs: %w", errors.Join(errs...))
	}
	return e
}

func (e *elevenLabs) Name() string { return "elevenlabs" }

// SelfID is the configured self ID, or the ID of the listed key with the hash of the management key. A management
// key in another service account has no ID here.
func (e *elevenLabs) SelfID() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.selfID
}
func (e *elevenLabs) Caps() Caps { return elevenLabsCaps }

// credits returns the credit limit for a USD limit. It rounds down, so the key never gets more than the budget.
func (e *elevenLabs) credits(usd float64) int64 {
	return int64(math.Floor(usd/e.usdPerCredit + 1e-9))
}

func (e *elevenLabs) usd(credits *int64) *float64 {
	if credits == nil {
		return nil
	}
	v := float64(*credits) * e.usdPerCredit
	return &v
}

func (e *elevenLabs) List(ctx context.Context) ([]Key, error) {
	if e.cfgErr != nil {
		return nil, e.cfgErr
	}
	keys, err := e.c.ListKeys(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Key, 0, len(keys))
	for _, k := range keys {
		if e.selfHash != "" && strings.EqualFold(k.HashedKey, e.selfHash) {
			e.mu.Lock()
			e.selfID = k.ID
			e.mu.Unlock()
		}
		pk := Key{
			ID:        k.ID,
			Name:      k.Name,
			Disabled:  k.Disabled,
			CreatedAt: k.Created(),
			ExpiresAt: k.Expires(),
			LimitUSD:  e.usd(k.CharacterLimit),
		}
		if pk.LimitUSD != nil {
			pk.LimitReset = "monthly"
		}
		used := int64(0)
		if k.CharacterCount != nil {
			used = *k.CharacterCount
		}
		pk.SpendMonthUSD = e.usd(&used)
		pk.UsageUSD = e.usd(&used)
		out = append(out, pk)
	}
	return out, nil
}

func (e *elevenLabs) Create(ctx context.Context, s Spec) (Key, string, error) {
	if e.cfgErr != nil {
		return Key{}, "", e.cfgErr
	}
	scopes := s.Scopes
	if len(scopes) == 0 {
		scopes = elevenlabs.DefaultPermissions
	}
	if err := checkElevenLabsScopes(scopes); err != nil {
		return Key{}, "", fmt.Errorf("scopes: %w", err)
	}
	r := elevenlabs.CreateRequest{Name: s.Name, Permissions: scopes}
	var exp *time.Time
	if s.ExpiresAt != nil {
		t := s.ExpiresAt.UTC().Truncate(time.Second)
		if err := elevenlabs.CheckExpiry(t, e.now()); err != nil {
			return Key{}, "", err
		}
		u := t.Unix()
		r.ExpiresAtUnix, exp = &u, &t
	}
	var limit *float64
	if s.LimitUSD != nil {
		c := e.credits(*s.LimitUSD)
		r.CharacterLimit = &c
		limit = e.usd(&c)
	}
	k, err := e.c.CreateKey(ctx, r)
	if err != nil {
		return Key{}, "", err
	}
	pk := Key{ID: k.ID, Name: s.Name, CreatedAt: e.now().UTC(), ExpiresAt: exp, LimitUSD: limit}
	if limit != nil {
		pk.LimitReset = "monthly"
	}
	return pk, k.Value, nil
}

// limitField returns the character_limit update value: credits, or "clear" for no limit.
func (e *elevenLabs) limitField(limit *float64) any {
	if limit == nil {
		return elevenlabs.Clear
	}
	return e.credits(*limit)
}

func (e *elevenLabs) SetLimit(ctx context.Context, id string, limit *float64, _ string) error {
	if e.cfgErr != nil {
		return e.cfgErr
	}
	return e.c.UpdateKey(ctx, id, elevenlabs.Update{"character_limit": e.limitField(limit)})
}

func (e *elevenLabs) Disable(ctx context.Context, id string) error {
	if e.cfgErr != nil {
		return e.cfgErr
	}
	return e.c.DisableKey(ctx, id)
}

// Enable sets is_enabled and the limit in one request. With a nil limit, the key keeps its limit.
// It refuses a key that ElevenLabs disabled because the key was public: with track_disabled, apply would enable a
// leaked key again.
func (e *elevenLabs) Enable(ctx context.Context, id string, limit *float64, _ string) error {
	if e.cfgErr != nil {
		return e.cfgErr
	}
	keys, err := e.c.ListKeys(ctx)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if k.ID == id && k.DisableReason != nil && *k.DisableReason == elevenlabs.ReasonExposed {
			return fmt.Errorf("key %s: elevenlabs disabled it because it was public (%s); rotate the key", id, elevenlabs.ReasonExposed)
		}
	}
	u := elevenlabs.Update{"is_enabled": true}
	if limit != nil {
		u["character_limit"] = e.limitField(limit)
	}
	return e.c.UpdateKey(ctx, id, u)
}

// Delete deletes the key. A key that does not exist gives 404 (workspace_api_key_not_found).
func (e *elevenLabs) Delete(ctx context.Context, id string) error {
	if e.cfgErr != nil {
		return e.cfgErr
	}
	err := e.c.DeleteKey(ctx, id)
	if httpx.StatusOf(err) == http.StatusNotFound {
		return nil
	}
	return err
}

// checkElevenLabsScopes refuses "all" and the permissions that control keys, members or the workspace: a key with
// them can make keys or add members.
func checkElevenLabsScopes(scopes []string) error {
	for _, s := range scopes {
		if slices.Contains(deniedElevenLabsScopes, s) {
			return fmt.Errorf("permission %s is not allowed", s)
		}
	}
	return nil
}
