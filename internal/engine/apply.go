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
		st, err := e.issue(ctx, a.Key, a.State, "")
		if st == nil {
			return err
		}
		return errors.Join(err, e.syncGitHubAndSave(ctx, a.Key, st))

	case ActImport:
		return e.adopt(ctx, a.Key, a.Current)

	case ActSyncGitHub:
		return e.syncGitHubAndSave(ctx, a.Key, a.State)

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

	case ActDisable:
		p, err := e.Providers.Get(a.Provider)
		if err != nil {
			return err
		}
		if err := p.Disable(ctx, a.State.KeyID); err != nil {
			return err
		}
		e.emit(a.State, LevelInfo, EventKeyDisabled, "key %s disabled: %s", short(a.State.KeyID), a.Detail)
		return nil

	case ActEnable:
		p, err := e.Providers.Get(a.Provider)
		if err != nil {
			return err
		}
		if err := p.Enable(ctx, a.State.KeyID); err != nil {
			return err
		}
		if p.Caps().PerKeyLimit {
			limit := a.Key.Budget.Limit
			if err := p.SetLimit(ctx, a.State.KeyID, &limit, a.Key.Budget.ResetOrDefault()); err != nil {
				return fmt.Errorf("set limit: %w", err)
			}
		}
		e.emit(a.State, LevelInfo, EventKeyEnabled, "key %s enabled: %s", short(a.State.KeyID), a.Detail)
		return nil

	case ActDelete:
		return e.remove(ctx, a.State, "manifest removed")
	}
	return fmt.Errorf("unknown action %q", a.Type)
}

func (e *Engine) remove(ctx context.Context, st *state.State, reason string) error {
	// Delete the GitHub secrets first: if that fails, nothing is revoked yet and the next run tries again.
	if len(st.GitHubSecrets) > 0 {
		err := e.syncGitHub(ctx, nil, st)
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
	return errors.Join(e.syncGitHubAndSave(ctx, k, st), e.disableIfWanted(ctx, p, k, st))
}

// disableIfWanted disables a new or adopted key when its manifest has disabled: true.
// On failure the state is already saved, so the next plan shows the disable again.
func (e *Engine) disableIfWanted(ctx context.Context, p provider.Provider, k *manifest.Key, st *state.State) error {
	if !k.Disabled {
		return nil
	}
	if err := p.Disable(ctx, st.KeyID); err != nil {
		return fmt.Errorf("%s: disable key %s: %w", k.Name, short(st.KeyID), err)
	}
	e.emit(st, LevelInfo, EventKeyDisabled, "key %s disabled: disabled: true in the manifest", short(st.KeyID))
	return nil
}
