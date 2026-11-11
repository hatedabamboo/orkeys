package engine

import (
	"slices"
	"strings"

	"github.com/hatedabamboo/orkeys/internal/manifest"
	"github.com/hatedabamboo/orkeys/internal/state"
)

// Sources of a tracked key.
const (
	SourceManifest = "manifest"
	SourceRequest  = "request"
	// SourceOrphan is a state with no manifest that is not a requested key. The next apply revokes it.
	SourceOrphan = "orphan"
)

// TrackedKey is one state entry and where the key comes from.
type TrackedKey struct {
	Source string `json:"source"`
	*state.State
}

// TrackedFilter selects tracked keys. Empty fields match all keys.
type TrackedFilter struct {
	Team     string
	Kind     string
	Owner    string
	Provider string
	Source   string
}

// Tracked returns the states that match f, sorted by name. It reads no provider.
func Tracked(inv *manifest.Inventory, states []*state.State, f TrackedFilter) []TrackedKey {
	out := []TrackedKey{}
	for _, st := range states {
		src := SourceOrphan
		switch {
		case inv.Key(st.Name) != nil:
			src = SourceManifest
		case inv.Policy.Requested(st.Kind, st.Owner):
			src = SourceRequest
		}
		if (f.Team != "" && st.Team != f.Team) ||
			(f.Kind != "" && st.Kind != f.Kind) ||
			(f.Owner != "" && !strings.EqualFold(st.Owner, f.Owner)) ||
			(f.Provider != "" && st.Provider != f.Provider) ||
			(f.Source != "" && src != f.Source) {
			continue
		}
		out = append(out, TrackedKey{Source: src, State: st})
	}
	slices.SortFunc(out, func(a, b TrackedKey) int { return strings.Compare(a.Name, b.Name) })
	return out
}
