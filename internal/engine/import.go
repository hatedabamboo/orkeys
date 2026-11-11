package engine

import (
	"context"

	"github.com/hatedabamboo/orkeys/internal/provider"
)

// Candidate is a provider key without a manifest, as input for a manifest with an import block.
type Candidate struct {
	Provider string        `json:"provider"`
	Key      *provider.Key `json:"key"`
}

// Import returns the provider keys that no manifest and no state names.
func (e *Engine) Import(ctx context.Context) ([]Candidate, error) {
	plan, lists, err := e.plan(ctx)
	if err != nil {
		return nil, err
	}
	out := []Candidate{}
	for _, fk := range plan.Unmanaged {
		if k := provider.Find(lists[fk.Provider], fk.ID); k != nil {
			out = append(out, Candidate{Provider: fk.Provider, Key: k})
		}
	}
	return out, nil
}
