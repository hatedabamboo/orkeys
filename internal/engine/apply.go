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

// ApplyOptions changes the safety limits of one apply.
type ApplyOptions struct {
	// AllowDeletes raises policy max_deletes for this run.
	AllowDeletes int
}

// Apply runs the actions of plan in order. It continues after a failed action and returns all errors.
// It refuses a plan with more deletes than max_deletes or opt.AllowDeletes. The check runs before the first change.
// A wrong checkout or a loader bug shows every key as removed.
func (e *Engine) Apply(ctx context.Context, plan *Plan, opt ApplyOptions) error {
	if err := e.ready(); err != nil {
		return err
	}
	if n, limit := plan.Deletes(), max(e.Inv.Policy.DeleteLimit(), opt.AllowDeletes); n > limit {
		return fmt.Errorf("plan revokes %d keys, more than the limit %d: check the plan, then run apply --allow-deletes %d", n, limit, n)
	}
	var errs []error
	for i, a := range plan.Actions {
		// The check runs between actions. An action that stops halfway leaves more to repair.
		if err := ctx.Err(); err != nil {
			errs = append(errs, fmt.Errorf("stopped before %d of %d actions: %w", len(plan.Actions)-i, len(plan.Actions), err))
			break
		}
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
		st, err := e.issue(ctx, a.Key, a.State, "")
		if st == nil {
			return err
		}
		return errors.Join(err, e.syncPushAndSave(ctx, a.Key, st))

	case ActImport:
		return e.adopt(ctx, a.Key, a.Current)

	case ActTrack:
		return e.track(ctx, a.Key, a.State, a.Current)

	case ActUntrack:
		if err := e.State.DeleteState(ctx, a.State.Name); err != nil {
			return err
		}
		e.emit(a.State, LevelInfo, EventKeyUntracked, "manifest removed; key %s stays at the provider", short(a.State.KeyID))
		return nil

	case ActResume:
		_, err := e.resume(ctx, a.Key, a.State)
		return err

	case ActSyncConsumers:
		return e.syncPushAndSave(ctx, a.Key, a.State)

	case ActReissue:
		_, err := e.startRotation(ctx, a.Key, a.State, "reissue: "+a.Detail)
		return err

	case ActUpdateLimit:
		l, err := e.limiter(a.Provider)
		if err != nil {
			return err
		}
		limit := a.Key.Budget.Limit
		if err := l.SetLimit(ctx, a.State.KeyID, &limit, a.Key.Budget.ResetOrDefault()); err != nil {
			return err
		}
		copyMeta(a.State, e.stateFor(a.Key))
		return e.putState(ctx, a.State)

	case ActUpdateState:
		copyMeta(a.State, e.stateFor(a.Key))
		return e.putState(ctx, a.State)

	case ActDisable:
		d, err := e.disabler(a.Provider)
		if err != nil {
			return err
		}
		if err := d.Disable(ctx, a.State.KeyID); err != nil {
			return err
		}
		e.emit(a.State, LevelInfo, EventKeyDisabled, "key %s disabled: %s", short(a.State.KeyID), a.Detail)
		a.State.Disabled = a.Key.Disabled
		return e.putState(ctx, a.State)

	case ActEnable:
		d, err := e.disabler(a.Provider)
		if err != nil {
			return err
		}
		var limit *float64
		if _, lerr := e.limiter(a.Provider); lerr == nil {
			v := a.Key.Budget.Limit
			limit = &v
		}
		if err := d.Enable(ctx, a.State.KeyID, limit, a.Key.Budget.ResetOrDefault()); err != nil {
			return err
		}
		e.emit(a.State, LevelInfo, EventKeyEnabled, "key %s enabled: %s", short(a.State.KeyID), a.Detail)
		a.State.Disabled = a.Key.Disabled
		return e.putState(ctx, a.State)

	case ActDelete:
		return e.remove(ctx, a.State, "manifest removed")
	}
	return fmt.Errorf("unknown action %q", a.Type)
}

func (e *Engine) remove(ctx context.Context, st *state.State, reason string) error {
	if st.TrackOnly {
		return fmt.Errorf("%s: tracked key %s; orkeys does not revoke it", st.Name, short(st.KeyID))
	}
	// The push targets go first. If their delete fails, no key is revoked yet. The next run tries again.
	if len(st.Pushed) > 0 {
		err := e.syncPush(ctx, nil, st)
		if serr := e.putState(ctx, st); serr != nil {
			err = errors.Join(err, serr)
		}
		if err != nil {
			return err
		}
	}
	var errs []error
	for _, id := range st.KnownIDs() {
		if err := e.revoke(ctx, st.Provider, id); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	for _, r := range st.Secrets {
		if err := e.Secrets.DeleteField(ctx, r.Path, r.Field); err != nil {
			return fmt.Errorf("remove secret %s: %w", r, err)
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
	src := k.SecretTargets()[0]
	value, err := secrets.ReadField(ctx, e.Secrets, src.Path, src.Field)
	if err != nil {
		return fmt.Errorf("write the current key to %s#%s first: %w", src.Path, src.Field, err)
	}
	if l, err := provider.AsLimiter(p); err == nil {
		limit := k.Budget.Limit
		if err := l.SetLimit(ctx, cur.ID, &limit, k.Budget.ResetOrDefault()); err != nil {
			return fmt.Errorf("set limit: %w", err)
		}
	}
	now := e.now()
	st := e.stateFor(k)
	st.KeyID, st.CreatedAt, st.ExpiresAt = cur.ID, now, now.Add(k.TTL.Duration)
	if cur.ExpiresAt != nil && cur.ExpiresAt.Before(st.ExpiresAt) {
		st.ExpiresAt = *cur.ExpiresAt
	}
	// Write the value to all places before the state is saved: on failure the import runs again next time.
	// The first place has the value already: this write adds the metadata.
	if err := e.writeSecrets(ctx, k, st.Secrets, value, cur.ID, ""); err != nil {
		return fmt.Errorf("%s: %w", k.Name, err)
	}
	if err := e.putState(ctx, st); err != nil {
		return err
	}
	e.emit(st, LevelInfo, EventKeyImported, "key %s (%s) imported from %s; expires %s",
		short(cur.ID), cur.Name, src, st.ExpiresAt.Format(time.DateOnly))
	return errors.Join(e.syncPushAndSave(ctx, k, st), e.disableIfWanted(ctx, p, k, st))
}

// track saves the state of a tracked key. It reads no secret and changes nothing at the provider.
// orig is the current state when import.id changed, else nil.
func (e *Engine) track(ctx context.Context, k *manifest.Key, orig *state.State, cur *provider.Key) error {
	st := e.stateFor(k)
	st.KeyID, st.CreatedAt = cur.ID, e.now()
	if cur.ExpiresAt != nil {
		st.ExpiresAt = *cur.ExpiresAt
	}
	if orig != nil {
		st.Rev = orig.Rev
	}
	if err := e.putState(ctx, st); err != nil {
		return err
	}
	e.emit(st, LevelInfo, EventKeyTracked, "key %s (%s) tracked; orkeys exports its spend and does not change it", short(cur.ID), cur.Name)
	return nil
}

// disableIfWanted disables a new or adopted key when its manifest has disabled: true.
// On failure the state is already saved, so the next plan shows the disable again.
func (e *Engine) disableIfWanted(ctx context.Context, p provider.Provider, k *manifest.Key, st *state.State) error {
	if !k.Disabled {
		return nil
	}
	d, err := provider.AsDisabler(p)
	if err != nil {
		return fmt.Errorf("%s: %w", k.Name, err)
	}
	if err := d.Disable(ctx, st.KeyID); err != nil {
		return fmt.Errorf("%s: disable key %s: %w", k.Name, short(st.KeyID), err)
	}
	e.emit(st, LevelInfo, EventKeyDisabled, "key %s disabled: disabled: true in the manifest", short(st.KeyID))
	return nil
}

func (e *Engine) limiter(name string) (provider.Limiter, error) {
	p, err := e.Providers.Get(name)
	if err != nil {
		return nil, err
	}
	return provider.AsLimiter(p)
}

func (e *Engine) disabler(name string) (provider.Disabler, error) {
	p, err := e.Providers.Get(name)
	if err != nil {
		return nil, err
	}
	return provider.AsDisabler(p)
}
