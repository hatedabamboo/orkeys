package vault

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hatedabamboo/orkeys/internal/httpx"
)

const (
	EnvAddr         = "VAULT_ADDR"
	EnvToken        = "VAULT_TOKEN"
	EnvNamespace    = "VAULT_NAMESPACE"
	EnvK8sRole      = "VAULT_K8S_ROLE"
	EnvK8sMount     = "VAULT_K8S_MOUNT"
	EnvRoleID       = "VAULT_ROLE_ID"
	EnvSecretID     = "VAULT_SECRET_ID"
	EnvAppRoleMount = "VAULT_APPROLE_MOUNT"
	EnvClientCert   = "VAULT_CLIENT_CERT"
	EnvClientKey    = "VAULT_CLIENT_KEY"
	EnvCACert       = "VAULT_CACERT"
	saTokenPath     = "/var/run/secrets/kubernetes.io/serviceaccount/token"
)

type Client struct {
	addr      string
	namespace string
	http      *http.Client

	mu    sync.Mutex
	token string
	login func(ctx context.Context) (string, error)
}

func New(addr, token string) *Client {
	return &Client{addr: strings.TrimRight(addr, "/"), token: token, http: &http.Client{Timeout: 30 * time.Second}}
}

func NewFromEnv(ctx context.Context) (*Client, error) {
	addr := strings.TrimSpace(os.Getenv(EnvAddr))
	if addr == "" {
		return nil, errors.New(EnvAddr + " is not set")
	}
	c := New(addr, strings.TrimSpace(os.Getenv(EnvToken)))
	c.namespace = os.Getenv(EnvNamespace)
	tlsCfg, err := tlsConfigFromEnv()
	if err != nil {
		return nil, err
	}
	if tlsCfg != nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.TLSClientConfig = tlsCfg
		c.http.Transport = tr
	}
	if c.token != "" {
		return c, nil
	}
	switch {
	case os.Getenv(EnvRoleID) != "":
		mount := envOr(EnvAppRoleMount, "approle")
		c.login = func(ctx context.Context) (string, error) {
			return c.loginWith(ctx, mount, map[string]string{
				"role_id":   os.Getenv(EnvRoleID),
				"secret_id": os.Getenv(EnvSecretID),
			})
		}
	case os.Getenv(EnvK8sRole) != "":
		mount := envOr(EnvK8sMount, "kubernetes")
		c.login = func(ctx context.Context) (string, error) {
			jwt, err := os.ReadFile(saTokenPath)
			if err != nil {
				return "", err
			}
			return c.loginWith(ctx, mount, map[string]string{"role": os.Getenv(EnvK8sRole), "jwt": strings.TrimSpace(string(jwt))})
		}
	default:
		return nil, errors.New("set " + EnvToken + ", " + EnvRoleID + " or " + EnvK8sRole)
	}
	tok, err := c.login(ctx)
	if err != nil {
		return nil, err
	}
	c.token = tok
	return c, nil
}

// tlsConfigFromEnv returns nil when no TLS variable is set.
func tlsConfigFromEnv() (*tls.Config, error) {
	certFile := strings.TrimSpace(os.Getenv(EnvClientCert))
	keyFile := strings.TrimSpace(os.Getenv(EnvClientKey))
	caFile := strings.TrimSpace(os.Getenv(EnvCACert))
	if certFile == "" && keyFile == "" && caFile == "" {
		return nil, nil
	}
	if (certFile == "") != (keyFile == "") {
		return nil, errors.New("set both " + EnvClientCert + " and " + EnvClientKey)
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if certFile != "" {
		pair, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("%s, %s: %w", EnvClientCert, EnvClientKey, err)
		}
		cfg.Certificates = []tls.Certificate{pair}
	}
	if caFile != "" {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", EnvCACert, err)
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s: no PEM certificates in %s", EnvCACert, caFile)
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func (c *Client) loginWith(ctx context.Context, mount string, body map[string]string) (string, error) {
	var out struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	err := httpx.Do(ctx, c.http, httpx.Request{
		Method: http.MethodPost,
		URL:    c.addr + "/v1/auth/" + mount + "/login",
		Header: c.header(""),
		In:     body,
		Out:    &out,
	})
	if err != nil {
		return "", fmt.Errorf("vault login at auth/%s: %w", mount, err)
	}
	return out.Auth.ClientToken, nil
}

func (c *Client) header(token string) http.Header {
	h := http.Header{}
	if token != "" {
		h.Set("X-Vault-Token", token)
	}
	if c.namespace != "" {
		h.Set("X-Vault-Namespace", c.namespace)
	}
	return h
}

func (c *Client) do(ctx context.Context, method, apiPath string, in, out any) error {
	c.mu.Lock()
	tok := c.token
	c.mu.Unlock()
	req := httpx.Request{Method: method, URL: c.addr + "/v1/" + apiPath, Header: c.header(tok), In: in, Out: out}
	err := httpx.Do(ctx, c.http, req)
	if httpx.StatusOf(err) == http.StatusForbidden && c.login != nil {
		tok, lerr := c.login(ctx)
		if lerr != nil {
			return errors.Join(err, lerr)
		}
		c.mu.Lock()
		c.token = tok
		c.mu.Unlock()
		req.Header = c.header(tok)
		err = httpx.Do(ctx, c.http, req)
	}
	return err
}

func split(path string) (mount, rest string, err error) {
	path = strings.Trim(path, "/")
	mount, rest, ok := strings.Cut(path, "/")
	if !ok || mount == "" || rest == "" {
		return "", "", fmt.Errorf("vault path %q: want <mount>/<path>", path)
	}
	return mount, rest, nil
}

func IsNotFound(err error) bool { return httpx.StatusOf(err) == http.StatusNotFound }

func (c *Client) Read(ctx context.Context, path string) (map[string]any, error) {
	mount, rest, err := split(path)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data struct {
			Data map[string]any `json:"data"`
		} `json:"data"`
	}
	err = c.do(ctx, http.MethodGet, mount+"/data/"+rest, nil, &out)
	if IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("vault read %s: %w", path, err)
	}
	return out.Data.Data, nil
}

func (c *Client) Write(ctx context.Context, path string, data map[string]any) error {
	mount, rest, err := split(path)
	if err != nil {
		return err
	}
	if err := c.do(ctx, http.MethodPost, mount+"/data/"+rest, map[string]any{"data": data}, nil); err != nil {
		return fmt.Errorf("vault write %s: %w", path, err)
	}
	return nil
}

func (c *Client) Patch(ctx context.Context, path string, data map[string]any) error {
	cur, err := c.Read(ctx, path)
	if err != nil {
		return err
	}
	if cur == nil {
		cur = map[string]any{}
	}
	for k, v := range data {
		cur[k] = v
	}
	return c.Write(ctx, path, cur)
}

func (c *Client) SetCustomMetadata(ctx context.Context, path string, meta map[string]string) error {
	mount, rest, err := split(path)
	if err != nil {
		return err
	}
	if err := c.do(ctx, http.MethodPost, mount+"/metadata/"+rest, map[string]any{"custom_metadata": meta}, nil); err != nil {
		return fmt.Errorf("vault metadata %s: %w", path, err)
	}
	return nil
}

func (c *Client) List(ctx context.Context, path string) ([]string, error) {
	mount, rest, err := split(path)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data struct {
			Keys []string `json:"keys"`
		} `json:"data"`
	}
	err = c.do(ctx, "LIST", mount+"/metadata/"+rest, nil, &out)
	if IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("vault list %s: %w", path, err)
	}
	return out.Data.Keys, nil
}

func (c *Client) Destroy(ctx context.Context, path string) error {
	mount, rest, err := split(path)
	if err != nil {
		return err
	}
	err = c.do(ctx, http.MethodDelete, mount+"/metadata/"+rest, nil, nil)
	if err != nil && !IsNotFound(err) {
		return fmt.Errorf("vault delete %s: %w", path, err)
	}
	return nil
}

func toMap(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(b, &m)
}

func fromMap(m map[string]any, v any) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

var ErrCASMismatch = errors.New("vault check-and-set mismatch")

func (c *Client) ReadVersion(ctx context.Context, path string) (map[string]any, int, error) {
	mount, rest, err := split(path)
	if err != nil {
		return nil, 0, err
	}
	var out struct {
		Data struct {
			Data     map[string]any `json:"data"`
			Metadata struct {
				Version int `json:"version"`
			} `json:"metadata"`
		} `json:"data"`
	}
	err = c.do(ctx, http.MethodGet, mount+"/data/"+rest, nil, &out)
	if IsNotFound(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("vault read %s: %w", path, err)
	}
	return out.Data.Data, out.Data.Metadata.Version, nil
}

func (c *Client) WriteCAS(ctx context.Context, path string, data map[string]any, cas int) error {
	mount, rest, err := split(path)
	if err != nil {
		return err
	}
	body := map[string]any{"data": data, "options": map[string]any{"cas": cas}}
	err = c.do(ctx, http.MethodPost, mount+"/data/"+rest, body, nil)
	if httpx.StatusOf(err) == http.StatusBadRequest && strings.Contains(err.Error(), "check-and-set") {
		return ErrCASMismatch
	}
	if err != nil {
		return fmt.Errorf("vault write %s: %w", path, err)
	}
	return nil
}
