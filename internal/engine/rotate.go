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
	if err := e.ready(); err != nil {
		return err
	}
	states, err := e.listStates(ctx)
	if err != nil {
		return err
	}
	lists, err := e.listAll(ctx)
	if err != nil {
		return err
	}
	e.debug(ctx, "rotate", "states", len(states), "only", strings.Join(opt.Only, ","), "retry", strings.Join(opt.Retry, ","),
		"now", strings.Join(opt.Now, ","))
	var errs []error
	for _, st := range states {
		if len(opt.Only) > 0 && !slices.Contains(opt.Only, st.Name) {
			e.debug(ctx, "rotate: skip key: not in --only", "key", st.Name)
			continue
		}
		if err := ctx.Err(); err != nil {
			errs = append(errs, fmt.Errorf("stopped before %s: %w", st.Name, err))
			break
		}
		if err := e.rotateOne(ctx, st, lists[st.Provider], slices.Contains(opt.Retry, st.Name), slices.Contains(opt.Now, st.Name)); err != nil {
			e.logf("%s: %v", st.Name, err)
			errs = append(errs, fmt.Errorf("%s: %w", st.Name, err))
		}
	}
	return errors.Join(errs...)
}

func (e *Engine) rotateOne(ctx context.Context, st *state.State, keys []provider.Key, retry, force bool) error {
	if st.TrackOnly {
		e.debug(ctx, "rotate: skip key: track_only", "key", st.Name)
		return nil
	}
	now := e.now()
	kp := e.Inv.Policy.Kind(st.Kind)
	changed := false
	e.debug(ctx, "rotate: key", "key", st.Name, "kind", st.Kind, "key_id", short(st.KeyID), "phase", st.Phase,
		"rotation", st.Rotation, "expires", st.ExpiresAt, "pending_revokes", len(st.PendingRevokes), "retry", retry, "force", force)

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

	case e.byHand(st.Provider):
		return e.rotateVoid(ctx, st, kp, keys, retry, force)

	default:
		k := e.Inv.Key(st.Name)
		if k == nil {
			e.debug(ctx, "rotate: skip key: no manifest; apply revokes it", "key", st.Name)
			return nil
		}
		if st.Phase == state.PhaseIssuing {
			next, err := e.resume(ctx, k, st)
			if err != nil {
				return err
			}
			*st = *next
		}
		if retry && st.Rotation == state.RotationFailed {
			st.Rotation, st.RotationError = state.RotationPending, ""
			// The retry says that the consumers are fixed. Only spend after the retry sync counts.
			// The old baseline stays below the usage forever: OpenRouter reports lifetime usage.
			for i := range st.PendingRevokes {
				if st.PendingRevokes[i].Source == state.RevokeRotation {
					st.PendingRevokes[i].After = time.Time{}
					st.PendingRevokes[i].Baseline = nil
					st.PendingRevokes[i].InUseAt = time.Time{}
				}
			}
			changed = true
		}
		due := !now.Before(st.ExpiresAt.Add(-kp.RotateBefore.Duration)) && !k.Disabled
		e.debug(ctx, "rotate: due check", "key", st.Name, "due", due, "rotate_from", st.ExpiresAt.Add(-kp.RotateBefore.Duration),
			"disabled", k.Disabled, "unsynced_revokes", hasUnsynced(st))
		if force && k.Disabled {
			return errors.New("key is disabled in the manifest; it does not rotate")
		}
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
			ids, ok, err := e.syncPending(ctx, k, st, kp, now)
			if !ok {
				return err
			}
			changed = true
			e.emit(st, LevelInfo, EventRotationSynced, "consumers use the new key; old key %s will be revoked after %s",
				strings.Join(ids, ", "), now.Add(kp.RevokeGrace.Duration).Format("2006-01-02 15:04 UTC"))
		}
	}

	if st.Rotation != state.RotationFailed {
		c, err := e.processRevokes(ctx, st, keys, now, recheckGrace(kp))
		changed = changed || c
		if err != nil {
			return errors.Join(err, e.putState(ctx, st))
		}
	}
	if changed {
		return e.putState(ctx, st)
	}
	e.debug(ctx, "rotate: no change", "key", st.Name)
	return nil
}

// syncPending syncs the consumers of a pending rotation. On success, it starts the grace time of the old keys,
// sets the rotation to idle and returns the short IDs of the old keys. On failure, it saves st and returns ok false.
func (e *Engine) syncPending(ctx context.Context, k *manifest.Key, st *state.State, kp *manifest.KindPolicy, now time.Time) (ids []string, ok bool, err error) {
	e.debug(ctx, "rotate: sync consumers", "key", st.Name, "key_id", short(st.KeyID), "consumers", len(k.Consumers))
	if err := e.syncConsumers(ctx, k, st); err != nil {
		if ctx.Err() != nil {
			// A stopped run is not a failed sync. The next run syncs the consumers again.
			return nil, false, errors.Join(err, e.putState(ctx, st))
		}
		st.Rotation, st.RotationError = state.RotationFailed, err.Error()
		e.emit(st, LevelAlert, EventRotationFailed, "rotation failed: %v; the old key stays active; after the fix run: orkeys rotate --retry %s", err, st.Name)
		return nil, false, e.putState(ctx, st)
	}
	for i := range st.PendingRevokes {
		if st.PendingRevokes[i].After.IsZero() {
			st.PendingRevokes[i].After = now.Add(kp.RevokeGrace.Duration)
			st.PendingRevokes[i].CheckFrom = now.Add(drain)
			ids = append(ids, short(st.PendingRevokes[i].KeyID))
		}
	}
	st.Rotation = state.RotationIdle
	return ids, true, nil
}

// fmtUSD formats a spend for logs. Nil is "unknown": the provider reports no spend.
func fmtUSD(v *float64) string {
	if v == nil {
		return "unknown"
	}
	return fmt.Sprintf("%.4f", *v)
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

// recheckGrace returns the recheck_grace of kp, or 0 for no kind. With 0, the first use of an old key fails the rotation.
func recheckGrace(kp *manifest.KindPolicy) time.Duration {
	if kp == nil {
		return 0
	}
	return kp.RecheckGrace.Duration
}

// processRevokes revokes the old keys of st after their grace time. The first use of an old key after the switch
// starts a wait of recheck with a new baseline. A use after that wait fails the rotation.
func (e *Engine) processRevokes(ctx context.Context, st *state.State, keys []provider.Key, now time.Time, recheck time.Duration) (bool, error) {
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
		e.debug(ctx, "revoke check", "key", st.Name, "old_key_id", short(pr.KeyID), "source", pr.Source,
			"at_provider", old != nil, "after", pr.After, "check_from", pr.CheckFrom, "usage", fmtUSD(usage), "baseline", fmtUSD(pr.Baseline))
		if !pr.After.IsZero() && !now.Before(pr.CheckFrom) && usage != nil {
			switch {
			case pr.Baseline == nil:
				v := *usage
				pr.Baseline = &v
				changed = true
				if !now.Before(pr.After) {
					keep = append(keep, pr)
					e.logf("%s: recorded usage of old key %s; revoke on the next run if it stays the same", st.Name, short(pr.KeyID))
					continue
				}
			case *usage < *pr.Baseline-usageEpsilon:
				// The spend is per period, and a new period starts. The old baseline is not valid.
				// Take a new baseline and decide on the next run.
				v := *usage
				pr.Baseline = &v
				changed = true
				keep = append(keep, pr)
				e.logf("%s: usage of old key %s went down (new period); recorded a new baseline", st.Name, short(pr.KeyID))
				continue
			}
		}
		if pr.After.IsZero() || now.Before(pr.After) {
			e.debug(ctx, "revoke check: wait", "key", st.Name, "old_key_id", short(pr.KeyID), "synced", !pr.After.IsZero(), "after", pr.After)
			keep = append(keep, pr)
			continue
		}
		// The current key is never revoked. An entry with the current ID only removes old secret places.
		current := pr.KeyID == st.KeyID
		why := ""
		if !current {
			why = stillUsed(pr, old, usage, p.Caps())
		}
		if why != "" && pr.InUseAt.IsZero() && recheck > 0 {
			// A consumer that missed the switch (a job that started before it, a late reload) can use the old key
			// for a while. Wait once more from a new baseline. Only a use after this wait needs a person.
			pr.InUseAt, pr.CheckFrom, pr.After = now, now, now.Add(recheck)
			if usage != nil {
				v := *usage
				pr.Baseline = &v
			}
			keep = append(keep, pr)
			changed = true
			e.emit(st, LevelInfo, EventOldKeyRecheck, "old key %s was used after the switch (%s); orkeys revokes it after %s if it is not used again",
				short(pr.KeyID), why, pr.After.Format("2006-01-02 15:04 UTC"))
			continue
		}
		rest := append(append(keep, pr), st.PendingRevokes[i+1:]...)
		if why != "" {
			if !pr.InUseAt.IsZero() {
				why += ", also after recheck_grace"
			}
			st.PendingRevokes = rest
			st.Rotation = state.RotationFailed
			st.RotationError = fmt.Sprintf("old key %s %s", short(pr.KeyID), why)
			e.emit(st, LevelAlert, EventOldKeyInUse, "old key %s is still in use (%s); not revoked; find the consumer that still has it",
				short(pr.KeyID), why)
			return true, nil
		}
		if old != nil && !current && usage == nil && old.LastUsedAt == nil && !p.Caps().LastUsed {
			e.logf("%s: %s reports no use of key %s; it is revoked after the grace period without a use check", st.Name, st.Provider, short(pr.KeyID))
		}
		if old != nil && !current {
			if err := p.Delete(ctx, pr.KeyID); err != nil {
				st.PendingRevokes = rest
				return changed, err
			}
		}
		changed = true
		for _, r := range pr.Secrets {
			e.debug(ctx, "delete old secret field", "key", st.Name, "secret", r.String())
			if err := e.Secrets.DeleteField(ctx, r.Path, r.Field); err != nil {
				e.logf("%s: remove old secret field %s: %v", st.Name, r, err)
			}
		}
		if current {
			continue
		}
		e.emit(st, LevelInfo, EventOldKeyRevoked, "old key %s revoked (%s)", short(pr.KeyID), pr.Reason)
	}
	st.PendingRevokes = keep
	return changed, nil
}

// stillUsed returns why the old key of pr is still in use, or "".
// Spend above the baseline is a use.
// A last use after CheckFrom (the sync time plus the drain time) is a use, if the provider reports exact times.
// The last use matters for OpenAI. Its spend arrives hours late.
func stillUsed(pr state.PendingRevoke, old *provider.Key, usage *float64, caps provider.Caps) string {
	if pr.Baseline != nil && usage != nil && *usage > *pr.Baseline+usageEpsilon {
		return fmt.Sprintf("$%.4f spent after the switch", *usage-*pr.Baseline)
	}
	if caps.LastUsedExact && old != nil && old.LastUsedAt != nil && !pr.CheckFrom.IsZero() && old.LastUsedAt.After(pr.CheckFrom) {
		return "used at " + old.LastUsedAt.UTC().Format("2006-01-02 15:04 UTC") + ", after the switch"
	}
	return ""
}
