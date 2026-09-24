// Package audit defines entries in the append-only audit log.
package audit

import "time"

type Event struct {
	ID          int64
	OccurredAt  time.Time
	Actor       string
	Action      string
	FlagKey     string // set for flag events
	Environment string
	SubjectUser string // set for user and token events
	Before      []byte // JSON, nil when absent
	After       []byte
}

// SystemActor records changes made outside the API, such as create-admin.
const SystemActor = "system"
