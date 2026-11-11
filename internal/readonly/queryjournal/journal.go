// Package queryjournal provides internal query admission and recovery state.
// Only trusted server assembly may supply scopes, identifiers and observers.
// It does not execute SQL, prove database termination, or configure KV durability.
// Records and reservations have no expiry. KV persistence, eviction protection,
// backup and loss recovery must be verified separately before runtime adoption.
package queryjournal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	ErrInvalid            = errors.New("query journal: invalid input")
	ErrUnavailable        = errors.New("query journal: store unavailable or invalid")
	ErrBusy               = errors.New("query journal: capacity exhausted")
	ErrDuplicate          = errors.New("query journal: execution already recorded")
	ErrNotFound           = errors.New("query journal: execution not found")
	ErrConflict           = errors.New("query journal: state changed or binding mismatch")
	ErrUnconfirmed        = errors.New("query journal: terminal cleanup unconfirmed")
	ErrJournalFull        = errors.New("query journal: retained record limit reached")
	ErrAdmissionUncertain = errors.New("query journal: admission expired; stored reservation may remain")
)

// Scope is fixed by trusted service configuration, never by an HTTP/model input.
// All managers for a target and policy capacity scope must use the same Scope.
// PolicyScope is a stable capacity ID, never a policy version or request ID.
type Scope struct{ Target, PolicyScope string }

// Binding contains safe references only: never tokens, DSNs, SQL or row data.
type Binding struct {
	ExecutionID         string
	SourceRef           string
	PolicyVersion       string
	ApprovalFingerprint string
	Owner               string
	Fence               uint64
}

type Phase string

const (
	Unknown  Phase = ""
	Reserved Phase = "reserved"
	Running  Phase = "running"
	Terminal Phase = "terminal"
)

type Outcome string

const (
	OutcomeUnknown Outcome = ""
	Succeeded      Outcome = "succeeded"
	Failed         Outcome = "failed"
	Cancelled      Outcome = "cancelled"
	TimedOut       Outcome = "timed_out"
)

type Completeness string

const (
	CompletenessUnknown Completeness = ""
	Complete            Completeness = "complete"
	Truncated           Completeness = "truncated"
)

type Cleanup string

const (
	CleanupUnknown   Cleanup = ""
	CleanupConfirmed Cleanup = "confirmed"
)

// Record is secret-free bookkeeping; returned values never authorize SQL replay.
type Record struct {
	Binding        Binding
	Scope          Scope
	DeadlineMS     int64
	BackendLocator string
	Phase          Phase
	Outcome        Outcome
	Completeness   Completeness
	Cleanup        Cleanup
	EvidenceRef    string
	Slot           int
	Released       bool
	Version        uint64
	CreatedMS      int64
	UpdatedMS      int64
}

// Observation must come from an independently wired trusted backend observer.
// A request must never construct an observer or supply its terminal proof.
type Observation struct {
	Binding        Binding
	Version        uint64
	BackendLocator string
	Terminal       bool
	Cleanup        Cleanup
	Outcome        Outcome
	Completeness   Completeness
	EvidenceRef    string
}
type Observer interface {
	Observe(context.Context, Record) (Observation, error)
}

// Manager has no default observer, background work, retry, cancel or release API.
type Manager struct {
	client   *redis.Client
	scope    Scope
	key      string
	epoch    string
	observer Observer
}

// NativePool is implemented by the already initialized kvrocks.Client.
// It reuses that pool without reading configuration or opening connections.
type NativePool interface{ GetClient() *redis.Client }

var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var fingerprint = regexp.MustCompile(`^[a-f0-9]{64}$`)

const maxExactInteger = 1<<53 - 1 // Keep counters within portable JSON integer precision.

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}
func validBinding(b Binding) bool {
	return safeID.MatchString(b.ExecutionID) && safeID.MatchString(b.SourceRef) &&
		safeID.MatchString(b.PolicyVersion) && fingerprint.MatchString(b.ApprovalFingerprint) &&
		safeID.MatchString(b.Owner) && b.Fence > 0 && b.Fence <= maxExactInteger
}

// New fixes the namespace and capacity (two) for this service instance. A nil
// observer permits admission/storage tests but can never release a reservation.
// client must be supplied from kvrocks.Get().GetClient() by server assembly.
// epoch must be checked against independent deployment authority; it is not a
// key suffix and changing it cannot bypass an existing scope's reservations.
// New does not initialize an absent store. NativePool is the existing pool seam.
func New(client *redis.Client, scope Scope, epoch string, observer Observer) (*Manager, error) {
	if client == nil || !safeID.MatchString(scope.Target) || !safeID.MatchString(scope.PolicyScope) || !safeID.MatchString(epoch) {
		return nil, ErrInvalid
	}
	if nilInterface(observer) {
		observer = nil
	}
	digest := sha256.Sum256([]byte(scope.Target + "\x00" + scope.PolicyScope))
	return &Manager{client: client, scope: scope, epoch: epoch, key: "readonly:queryjournal:v1:{" + hex.EncodeToString(digest[:]) + "}", observer: observer}, nil
}

// NewFromNativePool reuses an initialized native Kvrocks pool. The caller must
// check kvrocks.Get() is nonnil before passing it; no global config is read here.
func NewFromNativePool(pool NativePool, scope Scope, epoch string, observer Observer) (*Manager, error) {
	if nilInterface(pool) {
		return nil, ErrInvalid
	}
	return New(pool.GetClient(), scope, epoch, observer)
}

// provisionScript is deliberately not exposed through Manager. A deployment
// owner must independently authorize a fresh store/epoch and run this one-shot
// primitive. Existing keys are always rejected; it cannot reset a live scope.
// Missing-store recovery is NOT authorized by a new Manager or by this package.
const provisionScript = `
if redis.call('EXISTS', KEYS[1]) ~= 0 then return 0 end
redis.call('HSET', KEYS[1], 'schema', '1', 'epoch', ARGV[1])
return 1`

const verifyStoreScript = `
if redis.call('PTTL', KEYS[1]) ~= -1 then return redis.error_reply('unverified store') end
if redis.call('HGET', KEYS[1], 'schema') ~= '1' or redis.call('HGET', KEYS[1], 'epoch') ~= ARGV[#ARGV] then return redis.error_reply('unverified store') end
`

const reserveScript = verifyStoreScript + `
if redis.call('HEXISTS', KEYS[1], ARGV[1]) == 1 then return {0, ''} end
local retained = redis.call('HLEN', KEYS[1]) - 2
for i = 1, 2 do retained = retained - redis.call('HEXISTS', KEYS[1], 'slot:' .. i) end
if retained >= 4096 then return {3, ''} end
local slot = 0
for i = 1, 2 do
  if redis.call('HEXISTS', KEYS[1], 'slot:' .. i) == 0 then slot = i; break end
end
if slot == 0 then return {1, ''} end
local raw = ARGV[slot + 1]
redis.call('HSET', KEYS[1], ARGV[1], raw, 'slot:' .. slot, ARGV[4])
return {2, raw}`

// Reserve atomically acquires one of two global slots and creates a record.
// Duplicate execution IDs always fail, including after confirmed termination.
// At 4096 retained records admission stops; this module never silently deletes
// unknown or terminal records. Retention/GC needs a separate explicit policy.
func (m *Manager) Reserve(ctx context.Context, b Binding, deadline time.Time) (Record, error) {
	if !validBinding(b) || deadline.IsZero() || !deadline.After(time.Now()) || deadline.UnixMilli() > maxExactInteger {
		return Record{}, ErrInvalid
	}
	if ctx.Err() != nil {
		return Record{}, ErrAdmissionUncertain
	}
	now := time.Now().UnixMilli()
	r := Record{Binding: b, Scope: m.scope, DeadlineMS: deadline.UnixMilli(), Phase: Reserved, Version: 1, CreatedMS: now, UpdatedMS: now}
	r.Slot = 1
	raw1, _ := json.Marshal(r)
	r.Slot = 2
	raw2, _ := json.Marshal(r)
	values, err := m.client.Eval(ctx, reserveScript, []string{m.key}, "record:"+b.ExecutionID, string(raw1), string(raw2), b.ExecutionID, m.epoch).Slice()
	if err != nil || len(values) != 2 {
		return Record{}, ErrUnavailable
	}
	status, ok := values[0].(int64)
	if !ok {
		return Record{}, ErrUnavailable
	}
	switch status {
	case 0:
		return Record{}, ErrDuplicate
	case 1:
		return Record{}, ErrBusy
	case 3:
		return Record{}, ErrJournalFull
	case 2:
		stored, ok := values[1].(string)
		if !ok {
			return Record{}, ErrUnavailable
		}
		r, err := m.decode(stored, b.ExecutionID)
		if err != nil {
			return Record{}, err
		}
		// Native pools may finish socket I/O after request cancellation. Reject
		// late authorization but retain any atomically written unknown slot.
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return Record{}, ErrAdmissionUncertain
		}
		return r, nil
	default:
		return Record{}, ErrUnavailable
	}
}

func (m *Manager) decode(raw, id string) (Record, error) {
	var r Record
	if json.Unmarshal([]byte(raw), &r) != nil || !validBinding(r.Binding) || r.Binding.ExecutionID != id || r.Scope != m.scope || !validRecord(r) {
		return Record{}, ErrUnavailable
	}
	return r, nil
}

func knownOutcome(outcome Outcome) bool {
	return outcome == Succeeded || outcome == Failed || outcome == Cancelled || outcome == TimedOut
}
func validRecord(r Record) bool {
	if r.Version == 0 || r.Version > maxExactInteger || r.Slot < 1 || r.Slot > 2 ||
		r.DeadlineMS <= 0 || r.DeadlineMS > maxExactInteger || r.CreatedMS <= 0 || r.CreatedMS > maxExactInteger ||
		r.UpdatedMS <= 0 || r.UpdatedMS > maxExactInteger ||
		(r.BackendLocator != "" && !safeID.MatchString(r.BackendLocator)) ||
		(r.EvidenceRef != "" && !safeID.MatchString(r.EvidenceRef)) {
		return false
	}
	if r.Released {
		return r.Phase == Terminal && r.Cleanup == CleanupConfirmed && knownOutcome(r.Outcome) &&
			r.BackendLocator != "" && r.EvidenceRef != "" &&
			(r.Completeness == CompletenessUnknown || r.Completeness == Complete || r.Completeness == Truncated)
	}
	if r.Outcome != OutcomeUnknown || r.Completeness != CompletenessUnknown || r.Cleanup != CleanupUnknown || r.EvidenceRef != "" {
		return false
	}
	switch r.Phase {
	case Reserved:
		return r.BackendLocator == ""
	case Running:
		return r.BackendLocator != ""
	case Unknown:
		return true
	default:
		return false
	}
}
func (m *Manager) load(ctx context.Context, id string) (Record, string, error) {
	if !safeID.MatchString(id) {
		return Record{}, "", ErrInvalid
	}
	raw, err := m.client.Eval(ctx, verifyStoreScript+`return redis.call('HGET', KEYS[1], ARGV[1])`, []string{m.key}, "record:"+id, m.epoch).Text()
	if errors.Is(err, redis.Nil) {
		return Record{}, "", ErrNotFound
	}
	if err != nil {
		return Record{}, "", ErrUnavailable
	}
	r, err := m.decode(raw, id)
	return r, raw, err
}
func (m *Manager) Read(ctx context.Context, id string) (Record, error) {
	r, _, err := m.load(ctx, id)
	return r, err
}

const casScript = verifyStoreScript + `
if redis.call('HGET', KEYS[1], ARGV[1]) ~= ARGV[2] then return 0 end
if redis.call('HGET', KEYS[1], ARGV[3]) ~= ARGV[4] then return 0 end
redis.call('HSET', KEYS[1], ARGV[1], ARGV[5])
if ARGV[6] == 'release' then redis.call('HDEL', KEYS[1], ARGV[3]) end
return 1`

func (m *Manager) cas(ctx context.Context, old Record, raw string, next Record, release bool) (Record, error) {
	if old.Version >= maxExactInteger {
		return Record{}, ErrConflict
	}
	next.Version = old.Version + 1
	next.UpdatedMS = time.Now().UnixMilli()
	data, _ := json.Marshal(next)
	action := "retain"
	if release {
		action = "release"
	}
	n, err := m.client.Eval(ctx, casScript, []string{m.key}, "record:"+old.Binding.ExecutionID, raw, "slot:"+strconv.Itoa(old.Slot), old.Binding.ExecutionID, string(data), action, m.epoch).Int64()
	if err != nil {
		return Record{}, ErrUnavailable
	}
	if n != 1 {
		return Record{}, ErrConflict
	}
	return next, nil
}

// BindBackend attaches a safe opaque locator once, before backend observation.
// Callers are trusted server adapters, not request handlers accepting locators.
func (m *Manager) BindBackend(ctx context.Context, expected Binding, locator string) (Record, error) {
	if !validBinding(expected) || !safeID.MatchString(locator) {
		return Record{}, ErrInvalid
	}
	r, raw, err := m.load(ctx, expected.ExecutionID)
	if err != nil {
		return Record{}, err
	}
	if r.Binding != expected || r.Released || r.BackendLocator != "" {
		return Record{}, ErrConflict
	}
	next := r
	next.BackendLocator = locator
	next.Phase = Running
	return m.cas(ctx, r, raw, next, false)
}

// TransferOwner is an explicit trusted recovery operation, never a lease timeout.
// It retains the slot and backend locator and advances the fence/version, so an
// in-flight observation from the previous owner cannot release the reservation.
func (m *Manager) TransferOwner(ctx context.Context, expected Binding, owner string) (Record, error) {
	if !validBinding(expected) || !safeID.MatchString(owner) || owner == expected.Owner || expected.Fence >= maxExactInteger {
		return Record{}, ErrInvalid
	}
	r, raw, err := m.load(ctx, expected.ExecutionID)
	if err != nil {
		return Record{}, err
	}
	if r.Binding != expected || r.Released {
		return Record{}, ErrConflict
	}
	next := r
	next.Binding.Owner = owner
	next.Binding.Fence++
	next.Phase = Unknown
	return m.cas(ctx, r, raw, next, false)
}

// Reconcile only accepts an ID. A fresh trusted observer must confirm the same
// record, backend locator, owner, fence and version before atomic release.
// Deadlines, process death and observer failure never release capacity.
func (m *Manager) Reconcile(ctx context.Context, id string) (Record, error) {
	r, raw, err := m.load(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if r.Released {
		return r, nil
	}
	if m.observer == nil || r.BackendLocator == "" {
		return r, ErrUnconfirmed
	}
	o, observeErr := m.observer.Observe(ctx, r)
	if ctx.Err() != nil {
		return r, ErrUnconfirmed
	}
	if observeErr != nil {
		next := r
		next.Phase = Unknown
		updated, err := m.cas(ctx, r, raw, next, false)
		if err != nil {
			return r, err
		}
		return updated, ErrUnconfirmed
	}
	if o.Binding != r.Binding || o.Version != r.Version || o.BackendLocator != r.BackendLocator {
		return r, ErrConflict
	}
	if !o.Terminal || o.Cleanup != CleanupConfirmed {
		next := r
		next.Phase = Unknown
		updated, err := m.cas(ctx, r, raw, next, false)
		if err != nil {
			return r, err
		}
		return updated, ErrUnconfirmed
	}
	if !safeID.MatchString(o.EvidenceRef) || !knownOutcome(o.Outcome) || (o.Completeness != CompletenessUnknown && o.Completeness != Complete && o.Completeness != Truncated) {
		return r, ErrUnconfirmed
	}
	next := r
	next.Phase, next.Outcome, next.Completeness, next.Cleanup = Terminal, o.Outcome, o.Completeness, CleanupConfirmed
	next.EvidenceRef, next.Released = o.EvidenceRef, true
	return m.cas(ctx, r, raw, next, true)
}
