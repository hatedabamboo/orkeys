package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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

func TestTeams(t *testing.T) {
	// Team "big" has 150 members: user0..user149. Team "ai" has alice (active) and dave (pending).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/orgs/acme/teams/ai", "/orgs/acme/teams/big":
			fmt.Fprint(w, `{}`)
		case "/orgs/acme/teams/ai/memberships/alice":
			fmt.Fprint(w, `{"state":"active","role":"member"}`)
		case "/orgs/acme/teams/ai/memberships/dave":
			fmt.Fprint(w, `{"state":"pending","role":"member"}`)
		case "/orgs/acme/teams/ai/members":
			fmt.Fprint(w, `[{"login":"alice"}]`)
		case "/orgs/acme/teams/big/members":
			if r.URL.Query().Get("per_page") != "100" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			var users []string
			for i := (page - 1) * 100; i < page*100 && i < 150; i++ {
				users = append(users, fmt.Sprintf(`{"login":"user%d"}`, i))
			}
			fmt.Fprint(w, "["+strings.Join(users, ",")+"]")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	o := &Org{Name: "acme", Token: "tok", API: srv.URL}

	if ok, err := o.IsTeamMember(ctx, "ai", "alice"); !ok || err != nil {
		t.Fatal("alice", ok, err)
	}
	if ok, err := o.IsTeamMember(ctx, "ai", "bob"); ok || err != nil {
		t.Fatal("bob", ok, err)
	}
	if ok, err := o.IsTeamMember(ctx, "ai", "dave"); ok || err != nil {
		t.Fatal("pending invite must not count", ok, err)
	}
	if _, err := o.IsTeamMember(ctx, "nope", "alice"); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("missing team: want ErrTeamNotFound, got %v", err)
	}
	if err := o.CheckTeam(ctx, "ai"); err != nil {
		t.Fatal(err)
	}
	if err := o.CheckTeam(ctx, "nope"); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("want ErrTeamNotFound, got %v", err)
	}

	ms, err := o.TeamMembers(ctx, "big")
	if err != nil || len(ms) != 150 || ms[0] != "user0" || ms[149] != "user149" {
		t.Fatalf("pagination: %d members, %v", len(ms), err)
	}
	if ms, err := o.TeamMembers(ctx, "ai"); err != nil || len(ms) != 1 {
		t.Fatal(ms, err)
	}
	if _, err := o.TeamMembers(ctx, "nope"); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("missing team: want ErrTeamNotFound, got %v", err)
	}
	if _, err := (&Org{Name: "acme", Token: "bad", API: srv.URL}).IsTeamMember(ctx, "ai", "alice"); err == nil {
		t.Fatal("bad token must be an error")
	}
}
