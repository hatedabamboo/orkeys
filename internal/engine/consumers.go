package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/hatedabamboo/orkeys/internal/manifest"
	"github.com/hatedabamboo/orkeys/internal/state"
)

// Pusher is the driver of a push consumer type. orkeys writes the key to the target.
type Pusher interface {
	Push(ctx context.Context, spec manifest.PushSpec, value string) error
	// Remove deletes the target with this ID. The manifest can be gone, so Remove gets only the ID.
	// A missing target is not an error.
	Remove(ctx context.Context, targetID string) error
}

// Notifier is the driver of a notify consumer type. The consumer reads the key from the secrets store.
// Notify makes it read the new key.
type Notifier interface {
	Notify(ctx context.Context, spec manifest.NotifySpec) error
}

// Kube runs the Kubernetes steps of the eso and direct consumers.
type Kube interface {
	ForceSync(ctx context.Context, cluster, namespace, externalSecret string) error
	Restart(ctx context.Context, cluster, namespace, target string) error
}

// GitHubSecrets writes GitHub Actions secrets.
type GitHubSecrets interface {
	Put(ctx context.Context, t manifest.GitHubSecret, value string) error
	Delete(ctx context.Context, t manifest.GitHubSecret) error
}

// KubeNotifier is the driver of the eso and direct consumers. For eso it forces a sync of the ExternalSecret
// first. Then it restarts the workloads.
type KubeNotifier struct{ Kube Kube }

// Notify syncs and restarts. A failed sync skips the restarts. After a failed sync, a restart reads the old key.
func (n KubeNotifier) Notify(ctx context.Context, spec manifest.NotifySpec) error {
	var t *manifest.KubeTarget
	switch c := spec.(type) {
	case *manifest.ESOConsumer:
		if err := n.Kube.ForceSync(ctx, c.Cluster, c.Namespace, c.ExternalSecret); err != nil {
			return err
		}
		t = &c.KubeTarget
	case *manifest.DirectConsumer:
		t = &c.KubeTarget
	default:
		return fmt.Errorf("kubectl: no support for consumer %T", spec)
	}
	var errs []error
	for _, r := range t.Restart {
		errs = append(errs, n.Kube.Restart(ctx, t.Cluster, t.Namespace, r))
	}
	return errors.Join(errs...)
}

// GitHubPusher is the driver of the github consumer.
type GitHubPusher struct{ Secrets GitHubSecrets }

// Push writes value to the GitHub secret of spec.
func (g GitHubPusher) Push(ctx context.Context, spec manifest.PushSpec, value string) error {
	c, ok := spec.(*manifest.GitHubConsumer)
	if !ok {
		return fmt.Errorf("github: no support for consumer %T", spec)
	}
	return g.Secrets.Put(ctx, c.Target(), value)
}

// Remove deletes the GitHub secret with this ID.
func (g GitHubPusher) Remove(ctx context.Context, id string) error {
	t, err := manifest.ParseGitHubSecret(id)
	if err != nil {
		return err
	}
	return g.Secrets.Delete(ctx, t)
}

// Unavailable is a driver for a consumer type that this run cannot serve, for example GitHub without a token.
// Each call returns an error with the reason.
type Unavailable struct{ Reason string }

func (u Unavailable) Push(context.Context, manifest.PushSpec, string) error {
	return errors.New(u.Reason)
}
func (u Unavailable) Remove(context.Context, string) error              { return errors.New(u.Reason) }
func (u Unavailable) Notify(context.Context, manifest.NotifySpec) error { return errors.New(u.Reason) }

func splitPushKey(key string) (typ, id string) {
	typ, id, _ = strings.Cut(key, "/")
	return typ, id
}

// pushDiff returns the targets that do not hold the current key (push).
// It also returns the state keys of targets that the manifest no longer lists (drop).
// k is nil when the manifest is gone.
func pushDiff(k *manifest.Key, st *state.State) (push []manifest.PushTarget, drop []string) {
	want := map[string]bool{}
	if k != nil {
		for _, t := range k.PushTargets() {
			want[t.Key()] = true
			if st.Pushed[t.Key()] != st.KeyID {
				push = append(push, t)
			}
		}
	}
	for _, key := range slices.Sorted(maps.Keys(st.Pushed)) {
		if !want[key] {
			drop = append(drop, key)
		}
	}
	return push, drop
}

func pushDetail(push []manifest.PushTarget, drop []string) string {
	var d []string
	if len(push) > 0 {
		keys := make([]string, len(push))
		for i, t := range push {
			keys[i] = t.Key()
		}
		d = append(d, "write "+strings.Join(keys, ", "))
	}
	if len(drop) > 0 {
		d = append(d, "delete "+strings.Join(drop, ", "))
	}
	return strings.Join(d, "; ")
}

func (e *Engine) pusher(typ string) (Pusher, error) {
	if p, ok := e.Pushers[typ]; ok && p != nil {
		return p, nil
	}
	return nil, fmt.Errorf("consumer type %s: no driver configured", typ)
}

// syncPush writes the current key to the push targets of k. It deletes the targets that k no longer lists.
// It records each success in st.Pushed. The caller saves st.
// k is nil when the manifest is gone. Then syncPush deletes all targets.
func (e *Engine) syncPush(ctx context.Context, k *manifest.Key, st *state.State) error {
	push, drop := pushDiff(k, st)
	if len(push) == 0 && len(drop) == 0 {
		return nil
	}
	var errs []error
	if len(push) > 0 {
		value, err := e.readSecret(ctx, st)
		if err != nil {
			return fmt.Errorf("read key for push consumers: %w", err)
		}
		for _, t := range push {
			p, err := e.pusher(t.Type)
			if err == nil {
				err = p.Push(ctx, t.Spec, value)
			}
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if st.Pushed == nil {
				st.Pushed = map[string]string{}
			}
			st.Pushed[t.Key()] = st.KeyID
		}
	}
	for _, key := range drop {
		typ, id := splitPushKey(key)
		p, err := e.pusher(typ)
		if err == nil {
			err = p.Remove(ctx, id)
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		delete(st.Pushed, key)
	}
	return errors.Join(errs...)
}

// syncPushAndSave runs syncPush after a create, an import or a manifest change, and saves the state.
// On failure the next plan shows the sync again.
func (e *Engine) syncPushAndSave(ctx context.Context, k *manifest.Key, st *state.State) error {
	push, drop := pushDiff(k, st)
	if len(push) == 0 && len(drop) == 0 {
		return nil
	}
	err := e.syncPush(ctx, k, st)
	if serr := e.putState(ctx, st); serr != nil {
		err = errors.Join(err, serr)
	}
	if err != nil {
		e.emit(st, LevelAlert, EventConsumersSyncFailed, "push consumers not synced: %v; the next apply tries again", err)
		return err
	}
	e.emit(st, LevelInfo, EventConsumersSynced, "push consumers: %s", pushDetail(push, drop))
	return nil
}

// notifyConsumers runs the notify consumers of k, at most NotifyConcurrency at a time.
func (e *Engine) notifyConsumers(ctx context.Context, k *manifest.Key) error {
	limit := max(1, e.NotifyConcurrency)
	sem := make(chan struct{}, limit)
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	fail := func(err error) {
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
	}
	for _, c := range k.Consumers {
		spec, ok := c.Spec.(manifest.NotifySpec)
		if !ok {
			if _, push := c.Spec.(manifest.PushSpec); !push {
				fail(fmt.Errorf("consumer type %s: neither push nor notify", c.Type))
			}
			continue
		}
		n, ok := e.Notifiers[c.Type]
		if !ok || n == nil {
			fail(fmt.Errorf("consumer type %s: no driver configured", c.Type))
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := n.Notify(ctx, spec); err != nil {
				fail(err)
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// syncConsumers brings all consumers of k to the current key. Push targets go first.
func (e *Engine) syncConsumers(ctx context.Context, k *manifest.Key, st *state.State) error {
	return errors.Join(e.syncPush(ctx, k, st), e.notifyConsumers(ctx, k))
}
