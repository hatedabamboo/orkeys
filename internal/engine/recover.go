package engine

import (
	"context"
	"fmt"

	"github.com/hatedabamboo/orkeys/internal/manifest"
	"github.com/hatedabamboo/orkeys/internal/secrets"
	"github.com/hatedabamboo/orkeys/internal/state"
)

// resume finishes an issue that a crash cut short. The state has the new key ID with phase issuing.
// The secrets can hold the new key, the old key, or a mix.
//
//   - All places have the metadata of the new key. Only the last state write is missing.
//   - One place has it. Its value is the new key. resume writes it to all places.
//   - No place has it. The value of the new key is lost: providers show a key only once. resume issues another
//     key. The lost key waits for revoke like an old key. A consumer can have read it already.
func (e *Engine) resume(ctx context.Context, k *manifest.Key, st *state.State) (*state.State, error) {
	complete, value := true, ""
	for _, r := range st.Secrets {
		meta, err := e.Secrets.Meta(ctx, r.Path, r.Field)
		if err != nil {
			return nil, fmt.Errorf("%s: read metadata of %s: %w", st.Name, r, err)
		}
		if meta[secrets.MetaKeyID] != st.KeyID {
			complete = false
			continue
		}
		if value != "" {
			continue
		}
		data, err := e.Secrets.Read(ctx, r.Path)
		if err != nil {
			return nil, fmt.Errorf("%s: read %s: %w", st.Name, r, err)
		}
		value = data[r.Field]
	}

	switch {
	case complete:
		e.logf("%s: secrets hold key %s; the last state write was missing", st.Name, short(st.KeyID))
	case value != "":
		if err := e.writeSecrets(ctx, k, st.Secrets, value, st.KeyID, ""); err != nil {
			return nil, fmt.Errorf("%s: finish secret write: %w", st.Name, err)
		}
		e.logf("%s: wrote key %s to all secrets after an interrupted run", st.Name, short(st.KeyID))
	default:
		next, err := e.issue(ctx, k, st, "recover: key "+short(st.KeyID)+" was not written to the secrets")
		if err != nil {
			return nil, err
		}
		e.emit(next, LevelAlert, EventRotationStart, "an earlier run stopped before it wrote key %s to the secrets; issued key %s; consumers sync next",
			short(st.KeyID), short(next.KeyID))
		return next, nil
	}
	st.Phase = state.PhaseReady
	if err := e.putState(ctx, st); err != nil {
		return nil, err
	}
	return st, nil
}

// secretDrift describes the first secret place whose metadata names another key than st.KeyID.
// It returns "" when all places agree. A place without the metadata is not drift: a person or an old orkeys
// wrote it.
func (e *Engine) secretDrift(ctx context.Context, st *state.State) (string, error) {
	for _, r := range st.Secrets {
		meta, err := e.Secrets.Meta(ctx, r.Path, r.Field)
		if err != nil {
			return "", fmt.Errorf("%s: read metadata of %s: %w", st.Name, r, err)
		}
		if id := meta[secrets.MetaKeyID]; id != "" && id != st.KeyID {
			return fmt.Sprintf("secret %s holds key %s, state has key %s", r, short(id), short(st.KeyID)), nil
		}
	}
	return "", nil
}
