package deliver

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"os"
	"strings"
	"testing"

	"filippo.io/age"
	"filippo.io/age/agessh"
	"filippo.io/age/armor"
	"golang.org/x/crypto/ssh"
)

func TestDeliverRoundtrip(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sshPub, _ := ssh.NewPublicKey(pub)
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))

	d := &AgeSSH{OutDir: t.TempDir(), FetchKeys: func(context.Context, string) ([]string, error) {
		return []string{"ecdsa-sha2-nistp256 AAAA unsupported", line}, nil
	}}
	path, err := d.Deliver(context.Background(), "jdoe", "local-jdoe-1", "sk-secret")
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(path)
	defer func() { _ = f.Close() }()
	id, err := agessh.NewEd25519Identity(priv)
	if err != nil {
		t.Fatal(err)
	}
	r, err := age.Decrypt(armor.NewReader(f), id)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(r)
	if strings.TrimSpace(string(out)) != "sk-secret" {
		t.Fatalf("got %q", out)
	}

	d.FetchKeys = func(context.Context, string) ([]string, error) { return nil, nil }
	if _, err := d.Deliver(context.Background(), "nokeys", "x", "s"); err == nil {
		t.Fatal("want error without keys")
	}
}
