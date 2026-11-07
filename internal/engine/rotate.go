package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hatedabamboo/orkeys/internal/manifest"
	"github.com/hatedabamboo/orkeys/internal/provider"
	"github.com/hatedabamboo/orkeys/internal/state"
)

// drain is the time after a successful restart before a use of the old key counts as "still in use".
const drain = 15 * time.Minute

type RotateOptions struct {
	Retry []string
	Now   []string
	Only  []string
}

func (e *Engine) Rotate(ctx context.Context, opt RotateOptions) error {
	states, err := e.State.ListStates(ctx)
	if err != nil {
		return err
	}
	lists, err := e.listAll(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, st := range states {
		if len(opt.Only) > 0 && !slices.Contains(opt.Only, st.Name) {
			continue
		}
		if err := e.rotateOne(ctx, st, lists[st.Provider], slices.Contains(opt.Retry, st.Name), slices.Contains(opt.Now, st.Name)); err != nil {
			e.logf("%s: %v", st.Name, err)
			errs = append(errs, fmt.Errorf("%s: %w", st.Name, err))
		}
	}
	return errors.Join(errs...)
}

func (e *Engine) rotateOne(ctx context.Context, st *state.State, keys []provider.Key, retry, force bool) error {
	now := e.now()
	kp := e.Inv.Policy.Kind(st.Kind)
	changed := false

	switch {
	case e.Inv.Policy.Requested(st.Kind, st.Owner):
		if !now.Before(st.ExpiresAt) {
			return e.remove(ctx, st, fmt.Sprintf("%s key of %s expired; request a new one if needed", st.Kind, st.Owner))
		}
		if kp != nil && !st.ExpiryNotified && kp.NotifyBefore.Duration > 0 && !now.Before(st.ExpiresAt.Add(-kp.NotifyBefore.Duration)) {
			st.ExpiryNotified = true
			changed = true
			e.emit(st, LevelInfo, EventExpiryNotice, "%s key of %s (%s) expires %s; it will not be renewed",
				st.Kind, st.Owner, st.Purpose, st.ExpiresAt.Format(time.DateOnly))
		}

	case kp == nil:
		return fmt.Errorf("kind %q is not in policy.yaml kinds", st.Kind)

	default:
		k := e.Inv.Key(st.Name)
		if k == nil {
			return nil
		}
		if retry && st.Rotation == state.RotationFailed {
			st.Rotation, st.RotationError = state.RotationPending, ""
			for i := range st.PendingRevokes {
				if st.PendingRevokes[i].Source == state.RevokeRotation {
					st.PendingRevokes[i].After = time.Time{}
				}
			}
			changed = true
		}
		due := !now.Before(st.ExpiresAt.Add(-kp.RotateBefore.Duration))
		if force && !due && len(st.PendingRevokes) > 0 {
			return fmt.Errorf("rotation already in progress: old key %s waits for revoke", short(st.PendingRevokes[0].KeyID))
		}
		if st.Rotation == state.RotationIdle && !hasUnsynced(st) && (due || force) {
			reason := "expires " + st.ExpiresAt.Format(time.DateOnly)
			if force && !due {
				reason = "manual rotation"
			}
			next, err := e.startRotation(ctx, k, st, reason)
			if err != nil {
				return err
			}
			*st = *next
			changed = false
		}
		if st.Rotation == state.RotationPending {
			if err := e.syncConsumers(ctx, k); err != nil {
				st.Rotation, st.RotationError = state.RotationFailed, err.Error()
				e.emit(st, LevelAlert, EventRotationFailed, "rotation failed: %v; the old key stays active; after the fix run: orkeys rotate --retry %s", err, st.Name)
				return e.putState(ctx, st)
			}
			grace := kp.RevokeGrace.Duration
			var ids []string
			for i := range st.PendingRevokes {
				if st.PendingRevokes[i].After.IsZero() {
					st.PendingRevokes[i].After = now.Add(grace)
					st.PendingRevokes[i].CheckFrom = now.Add(drain)
					ids = append(ids, short(st.PendingRevokes[i].KeyID))
				}
			}
			st.Rotation = state.RotationIdle
			changed = true
			e.emit(st, LevelInfo, EventRotationSynced, "consumers use the new key; old key %s will be revoked after %s",
				strings.Join(ids, ", "), now.Add(grace).Format("2006-01-02 15:04 UTC"))
		}
	}

	if st.Rotation != state.RotationFailed {
		c, err := e.processRevokes(ctx, st, keys, now)
		changed = changed || c
		if err != nil {
			return errors.Join(err, e.putState(ctx, st))
		}
	}
	if changed {
		return e.putState(ctx, st)
	}
	return nil
}

func hasUnsynced(st *state.State) bool {
	for _, p := range st.PendingRevokes {
		if p.After.IsZero() {
			return true
		}
	}
	return false
}

// usageEpsilon ignores rounding noise in provider spend reports.
const usageEpsilon = 0.0001

func (e *Engine) processRevokes(ctx context.Context, st *state.State, keys []provider.Key, now time.Time) (bool, error) {
	p, err := e.Providers.Get(st.Provider)
	if err != nil {
		return false, err
	}
	changed := false
	var keep []state.PendingRevoke
	for i, pr := range st.PendingRevokes {
		old := provider.Find(keys, pr.KeyID)
		var usage *float64
		if old != nil {
			usage = old.UsageUSD
		}
		if !pr.After.IsZero() && !now.Before(pr.CheckFrom) && pr.Baseline == nil && usage != nil {
			v := *usage
			pr.Baseline = &v
			changed = true
			if !now.Before(pr.After) {
				keep = append(keep, pr)
				e.logf("%s: recorded usage of old key %s; revoke on the next run if it stays the same", st.Name, short(pr.KeyID))
				continue
			}
		}
		if pr.After.IsZero() || now.Before(pr.After) {
			keep = append(keep, pr)
			continue
		}
		rest := append(append(keep, pr), st.PendingRevokes[i+1:]...)
		if pr.Baseline != nil && usage != nil && *usage > *pr.Baseline+usageEpsilon {
			st.PendingRevokes = rest
			st.Rotation = state.RotationFailed
			st.RotationError = fmt.Sprintf("old key %s spent $%.4f after the switch", short(pr.KeyID), *usage-*pr.Baseline)
			e.emit(st, LevelAlert, EventOldKeyInUse, "old key %s is still in use ($%.4f spent after the switch); not revoked; find the consumer that still has it",
				short(pr.KeyID), *usage-*pr.Baseline)
			return true, nil
		}
		if old != nil {
			if err := p.Delete(ctx, pr.KeyID); err != nil {
				st.PendingRevokes = rest
				return changed, err
			}
		}
		changed = true
		if pr.SecretPath != "" {
			if err := e.Secrets.DeleteField(ctx, pr.SecretPath, pr.SecretField); err != nil {
				e.logf("%s: remove old secret field %s#%s: %v", st.Name, pr.SecretPath, pr.SecretField, err)
			}
		}
		e.emit(st, LevelInfo, EventOldKeyRevoked, "old key %s revoked (%s)", short(pr.KeyID), pr.Reason)
	}
	st.PendingRevokes = keep
	return changed, nil
}

func (e *Engine) syncConsumers(ctx context.Context, k *manifest.Key) error {
	if e.Kube == nil {
		return errors.New("no kubectl configured")
	}
	var errs []error
	for _, c := range k.Consumers {
		if c.Type == manifest.ConsumerESO {
			if err := e.Kube.ForceSync(ctx, c.Cluster, c.Namespace, c.ExternalSecret); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		for _, r := range c.Restart {
			if err := e.Kube.Restart(ctx, c.Cluster, c.Namespace, r); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
