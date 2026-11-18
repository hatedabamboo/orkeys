package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/hatedabamboo/orkeys/internal/elevenlabs"
)

type fakeELServer struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeELServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(body)))
	const base = "/v1/service-accounts/sa_1/api-keys"
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/service-accounts":
		_, _ = io.WriteString(w, `{"service-accounts":[{"service_account_user_id":"sa_1","name":"llm","created_at_unix":1793520000,"api-keys":[{"key_id":"self"}]}]}`)
	case r.Method == http.MethodGet && r.URL.Path == base:
		// The management key is test-mgmt. The list gives the SHA-256 of its value.
		_, _ = io.WriteString(w, strings.Replace(`{"api-keys":[
			{"name":"orkeys","hint":"aaaa","key_id":"self","service_account_user_id":"sa_1","hashed_xi_api_key":"h0","is_disabled":false,"permissions":["service_account_write"]},
			{"name":"alpha","hint":"bbbb","key_id":"k1","service_account_user_id":"sa_1","hashed_xi_api_key":"h1","created_at_unix":1793520000,
			 "is_disabled":false,"permissions":["text_to_speech","voices_read"],"character_limit":100000,"character_count":2500,"expires_at_unix":1801296000},
			{"name":"beta","hint":"cccc","key_id":"k2","service_account_user_id":"sa_1","hashed_xi_api_key":"h2",
			 "is_disabled":true,"disable_reason":"exposed_publicly","character_limit":null,"character_count":null}]}`, "h0", elevenlabs.KeyHash("test-mgmt"), 1))
	case r.Method == http.MethodPost && r.URL.Path == base:
		_, _ = io.WriteString(w, `{"xi-api-key":"sk_new","key_id":"k3"}`)
	case strings.HasPrefix(r.URL.Path, base+"/"):
		_, _ = io.WriteString(w, `null`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func runEL(t *testing.T, srv *httptest.Server, args ...string) (string, error) {
	t.Helper()
	t.Setenv(EnvELKey, "test-mgmt")
	t.Setenv(EnvELBase, srv.URL)
	t.Setenv(EnvELServiceAccount, "sa_1")
	t.Setenv(EnvELPrice, "0.0002")
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	cmd := (&app{version: "test"}).root()
	cmd.SetArgs(append([]string{"el"}, args...))
	err := cmd.ExecuteContext(context.Background())
	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out), err
}

func (f *fakeELServer) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

func TestElevenLabsCommands(t *testing.T) {
	f := &fakeELServer{}
	srv := httptest.NewServer(f)
	defer srv.Close()

	out, err := runEL(t, srv, "sa")
	if err != nil || !strings.Contains(out, "sa_1") || !strings.Contains(out, "llm") {
		t.Fatalf("service-accounts: %v\n%s", err, out)
	}

	out, err = runEL(t, srv, "list", "-o", "wide")
	for _, want := range []string{"alpha", "2500", "100000", "$0.50", "$20.00", "2027-01-30 08:00", "text_to_speech,voices_read", "disabled:exposed_publicly"} {
		if err != nil || !strings.Contains(out, want) {
			t.Fatalf("list: want %q: %v\n%s", want, err, out)
		}
	}

	out, err = runEL(t, srv, "get", "beta")
	if err != nil || !strings.Contains(out, "exposed_publicly") || !strings.Contains(out, "cccc") {
		t.Fatalf("get: %v\n%s", err, out)
	}

	out, err = runEL(t, srv, "create", "--name", "ci", "--credit-limit", "5000", "-q")
	if err != nil || strings.TrimSpace(out) != "sk_new" {
		t.Fatalf("create: %v\n%s", err, out)
	}
	if want := `POST /v1/service-accounts/sa_1/api-keys {"name":"ci","permissions":["text_to_speech","speech_to_speech","speech_to_text","voices_read","models_read"],"character_limit":5000}`; f.last() != want {
		t.Fatalf("create request:\n got %s\nwant %s", f.last(), want)
	}

	if _, err := runEL(t, srv, "update", "alpha", "--name", "alpha2", "--credit-limit", "none", "--expires", "none", "--scopes", "speech_to_text"); err != nil {
		t.Fatal(err)
	}
	if want := `PATCH /v1/service-accounts/sa_1/api-keys/k1 {"character_limit":"clear","expires_at_unix":"clear","name":"alpha2","permissions":["speech_to_text"]}`; f.last() != want {
		t.Fatalf("update request:\n got %s\nwant %s", f.last(), want)
	}

	if _, err := runEL(t, srv, "disable", "alpha"); err != nil || f.last() != "POST /v1/service-accounts/sa_1/api-keys/k1/disable " {
		t.Fatalf("disable: %v %s", err, f.last())
	}
	if _, err := runEL(t, srv, "enable", "alpha"); err != nil || f.last() != `PATCH /v1/service-accounts/sa_1/api-keys/k1 {"is_enabled":true}` {
		t.Fatalf("enable: %v %s", err, f.last())
	}
	if _, err := runEL(t, srv, "enable", "beta"); err == nil || strings.HasPrefix(f.last(), "PATCH") {
		t.Fatalf("enable of an exposed key: %v %s", err, f.last())
	}

	if _, err := runEL(t, srv, "delete", "alpha", "-y"); err != nil || f.last() != "DELETE /v1/service-accounts/sa_1/api-keys/k1 " {
		t.Fatalf("delete: %v %s", err, f.last())
	}

	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"delete", "orkeys", "-y"}, "refusing to delete the key in " + EnvELKey},
		{[]string{"disable", "self"}, "refusing to disable the key in " + EnvELKey},
		{[]string{"delete", "nope", "-y"}, `no key matches "nope"`},
		{[]string{"create", "--name", "x", "--scopes", "all"}, "permission all is not allowed"},
		{[]string{"create", "--name", "x", "--expires", "5m"}, "elevenlabs allows 15m to 90d from now"},
		{[]string{"update", "alpha"}, "nothing to change"},
		{[]string{"update", "alpha", "--credit-limit", "lots"}, "is not a number of credits or none"},
		{[]string{"list", "-s", ""}, "service account is not set"},
	} {
		if _, err := runEL(t, srv, c.args...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%v: %v, want %q", c.args, err, c.want)
		}
	}
}
