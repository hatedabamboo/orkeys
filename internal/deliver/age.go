package deliver

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
	"filippo.io/age/agessh"
	"filippo.io/age/armor"
)

type AgeSSH struct {
	OutDir    string
	KeysURL   string
	FetchKeys func(ctx context.Context, user string) ([]string, error)
}

func (a *AgeSSH) Deliver(ctx context.Context, user, name, secret string) (string, error) {
	fetch := a.FetchKeys
	if fetch == nil {
		fetch = a.githubKeys
	}
	lines, err := fetch(ctx, user)
	if err != nil {
		return "", err
	}
	var recipients []age.Recipient
	for _, l := range lines {
		if r, err := agessh.ParseRecipient(l); err == nil {
			recipients = append(recipients, r)
		}
	}
	if len(recipients) == 0 {
		return "", fmt.Errorf("user %s has no ssh-ed25519 or ssh-rsa key on GitHub", user)
	}
	var buf bytes.Buffer
	aw := armor.NewWriter(&buf)
	w, err := age.Encrypt(aw, recipients...)
	if err != nil {
		return "", err
	}
	if _, err := w.Write([]byte(secret + "\n")); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	if err := aw.Close(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(a.OutDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(a.OutDir, name+".age")
	return path, os.WriteFile(path, buf.Bytes(), 0o644)
}

func (a *AgeSSH) githubKeys(ctx context.Context, user string) ([]string, error) {
	base := a.KeysURL
	if base == "" {
		base = "https://github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/"+user+".keys", nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github keys for %s: HTTP %d", user, resp.StatusCode)
	}
	var out []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			out = append(out, l)
		}
	}
	return out, sc.Err()
}
