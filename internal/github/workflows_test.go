package github

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hatedabamboo/orkeys/internal/manifest"
)

func TestDispatch(t *testing.T) {
	type run struct {
		Path   string
		Ref    string
		Inputs map[string]string
	}
	var runs []run
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		switch {
		case r.Method == http.MethodGet && path == "/repos/acme/search":
			_ = json.NewEncoder(w).Encode(map[string]string{"default_branch": "trunk"})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/actions/workflows/missing.yml/dispatches"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/dispatches"):
			var b struct {
				Ref    string            `json:"ref"`
				Inputs map[string]string `json:"inputs"`
			}
			_ = json.NewDecoder(r.Body).Decode(&b)
			runs = append(runs, run{path, b.Ref, b.Inputs})
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	ctx := t.Context()
	s := &Secrets{Token: "tok", API: srv.URL}

	if err := s.Dispatch(ctx, manifest.GitHubWorkflow{Repo: "acme/search", Workflow: "deploy.yml", Ref: "v1", Inputs: map[string]string{"env": "prod"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Dispatch(ctx, manifest.GitHubWorkflow{Repo: "acme/search", Workflow: "deploy.yml"}); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].Path != "/repos/acme/search/actions/workflows/deploy.yml/dispatches" ||
		runs[0].Ref != "v1" || runs[0].Inputs["env"] != "prod" || runs[1].Ref != "trunk" || runs[1].Inputs != nil {
		t.Fatalf("runs: %+v", runs)
	}
	err := s.Dispatch(ctx, manifest.GitHubWorkflow{Repo: "acme/search", Workflow: "missing.yml", Ref: "main"})
	if err == nil || !strings.Contains(err.Error(), "acme/search/missing.yml at main: HTTP 404: Not Found") {
		t.Fatalf("want 404, got %v", err)
	}
	err = s.Dispatch(ctx, manifest.GitHubWorkflow{Repo: "acme/gone", Workflow: "deploy.yml"})
	if err == nil || !strings.Contains(err.Error(), "read default branch: HTTP 404") {
		t.Fatalf("want default branch error, got %v", err)
	}
}
