package engine

import (
	"fmt"
	"strings"
	"time"

	"github.com/hatedabamboo/orkeys/internal/state"
)

const (
	LevelInfo  = "info"
	LevelAlert = "alert"
)

const (
	EventKeyIssued           = "key_issued"
	EventKeyImported         = "key_imported"
	EventKeyRevoked          = "key_revoked"
	EventKeyDisabled         = "key_disabled"
	EventKeyEnabled          = "key_enabled"
	EventRotationStart       = "rotation_started"
	EventRotationSynced      = "rotation_synced"
	EventRotationFailed      = "rotation_failed"
	EventOldKeyRevoked       = "old_key_revoked"
	EventOldKeyInUse         = "old_key_in_use"
	EventExpiryNotice        = "expiry_notice"
	EventConsumersSynced     = "consumers_synced"
	EventConsumersSyncFailed = "consumers_sync_failed"
)

type Event struct {
	Time     time.Time `json:"time"`
	Level    string    `json:"level"`
	Type     string    `json:"type"`
	Key      string    `json:"key"`
	Kind     string    `json:"kind"`
	Team     string    `json:"team"`
	Provider string    `json:"provider"`
	Owner    string    `json:"owner,omitempty"`
	Channel  string    `json:"channel,omitempty"`
	Message  string    `json:"message"`
	RunURL   string    `json:"run_url,omitempty"`
}

func (ev Event) String() string {
	return fmt.Sprintf("[%s] %s %s: %s", ev.Level, ev.Type, ev.Key, ev.Message)
}

type EventSink interface {
	Emit(Event)
}

type EventLog struct{ Events []Event }

func (l *EventLog) Emit(ev Event) { l.Events = append(l.Events, ev) }

func (l *EventLog) Has(typ, substr string) bool {
	for _, ev := range l.Events {
		if ev.Type == typ && strings.Contains(ev.Message, substr) {
			return true
		}
	}
	return false
}

func (e *Engine) emit(st *state.State, level, typ, format string, args ...any) {
	if e.Events == nil {
		return
	}
	ch := st.NotifyChannel
	if level == LevelAlert {
		ch = st.AlertChannel
	}
	e.Events.Emit(Event{
		Time: e.now(), Level: level, Type: typ, Key: st.Name, Kind: st.Kind, Team: st.Team, Provider: st.Provider,
		Owner: st.Owner, Channel: ch, Message: fmt.Sprintf(format, args...), RunURL: e.RunURL,
	})
}
