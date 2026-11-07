package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hatedabamboo/orkeys/internal/manifest"
	"github.com/hatedabamboo/orkeys/internal/provider"
	"github.com/hatedabamboo/orkeys/internal/secrets"
	"github.com/hatedabamboo/orkeys/internal/state"
)

func (e *Engine) Apply(ctx context.Context, plan *Plan) error {
	var errs []error
	for _, a := range plan.Actions {
		e.logf("%s %s: %s", a.Type, a.Name, a.Detail)
		if err := e.applyOne(ctx, a); err != nil {
			e.logf("  failed: %v", err)
			errs = append(errs, fmt.Errorf("%s %s: %w", a.Type, a.Name, err))
		}
	}
	return errors.Join(errs...)
}

func (e *Engine) applyOne(ctx context.Context, a Action) error {
	switch a.Type {
	case ActCreate, ActRecreate:
		_, err := e.issue(ctx, a.Key, a.State, "")
		return err

	case ActImport:
		return e.adopt(ctx, a.Key, a.Current)

	case ActReissue:
		_, err := e.startRotation(ctx, a.Key, a.State, "reissue: "+a.Detail)
		return err

	case ActUpdateLimit:
		p, err := e.Providers.Get(a.Provider)
		if err != nil {
			return err
		}
		limit := a.Key.Budget.Limit
		if err := p.SetLimit(ctx, a.State.KeyID, &limit, a.Key.Budget.ResetOrDefault()); err != nil {
			return err
		}
		copyMeta(a.State, e.stateFor(a.Key))
		return e.putState(ctx, a.State)

	case ActUpdateState:
		copyMeta(a.State, e.stateFor(a.Key))
		return e.putState(ctx, a.State)

	case ActDelete:
		return e.remove(ctx, a.State, "manifest removed")
	}
	return fmt.Errorf("unknown action %q", a.Type)
}

func (e *Engine) remove(ctx context.Context, st *state.State, reason string) error {
	var errs []error
	for _, id := range st.KnownIDs() {
		if err := e.revoke(ctx, st.Provider, id); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	if st.SecretPath != "" {
		if err := e.Secrets.DeleteField(ctx, st.SecretPath, st.SecretField); err != nil {
			return err
		}
	}
	if err := e.State.DeleteState(ctx, st.Name); err != nil {
		return err
	}
	e.emit(st, LevelInfo, EventKeyRevoked, "key revoked: %s", reason)
	return nil
}

func (e *Engine) startRotation(ctx context.Context, k *manifest.Key, cur *state.State, reason string) (*state.State, error) {
	st, err := e.issue(ctx, k, cur, reason)
	if err != nil {
		return nil, err
	}
	e.emit(st, LevelInfo, EventRotationStart, "rotation started (%s); consumers sync next", reason)
	return st, nil
}

// adopt takes over an existing provider key. The user has already written its value to the secret.
func (e *Engine) adopt(ctx context.Context, k *manifest.Key, cur *provider.Key) error {
	p, err := e.Providers.Get(k.Provider)
	if err != nil {
		return err
	}
	field := k.SecretField()
	value, err := secrets.ReadField(ctx, e.Secrets, k.Secret.Path, field)
	if err != nil {
		return fmt.Errorf("write the current key to %s#%s first: %w", k.Secret.Path, field, err)
	}
	if p.Caps().PerKeyLimit {
		limit := k.Budget.Limit
		if err := p.SetLimit(ctx, cur.ID, &limit, k.Budget.ResetOrDefault()); err != nil {
			return fmt.Errorf("set limit: %w", err)
		}
	}
	now := e.now()
	st := e.stateFor(k)
	st.KeyID, st.CreatedAt, st.ExpiresAt = cur.ID, now, now.Add(k.TTL.Duration)
	if cur.ExpiresAt != nil && cur.ExpiresAt.Before(st.ExpiresAt) {
		st.ExpiresAt = *cur.ExpiresAt
	}
	if err := e.putState(ctx, st); err != nil {
		return err
	}
	meta := map[string]string{"orkeys-key": k.Name, "orkeys-key-id": cur.ID, "orkeys-team": k.Team}
	if err := e.Secrets.Put(ctx, st.SecretPath, field, value, meta); err != nil {
		e.logf("%s: write secret metadata: %v", k.Name, err)
	}
	e.emit(st, LevelInfo, EventKeyImported, "key %s (%s) imported from %s; expires %s",
		short(cur.ID), cur.Name, st.SecretPath, st.ExpiresAt.Format(time.DateOnly))
	return nil
}
