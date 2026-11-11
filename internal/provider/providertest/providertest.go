// Package providertest is the contract test of a provider adapter.
// Run it against a fake of the provider API: an httptest server that keeps keys in memory.
// A new adapter must pass it before the engine uses it.
package providertest

import (
	"context"
	"testing"
	"time"

	"github.com/hatedabamboo/orkeys/internal/provider"
)

// Run checks the behavior that the engine needs from p:
//
//   - Create returns an ID and a secret, and List shows the new key with its name.
//   - With Caps.PerKeyLimit, the adapter is a Limiter, and List shows the new limit.
//   - With Caps.Disable, the adapter is a Disabler. Disable and Enable show in List. Enable sets the limit.
//   - Delete removes the key, and a second Delete of the same key is not an error.
//   - SelfID is not the new key.
func Run(t *testing.T, p provider.Provider) {
	t.Helper()
	ctx := context.Background()
	caps := p.Caps()

	_, isLimiter := p.(provider.Limiter)
	_, isDisabler := p.(provider.Disabler)
	if caps.PerKeyLimit != isLimiter || caps.Disable != isDisabler {
		t.Fatalf("caps PerKeyLimit=%t Disable=%t, but Limiter=%t Disabler=%t", caps.PerKeyLimit, caps.Disable, isLimiter, isDisabler)
	}

	before, err := p.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	limit := 10.0
	spec := provider.Spec{Name: "providertest-key", LimitUSD: &limit, LimitReset: "monthly"}
	if caps.NativeExpiry {
		exp := time.Now().Add(24 * time.Hour)
		spec.ExpiresAt = &exp
	}
	k, secret, err := p.Create(ctx, spec)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if k.ID == "" || secret == "" {
		t.Fatalf("create returned id %q and an empty secret: %t", k.ID, secret == "")
	}
	if k.ID == p.SelfID() {
		t.Fatalf("new key has the id of the management key: %s", k.ID)
	}
	got := find(t, p, k.ID)
	if got == nil {
		t.Fatalf("list after create: key %s not found", k.ID)
	}
	if got.Name != spec.Name {
		t.Fatalf("list after create: name %q, want %q", got.Name, spec.Name)
	}
	if after, _ := p.List(ctx); len(after) != len(before)+1 {
		t.Fatalf("list after create: %d keys, want %d", len(after), len(before)+1)
	}

	if caps.PerKeyLimit {
		l, _ := provider.AsLimiter(p)
		more := 20.0
		if err := l.SetLimit(ctx, k.ID, &more, "monthly"); err != nil {
			t.Fatalf("set limit: %v", err)
		}
		if got := find(t, p, k.ID); got.LimitUSD == nil || *got.LimitUSD != more {
			t.Fatalf("list after set limit: limit %v, want %v", got.LimitUSD, more)
		}
	}

	if caps.Disable {
		d, _ := provider.AsDisabler(p)
		if err := d.Disable(ctx, k.ID); err != nil {
			t.Fatalf("disable: %v", err)
		}
		if got := find(t, p, k.ID); !got.Disabled {
			t.Fatal("list after disable: key is not disabled")
		}
		again := 30.0
		if err := d.Enable(ctx, k.ID, &again, "monthly"); err != nil {
			t.Fatalf("enable: %v", err)
		}
		got := find(t, p, k.ID)
		if got.Disabled {
			t.Fatal("list after enable: key is still disabled")
		}
		if caps.PerKeyLimit && (got.LimitUSD == nil || *got.LimitUSD != again) {
			t.Fatalf("list after enable: limit %v, want %v", got.LimitUSD, again)
		}
	}

	if err := p.Delete(ctx, k.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if find(t, p, k.ID) != nil {
		t.Fatal("list after delete: key is still there")
	}
	if err := p.Delete(ctx, k.ID); err != nil {
		t.Fatalf("second delete of the same key: %v", err)
	}
}

func find(t *testing.T, p provider.Provider, id string) *provider.Key {
	t.Helper()
	keys, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	return provider.Find(keys, id)
}
