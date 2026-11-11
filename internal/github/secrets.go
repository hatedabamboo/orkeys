package github

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/nacl/box"

	"github.com/hatedabamboo/orkeys/internal/manifest"
)

// Secrets writes GitHub Actions secrets: repository, environment and organization secrets.
// The token needs "Secrets: read and write" on the repositories (fine-grained token or GitHub App),
// and organization admin for organization secrets.
type Secrets struct {
	Token string
	API   string
	http  *http.Client
}

func (s *Secrets) client() *http.Client {
	if s.http == nil {
		s.http = &http.Client{Timeout: 30 * time.Second}
	}
	return s.http
}

func (s *Secrets) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	api := s.API
	if api == "" {
		api = "https://api.github.com"
	}
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(api, "/")+path, r)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, err
}

// base returns the API path of the secrets collection of the target.
func base(t manifest.GitHubSecret) string {
	switch {
	case t.Org != "":
		return "/orgs/" + url.PathEscape(t.Org) + "/actions/secrets"
	case t.Environment != "":
		return "/repos/" + t.Repo + "/environments/" + url.PathEscape(t.Environment) + "/secrets"
	}
	return "/repos/" + t.Repo + "/actions/secrets"
}

func httpErr(op string, t manifest.GitHubSecret, code int, body []byte) error {
	var m struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &m)
	if m.Message != "" {
		return fmt.Errorf("github %s %s: HTTP %d: %s", op, t, code, m.Message)
	}
	return fmt.Errorf("github %s %s: HTTP %d", op, t, code)
}

// Put encrypts value with the public key of the target and writes the secret.
func (s *Secrets) Put(ctx context.Context, t manifest.GitHubSecret, value string) error {
	code, body, err := s.do(ctx, http.MethodGet, base(t)+"/public-key", nil)
	if err != nil {
		return fmt.Errorf("github public key %s: %w", t, err)
	}
	if code != http.StatusOK {
		return httpErr("public key", t, code, body)
	}
	var pk struct {
		KeyID string `json:"key_id"`
		Key   string `json:"key"`
	}
	if err := json.Unmarshal(body, &pk); err != nil {
		return fmt.Errorf("github public key %s: %w", t, err)
	}
	raw, err := base64.StdEncoding.DecodeString(pk.Key)
	if err != nil || len(raw) != 32 {
		return fmt.Errorf("github public key %s: not a 32-byte base64 key", t)
	}
	var recipient [32]byte
	copy(recipient[:], raw)
	sealed, err := box.SealAnonymous(nil, []byte(value), &recipient, rand.Reader)
	if err != nil {
		return fmt.Errorf("github encrypt %s: %w", t, err)
	}
	req := map[string]string{
		"encrypted_value": base64.StdEncoding.EncodeToString(sealed),
		"key_id":          pk.KeyID,
	}
	if t.Org != "" {
		req["visibility"] = t.Visibility
		if req["visibility"] == "" {
			req["visibility"] = "private"
		}
	}
	code, body, err = s.do(ctx, http.MethodPut, base(t)+"/"+url.PathEscape(t.Name), req)
	if err != nil {
		return fmt.Errorf("github write %s: %w", t, err)
	}
	if code != http.StatusCreated && code != http.StatusNoContent {
		return httpErr("write", t, code, body)
	}
	return nil
}

// Delete removes the secret. A missing secret is not an error.
func (s *Secrets) Delete(ctx context.Context, t manifest.GitHubSecret) error {
	code, body, err := s.do(ctx, http.MethodDelete, base(t)+"/"+url.PathEscape(t.Name), nil)
	if err != nil {
		return fmt.Errorf("github delete %s: %w", t, err)
	}
	if code != http.StatusNoContent && code != http.StatusNotFound {
		return httpErr("delete", t, code, body)
	}
	return nil
}
