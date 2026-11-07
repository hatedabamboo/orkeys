package backend

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/hatedabamboo/orkeys/internal/awssm"
	"github.com/hatedabamboo/orkeys/internal/s3store"
	"github.com/hatedabamboo/orkeys/internal/secrets"
	"github.com/hatedabamboo/orkeys/internal/state"
	"github.com/hatedabamboo/orkeys/internal/vault"
)

const (
	SecretsVault = "vault"
	SecretsAWS   = "aws"
)

type Factory struct {
	mu    sync.Mutex
	vault *vault.Client
	aws   *aws.Config
}

func (f *Factory) vaultClient(ctx context.Context) (*vault.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.vault == nil {
		c, err := vault.NewFromEnv(ctx)
		if err != nil {
			return nil, err
		}
		f.vault = c
	}
	return f.vault, nil
}

func (f *Factory) awsConfig(ctx context.Context) (aws.Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.aws == nil {
		c, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			return aws.Config{}, fmt.Errorf("aws config: %w", err)
		}
		f.aws = &c
	}
	return *f.aws, nil
}

func ParseState(raw string) (scheme, bucketOrPath, prefix string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", "", fmt.Errorf("state %q: %w", raw, err)
	}
	switch u.Scheme {
	case "vault":
		p := strings.Trim(u.Host+u.Path, "/")
		if !strings.Contains(p, "/") {
			return "", "", "", fmt.Errorf("state %q: want vault://<mount>/<path>", raw)
		}
		return "vault", p, "", nil
	case "s3":
		if u.Host == "" {
			return "", "", "", fmt.Errorf("state %q: want s3://<bucket>/<prefix>", raw)
		}
		return "s3", u.Host, strings.Trim(u.Path, "/"), nil
	}
	return "", "", "", fmt.Errorf("state %q: want vault://<mount>/<path> or s3://<bucket>/<prefix>", raw)
}

func (f *Factory) State(ctx context.Context, raw string) (state.Store, error) {
	scheme, a, b, err := ParseState(raw)
	if err != nil {
		return nil, err
	}
	if scheme == "vault" {
		c, err := f.vaultClient(ctx)
		if err != nil {
			return nil, err
		}
		return &vault.StateStore{C: c, Prefix: a}, nil
	}
	cfg, err := f.awsConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &s3store.StateStore{API: s3.NewFromConfig(cfg), Bucket: a, Prefix: b}, nil
}

func (f *Factory) Secrets(ctx context.Context, kind string) (secrets.Store, error) {
	switch kind {
	case SecretsVault, "":
		c, err := f.vaultClient(ctx)
		if err != nil {
			return nil, err
		}
		return &vault.SecretStore{C: c}, nil
	case SecretsAWS:
		cfg, err := f.awsConfig(ctx)
		if err != nil {
			return nil, err
		}
		return &awssm.SecretStore{API: secretsmanager.NewFromConfig(cfg), RecoveryWindow: 7}, nil
	}
	return nil, fmt.Errorf("secrets %q: want vault or aws", kind)
}
