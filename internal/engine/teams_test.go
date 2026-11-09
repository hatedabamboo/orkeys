package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (v *env) writeTeam(t *testing.T, name, body string) {
	t.Helper()
	dir := filepath.Join(v.root, "teams", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "team.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	v.reload(t)
}

func TestRequestGitHubTeam(t *testing.T) {
	v := setup(t)
	v.writeTeam(t, "chat-core", "team: chat-core\nchannels: {notifications: \"#n\"}\n")
	org := &fakeOrg{members: map[string]bool{"jdoe": true}, teams: map[string][]string{"chat-core": {"jdoe"}}}
	v.e.Org = org
	r := Request{User: "JDoe", Kind: "local", Provider: "openrouter", Purpose: "dev"}

	is, err := v.request(t, r)
	if err != nil {
		t.Fatal(err)
	}
	if st := v.store.states[is.Name]; st.Team != "chat-core" || st.AlertChannel != "#n" {
		t.Fatalf("state %+v", st)
	}

	if _, err := v.request(t, Request{User: "asmith", Kind: "local", Provider: "openrouter", Purpose: "dev"}); err == nil ||
		!strings.Contains(err.Error(), "not a member of any team") {
		t.Fatalf("non-member: %v", err)
	}

	v.e.Org = nil
	if _, err := v.request(t, r); err == nil || !strings.Contains(err.Error(), "GITHUB_TOKEN") {
		t.Fatalf("no token: want GITHUB_TOKEN error, got %v", err)
	}

	v.e.Org = &fakeOrg{teams: map[string][]string{}}
	if _, err := v.request(t, r); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing team: want not found, got %v", err)
	}

	v.e.Org = org
	v.writeTeam(t, "chat-core", "team: chat-core\ngithub_team: chat\n")
	org.teams["chat"] = []string{"asmith"}
	if _, err := v.request(t, Request{User: "asmith", Kind: "local", Provider: "openrouter", Purpose: "dev"}); err != nil {
		t.Fatalf("github_team slug: %v", err)
	}
}

func TestRequestSeveralTeams(t *testing.T) {
	v := setup(t)
	v.writeTeam(t, "ml", "team: ml\n")
	v.e.Org = &fakeOrg{teams: map[string][]string{"ml": {"jdoe"}}}
	r := Request{User: "jdoe", Kind: "local", Provider: "openrouter", Purpose: "dev"}
	if _, err := v.request(t, r); err == nil || !strings.Contains(err.Error(), "several teams (chat-core, ml)") {
		t.Fatalf("want several-teams error, got %v", err)
	}
	r.Team = "ml"
	is, err := v.request(t, r)
	if err != nil {
		t.Fatal(err)
	}
	if v.store.states[is.Name].Team != "ml" {
		t.Fatal("wrong team")
	}
	r.Team = "nope"
	if _, err := v.request(t, r); err == nil {
		t.Fatal("unknown --team accepted")
	}
	r.User, r.Team = "asmith", "ml"
	if _, err := v.request(t, r); err == nil || !strings.Contains(err.Error(), "not a member of team ml") {
		t.Fatalf("want not-a-member error, got %v", err)
	}
	// A static team needs no token.
	v.e.Org = nil
	r.Team = "chat-core"
	if _, err := v.request(t, r); err != nil {
		t.Fatal(err)
	}
}

func TestHygieneMissingGitHubTeamRevokesNothing(t *testing.T) {
	v := setup(t)
	pk, err := v.request(t, Request{User: "jdoe", Kind: "personal", Provider: "openrouter", Purpose: "x"})
	if err != nil {
		t.Fatal(err)
	}
	v.writeTeam(t, "ml", "team: ml\n")
	v.e.Org = &fakeOrg{members: map[string]bool{"asmith": true}, teams: map[string][]string{}}
	fs, err := v.e.Hygiene(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "no key revoked") || !strings.Contains(err.Error(), "team ml") {
		t.Fatalf("want error, got %v", err)
	}
	for _, f := range fs {
		if f.Kind == "owner-left" {
			t.Fatalf("owner check ran: %+v", f)
		}
	}
	if _, ok := v.store.states[pk.Name]; !ok {
		t.Fatal("key was revoked")
	}

	// With the team present, its members count for the sanity check.
	v.e.Org = &fakeOrg{members: map[string]bool{"carol": true}, teams: map[string][]string{"ml": {"carol"}}}
	fs, err = v.e.Hygiene(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 || fs[0].Kind != "owner-left" || fs[0].Done {
		t.Fatalf("findings %+v", fs)
	}
}

func TestPlanWarnsMissingGitHubTeam(t *testing.T) {
	v := setup(t)
	v.writeTeam(t, "ml", "team: ml\n")
	v.e.Org = &fakeOrg{teams: map[string][]string{}}
	if p := v.plan(t); len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "teams/ml/team.yaml") {
		t.Fatalf("warnings %v", p.Warnings)
	}
	v.e.Org = &fakeOrg{teams: map[string][]string{"ml": nil}}
	if p := v.plan(t); len(p.Warnings) != 0 {
		t.Fatalf("warnings %v", p.Warnings)
	}
	v.e.Org = nil
	if p := v.plan(t); len(p.Warnings) != 0 {
		t.Fatalf("no token: warnings %v", p.Warnings)
	}
}
