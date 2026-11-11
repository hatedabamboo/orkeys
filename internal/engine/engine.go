package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
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

type Kube interface {
	ForceSync(ctx context.Context, cluster, namespace, externalSecret string) error
	Restart(ctx context.Context, cluster, namespace, target string) error
}

// GitHubSecrets writes GitHub Actions secrets for github consumers.
type GitHubSecrets interface {
	Put(ctx context.Context, t manifest.GitHubSecret, value string) error
	Delete(ctx context.Context, t manifest.GitHubSecret) error
}

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
	Kube      Kube
	GitHub    GitHubSecrets
	Events    EventSink
	Deliverer Deliverer
	Org       OrgChecker
	Now       func() time.Time
	RunURL    string
	Log       io.Writer
	// RetryDelay is the base delay between state write attempts. Default: 1s.
	RetryDelay time.Duration
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

func (e *Engine) logf(format string, args ...any) {
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

func (e *Engine) putState(ctx context.Context, st *state.State) error {
	var err error
	for i := 0; i < 3; i++ {
		if err = e.State.PutState(ctx, st); err == nil {
			return nil
		}
		d := e.RetryDelay
		if d == 0 {
			d = time.Second
		}
		time.Sleep(time.Duration(i+1) * d)
	}
	return fmt.Errorf("save state %s (provider key %s): %w", st.Name, st.KeyID, err)
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
	if orig != nil {
		// The GitHub secrets still hold the old key; the next push replaces them.
		st.GitHubSecrets = maps.Clone(orig.GitHubSecrets)
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
		errs := []error{cause}
		if err := p.Delete(ctx, pk.ID); err != nil {
			errs = append(errs, fmt.Errorf("delete new key %s: %w", pk.ID, err))
		}
		if orig != nil {
			if err := e.putState(ctx, orig); err != nil {
				errs = append(errs, fmt.Errorf("restore state: %w", err))
			}
		} else if err := e.State.DeleteState(ctx, st.Name); err != nil {
			errs = append(errs, fmt.Errorf("delete state: %w", err))
		}
		return fmt.Errorf("%s: %w", k.Name, errors.Join(errs...))
	}
	if err := e.putState(ctx, st); err != nil {
		if derr := p.Delete(ctx, pk.ID); derr != nil {
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
	e.emit(st, LevelInfo, EventKeyIssued, "new key %s written to %s; expires %s",
		short(pk.ID), secretList(st.Secrets), exp.Format(time.DateOnly))
	return st, e.disableIfWanted(ctx, p, k, st)
}

func secretMeta(k *manifest.Key, keyID string) map[string]string {
	return map[string]string{"orkeys-key": k.Name, "orkeys-key-id": keyID, "orkeys-team": k.Team}
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
		errs := []error{cause}
		for _, d := range slices.Backward(done) {
			var err error
			if d.value == "" {
				err = e.Secrets.DeleteField(ctx, d.ref.Path, d.ref.Field)
			} else {
				var meta map[string]string
				if oldID != "" {
					meta = secretMeta(k, oldID)
				}
				err = e.Secrets.Put(ctx, d.ref.Path, d.ref.Field, d.value, meta)
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
