package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrTeamNotFound is returned when the GitHub team does not exist or the token cannot see it.
var ErrTeamNotFound = errors.New("github team not found (wrong slug, or the token has no read:org)")

const perPage = 100

type Org struct {
	Name  string
	Token string
	API   string
	http  *http.Client
}

func (o *Org) client() *http.Client {
	if o.http == nil {
		// Without this, a 302 (caller is not an org member) is followed to the public-members endpoint.
		o.http = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return o.http
}

// get returns the status code and, for 200, the body (at most 4 MiB).
func (o *Org) get(ctx context.Context, path string) (int, []byte, error) {
	api := o.API
	if api == "" {
		api = "https://api.github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(api, "/")+path, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+o.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := o.client().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, body, err
}

func (o *Org) teamPath(slug string) string {
	return "/orgs/" + url.PathEscape(o.Name) + "/teams/" + url.PathEscape(slug)
}

func (o *Org) Check(ctx context.Context) error {
	code, _, err := o.get(ctx, "/orgs/"+url.PathEscape(o.Name))
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("github org %q: HTTP %d (wrong name or no access)", o.Name, code)
	}
	return nil
}

func (o *Org) IsMember(ctx context.Context, user string) (bool, error) {
	code, _, err := o.get(ctx, "/orgs/"+url.PathEscape(o.Name)+"/members/"+url.PathEscape(user))
	if err != nil {
		return false, err
	}
	switch code {
	case http.StatusNoContent:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	}
	return false, fmt.Errorf("github org membership %s/%s: HTTP %d", o.Name, user, code)
}

// CheckTeam returns ErrTeamNotFound (wrapped) if the team does not exist.
func (o *Org) CheckTeam(ctx context.Context, slug string) error {
	code, _, err := o.get(ctx, o.teamPath(slug))
	if err != nil {
		return err
	}
	switch code {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return fmt.Errorf("%s/%s: %w", o.Name, slug, ErrTeamNotFound)
	}
	return fmt.Errorf("github team %s/%s: HTTP %d", o.Name, slug, code)
}

// IsTeamMember reports whether user is an active member of the team or of one of its child teams.
// A pending invitation is not membership. A missing team is an error, not "not a member".
func (o *Org) IsTeamMember(ctx context.Context, slug, user string) (bool, error) {
	code, body, err := o.get(ctx, o.teamPath(slug)+"/memberships/"+url.PathEscape(user))
	if err != nil {
		return false, err
	}
	switch code {
	case http.StatusOK:
		var m struct {
			State string `json:"state"`
		}
		if err := json.Unmarshal(body, &m); err != nil {
			return false, fmt.Errorf("github team membership %s/%s/%s: %w", o.Name, slug, user, err)
		}
		return m.State == "active", nil
	case http.StatusNotFound:
		// 404 means "not a member" or "no such team". Tell them apart.
		if err := o.CheckTeam(ctx, slug); err != nil {
			return false, err
		}
		return false, nil
	}
	return false, fmt.Errorf("github team membership %s/%s/%s: HTTP %d", o.Name, slug, user, code)
}

// TeamMembers returns the logins of the team members, child teams included.
func (o *Org) TeamMembers(ctx context.Context, slug string) ([]string, error) {
	var out []string
	for page := 1; ; page++ {
		code, body, err := o.get(ctx, fmt.Sprintf("%s/members?per_page=%d&page=%d", o.teamPath(slug), perPage, page))
		if err != nil {
			return nil, err
		}
		switch code {
		case http.StatusOK:
		case http.StatusNotFound:
			return nil, fmt.Errorf("%s/%s: %w", o.Name, slug, ErrTeamNotFound)
		default:
			return nil, fmt.Errorf("github team members %s/%s: HTTP %d", o.Name, slug, code)
		}
		var users []struct {
			Login string `json:"login"`
		}
		if err := json.Unmarshal(body, &users); err != nil {
			return nil, fmt.Errorf("github team members %s/%s: %w", o.Name, slug, err)
		}
		for _, u := range users {
			out = append(out, u.Login)
		}
		if len(users) < perPage {
			return out, nil
		}
	}
}
