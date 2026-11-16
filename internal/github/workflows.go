package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/hatedabamboo/orkeys/internal/manifest"
)

// Dispatch starts a workflow_dispatch run of w. Without w.Ref, it uses the default branch of the repository.
// The token needs "Actions: read and write" on the repository. The workflow must have a workflow_dispatch trigger.
func (s *Secrets) Dispatch(ctx context.Context, w manifest.GitHubWorkflow) error {
	ref := w.Ref
	if ref == "" {
		var err error
		if ref, err = s.defaultBranch(ctx, w.Repo); err != nil {
			return fmt.Errorf("github run workflow %s: %w", w, err)
		}
	}
	slog.DebugContext(ctx, "github dispatch workflow", "repo", w.Repo, "workflow", w.Workflow, "ref", ref, "inputs", len(w.Inputs))
	req := map[string]any{"ref": ref}
	if len(w.Inputs) > 0 {
		req["inputs"] = w.Inputs
	}
	code, body, err := s.do(ctx, http.MethodPost, "/repos/"+w.Repo+"/actions/workflows/"+url.PathEscape(w.Workflow)+"/dispatches", req)
	if err != nil {
		return fmt.Errorf("github run workflow %s: %w", w, err)
	}
	// 204 is the classic answer. 200 comes with run details when the API returns them.
	if code != http.StatusNoContent && code != http.StatusOK {
		return fmt.Errorf("github run workflow %s at %s: %w", w, ref, apiErr(code, body))
	}
	return nil
}

func (s *Secrets) defaultBranch(ctx context.Context, repo string) (string, error) {
	code, body, err := s.do(ctx, http.MethodGet, "/repos/"+repo, nil)
	if err != nil {
		return "", err
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("read default branch: %w", apiErr(code, body))
	}
	var r struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.DefaultBranch == "" {
		return "", fmt.Errorf("read default branch of %s: no default_branch in the answer", repo)
	}
	return r.DefaultBranch, nil
}
