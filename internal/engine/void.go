package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hatedabamboo/orkeys/internal/manifest"
	"github.com/hatedabamboo/orkeys/internal/provider"
	"github.com/hatedabamboo/orkeys/internal/secrets"
	"github.com/hatedabamboo/orkeys/internal/state"
	"github.com/hatedabamboo/orkeys/internal/values"
)

// A void provider has no API. A person makes a key at the vendor and writes it with orkeys put. orkeys keeps the
// expiry, writes the value to the secret places, syncs the consumers and reminds the team to rotate the key.
//
// A void key has no provider ID. Its ID is valueID of the value. The ID goes to the state, the secret metadata and
// the push records, as a provider ID does. Thus drift checks and push sync work without changes.
// A void key without secret has no value and an empty ID. orkeys tracks only its expiry.
//
// A provider with Caps.ManualCreate (fal) uses the same flow, because its API cannot create keys. Its value holds
// the provider ID (provider.KeyIdentifier), so the state has the real ID. orkeys then reads the spend of the key,
// and rotate revokes the old key at the provider after the grace time, with the usage check of processRevokes.

// valueID returns the ID of a void key value: a hash of the key name and the value.
// The hash is safe for random API keys. It does not protect a short password: a guess is easy to check.
func valueID(name, value string) string {
	sum := sha256.Sum256([]byte(name + "\x00" + value))
	return "fp-" + hex.EncodeToString(sum[:8])
}

// manual reports whether the provider name has no API.
func (e *Engine) manual(name string) bool {
	p, err := e.Providers.Get(name)
	return err == nil && p.Caps().Manual
}

// byHand reports whether a person makes the keys of the provider name: it has no API or cannot create keys.
func (e *Engine) byHand(name string) bool {
	p, err := e.Providers.Get(name)
	return err == nil && p.Caps().ByHand()
}

// keyID returns the ID of a value that a person entered for key k. A provider that names its keys in the value
// gives the provider ID. A void key gets valueID.
func (e *Engine) keyID(k *manifest.Key, value string) (string, error) {
	p, err := e.Providers.Get(k.Provider)
	if err != nil {
		return "", err
	}
	ki, ok := p.(provider.KeyIdentifier)
	if !ok {
		return valueID(k.Name, value), nil
	}
	if id := ki.KeyIDOf(value); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("%w: %s", errNotProviderKey, k.Provider)
}

// errNotProviderKey: the value of a key is not a key of its provider. Then orkeys has no provider ID for it.
var errNotProviderKey = errors.New("the value is not a key of the provider")

// checkValue returns a warning when the value of key k cannot become its key: it is not a key of the provider, or
// the provider does not list it. keys is the provider list. A void provider has no list, so it checks nothing.
func (e *Engine) checkValue(k *manifest.Key, value string, keys []provider.Key) (id, warning string, err error) {
	id, err = e.keyID(k, value)
	if errors.Is(err, errNotProviderKey) {
		return "", fmt.Sprintf("%v; write the full key to the secret or run orkeys put %s", err, k.Name), nil
	}
	if err != nil {
		return "", "", err
	}
	if !e.manual(k.Provider) && provider.Find(keys, id) == nil {
		return id, fmt.Sprintf("key %s from the secret is not at %s; check the value, or wait until the new key shows in the list", short(id), k.Provider), nil
	}
	return id, "", nil
}

// vendor names a void provider for a person: the name, and the console page if policy.yaml sets it.
func (e *Engine) vendor(name string) string {
	if c := e.Inv.Policy.Providers[name].Options.Type[provider.OptConsole]; c != "" {
		return name + " (" + c + ")"
	}
	return name
}

// waiting reports whether a void key has secret places but no value yet.
func waiting(st *state.State) bool { return st.KeyID == "" && len(st.Secrets) > 0 }

// planVoid plans a key that a person makes at the vendor. orkeys cannot make the key. It saves the state, moves the
// value between secret places and syncs the push consumers. keys is the provider list (empty for void).
func (e *Engine) planVoid(ctx context.Context, plan *Plan, act Action, keys []provider.Key) error {
	k, st := act.Key, act.State
	warn := func(format string, args ...any) {
		plan.Warnings = append(plan.Warnings, k.Name+": "+fmt.Sprintf(format, args...))
	}
	add := func(typ ActionType, detail string) {
		a := act
		a.Type, a.Detail = typ, detail
		plan.Actions = append(plan.Actions, a)
	}
	want := e.stateFor(k)
	switch {
	case st == nil:
		detail, warning, err := e.registerDetail(ctx, k, want, keys)
		switch {
		case err != nil:
			return err
		case warning != "":
			warn("%s", warning)
		default:
			add(ActRegister, detail)
		}
		return nil
	case st.Provider != k.Provider || st.Kind != k.Kind:
		warn("provider or kind changed; add a key with a new name instead")
		return nil
	case !state.SameSecrets(want.Secrets, st.Secrets):
		add(ActMoveSecret, moveDetail(st, want.Secrets))
		return nil
	}
	typ, detail, warning, err := e.planValue(ctx, k, st, keys)
	switch {
	case err != nil:
		return err
	case warning != "":
		warn("%s", warning)
	case typ != "":
		add(typ, detail)
	case st.KeyID != "":
		drift, err := e.secretDrift(ctx, st)
		if err != nil {
			return err
		}
		if drift != "" {
			warn("%s; write one value to all places with orkeys put %s", drift, k.Name)
		}
	}
	d := metaDiff(st, want)
	if st.Disabled != want.Disabled {
		d = strings.TrimPrefix(d+fmt.Sprintf(", disabled %v -> %v", st.Disabled, want.Disabled), ", ")
	}
	if d != "" {
		add(ActUpdateState, d)
	}
	// During a rotation, rotate writes the push consumers.
	if st.KeyID != "" && st.Rotation == state.RotationIdle {
		if push, drop := pushDiff(k, st); len(push)+len(drop) > 0 {
			add(ActSyncConsumers, pushDetail(push, drop))
		}
	}
	return nil
}

// registerDetail describes the register action of a new void key, or returns a warning when apply cannot register it.
func (e *Engine) registerDetail(ctx context.Context, k *manifest.Key, want *state.State, keys []provider.Key) (detail, warning string, err error) {
	when := issuedText(k, nil, true)
	if len(want.Secrets) == 0 {
		return fmt.Sprintf("track %s key; %s", k.Kind, when), "", nil
	}
	value, err := e.currentValue(ctx, want)
	switch {
	case errors.Is(err, errDifferentValues):
		return "", fmt.Sprintf("%s hold different values; make them equal, then apply", secretList(want.Secrets)), nil
	case err != nil:
		return "", "", err
	case value == "":
		return fmt.Sprintf("%s key; %s is empty: after apply, write the key there or run orkeys put %s", k.Kind, secretList(want.Secrets), k.Name), "", nil
	}
	id, warning, err := e.checkValue(k, value, keys)
	if err != nil || warning != "" {
		return "", warning, err
	}
	return fmt.Sprintf("%s key; adopt value %s from %s; %s", k.Kind, short(id), secretList(want.Secrets), when), "", nil
}

// planValue compares the value and the dates of a void key with its secret and its manifest. It reads the secret
// only when the secret can hold a new value: the key waits for a value, or the issued date of the manifest changed.
func (e *Engine) planValue(ctx context.Context, k *manifest.Key, st *state.State, keys []provider.Key) (typ ActionType, detail, warning string, err error) {
	issued := orNone(k.Issued.String())
	if k.Issued.String() == st.Issued && !waiting(st) {
		if exp := st.CreatedAt.Add(k.TTL.Duration); !st.CreatedAt.IsZero() && !exp.Equal(st.ExpiresAt) {
			return ActSetDates, fmt.Sprintf("expires %s -> %s (ttl %s)", day(st.ExpiresAt), day(exp), values.Days(k.TTL.Duration)), "", nil
		}
		return "", "", "", nil
	}
	if len(st.Secrets) == 0 {
		return ActSetDates, fmt.Sprintf("issued %s -> %s; %s", orNone(st.Issued), issued, issuedText(k, st, false)), "", nil
	}
	value, err := e.currentValue(ctx, st)
	switch {
	case errors.Is(err, errDifferentValues):
		return "", "", fmt.Sprintf("%s hold different values; make them equal, or run orkeys put %s", secretList(st.Secrets), k.Name), nil
	case err != nil:
		return "", "", "", err
	case value == "" && waiting(st):
		return "", "", fmt.Sprintf("no value yet; make a key at %s, then write it to %s or run: orkeys put %s",
			e.vendor(k.Provider), secretList(st.Secrets), k.Name), nil
	case value == "":
		return "", "", fmt.Sprintf("issued changed, but %s is empty; write the new key there first", secretList(st.Secrets)), nil
	}
	id, err := e.keyID(k, value)
	if err == nil && id == st.KeyID {
		return ActSetDates, fmt.Sprintf("same value %s; issued %s -> %s; %s", short(id), orNone(st.Issued), issued, issuedText(k, st, false)), "", nil
	}
	id, warning, err = e.checkValue(k, value, keys)
	if err != nil || warning != "" {
		return "", "", warning, err
	}
	detail = fmt.Sprintf("new value %s in %s; %s", short(id), secretList(st.Secrets), issuedText(k, st, true))
	switch {
	case st.KeyID == "":
	case e.manual(k.Provider):
		detail += "; old value " + short(st.KeyID) + " waits for revoke by hand"
	default:
		detail += "; rotate revokes old key " + short(st.KeyID) + " after the consumers sync and revoke_grace"
	}
	return ActAdoptValue, detail, "", nil
}

// issuedFor returns the creation time of the value of a void key: the issued date of the manifest, else the time in
// st, else now. A new value (fresh) does not take the time in st.
func (e *Engine) issuedFor(k *manifest.Key, st *state.State, fresh bool) time.Time {
	switch {
	case !k.Issued.IsZero():
		return k.Issued.Time
	case !fresh && st != nil && !st.CreatedAt.IsZero():
		return st.CreatedAt
	}
	return e.now()
}

// issuedText describes issuedFor in a plan detail. It does not write the current time: then the plan fingerprint
// stays the same between plan and apply --plan.
func issuedText(k *manifest.Key, st *state.State, fresh bool) string {
	var t time.Time
	switch {
	case !k.Issued.IsZero():
		t = k.Issued.Time
	case !fresh && st != nil && !st.CreatedAt.IsZero():
		t = st.CreatedAt
	default:
		return "issued now, expires in " + values.Days(k.TTL.Duration)
	}
	return "issued " + day(t) + ", expires " + day(t.Add(k.TTL.Duration))
}

// setDates sets the dates of a void key from issued and the ttl, and records the issued date of the manifest.
func setDates(st *state.State, k *manifest.Key, issued time.Time) {
	exp := issued.Add(k.TTL.Duration)
	if !exp.Equal(st.ExpiresAt) {
		st.ExpiryNotified, st.LastReminded = false, time.Time{}
	}
	st.CreatedAt, st.ExpiresAt, st.Issued = issued, exp, k.Issued.String()
}

// replaceValue makes id the current value of st, and the next rotate syncs the consumers. The old value of a void
// key waits for a person to revoke it (manual). Else rotate revokes it at the provider after the grace time.
func replaceValue(st *state.State, id, reason string, manual bool) {
	if st.KeyID != "" {
		source := state.RevokeRotation
		if manual {
			source = state.RevokeManual
		}
		st.PendingRevokes = append(st.PendingRevokes, state.PendingRevoke{KeyID: st.KeyID, Source: source, Reason: reason})
	}
	st.KeyID = id
	st.Rotation, st.RotationError = state.RotationPending, ""
}

// adoptValue takes the value that a person wrote to the secret of a void key. Then it works as orkeys put.
func (e *Engine) adoptValue(ctx context.Context, k *manifest.Key, st *state.State) error {
	value, err := e.currentValue(ctx, st)
	if err != nil {
		return fmt.Errorf("%s: %w", k.Name, err)
	}
	if value == "" {
		return fmt.Errorf("%s: %s is empty", k.Name, secretList(st.Secrets))
	}
	id, err := e.keyID(k, value)
	if err != nil {
		return fmt.Errorf("%s: %w", k.Name, err)
	}
	if id == st.KeyID {
		// The secret changed back after the plan. Only the dates change.
		setDates(st, k, e.issuedFor(k, st, false))
		return e.putState(ctx, st)
	}
	// The value is in one place at least. This write adds the metadata and fills the other places.
	if err := e.writeSecrets(ctx, k, st.Secrets, value, id, st.KeyID); err != nil {
		return fmt.Errorf("%s: %w", k.Name, err)
	}
	replaceValue(st, id, "replaced in the secret", e.manual(k.Provider))
	setDates(st, k, e.issuedFor(k, st, true))
	if err := e.putState(ctx, st); err != nil {
		return err
	}
	e.emit(st, LevelInfo, EventKeyIssued, "new value %s adopted from %s; expires %s; consumers sync at the next rotate",
		short(id), secretList(st.Secrets), day(st.ExpiresAt))
	return nil
}

func day(t time.Time) string { return t.Format(time.DateOnly) }

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func moveDetail(st *state.State, want []state.SecretRef) string {
	switch {
	case len(st.Secrets) == 0:
		return "add secret " + secretList(want) + "; wait for orkeys put"
	case len(want) == 0:
		return "remove secret " + secretList(st.Secrets) + "; track only the expiry"
	case st.KeyID == "":
		return fmt.Sprintf("secret moved %s -> %s; no value to copy", secretList(st.Secrets), secretList(want))
	}
	return fmt.Sprintf("secret moved %s -> %s; copy key %s", secretList(st.Secrets), secretList(want), short(st.KeyID))
}

func voidDeleteDetail(st *state.State) string {
	d := "manifest removed; remove the state"
	if len(st.Secrets) > 0 {
		d += "; delete secret " + secretList(st.Secrets)
	}
	if ids := oldValues(st, ""); len(ids) > 0 {
		d += "; revoke " + strings.Join(ids, ", ") + " at the vendor by hand"
	}
	return d
}

// oldValues returns the short IDs of the values in st that a person must revoke, without current.
func oldValues(st *state.State, current string) []string {
	var out []string
	for _, id := range st.KnownIDs() {
		if id != "" && id != current && !slices.Contains(out, short(id)) {
			out = append(out, short(id))
		}
	}
	return out
}

// errDifferentValues: the secret places of a void key hold different values. orkeys does not choose one.
var errDifferentValues = errors.New("secret places hold different values")

// currentValue returns the value in the secret places of st, or "" when all places are empty or missing.
// Two different values are an error: orkeys does not choose one.
func (e *Engine) currentValue(ctx context.Context, st *state.State) (string, error) {
	value := ""
	for _, r := range st.Secrets {
		data, err := e.Secrets.Read(ctx, r.Path)
		if errors.Is(err, secrets.ErrNotFound) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("read secret %s: %w", r, err)
		}
		switch v := data[r.Field]; {
		case v == "" || v == value:
		case value == "":
			value = v
		default:
			return "", fmt.Errorf("%w; write one value with orkeys put %s", errDifferentValues, st.Name)
		}
	}
	return value, nil
}

// register saves the state of a new void key. If the secret places hold a value, register adopts it: it writes the
// value with its metadata to all places and pushes it to the push consumers. Else the key waits for orkeys put.
func (e *Engine) register(ctx context.Context, k *manifest.Key) error {
	st := e.stateFor(k)
	value, err := e.currentValue(ctx, st)
	if err != nil {
		return fmt.Errorf("%s: %w", k.Name, err)
	}
	if waiting(st) && value == "" {
		st.Issued = k.Issued.String()
		if err := e.putState(ctx, st); err != nil {
			return err
		}
		e.emit(st, LevelInfo, EventKeyTracked, "key registered; %s is empty; make a key at %s, then write it there or run: orkeys put %s",
			secretList(st.Secrets), e.vendor(k.Provider), k.Name)
		return nil
	}
	setDates(st, k, e.issuedFor(k, nil, true))
	if value != "" {
		if st.KeyID, err = e.keyID(k, value); err != nil {
			return fmt.Errorf("%s: %w", k.Name, err)
		}
		// The value is in one place at least. This write adds the metadata and fills the other places.
		if err := e.writeSecrets(ctx, k, st.Secrets, value, st.KeyID, ""); err != nil {
			return fmt.Errorf("%s: %w", k.Name, err)
		}
	}
	if err := e.putState(ctx, st); err != nil {
		return err
	}
	from := "no secret"
	if value != "" {
		from = "value " + short(st.KeyID) + " adopted from " + secretList(st.Secrets)
	}
	e.emit(st, LevelInfo, EventKeyTracked, "key registered (%s); expires %s", from, day(st.ExpiresAt))
	return e.syncPushAndSave(ctx, k, st)
}

// moveSecret writes the value of a void key to the new secret places of k. The places that k no longer lists wait
// for the grace time, as the places of an old key do. The consumers sync at the next rotate: an ExternalSecret can
// point to a new place.
func (e *Engine) moveSecret(ctx context.Context, k *manifest.Key, st *state.State) error {
	want := e.stateFor(k).Secrets
	var gone []state.SecretRef
	for _, r := range st.Secrets {
		if !slices.Contains(want, r) {
			gone = append(gone, r)
		}
	}
	if st.KeyID != "" && len(want) > 0 {
		value, err := e.readSecret(ctx, st)
		if err != nil {
			return err
		}
		if id, err := e.keyID(k, value); err != nil || id != st.KeyID {
			return fmt.Errorf("%s: %s does not hold key %s; write the value with orkeys put %s", k.Name, st.Secrets[0], short(st.KeyID), k.Name)
		}
		if err := e.writeSecrets(ctx, k, want, value, st.KeyID, st.KeyID); err != nil {
			return fmt.Errorf("%s: %w", k.Name, err)
		}
	}
	detail := moveDetail(st, want)
	removed := ""
	switch {
	case st.KeyID != "" && len(want) > 0:
		if len(gone) > 0 {
			st.PendingRevokes = append(st.PendingRevokes, state.PendingRevoke{
				KeyID: st.KeyID, Source: state.RevokeManual, Reason: "secret moved", Secrets: gone,
			})
		}
		st.Rotation, st.RotationError = state.RotationPending, ""
	default:
		// No consumer reads a key without a value, or a key without secret. Validation allows consumers only
		// with a secret, so the push targets that are left go now.
		if len(want) == 0 && len(st.Pushed) > 0 {
			if err := e.syncPush(ctx, k, st); err != nil {
				return errors.Join(err, e.putState(ctx, st))
			}
		}
		for _, r := range gone {
			if err := e.Secrets.DeleteField(ctx, r.Path, r.Field); err != nil {
				return fmt.Errorf("remove secret %s: %w", r, err)
			}
		}
		if len(want) == 0 {
			removed = st.KeyID
			st.KeyID = ""
		}
	}
	if len(st.Secrets) == 0 {
		// A key without secret had only dates. The value that orkeys put writes next starts new dates.
		st.CreatedAt, st.ExpiresAt = time.Time{}, time.Time{}
	}
	st.Secrets = want
	copyMeta(st, e.stateFor(k))
	if err := e.putState(ctx, st); err != nil {
		return err
	}
	if removed != "" {
		e.emit(st, LevelAlert, EventRevokeByHand, "%s; revoke key %s at %s", detail, short(removed), e.vendor(st.Provider))
		return nil
	}
	e.emit(st, LevelInfo, EventRotationStart, "%s; consumers sync at the next rotate", detail)
	return nil
}

// voidRemoved sends the event after remove of a void key. orkeys cannot revoke it, so a person must.
func (e *Engine) voidRemoved(st *state.State, reason string) {
	ids := oldValues(st, "")
	if len(ids) == 0 {
		e.emit(st, LevelInfo, EventKeyUntracked, "%s; state removed", reason)
		return
	}
	if len(st.Secrets) > 0 {
		reason += "; secret deleted"
	}
	e.emit(st, LevelAlert, EventRevokeByHand, "%s; revoke key %s at %s", reason, strings.Join(ids, ", "), e.vendor(st.Provider))
}

// remindEvery is the time between two repeated alerts about one void key. It is shorter than a day: a daily rotate
// schedule can start some seconds early.
const remindEvery = 20 * time.Hour

// rotateVoid runs rotate for a key that a person makes. orkeys cannot make a new key. It syncs the consumers after
// orkeys put, reminds the team to rotate the key at the vendor and handles the old values. A void provider has no
// API: orkeys forgets an old value after the grace time, and a person revokes it. A provider with an API
// (ManualCreate) gets the old key revoked by processRevokes, with the usage check. keys is the provider list.
func (e *Engine) rotateVoid(ctx context.Context, st *state.State, kp *manifest.KindPolicy, keys []provider.Key, retry, force bool) error {
	k := e.Inv.Key(st.Name)
	if k == nil {
		e.debug(ctx, "rotate: skip key: no manifest; apply removes it", "key", st.Name)
		return nil
	}
	if force {
		return fmt.Errorf("a person makes %s keys; make a new key at %s, then run: orkeys put %s", st.Provider, e.vendor(st.Provider), st.Name)
	}
	manual := e.manual(st.Provider)
	now := e.now()
	changed := false
	if retry && st.Rotation == state.RotationFailed {
		st.Rotation, st.RotationError = state.RotationPending, ""
		// As in rotateOne: the retry says that the consumers are fixed. Only spend after the retry sync counts.
		for i := range st.PendingRevokes {
			if st.PendingRevokes[i].Source == state.RevokeRotation {
				st.PendingRevokes[i].After = time.Time{}
				st.PendingRevokes[i].Baseline = nil
			}
		}
		changed = true
	}
	if st.Rotation == state.RotationPending {
		ids, ok, err := e.syncPending(ctx, k, st, kp, now)
		if !ok {
			return err
		}
		changed = true
		// A value that only moved to another place is not old.
		ids = slices.DeleteFunc(slices.Compact(ids), func(id string) bool { return id == short(st.KeyID) })
		switch {
		case len(ids) == 0:
			e.emit(st, LevelInfo, EventRotationSynced, "consumers use key %s", short(st.KeyID))
		case manual:
			e.emit(st, LevelAlert, EventRevokeByHand, "consumers use key %s; revoke old key %s at %s now",
				short(st.KeyID), strings.Join(ids, ", "), e.vendor(st.Provider))
		default:
			e.emit(st, LevelInfo, EventRotationSynced, "consumers use key %s; old key %s will be revoked after %s",
				short(st.KeyID), strings.Join(ids, ", "), now.Add(kp.RevokeGrace.Duration).Format("2006-01-02 15:04 UTC"))
		}
	}
	// Manual entries are old values of a void key, and old secret places of a moved value. Both only wait for the
	// grace time. The other entries are old provider keys.
	var keep, revoke []state.PendingRevoke
	for _, pr := range st.PendingRevokes {
		if !manual && pr.Source != state.RevokeManual {
			revoke = append(revoke, pr)
			continue
		}
		if pr.After.IsZero() || now.Before(pr.After) {
			keep = append(keep, pr)
			continue
		}
		changed = true
		e.debug(ctx, "rotate: forget old value", "key", st.Name, "old_key_id", short(pr.KeyID), "reason", pr.Reason)
		for _, r := range pr.Secrets {
			if slices.Contains(st.Secrets, r) {
				continue
			}
			if err := e.Secrets.DeleteField(ctx, r.Path, r.Field); err != nil {
				e.logf("%s: remove old secret field %s: %v", st.Name, r, err)
			}
		}
	}
	st.PendingRevokes = revoke
	var rerr error
	if len(revoke) > 0 && st.Rotation != state.RotationFailed {
		c, err := e.processRevokes(ctx, st, keys, now)
		changed, rerr = changed || c, err
	}
	st.PendingRevokes = append(keep, st.PendingRevokes...)
	if !k.Disabled && e.remindVoid(st, kp, now) {
		changed = true
	}
	if rerr != nil {
		return errors.Join(rerr, e.putState(ctx, st))
	}
	if changed {
		return e.putState(ctx, st)
	}
	e.debug(ctx, "rotate: no change", "key", st.Name)
	return nil
}

// remindVoid sends the rotation reminders of a void key and returns true when st changed.
// The notice goes once, at rotate_before. Alerts about an expired key or a missing value repeat once a day.
func (e *Engine) remindVoid(st *state.State, kp *manifest.KindPolicy, now time.Time) bool {
	next := fmt.Sprintf("make a new key at %s, then run: orkeys put %s", e.vendor(st.Provider), st.Name)
	again := st.LastReminded.IsZero() || now.Sub(st.LastReminded) >= remindEvery
	switch {
	case waiting(st):
		if !again {
			return false
		}
		st.LastReminded = now
		e.emit(st, LevelAlert, EventValueMissing, "no value in %s; %s", secretList(st.Secrets), next)
	case st.ExpiresAt.IsZero():
		return false
	case !now.Before(st.ExpiresAt):
		if !again {
			return false
		}
		st.LastReminded = now
		e.emit(st, LevelAlert, EventKeyExpired, "key expired %s; %s", st.ExpiresAt.Format(time.DateOnly), next)
	case !st.ExpiryNotified && !now.Before(st.ExpiresAt.Add(-kp.RotateBefore.Duration)):
		st.ExpiryNotified = true
		e.emit(st, LevelInfo, EventExpiryNotice, "key expires %s; %s", st.ExpiresAt.Format(time.DateOnly), next)
	default:
		return false
	}
	return true
}

// checkListed returns an error when the provider of k has an API and does not list key id. A typo in the value
// must not replace a working key. A void provider has no list.
func (e *Engine) checkListed(ctx context.Context, k *manifest.Key, id string) error {
	if e.manual(k.Provider) {
		return nil
	}
	p, err := e.Providers.Get(k.Provider)
	if err != nil {
		return err
	}
	keys, err := p.List(ctx)
	if err != nil {
		return fmt.Errorf("list %s keys: %w", k.Provider, err)
	}
	if provider.Find(keys, id) == nil {
		return fmt.Errorf("%s does not list key %s; check the value", k.Provider, short(id))
	}
	return nil
}

// PutOptions is the input of Put.
type PutOptions struct {
	// Value is the new key. It is empty for a key without secret.
	Value string
	// Issued is the time when the vendor made the key. Zero means now. The key expires at Issued plus the ttl.
	Issued time.Time
}

// Put writes a new value of a void key to its secret places and starts a rotation. The next rotate syncs the
// consumers and tells the team to revoke the old value. For a key without secret, Put records only the dates.
// Put does not change the issued date that the state keeps from the manifest. If the manifest sets issued, the
// event tells the person to update it.
func (e *Engine) Put(ctx context.Context, name string, opt PutOptions) error {
	if err := e.ready(); err != nil {
		return err
	}
	k := e.Inv.Key(name)
	if k == nil {
		return fmt.Errorf("key %s: no manifest", name)
	}
	if !e.byHand(k.Provider) {
		return fmt.Errorf("key %s: provider %s has an API; to rotate the key run: orkeys rotate --now %s", name, k.Provider, name)
	}
	if k.TrackOnly() {
		return fmt.Errorf("key %s: track_only; orkeys does not write a tracked key", name)
	}
	st, err := e.State.GetState(ctx, name)
	if err != nil {
		return fmt.Errorf("key %s: %w", name, err)
	}
	if st == nil {
		return fmt.Errorf("key %s: no state; merge the manifest and run apply first", name)
	}
	if err := state.CheckSchema(st); err != nil {
		return err
	}
	if !state.SameSecrets(e.stateFor(k).Secrets, st.Secrets) {
		return fmt.Errorf("key %s: secret changed in the manifest; run apply first", name)
	}
	now := e.now()
	issued := opt.Issued.UTC()
	if opt.Issued.IsZero() {
		issued = now
	}
	if issued.After(now) {
		return fmt.Errorf("key %s: issued %s is in the future", name, day(issued))
	}
	exp := issued.Add(k.TTL.Duration)
	if !exp.After(now) {
		return fmt.Errorf("key %s: the key expires %s, in the past; make a new key at %s", name, day(exp), e.vendor(k.Provider))
	}
	switch {
	case len(st.Secrets) > 0 && opt.Value == "":
		return fmt.Errorf("key %s: empty value", name)
	case len(st.Secrets) == 0 && opt.Value != "":
		return fmt.Errorf("key %s: no secret in the manifest; orkeys has no place for the value", name)
	}

	oldID := st.KeyID
	if opt.Value != "" {
		id, err := e.keyID(k, opt.Value)
		if err != nil {
			return fmt.Errorf("key %s: %w", name, err)
		}
		if err := e.checkListed(ctx, k, id); err != nil {
			return fmt.Errorf("key %s: %w", name, err)
		}
		if id == oldID && opt.Issued.IsZero() {
			return fmt.Errorf("key %s: the secrets hold this value already; to change only the date, add --issued", name)
		}
		if id != oldID {
			if err := e.writeSecrets(ctx, k, st.Secrets, opt.Value, id, oldID); err != nil {
				return fmt.Errorf("key %s: %w", name, err)
			}
			replaceValue(st, id, "replaced by orkeys put", e.manual(k.Provider))
		}
	}
	st.CreatedAt, st.ExpiresAt = issued, exp
	st.ExpiryNotified, st.LastReminded = false, time.Time{}
	if err := e.putState(ctx, st); err != nil {
		if st.KeyID != oldID {
			return fmt.Errorf("%w; the secrets hold the new value: run orkeys put %s again", err, name)
		}
		return err
	}
	note := ""
	if !k.Issued.IsZero() && k.Issued.String() != day(issued) {
		note = fmt.Sprintf("; set issued: %s in %s", day(issued), k.Path)
	}
	if st.KeyID != oldID {
		e.emit(st, LevelInfo, EventKeyIssued, "new value %s written to %s; expires %s; consumers sync at the next rotate%s",
			short(st.KeyID), secretList(st.Secrets), day(exp), note)
		return nil
	}
	e.emit(st, LevelInfo, EventKeyIssued, "key dates set: issued %s, expires %s%s", day(issued), day(exp), note)
	return nil
}
