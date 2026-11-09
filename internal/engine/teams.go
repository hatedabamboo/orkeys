package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/hatedabamboo/orkeys/internal/manifest"
)

func (e *Engine) slugOf(t *manifest.Team) string { return t.GitHubSlug(e.Inv.Policy.GitHub.Org) }

// inTeam reports whether login is a member of t: from team.yaml members, or from the GitHub team.
func (e *Engine) inTeam(ctx context.Context, t *manifest.Team, login string) (bool, error) {
	slug := e.slugOf(t)
	if slug == "" {
		return t.HasMember(login), nil
	}
	if e.Org == nil {
		return false, fmt.Errorf("team %s reads its members from GitHub team %s/%s: set GITHUB_TOKEN (read:org)",
			t.Team, e.Inv.Policy.GitHub.Org, slug)
	}
	ok, err := e.Org.IsTeamMember(ctx, slug, login)
	if err != nil {
		return false, fmt.Errorf("team %s: %w", t.Team, err)
	}
	return ok, nil
}

// teamOf finds the team of login. With name set, it checks only that team.
// A login in several teams needs name.
func (e *Engine) teamOf(ctx context.Context, login, name string) (*manifest.Team, error) {
	if name != "" {
		t := e.Inv.Teams[name]
		if t == nil {
			return nil, fmt.Errorf("team %q: no teams/%s/team.yaml", name, name)
		}
		ok, err := e.inTeam(ctx, t, login)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%s is not a member of team %s", login, name)
		}
		return t, nil
	}
	var found []*manifest.Team
	for _, n := range e.Inv.TeamNames() {
		ok, err := e.inTeam(ctx, e.Inv.Teams[n], login)
		if err != nil {
			return nil, err
		}
		if ok {
			found = append(found, e.Inv.Teams[n])
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("%s is not a member of any team in teams/*/team.yaml", login)
	case 1:
		return found[0], nil
	}
	names := make([]string, len(found))
	for i, t := range found {
		names[i] = t.Team
	}
	return nil, fmt.Errorf("%s is a member of several teams (%s); choose one with --team", login, strings.Join(names, ", "))
}

// allMembers returns the members of all teams. A missing GitHub team is an error.
func (e *Engine) allMembers(ctx context.Context) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, n := range e.Inv.TeamNames() {
		t := e.Inv.Teams[n]
		members := t.Members
		if slug := e.slugOf(t); slug != "" {
			var err error
			if members, err = e.Org.TeamMembers(ctx, slug); err != nil {
				return nil, fmt.Errorf("team %s: %w", t.Team, err)
			}
		}
		for _, m := range members {
			if l := strings.ToLower(m); !seen[l] {
				seen[l] = true
				out = append(out, m)
			}
		}
	}
	return out, nil
}
