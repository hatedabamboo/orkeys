package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOrg(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/orgs/acme":
			w.WriteHeader(http.StatusOK)
		case "/orgs/acme/members/alice":
			w.WriteHeader(http.StatusNoContent)
		case "/orgs/acme/members/bob":
			w.WriteHeader(http.StatusNotFound)
		case "/orgs/acme/members/carol":
			http.Redirect(w, r, "/orgs/acme/public_members/carol", http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	o := &Org{Name: "acme", Token: "tok", API: srv.URL}
	if err := o.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if err := (&Org{Name: "acmee", Token: "tok", API: srv.URL}).Check(ctx); err == nil {
		t.Fatal("wrong org name must fail")
	}
	if ok, err := o.IsMember(ctx, "alice"); !ok || err != nil {
		t.Fatal("alice", ok, err)
	}
	if ok, err := o.IsMember(ctx, "bob"); ok || err != nil {
		t.Fatal("bob", ok, err)
	}
	if _, err := o.IsMember(ctx, "carol"); err == nil {
		t.Fatal("a redirect (token without org access) must be an error, not 'not a member'")
	}
}
