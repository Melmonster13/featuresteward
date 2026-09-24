package flag

import (
	"reflect"
	"time"
)

// RequestStatus is where a change request is in its life.
type RequestStatus string

const (
	RequestPending   RequestStatus = "pending"
	RequestApproved  RequestStatus = "approved"
	RequestRejected  RequestStatus = "rejected"
	RequestCancelled RequestStatus = "cancelled"
	RequestExpired   RequestStatus = "expired"
)

// RequestTTL is how long a change request waits for review.
const RequestTTL = 7 * 24 * time.Hour

// ChangeRequest proposes a new config for a flag in one environment,
// for a second person to approve.
type ChangeRequest struct {
	ID          int64
	FlagKey     string
	Environment string
	RequestedBy string
	Reason      string
	// Base is the environment's config when the request was made.
	// Approval applies Proposed only if the environment still has Base.
	Base          EnvConfig
	Proposed      EnvConfig
	Status        RequestStatus
	ReviewedBy    string
	ReviewComment string
	CreatedAt     time.Time
	ExpiresAt     time.Time
	ResolvedAt    *time.Time
}

// RequestFilter selects change requests; empty fields match everything.
type RequestFilter struct {
	Status  RequestStatus
	FlagKey string
}

const (
	ActionRequestCreated   = "change_request.created"
	ActionRequestApproved  = "change_request.approved"
	ActionRequestRejected  = "change_request.rejected"
	ActionRequestCancelled = "change_request.cancelled"
	ActionRequestExpired   = "change_request.expired"
)

// RequestSnapshot is the audit record of a change request.
type RequestSnapshot struct {
	ID          int64         `json:"id"`
	Status      RequestStatus `json:"status"`
	RequestedBy string        `json:"requested_by"`
	Proposed    EnvConfig     `json:"proposed"`
	Reason      string        `json:"reason,omitempty"`
	Comment     string        `json:"comment,omitempty"`
}

func NewRequestSnapshot(r ChangeRequest) RequestSnapshot {
	return RequestSnapshot{
		ID: r.ID, Status: r.Status, RequestedBy: r.RequestedBy, Proposed: r.Proposed,
		Reason: r.Reason, Comment: r.ReviewComment,
	}
}

// Equal reports whether two configs behave the same. Nil and empty rules
// are equal.
func (c EnvConfig) Equal(o EnvConfig) bool {
	if len(c.Rules) == 0 && len(o.Rules) == 0 {
		return c.Enabled == o.Enabled && c.RolloutPercentage == o.RolloutPercentage
	}
	return reflect.DeepEqual(c, o)
}
