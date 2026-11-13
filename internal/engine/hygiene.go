package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/hatedabamboo/orkeys/internal/provider"
	"github.com/hatedabamboo/orkeys/internal/state"
)

const unusedAfter = 30 * 24 * time.Hour

type Finding struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
	Done   bool   `json:"done"`
}

func (e *Engine) Hygiene(ctx context.Context, dryRun bool) ([]Finding, error) {
	if err := e.ready(); err != nil {
		return nil, err
	}
	states, err := e.listStates(ctx)
	if err != nil {
		return nil, err
	}
	lists, err := e.listAll(ctx)
	if err != nil {
		return nil, err
	}
	now := e.now()
	var out []Finding
	var errs []error

	ownersOK := false
	if e.Org != nil && hasOwners(states) {
		if err := e.orgSane(ctx); err != nil {
			errs = append(errs, fmt.Errorf("owner checks skipped, no key revoked: %w", err))
		} else {
			ownersOK = true
		}
	}
	for _, st := range states {
		if st.Owner == "" || !ownersOK {
			continue
		}
		member, err := e.Org.IsMember(ctx, st.Owner)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if member {
			continue
		}
		f := Finding{Kind: "owner-left", Name: st.Name, Detail: st.Owner + " is not in the GitHub org"}
		if !dryRun {
			err = e.remove(ctx, st, st.Owner+" left the GitHub org")
			f.Done = err == nil
			if err != nil {
				errs = append(errs, err)
			}
		}
		out = append(out, f)
	}

	for _, st := range states {
		if st.TrackOnly || e.Inv.Policy.Requested(st.Kind, st.Owner) {
			continue
		}
		if k := e.Inv.Key(st.Name); k != nil && k.Disabled {
			continue
		}
		p, perr := e.Providers.Get(st.Provider)
		if perr != nil || !p.Caps().LastUsed {
			continue
		}
		cur := provider.Find(lists[st.Provider], st.KeyID)
		if cur == nil || now.Sub(st.CreatedAt) < unusedAfter {
			continue
		}
		if cur.LastUsedAt == nil || now.Sub(*cur.LastUsedAt) > unusedAfter {
			out = append(out, Finding{Kind: "unused", Name: st.Name, Detail: "no use for 30 days"})
		}
	}
	return out, errors.Join(errs...)
}

func hasOwners(states []*state.State) bool {
	for _, st := range states {
		if st.Owner != "" {
			return true
		}
	}
	return false
}

// orgSane guards against a 404 storm: a wrong org name or a token without access makes every
// membership check return "not a member", which would revoke every personal and local key.
func (e *Engine) orgSane(ctx context.Context) error {
	if err := e.Org.Check(ctx); err != nil {
		return err
	}
	members, err := e.allMembers(ctx)
	if err != nil {
		return err
	}
	sort.Strings(members)
	for _, m := range members {
		ok, err := e.Org.IsMember(ctx, m)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return errors.New("no team member (team.yaml or GitHub team) is reported as an org member; check the org name and the token scope (read:org)")
}
