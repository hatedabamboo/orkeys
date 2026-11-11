package github

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/nacl/box"

	"github.com/hatedabamboo/orkeys/internal/manifest"
)

func TestSecretsPutAndDelete(t *testing.T) {
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{} // path -> decrypted value
	vis := map[string]string{} // path -> visibility
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		path := r.URL.EscapedPath()
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/public-key"):
			_ = json.NewEncoder(w).Encode(map[string]string{"key_id": "k1", "key": base64.StdEncoding.EncodeToString(pub[:])})
		case r.Method == http.MethodPut:
			var b struct {
				Value      string `json:"encrypted_value"`
				KeyID      string `json:"key_id"`
				Visibility string `json:"visibility"`
			}
			_ = json.NewDecoder(r.Body).Decode(&b)
			sealed, _ := base64.StdEncoding.DecodeString(b.Value)
			plain, ok := box.OpenAnonymous(nil, sealed, pub, priv)
			if !ok || b.KeyID != "k1" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			got[path], vis[path] = string(plain), b.Visibility
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodDelete && strings.HasSuffix(path, "/GONE"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodDelete:
			delete(got, path)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	s := &Secrets{Token: "tok", API: srv.URL}

	cases := map[string]manifest.GitHubSecret{
		"/repos/acme/search/actions/secrets/OR_KEY":            {Repo: "acme/search", Name: "OR_KEY"},
		"/repos/acme/search/environments/prod%20eu/secrets/OR": {Repo: "acme/search", Environment: "prod eu", Name: "OR"},
		"/orgs/acme/actions/secrets/OR_ORG":                    {Org: "acme", Name: "OR_ORG"},
	}
	for path, target := range cases {
		if err := s.Put(ctx, target, "sk-"+target.Name); err != nil {
			t.Fatal(err)
		}
		if got[path] != "sk-"+target.Name {
			t.Fatalf("%s: got %q (all: %v)", path, got[path], got)
		}
	}
	if vis["/orgs/acme/actions/secrets/OR_ORG"] != "private" || vis["/repos/acme/search/actions/secrets/OR_KEY"] != "" {
		t.Fatalf("visibility: %v", vis)
	}
	if err := s.Delete(ctx, manifest.GitHubSecret{Repo: "acme/search", Name: "OR_KEY"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["/repos/acme/search/actions/secrets/OR_KEY"]; ok {
		t.Fatal("not deleted")
	}
	if err := s.Delete(ctx, manifest.GitHubSecret{Repo: "acme/search", Name: "GONE"}); err != nil {
		t.Fatal("a missing secret must not be an error:", err)
	}
	bad := &Secrets{Token: "nope", API: srv.URL}
	if err := bad.Put(ctx, manifest.GitHubSecret{Repo: "acme/search", Name: "X"}, "v"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("want 401, got %v", err)
	}
}
