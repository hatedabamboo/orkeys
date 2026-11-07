package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

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

func (o *Org) get(ctx context.Context, path string) (int, error) {
	api := o.API
	if api == "" {
		api = "https://api.github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(api, "/")+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+o.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := o.client().Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

func (o *Org) Check(ctx context.Context) error {
	code, err := o.get(ctx, "/orgs/"+o.Name)
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("github org %q: HTTP %d (wrong name or no access)", o.Name, code)
	}
	return nil
}

func (o *Org) IsMember(ctx context.Context, user string) (bool, error) {
	code, err := o.get(ctx, "/orgs/"+o.Name+"/members/"+user)
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
