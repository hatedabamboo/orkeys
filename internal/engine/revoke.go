package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hatedabamboo/orkeys/internal/state"
)

// Revoke revokes requested keys before they expire: it deletes the provider keys, the secret
// fields and the GitHub secrets, and removes the state. It does not revoke manifest keys, because
// the next apply would create them again.
func (e *Engine) Revoke(ctx context.Context, names []string, reason string) error {
	if err := e.ready(); err != nil {
		return err
	}
	if len(names) == 0 {
		return errors.New("no key name given")
	}
	var todo []*state.State
	for _, n := range names {
		st, err := e.State.GetState(ctx, n)
		if err != nil {
			return fmt.Errorf("key %s: %w", n, err)
		}
		if st == nil {
			return fmt.Errorf("key %s: not in state", n)
		}
		if err := state.CheckSchema(st); err != nil {
			return err
		}
		if st.TrackOnly {
			return fmt.Errorf("key %s: tracked key; orkeys does not revoke it; delete the manifest to stop tracking", n)
		}
		if k := e.Inv.Key(n); k != nil {
			return fmt.Errorf("key %s: has a manifest; delete the file or set disabled: true", n)
		}
		if !e.Inv.Policy.Requested(st.Kind, st.Owner) {
			return fmt.Errorf("key %s: kind %s is not a request kind; apply revokes keys without a manifest", n, st.Kind)
		}
		todo = append(todo, st)
	}
	reason = strings.TrimSpace(reason)
	var errs []error
	for _, st := range todo {
		r := fmt.Sprintf("%s key of %s revoked by hand", st.Kind, st.Owner)
		if reason != "" {
			r += ": " + reason
		}
		if err := e.remove(ctx, st, r); err != nil {
			errs = append(errs, fmt.Errorf("key %s: %w", st.Name, err))
		}
	}
	return errors.Join(errs...)
}
