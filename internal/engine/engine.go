package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/hatedabamboo/orkeys/internal/manifest"
	"github.com/hatedabamboo/orkeys/internal/provider"
	"github.com/hatedabamboo/orkeys/internal/secrets"
	"github.com/hatedabamboo/orkeys/internal/state"
)

type Deliverer interface {
	Deliver(ctx context.Context, user, name, secret string) (string, error)
}

type OrgChecker interface {
	Check(ctx context.Context) error
	IsMember(ctx context.Context, user string) (bool, error)
	CheckTeam(ctx context.Context, slug string) error
	IsTeamMember(ctx context.Context, slug, user string) (bool, error)
	TeamMembers(ctx context.Context, slug string) ([]string, error)
}

type Engine struct {
	Inv       *manifest.Inventory
	Providers provider.Set
	State     state.Store
	Secrets   secrets.Store
	// Pushers and Notifiers are the drivers of the consumer types, by type name.
	Pushers   map[string]Pusher
	Notifiers map[string]Notifier
	// NotifyConcurrency is how many notify consumers of one key run at a time. Default: 1.
	NotifyConcurrency int
	Events            EventSink
	Deliverer         Deliverer
	Org               OrgChecker
	Now               func() time.Time
	RunURL            string
	// Log gets plain log lines. A non-nil Logger gets structured records in place of Log.
	Log    io.Writer
	Logger *slog.Logger
	// RetryDelay is the base delay between state write attempts. Default: 1s.
	RetryDelay time.Duration
	// Guard returns an error when this process must not write state. An example is a lost state lock.
	// A nil Guard means no check.
	Guard func() error
}

// ready returns an error for a missing dependency. The error is clearer than a nil pointer panic.
func (e *Engine) ready() error {
	var missing []string
	if e.Inv == nil {
		missing = append(missing, "inventory")
	}
	if len(e.Providers) == 0 {
		missing = append(missing, "providers")
	}
	if e.State == nil {
		missing = append(missing, "state store")
	}
	if e.Secrets == nil {
		missing = append(missing, "secrets store")
	}
	if len(missing) > 0 {
		return fmt.Errorf("engine: no %s", strings.Join(missing, ", "))
	}
	return nil
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

func (e *Engine) logf(format string, args ...any) {
	if e.Logger != nil {
		e.Logger.Info(fmt.Sprintf(format, args...))
		return
	}
	w := e.Log
	if w == nil {
		w = os.Stderr
	}
	fmt.Fprintf(w, format+"\n", args...)
}

func (e *Engine) listAll(ctx context.Context) (map[string][]provider.Key, error) {
	out := map[string][]provider.Key{}
	names := make([]string, 0, len(e.Providers))
	for n := range e.Providers {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		keys, err := e.Providers[n].List(ctx)
		if err != nil {
			return nil, fmt.Errorf("%s: list keys: %w", n, err)
		}
		out[n] = keys
	}
	return out, nil
}

func (e *Engine) stateFor(k *manifest.Key) *state.State {
	st := &state.State{
		Name:       k.Name,
		Team:       k.Team,
		Kind:       k.Kind,
		Env:        k.Env,
		Service:    k.Service,
		Provider:   k.Provider,
		LimitUSD:   k.Budget.Limit,
		LimitReset: k.Budget.ResetOrDefault(),
	}
	for _, sec := range k.SecretTargets() {
		st.Secrets = append(st.Secrets, state.SecretRef{Path: sec.Path, Field: sec.Field})
	}
	if t := e.Inv.Teams[k.Team]; t != nil {
		st.NotifyChannel = t.Channels.Notifications
		st.AlertChannel = t.ChannelFor(e.Inv.Policy.Kind(k.Kind))
	}
	return st
}

func copyMeta(dst, src *state.State) {
	dst.Team, dst.Env, dst.Service = src.Team, src.Env, src.Service
	dst.LimitUSD, dst.LimitReset = src.LimitUSD, src.LimitReset
	dst.NotifyChannel, dst.AlertChannel = src.NotifyChannel, src.AlertChannel
}

// cleanupTimeout bounds one compensating action or state write after the run context is cancelled.
const cleanupTimeout = 30 * time.Second

// detached returns a context that stays valid after ctx is cancelled. Cleanup steps and state writes use it.
// A SIGTERM in the middle of a change must not leave a provider key without state.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
}

// putState saves st. It retries twice. It writes also after ctx is cancelled.
// The state must record each change at the provider.
func (e *Engine) putState(ctx context.Context, st *state.State) error {
	if e.Guard != nil {
		if err := e.Guard(); err != nil {
			return fmt.Errorf("save state %s (provider key %s): %w", st.Name, st.KeyID, err)
		}
	}
	if err := state.CheckSchema(st); err != nil {
		return err
	}
	st.Schema = state.SchemaVersion
	dctx, cancel := detached(ctx)
	defer cancel()
	d := e.RetryDelay
	if d == 0 {
		d = time.Second
	}
	var err error
	for i := 0; i < 3; i++ {
		if err = e.State.PutState(dctx, st); err == nil || errors.Is(err, state.ErrConflict) {
			// A retry does not fix a conflict. Another process has changed the state.
			break
		}
		t := time.NewTimer(time.Duration(i+1) * d)
		select {
		case <-dctx.Done():
			t.Stop()
			return fmt.Errorf("save state %s (provider key %s): %w", st.Name, st.KeyID, errors.Join(err, dctx.Err()))
		case <-t.C:
		}
	}
	if err != nil {
		return fmt.Errorf("save state %s (provider key %s): %w", st.Name, st.KeyID, err)
	}
	return nil
}

// listStates reads all states and refuses states that a newer orkeys wrote.
func (e *Engine) listStates(ctx context.Context) ([]*state.State, error) {
	states, err := e.State.ListStates(ctx)
	if err != nil {
		return nil, err
	}
	return states, state.CheckSchema(states...)
}

// issue creates a new key for k. With orig set and rotateReason non-empty, the old key of orig
// waits for revoke and the state goes to RotationPending.
func (e *Engine) issue(ctx context.Context, k *manifest.Key, orig *state.State, rotateReason string) (*state.State, error) {
	p, err := e.Providers.Get(k.Provider)
	if err != nil {
		return nil, err
	}
	now := e.now()
	exp := now.Add(k.TTL.Duration)
	limit := k.Budget.Limit
	spec := provider.Spec{
		Name:       k.Name + "." + now.Format("20060102"),
		LimitUSD:   &limit,
		LimitReset: k.Budget.ResetOrDefault(),
		Scopes:     k.Scopes,
	}
	if p.Caps().NativeExpiry {
		spec.ExpiresAt = &exp
	}
	pk, secret, err := p.Create(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("%s: create key: %w", k.Name, err)
	}

	st := e.stateFor(k)
	st.KeyID, st.CreatedAt, st.ExpiresAt = pk.ID, now, exp
	st.Phase = state.PhaseIssuing
	if orig != nil {
		st.Rev = orig.Rev
		// The push targets hold the old key. The next push replaces it.
		st.Pushed = maps.Clone(orig.Pushed)
		st.PendingRevokes = append([]state.PendingRevoke(nil), orig.PendingRevokes...)
		if rotateReason != "" {
			pr := state.PendingRevoke{KeyID: orig.KeyID, Source: state.RevokeRotation, Reason: rotateReason}
			for _, r := range orig.Secrets {
				if !slices.Contains(st.Secrets, r) {
					pr.Secrets = append(pr.Secrets, r)
				}
			}
			st.PendingRevokes = append(st.PendingRevokes, pr)
			st.Rotation = state.RotationPending
		}
	}

	undo := func(cause error) error {
		dctx, cancel := detached(ctx)
		defer cancel()
		errs := []error{cause}
		if err := p.Delete(dctx, pk.ID); err != nil {
			errs = append(errs, fmt.Errorf("delete new key %s: %w", pk.ID, err))
		}
		if orig != nil {
			orig.Rev = st.Rev
			if err := e.putState(ctx, orig); err != nil {
				errs = append(errs, fmt.Errorf("restore state: %w", err))
			}
		} else if err := e.State.DeleteState(dctx, st.Name); err != nil {
			errs = append(errs, fmt.Errorf("delete state: %w", err))
		}
		return fmt.Errorf("%s: %w", k.Name, errors.Join(errs...))
	}
	if err := e.putState(ctx, st); err != nil {
		dctx, cancel := detached(ctx)
		defer cancel()
		if derr := p.Delete(dctx, pk.ID); derr != nil {
			return nil, fmt.Errorf("%w; delete new key %s also failed: %w", err, pk.ID, derr)
		}
		return nil, fmt.Errorf("%w (new key deleted)", err)
	}
	oldID := ""
	if orig != nil {
		oldID = orig.KeyID
	}
	if err := e.writeSecrets(ctx, k, st.Secrets, secret, pk.ID, oldID); err != nil {
		return nil, undo(err)
	}
	st.Phase = state.PhaseReady
	if err := e.putState(ctx, st); err != nil {
		// The secrets hold the new key. The next plan or rotate finds the issuing phase and finishes it.
		return nil, err
	}
	e.emit(st, LevelInfo, EventKeyIssued, "new key %s written to %s; expires %s",
		short(pk.ID), secretList(st.Secrets), exp.Format(time.DateOnly))
	return st, e.disableIfWanted(ctx, p, k, st)
}

func secretMeta(k *manifest.Key, keyID string) map[string]string {
	return map[string]string{"orkeys-key": k.Name, secrets.MetaKeyID: keyID, "orkeys-team": k.Team}
}

func secretList(refs []state.SecretRef) string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.String()
	}
	return strings.Join(out, ", ")
}

// writeSecrets writes the key value to all places in refs. If a write fails, it puts back the values
// it replaced (with the metadata of oldID) and removes the fields it added, so all places keep the same key.
func (e *Engine) writeSecrets(ctx context.Context, k *manifest.Key, refs []state.SecretRef, value, keyID, oldID string) error {
	type prev struct {
		ref   state.SecretRef
		value string
	}
	var done []prev
	restore := func(cause error) error {
		dctx, cancel := detached(ctx)
		defer cancel()
		errs := []error{cause}
		for _, d := range slices.Backward(done) {
			var err error
			if d.value == "" {
				err = e.Secrets.DeleteField(dctx, d.ref.Path, d.ref.Field)
			} else {
				var meta map[string]string
				if oldID != "" {
					meta = secretMeta(k, oldID)
				}
				err = e.Secrets.Put(dctx, d.ref.Path, d.ref.Field, d.value, meta)
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("restore secret %s: %w", d.ref, err))
			}
		}
		return errors.Join(errs...)
	}
	for _, r := range refs {
		data, err := e.Secrets.Read(ctx, r.Path)
		if err != nil {
			return restore(fmt.Errorf("read secret %s: %w", r, err))
		}
		if err := e.Secrets.Put(ctx, r.Path, r.Field, value, secretMeta(k, keyID)); err != nil {
			return restore(fmt.Errorf("write secret %s: %w", r, err))
		}
		done = append(done, prev{ref: r, value: data[r.Field]})
	}
	return nil
}

// readSecret reads the current key value from the first place of st.
func (e *Engine) readSecret(ctx context.Context, st *state.State) (string, error) {
	if len(st.Secrets) == 0 {
		return "", fmt.Errorf("%s: no secret in state", st.Name)
	}
	r := st.Secrets[0]
	return secrets.ReadField(ctx, e.Secrets, r.Path, r.Field)
}

func (e *Engine) revoke(ctx context.Context, providerName, id string) error {
	p, err := e.Providers.Get(providerName)
	if err != nil {
		return err
	}
	return p.Delete(ctx, id)
}
