package vault

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}

// clientCert writes a self-signed client certificate and key, and returns it for the server CA pool.
func clientCert(t *testing.T, dir string) (*x509.Certificate, string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "orkeys-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := filepath.Join(dir, "client.crt"), filepath.Join(dir, "client.key")
	writePEM(t, certFile, "CERTIFICATE", der)
	writePEM(t, keyFile, "PRIVATE KEY", keyDER)
	return cert, certFile, keyFile
}

func TestClientCertFromEnv(t *testing.T) {
	dir := t.TempDir()
	cert, certFile, keyFile := clientCert(t, dir)

	logins := 0
	f := &fakeVault{data: map[string]map[string]any{"kv/a/b": {"k": "v"}}, meta: map[string]map[string]string{}}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/approle/login" {
			logins++
			_ = json.NewEncoder(w).Encode(map[string]any{"auth": map[string]any{"client_token": "t"}})
			return
		}
		f.ServeHTTP(w, r)
	}))
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	srv.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
	srv.StartTLS()
	defer srv.Close()

	caFile := filepath.Join(dir, "ca.crt")
	writePEM(t, caFile, "CERTIFICATE", srv.Certificate().Raw)

	t.Setenv(EnvAddr, srv.URL)
	t.Setenv(EnvToken, "")
	t.Setenv(EnvRoleID, "rid")
	t.Setenv(EnvSecretID, "sid")
	t.Setenv(EnvCACert, caFile)
	t.Setenv(EnvClientCert, certFile)
	t.Setenv(EnvClientKey, keyFile)

	c, err := NewFromEnv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data, err := c.Read(context.Background(), "kv/a/b")
	if err != nil || data["k"] != "v" || logins != 1 {
		t.Fatalf("with cert: data=%v err=%v logins=%d", data, err, logins)
	}

	t.Setenv(EnvClientCert, "")
	t.Setenv(EnvClientKey, "")
	if _, err := NewFromEnv(context.Background()); err == nil {
		t.Fatal("login without client cert must fail")
	}
}

func TestClientCertEnvErrors(t *testing.T) {
	dir := t.TempDir()
	_, certFile, keyFile := clientCert(t, dir)
	t.Setenv(EnvAddr, "https://vault.invalid")
	t.Setenv(EnvToken, "t")

	t.Setenv(EnvClientCert, certFile)
	t.Setenv(EnvClientKey, "")
	if _, err := NewFromEnv(context.Background()); err == nil || !strings.Contains(err.Error(), EnvClientKey) {
		t.Fatalf("cert without key: %v", err)
	}

	t.Setenv(EnvClientCert, "")
	t.Setenv(EnvClientKey, keyFile)
	if _, err := NewFromEnv(context.Background()); err == nil || !strings.Contains(err.Error(), EnvClientCert) {
		t.Fatalf("key without cert: %v", err)
	}

	t.Setenv(EnvClientCert, filepath.Join(dir, "missing.crt"))
	if _, err := NewFromEnv(context.Background()); err == nil || !strings.Contains(err.Error(), EnvClientCert) {
		t.Fatalf("missing cert file: %v", err)
	}

	t.Setenv(EnvClientCert, "")
	t.Setenv(EnvClientKey, "")
	t.Setenv(EnvCACert, filepath.Join(dir, "missing-ca.crt"))
	if _, err := NewFromEnv(context.Background()); err == nil || !strings.Contains(err.Error(), EnvCACert) {
		t.Fatalf("missing CA file: %v", err)
	}

	t.Setenv(EnvCACert, "")
	if _, err := NewFromEnv(context.Background()); err != nil {
		t.Fatalf("no TLS variables: %v", err)
	}
}
