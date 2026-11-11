package queryjournal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type observerFunc func(context.Context, Record) (Observation, error)

func (f observerFunc) Observe(ctx context.Context, r Record) (Observation, error) { return f(ctx, r) }
func confirmed(_ context.Context, r Record) (Observation, error) {
	return Observation{Binding: r.Binding, Version: r.Version, BackendLocator: r.BackendLocator, Terminal: true, Cleanup: CleanupConfirmed, Outcome: Succeeded, Completeness: Complete, EvidenceRef: "proof_1"}, nil
}
func fixture(t *testing.T, observer Observer) (*Manager, *Manager, *miniredis.Miniredis) {
	t.Helper()
	s := miniredis.RunT(t)
	makeManager := func() *Manager {
		client := redis.NewClient(&redis.Options{Addr: s.Addr(), MaxRetries: -1})
		t.Cleanup(func() { _ = client.Close() })
		m, err := New(client, Scope{Target: "fixed_target", PolicyScope: "fixed_policy"}, "deployment_epoch_1", observer)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	m1, m2 := makeManager(), makeManager()
	if n, err := m1.client.Eval(context.Background(), provisionScript, []string{m1.key}, m1.epoch).Int64(); err != nil || n != 1 {
		t.Fatalf("fixture provisioning %d %v", n, err)
	}
	return m1, m2, s
}
func binding(id string) Binding {
	return Binding{ExecutionID: id, SourceRef: "source_1", PolicyVersion: "policy_1", ApprovalFingerprint: strings.Repeat("a", 64), Owner: "owner_1", Fence: 1}
}
func reserve(t *testing.T, m *Manager, id string) Record {
	t.Helper()
	r, err := m.Reserve(context.Background(), binding(id), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func bind(t *testing.T, m *Manager, r Record) Record {
	t.Helper()
	r, err := m.BindBackend(context.Background(), r.Binding, "backend_1")
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func wantErr(t *testing.T, actual, expected error) {
	t.Helper()
	if !errors.Is(actual, expected) {
		t.Fatalf("error = %v, want %v", actual, expected)
	}
}

func TestAtomicAdmissionAcrossClients(t *testing.T) {
	m1, m2, _ := fixture(t, nil)
	var admitted atomic.Int32
	var busy atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m := m1
			if i%2 == 0 {
				m = m2
			}
			_, err := m.Reserve(context.Background(), binding(fmt.Sprintf("exec_%d", i)), time.Now().Add(time.Minute))
			if err == nil {
				admitted.Add(1)
			} else if errors.Is(err, ErrBusy) {
				busy.Add(1)
			} else {
				t.Errorf("reserve: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if admitted.Load() != 2 || busy.Load() != 38 {
		t.Fatalf("admitted=%d busy=%d", admitted.Load(), busy.Load())
	}
}

func TestDuplicateNeverReauthorizes(t *testing.T) {
	m, _, _ := fixture(t, observerFunc(confirmed))
	r := bind(t, m, reserve(t, m, "execution"))
	for _, change := range []func(*Binding){func(*Binding) {}, func(b *Binding) { b.Owner = "owner_2" }, func(b *Binding) { b.Fence++ }, func(b *Binding) { b.SourceRef = "source_2" }, func(b *Binding) { b.ApprovalFingerprint = strings.Repeat("b", 64) }} {
		b := r.Binding
		change(&b)
		_, err := m.Reserve(context.Background(), b, time.Now().Add(time.Minute))
		wantErr(t, err, ErrDuplicate)
	}
	if _, err := m.Reconcile(context.Background(), "execution"); err != nil {
		t.Fatal(err)
	}
	_, err := m.Reserve(context.Background(), r.Binding, time.Now().Add(time.Minute))
	wantErr(t, err, ErrDuplicate)
}

func TestUnknownSurvivesDeadlineAndNewManager(t *testing.T) {
	m, restarted, s := fixture(t, nil)
	r, err := m.Reserve(context.Background(), binding("unknown_1"), time.Now().Add(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	reserve(t, m, "unknown_2")
	time.Sleep(15 * time.Millisecond)
	s.FastForward(24 * time.Hour)
	if ttl := s.TTL(m.key); ttl != 0 {
		t.Fatalf("unexpected TTL %v", ttl)
	}
	read, err := restarted.Read(context.Background(), r.Binding.ExecutionID)
	if err != nil || read.Released {
		t.Fatalf("restart read %v %v", read, err)
	}
	_, err = restarted.Reconcile(context.Background(), r.Binding.ExecutionID)
	wantErr(t, err, ErrUnconfirmed)
	_, err = restarted.Reserve(context.Background(), binding("third"), time.Now().Add(time.Minute))
	wantErr(t, err, ErrBusy)
}

func TestRetainedMetadataHasFixedLimit(t *testing.T) {
	m, _, s := fixture(t, nil)
	for i := 0; i < 4096; i++ {
		s.HSet(m.key, fmt.Sprintf("record:old_%d", i), "retained")
	}
	_, err := m.Reserve(context.Background(), binding("execution"), time.Now().Add(time.Minute))
	wantErr(t, err, ErrJournalFull)
	if s.HGet(m.key, "slot:1") != "" {
		t.Fatal("full journal admitted slot")
	}
}

func TestObserverFailuresRetainCapacity(t *testing.T) {
	cases := []struct {
		name    string
		observe observerFunc
	}{
		{"unknown", func(_ context.Context, r Record) (Observation, error) {
			o, _ := confirmed(context.Background(), r)
			o.Terminal = false
			return o, nil
		}},
		{"cleanup_unknown", func(_ context.Context, r Record) (Observation, error) {
			o, _ := confirmed(context.Background(), r)
			o.Cleanup = CleanupUnknown
			return o, nil
		}},
		{"raw_error", func(context.Context, Record) (Observation, error) {
			return Observation{}, errors.New("CANARY_password_DSN_SQL_rows")
		}},
		{"source", func(_ context.Context, r Record) (Observation, error) {
			o, _ := confirmed(context.Background(), r)
			o.Binding.SourceRef = "wrong_source"
			return o, nil
		}},
		{"owner", func(_ context.Context, r Record) (Observation, error) {
			o, _ := confirmed(context.Background(), r)
			o.Binding.Owner = "wrong_owner"
			return o, nil
		}},
		{"fence", func(_ context.Context, r Record) (Observation, error) {
			o, _ := confirmed(context.Background(), r)
			o.Binding.Fence++
			return o, nil
		}},
		{"fingerprint", func(_ context.Context, r Record) (Observation, error) {
			o, _ := confirmed(context.Background(), r)
			o.Binding.ApprovalFingerprint = strings.Repeat("b", 64)
			return o, nil
		}},
		{"execution", func(_ context.Context, r Record) (Observation, error) {
			o, _ := confirmed(context.Background(), r)
			o.Binding.ExecutionID = "other"
			return o, nil
		}},
		{"version", func(_ context.Context, r Record) (Observation, error) {
			o, _ := confirmed(context.Background(), r)
			o.Version++
			return o, nil
		}},
		{"locator", func(_ context.Context, r Record) (Observation, error) {
			o, _ := confirmed(context.Background(), r)
			o.BackendLocator = "wrong_backend"
			return o, nil
		}},
		{"outcome_unknown", func(_ context.Context, r Record) (Observation, error) {
			o, _ := confirmed(context.Background(), r)
			o.Outcome = OutcomeUnknown
			return o, nil
		}},
		{"unsafe_evidence", func(_ context.Context, r Record) (Observation, error) {
			o, _ := confirmed(context.Background(), r)
			o.EvidenceRef = "mysql://secret"
			return o, nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _, s := fixture(t, tc.observe)
			bind(t, m, reserve(t, m, "execution"))
			reserve(t, m, "second")
			r, err := m.Reconcile(context.Background(), "execution")
			if err == nil || r.Released {
				t.Fatalf("unexpected release: %v %v", r, err)
			}
			if strings.Contains(err.Error(), "CANARY") || strings.Contains(s.Dump(), "CANARY") {
				t.Fatal("raw observer error leaked")
			}
			_, err = m.Reserve(context.Background(), binding("third"), time.Now().Add(time.Minute))
			wantErr(t, err, ErrBusy)
		})
	}
}

func TestUnboundBackendNeverObserved(t *testing.T) {
	var calls int
	m, _, _ := fixture(t, observerFunc(func(ctx context.Context, r Record) (Observation, error) { calls++; return confirmed(ctx, r) }))
	reserve(t, m, "execution")
	_, err := m.Reconcile(context.Background(), "execution")
	wantErr(t, err, ErrUnconfirmed)
	if calls != 0 {
		t.Fatal("unbound backend observed")
	}
}

func TestConfirmedReleaseExactlyOnce(t *testing.T) {
	m, m2, s := fixture(t, observerFunc(confirmed))
	r := bind(t, m, reserve(t, m, "execution"))
	reserve(t, m, "second")
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m2.Reconcile(context.Background(), "execution")
			if err != nil && !errors.Is(err, ErrConflict) {
				t.Errorf("reconcile %v", err)
			}
		}()
	}
	wg.Wait()
	read, err := m.Read(context.Background(), "execution")
	if err != nil || !read.Released || read.Phase != Terminal || read.Cleanup != CleanupConfirmed {
		t.Fatalf("terminal: %v %v", read, err)
	}
	if _, err := m.Reconcile(context.Background(), "execution"); err != nil {
		t.Fatal(err)
	}
	reserve(t, m, "third")
	_, err = m.Reserve(context.Background(), binding("fourth"), time.Now().Add(time.Minute))
	wantErr(t, err, ErrBusy)
	if !s.Exists(m.key) || r.Slot < 1 {
		t.Fatal("record disappeared")
	}
}

func TestOwnerTransferRacesWithObservation(t *testing.T) {
	entered := make(chan struct{})
	resume := make(chan struct{})
	m, m2, _ := fixture(t, observerFunc(func(ctx context.Context, r Record) (Observation, error) {
		close(entered)
		<-resume
		return confirmed(ctx, r)
	}))
	r := bind(t, m, reserve(t, m, "execution"))
	reserve(t, m, "second")
	done := make(chan error, 1)
	go func() { _, err := m.Reconcile(context.Background(), "execution"); done <- err }()
	<-entered
	newRecord, err := m2.TransferOwner(context.Background(), r.Binding, "owner_2")
	if err != nil {
		t.Fatal(err)
	}
	close(resume)
	wantErr(t, <-done, ErrConflict)
	if newRecord.Binding.Fence != r.Binding.Fence+1 || newRecord.BackendLocator != r.BackendLocator || newRecord.Released {
		t.Fatalf("transfer %v", newRecord)
	}
	_, err = m.BindBackend(context.Background(), r.Binding, "old_backend")
	wantErr(t, err, ErrConflict)
	_, err = m2.Reserve(context.Background(), binding("third"), time.Now().Add(time.Minute))
	wantErr(t, err, ErrBusy)
}

func TestContextCancelledAfterObserverRetainsSlot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, _, _ := fixture(t, observerFunc(func(_ context.Context, r Record) (Observation, error) {
		cancel()
		return confirmed(context.Background(), r)
	}))
	bind(t, m, reserve(t, m, "execution"))
	reserve(t, m, "second")
	_, err := m.Reconcile(ctx, "execution")
	wantErr(t, err, ErrUnconfirmed)
	_, err = m.Reserve(context.Background(), binding("third"), time.Now().Add(time.Minute))
	wantErr(t, err, ErrBusy)
}

func TestStoreUnavailableAdmitsNothing(t *testing.T) {
	m, _, s := fixture(t, nil)
	s.Close()
	_, err := m.Reserve(context.Background(), binding("execution"), time.Now().Add(time.Minute))
	wantErr(t, err, ErrUnavailable)
}

func TestBindingAndRecordDoNotAcceptPayload(t *testing.T) {
	m, _, s := fixture(t, nil)
	for _, payload := range []string{"mysql://user:CANARY@host/db", "SELECT secret FROM users", "CANARY\npassword"} {
		b := binding("execution")
		b.SourceRef = payload
		_, err := m.Reserve(context.Background(), b, time.Now().Add(time.Minute))
		wantErr(t, err, ErrInvalid)
	}
	r := reserve(t, m, "execution")
	_, err := m.BindBackend(context.Background(), r.Binding, "backend://CANARY")
	wantErr(t, err, ErrInvalid)
	if strings.Contains(s.Dump(), "CANARY") {
		t.Fatal("payload persisted")
	}
	for _, mutate := range []func(*Binding){func(b *Binding) { b.Owner = "other" }, func(b *Binding) { b.Fence++ }, func(b *Binding) { b.SourceRef = "other" }, func(b *Binding) { b.ApprovalFingerprint = strings.Repeat("b", 64) }} {
		b := r.Binding
		mutate(&b)
		_, err = m.BindBackend(context.Background(), b, "backend")
		wantErr(t, err, ErrConflict)
	}
}

func TestLostAdmissionReplyCannotCreateSecondAuthorization(t *testing.T) {
	// A crash after the atomic write but before execution/reply leaves a record.
	m, restarted, _ := fixture(t, nil)
	reserve(t, m, "crash_window")
	reserve(t, m, "other_unknown")
	_, err := restarted.Reserve(context.Background(), binding("crash_window"), time.Now().Add(time.Minute))
	wantErr(t, err, ErrDuplicate)
	_, err = restarted.Reserve(context.Background(), binding("new_attempt"), time.Now().Add(time.Minute))
	wantErr(t, err, ErrBusy)
}

func TestUnverifiedStoreCannotAdmitOrReconcile(t *testing.T) {
	for _, kind := range []string{"missing", "schema_missing", "epoch_missing", "epoch_changed", "ttl", "expired"} {
		t.Run(kind, func(t *testing.T) {
			m, restarted, s := fixture(t, observerFunc(confirmed))
			bind(t, m, reserve(t, m, "execution"))
			switch kind {
			case "missing":
				s.Del(m.key)
			case "schema_missing":
				s.HDel(m.key, "schema")
			case "epoch_missing":
				s.HDel(m.key, "epoch")
			case "epoch_changed":
				s.HSet(m.key, "epoch", "other_epoch")
			case "ttl":
				s.SetTTL(m.key, time.Hour)
			case "expired":
				s.SetTTL(m.key, time.Second)
				s.FastForward(2 * time.Second)
			}
			before := s.Dump()
			_, err := restarted.Reserve(context.Background(), binding("new_execution"), time.Now().Add(time.Minute))
			wantErr(t, err, ErrUnavailable)
			_, err = restarted.Reconcile(context.Background(), "execution")
			wantErr(t, err, ErrUnavailable)
			if s.Dump() != before {
				t.Fatal("unverified store changed")
			}
		})
	}
}

func TestProvisionNeverResetsExistingScope(t *testing.T) {
	m, _, s := fixture(t, nil)
	reserve(t, m, "execution")
	before := s.Dump()
	for _, epoch := range []string{m.epoch, "other_epoch"} {
		n, err := m.client.Eval(context.Background(), provisionScript, []string{m.key}, epoch).Int64()
		if err != nil || n != 0 {
			t.Fatalf("provision %d %v", n, err)
		}
	}
	if s.Dump() != before {
		t.Fatal("provision reset a live scope")
	}
	changedEpoch, err := New(m.client, m.scope, "other_epoch", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = changedEpoch.Reserve(context.Background(), binding("new_execution"), time.Now().Add(time.Minute))
	wantErr(t, err, ErrUnavailable)
}

type testNativePool struct{ client *redis.Client }

func (p *testNativePool) GetClient() *redis.Client { return p.client }

type nilObserver struct{}

func (*nilObserver) Observe(context.Context, Record) (Observation, error) {
	panic("typed-nil observer must not run")
}

func TestNativePoolAndInputBoundaries(t *testing.T) {
	m, _, _ := fixture(t, nil)
	var typedNil *testNativePool
	_, err := NewFromNativePool(typedNil, m.scope, m.epoch, nil)
	wantErr(t, err, ErrInvalid)
	_, err = NewFromNativePool(nil, m.scope, m.epoch, nil)
	wantErr(t, err, ErrInvalid)
	_, err = NewFromNativePool(&testNativePool{}, m.scope, m.epoch, nil)
	wantErr(t, err, ErrInvalid)
	reused, err := NewFromNativePool(&testNativePool{m.client}, m.scope, m.epoch, nil)
	if err != nil || reused.client != m.client {
		t.Fatalf("pool not reused %v", err)
	}
	for _, deadline := range []time.Time{time.Time{}, time.Now().Add(-time.Second)} {
		_, err = m.Reserve(context.Background(), binding("execution"), deadline)
		wantErr(t, err, ErrInvalid)
	}
	r, err := m.Reserve(context.Background(), binding("execution"), time.Now().Add(time.Minute))
	if err != nil || r.DeadlineMS == 0 {
		t.Fatalf("deadline %v %v", r, err)
	}
	if _, err := m.Read(context.Background(), "absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("read absent: %v", err)
	}
	var observer *nilObserver
	missingObserver, err := New(m.client, m.scope, m.epoch, observer)
	if err != nil {
		t.Fatal(err)
	}
	bind(t, missingObserver, r)
	_, err = missingObserver.Reconcile(context.Background(), "execution")
	wantErr(t, err, ErrUnconfirmed)
}
