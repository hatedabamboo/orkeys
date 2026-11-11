package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/hatedabamboo/orkeys/internal/manifest"
	"github.com/hatedabamboo/orkeys/internal/state"
)

// githubDiff returns the GitHub secrets that do not hold the current key (push) and the GitHub secrets
// that orkeys wrote but the manifest no longer lists (drop).
func githubDiff(k *manifest.Key, st *state.State) (push []manifest.GitHubSecret, drop []string) {
	want := map[string]bool{}
	if k != nil {
		for _, g := range k.GitHubSecrets() {
			want[g.ID()] = true
			if st.GitHubSecrets[g.ID()] != st.KeyID {
				push = append(push, g)
			}
		}
	}
	for _, id := range slices.Sorted(maps.Keys(st.GitHubSecrets)) {
		if !want[id] {
			drop = append(drop, id)
		}
	}
	return push, drop
}

func githubDetail(push []manifest.GitHubSecret, drop []string) string {
	var d []string
	if len(push) > 0 {
		ids := make([]string, len(push))
		for i, g := range push {
			ids[i] = g.ID()
		}
		d = append(d, "write "+strings.Join(ids, ", "))
	}
	if len(drop) > 0 {
		d = append(d, "delete "+strings.Join(drop, ", "))
	}
	return strings.Join(d, "; ")
}

// syncGitHub writes the current key to the GitHub secrets of k and deletes the GitHub secrets that k no
// longer lists. It records each success in st.GitHubSecrets; the caller saves st. k is nil when the
// manifest was removed: then all GitHub secrets are deleted.
func (e *Engine) syncGitHub(ctx context.Context, k *manifest.Key, st *state.State) error {
	push, drop := githubDiff(k, st)
	if len(push) == 0 && len(drop) == 0 {
		return nil
	}
	if e.GitHub == nil {
		return errors.New("github consumers: no GitHub token (set ORKEYS_GITHUB_SECRETS_TOKEN or GITHUB_TOKEN)")
	}
	var errs []error
	if len(push) > 0 {
		value, err := e.readSecret(ctx, st)
		if err != nil {
			return fmt.Errorf("read key for github consumers: %w", err)
		}
		for _, g := range push {
			if err := e.GitHub.Put(ctx, g, value); err != nil {
				errs = append(errs, err)
				continue
			}
			if st.GitHubSecrets == nil {
				st.GitHubSecrets = map[string]string{}
			}
			st.GitHubSecrets[g.ID()] = st.KeyID
		}
	}
	for _, id := range drop {
		g, err := manifest.ParseGitHubSecret(id)
		if err == nil {
			err = e.GitHub.Delete(ctx, g)
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		delete(st.GitHubSecrets, id)
	}
	return errors.Join(errs...)
}

// syncGitHubAndSave runs syncGitHub after a create, an import or a manifest change, and saves the state.
// On failure the next plan shows the sync again.
func (e *Engine) syncGitHubAndSave(ctx context.Context, k *manifest.Key, st *state.State) error {
	push, drop := githubDiff(k, st)
	if len(push) == 0 && len(drop) == 0 {
		return nil
	}
	err := e.syncGitHub(ctx, k, st)
	if serr := e.putState(ctx, st); serr != nil {
		err = errors.Join(err, serr)
	}
	if err != nil {
		e.emit(st, LevelAlert, EventGitHubSyncFailed, "github secrets not synced: %v; the next apply tries again", err)
		return err
	}
	e.emit(st, LevelInfo, EventGitHubSynced, "github secrets: %s", githubDetail(push, drop))
	return nil
}
