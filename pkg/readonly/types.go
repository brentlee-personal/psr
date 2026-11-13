// Package readonly provides native PostgreSQL approval and model-external execution.
package readonly

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"
	"ragflow/pkg/authn"
)

var (
	ErrUnverified = errors.New("readonly source unverified")
	ErrDenied = errors.New("readonly request denied")
	ErrExecution = errors.New("readonly execution failed")
	ErrCleanup = errors.New("readonly cleanup unconfirmed")
)

type Column struct { Name string `json:"name"`; Type string `json:"type"` }
// Policy is a protected current test-source grant, not an HTTP/model payload.
type Policy struct {
 Dialect string `json:"dialect,omitempty"`
 StarRocks *StarRocksPolicy `json:"starrocks,omitempty"`
	SourceID string `json:"source_id"`
	Database string `json:"database"`
	Role string `json:"readonly_role"`
	UserID string `json:"user_id"`
	TenantID string `json:"tenant_id"`
	State string `json:"state"`
	ExpiresAt time.Time `json:"expires_at"`
	Schema string `json:"schema"`
	Table string `json:"table"`
	RelationID string `json:"relation_id"`
	Columns []Column `json:"columns"`
	Functions []string `json:"functions"`
	ConnectionRef string `json:"connection_ref"`
	ReadonlyIdentityRef string `json:"readonly_identity_ref"`
	TargetID string `json:"target_id"`
	KnowledgeSHA256 string `json:"knowledge_sha256"`
	PlannerSHA256 string `json:"planner_sha256"`
	DataBindingSHA256 string `json:"data_binding_sha256"`
	SnapshotRef string `json:"snapshot_ref"`
	EnforcementRef string `json:"enforcement_ref"`
	LimitsRef string `json:"limits_ref"`
}
type Authority struct { Policy Policy; SHA256 string }
type Request struct {
	Identity authn.VerifiedIdentity
	SQL string
	ScopeSHA256 string
	Fingerprint string
	MaxRows int
	MaxBytes int
	Timeout time.Duration
}
type Approval struct { proof *approvalProof }
type approvalProof struct {
 compiledSQL string
	issuer *Source
	request Request
	id string
	expires time.Time
	used atomic.Bool
}
func (Approval) Format(f fmt.State,_ rune) { io.WriteString(f,"readonly-approval:redacted") }
func (Approval) MarshalJSON() ([]byte,error) { return []byte(`{"readonly_approval":"redacted"}`),nil }
func (*Approval) UnmarshalJSON([]byte) error { return ErrDenied }
func (a Approval) ID() string { if a.proof==nil{return ""};return a.proof.id }
func (a Approval) Fingerprint() string { if a.proof==nil{return ""};return a.proof.request.Fingerprint }
func (a Approval) ExpiresAt() time.Time { if a.proof==nil{return time.Time{}};return a.proof.expires }
type Result struct {
 Summary *ResultSummary
	Fingerprint string
	Terminal string
	CleanupConfirmed bool
	Complete bool
	Columns []string
	Rows [][]any
}
type Source struct {
 dialect string
	db *sql.DB
	verifier *authn.Verifier
	policyPath string
	connectionPath string
	connectionHash string
	slots chan struct{}
	uncertain atomic.Bool
}
func (*Source) Format(f fmt.State,_ rune) { io.WriteString(f,"readonly-source:redacted") }
func (s *Source) Close() error { return s.db.Close() }
func (s *Source) CheckIdentity(ctx context.Context,identity authn.VerifiedIdentity) error {
	if s==nil || s.verifier==nil || ctx==nil {return ErrUnverified}
	if err:=s.verifier.CheckCurrentIdentity(ctx,identity);err!=nil{if errors.Is(err,authn.ErrDenied){return ErrDenied};return ErrUnverified};return nil
}
