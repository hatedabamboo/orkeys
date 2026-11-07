package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type Kubectl struct {
	Bin          string
	Context      func(cluster string) string
	SyncTimeout  time.Duration
	RolloutLimit time.Duration
	PollEvery    time.Duration
	run          func(ctx context.Context, args ...string) ([]byte, error)
}

func (k *Kubectl) exec(ctx context.Context, cluster, ns string, args ...string) ([]byte, error) {
	full := append([]string{"--context", k.Context(cluster), "--namespace", ns}, args...)
	if k.run != nil {
		return k.run(ctx, full...)
	}
	bin := k.Bin
	if bin == "" {
		bin = "kubectl"
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, full...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("kubectl %s: %w: %s", strings.Join(full, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (k *Kubectl) ForceSync(ctx context.Context, cluster, ns, name string) error {
	start := time.Now().UTC().Truncate(time.Second)
	ann := "force-sync=" + strconv.FormatInt(start.Unix(), 10)
	if _, err := k.exec(ctx, cluster, ns, "annotate", "externalsecret", name, ann, "--overwrite"); err != nil {
		return err
	}
	timeout, every := k.SyncTimeout, k.PollEvery
	if timeout == 0 {
		timeout = 3 * time.Minute
	}
	if every == 0 {
		every = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		out, err := k.exec(ctx, cluster, ns, "get", "externalsecret", name, "-o", "json")
		if err != nil {
			return err
		}
		ok, err := synced(out, start)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("externalsecret %s/%s/%s: not synced after %s", cluster, ns, name, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
	}
}

func synced(raw []byte, since time.Time) (bool, error) {
	var es struct {
		Status struct {
			RefreshTime string `json:"refreshTime"`
			Conditions  []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
		} `json:"status"`
	}
	if err := json.Unmarshal(raw, &es); err != nil {
		return false, err
	}
	if es.Status.RefreshTime == "" {
		return false, nil
	}
	t, err := time.Parse(time.RFC3339, es.Status.RefreshTime)
	if err != nil {
		return false, fmt.Errorf("externalsecret refreshTime: %w", err)
	}
	if t.Before(since) {
		return false, nil
	}
	for _, c := range es.Status.Conditions {
		if c.Type == "Ready" {
			return c.Status == "True", nil
		}
	}
	return false, nil
}

func (k *Kubectl) Restart(ctx context.Context, cluster, ns, target string) error {
	kind, name, _ := strings.Cut(target, "/")
	switch strings.ToLower(kind) {
	case "cronjob", "cj":
		return nil // the next job run reads the new secret
	case "deploy":
		kind = "deployment"
	case "sts":
		kind = "statefulset"
	case "ds":
		kind = "daemonset"
	}
	ref := kind + "/" + name
	if _, err := k.exec(ctx, cluster, ns, "rollout", "restart", ref); err != nil {
		return err
	}
	limit := k.RolloutLimit
	if limit == 0 {
		limit = 10 * time.Minute
	}
	_, err := k.exec(ctx, cluster, ns, "rollout", "status", ref, "--timeout", limit.String())
	return err
}
