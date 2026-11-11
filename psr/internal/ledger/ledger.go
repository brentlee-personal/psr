//
//  Copyright 2026 The InfiniFlow Authors. All Rights Reserved.
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.
//

package ledger

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	formatVersion = 1
	maxStoreSize  = 64 << 20
)

var (
	ErrNoSnapshot = errors.New("no snapshot exists for scope and time")
	ErrConflict   = errors.New("snapshot conflicts with recorded history")
)

// Snapshot contains a complete observation of one caller-defined scope.
type Snapshot struct {
	Scope      string    `json:"scope"`
	ObservedAt time.Time `json:"observed_at"`
	Complete   bool      `json:"complete"`
	Records    []Record  `json:"records"`
}

// Record is an unverified candidate with a stable ID and source evidence.
type Record struct {
	ID             string `json:"id"`
	Content        string `json:"content"`
	SourceURI      string `json:"source_uri"`
	SourceRevision string `json:"source_revision"`
	Anchor         string `json:"anchor"`
	ContentSHA256  string `json:"content_sha256"`
	State          string `json:"state"`
}

type ChangeKind string

const (
	ChangeAdded   ChangeKind = "added"
	ChangeUpdated ChangeKind = "updated"
	ChangeRemoved ChangeKind = "removed"
)

type Change struct {
	Kind   ChangeKind `json:"kind"`
	ID     string     `json:"id"`
	Before *Record    `json:"before,omitempty"`
	After  *Record    `json:"after,omitempty"`
}

type Receipt struct {
	Scope          string    `json:"scope"`
	ObservedAt     time.Time `json:"observed_at"`
	SnapshotSHA256 string    `json:"snapshot_sha256"`
	Recorded       bool      `json:"recorded"`
	Changes        []Change  `json:"changes"`
}

type fileStore struct {
	FormatVersion int        `json:"format_version"`
	Snapshots     []Snapshot `json:"snapshots"`
}

// Ledger stores immutable snapshots in a local JSON file. It is intentionally
// independent of RAGFlow's metadata database and does not provide authentication.
type Ledger struct {
	path string
	mu   sync.Mutex
}

// Open creates a ledger handle. The file is created on the first successful ingest.
func Open(path string) (*Ledger, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("ledger path is required")
	}
	return &Ledger{path: path}, nil
}

// Ingest appends a complete observation. Partial observations are rejected so a
// missing record cannot be mistaken for a source deletion.
func (l *Ledger) Ingest(snapshot Snapshot) (Receipt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	normalized, err := normalizeSnapshot(snapshot, false)
	if err != nil {
		return Receipt{}, err
	}
	store, err := readStore(l.path)
	if err != nil {
		return Receipt{}, err
	}
	digest, err := snapshotDigest(normalized)
	if err != nil {
		return Receipt{}, err
	}

	var previous *Snapshot
	for i := range store.Snapshots {
		current := &store.Snapshots[i]
		if current.Scope != normalized.Scope {
			continue
		}
		if previous == nil || current.ObservedAt.After(previous.ObservedAt) {
			previous = current
		}
	}

	receipt := Receipt{
		Scope:          normalized.Scope,
		ObservedAt:     normalized.ObservedAt,
		SnapshotSHA256: digest,
		Changes:        []Change{},
	}
	if previous != nil {
		if normalized.ObservedAt.Before(previous.ObservedAt) {
			return Receipt{}, fmt.Errorf("%w: observation time must increase for scope %q", ErrConflict, normalized.Scope)
		}
		if normalized.ObservedAt.Equal(previous.ObservedAt) {
			previousDigest, err := snapshotDigest(*previous)
			if err != nil {
				return Receipt{}, err
			}
			if digest == previousDigest {
				return receipt, nil
			}
			return Receipt{}, fmt.Errorf("%w: different content has the same observation time for scope %q", ErrConflict, normalized.Scope)
		}
		receipt.Changes = diff(*previous, normalized)
	} else {
		receipt.Changes = diff(Snapshot{}, normalized)
	}

	store.Snapshots = append(store.Snapshots, normalized)
	if err := writeStore(l.path, store); err != nil {
		return Receipt{}, err
	}
	receipt.Recorded = true
	return receipt, nil
}

// Current returns the latest complete observation for a scope.
func (l *Ledger) Current(scope string) (Snapshot, error) {
	return l.asOf(scope, time.Time{}, true)
}

// AsOfObservation returns the latest observation at or before at. Its time axis
// is source-observation time, not the business-validity time of a claim.
func (l *Ledger) AsOfObservation(scope string, at time.Time) (Snapshot, error) {
	if at.IsZero() {
		return Snapshot{}, errors.New("observation time is required")
	}
	return l.asOf(scope, at, false)
}

// Diff compares the complete observations selected by from and to.
func (l *Ledger) Diff(scope string, from, to time.Time) ([]Change, error) {
	if from.IsZero() || to.IsZero() {
		return nil, errors.New("both observation times are required")
	}
	if to.Before(from) {
		return nil, errors.New("to observation time must not precede from observation time")
	}
	before, err := l.AsOfObservation(scope, from)
	if err != nil {
		return nil, err
	}
	after, err := l.AsOfObservation(scope, to)
	if err != nil {
		return nil, err
	}
	return diff(before, after), nil
}

func (l *Ledger) asOf(scope string, at time.Time, latest bool) (Snapshot, error) {
	if strings.TrimSpace(scope) == "" {
		return Snapshot{}, errors.New("scope is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	store, err := readStore(l.path)
	if err != nil {
		return Snapshot{}, err
	}
	var selected *Snapshot
	for i := range store.Snapshots {
		candidate := &store.Snapshots[i]
		if candidate.Scope != scope || (!latest && candidate.ObservedAt.After(at)) {
			continue
		}
		if selected == nil || candidate.ObservedAt.After(selected.ObservedAt) {
			selected = candidate
		}
	}
	if selected == nil {
		return Snapshot{}, fmt.Errorf("%w: %q", ErrNoSnapshot, scope)
	}
	return cloneSnapshot(*selected), nil
}

func normalizeSnapshot(snapshot Snapshot, stored bool) (Snapshot, error) {
	snapshot.Scope = strings.TrimSpace(snapshot.Scope)
	if snapshot.Scope == "" {
		return Snapshot{}, errors.New("snapshot scope is required")
	}
	if snapshot.ObservedAt.IsZero() {
		return Snapshot{}, errors.New("snapshot observed_at is required")
	}
	snapshot.ObservedAt = snapshot.ObservedAt.UTC().Round(0)
	if !snapshot.Complete {
		return Snapshot{}, errors.New("snapshot must be marked complete")
	}

	records := make([]Record, len(snapshot.Records))
	copy(records, snapshot.Records)
	seen := make(map[string]struct{}, len(records))
	for i := range records {
		record := &records[i]
		record.ID = strings.TrimSpace(record.ID)
		if record.ID == "" {
			return Snapshot{}, errors.New("record id is required")
		}
		if _, exists := seen[record.ID]; exists {
			return Snapshot{}, fmt.Errorf("duplicate record id %q", record.ID)
		}
		seen[record.ID] = struct{}{}
		if strings.TrimSpace(record.Content) == "" {
			return Snapshot{}, fmt.Errorf("record %q content is required", record.ID)
		}
		record.SourceURI = strings.TrimSpace(record.SourceURI)
		if err := validateSourceURI(record.SourceURI); err != nil {
			return Snapshot{}, fmt.Errorf("record %q: %w", record.ID, err)
		}
		record.SourceRevision = strings.TrimSpace(record.SourceRevision)
		if strings.TrimSpace(record.SourceRevision) == "" {
			return Snapshot{}, fmt.Errorf("record %q source_revision is required", record.ID)
		}
		record.Anchor = strings.TrimSpace(record.Anchor)
		if strings.TrimSpace(record.Anchor) == "" {
			return Snapshot{}, fmt.Errorf("record %q evidence anchor is required", record.ID)
		}
		sum := sha256.Sum256([]byte(record.Content))
		hash := hex.EncodeToString(sum[:])
		if record.ContentSHA256 != "" && record.ContentSHA256 != hash {
			return Snapshot{}, fmt.Errorf("record %q content_sha256 does not match content", record.ID)
		}
		if stored && record.ContentSHA256 == "" {
			return Snapshot{}, fmt.Errorf("stored record %q has no content_sha256", record.ID)
		}
		record.ContentSHA256 = hash
		if record.State != "" && record.State != "candidate" {
			return Snapshot{}, fmt.Errorf("record %q has unsupported state %q", record.ID, record.State)
		}
		record.State = "candidate"
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	snapshot.Records = records
	return snapshot, nil
}

func validateSourceURI(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid source_uri: %w", err)
	}
	if (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return errors.New("source_uri must be an absolute HTTP(S) URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("source_uri must not contain credentials, query parameters, or a fragment")
	}
	return nil
}

func snapshotDigest(snapshot Snapshot) (string, error) {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func diff(before, after Snapshot) []Change {
	oldRecords := make(map[string]Record, len(before.Records))
	newRecords := make(map[string]Record, len(after.Records))
	for _, record := range before.Records {
		oldRecords[record.ID] = record
	}
	for _, record := range after.Records {
		newRecords[record.ID] = record
	}

	ids := make(map[string]struct{}, len(oldRecords)+len(newRecords))
	for id := range oldRecords {
		ids[id] = struct{}{}
	}
	for id := range newRecords {
		ids[id] = struct{}{}
	}
	orderedIDs := make([]string, 0, len(ids))
	for id := range ids {
		orderedIDs = append(orderedIDs, id)
	}
	sort.Strings(orderedIDs)

	changes := make([]Change, 0)
	for _, id := range orderedIDs {
		oldRecord, wasPresent := oldRecords[id]
		newRecord, isPresent := newRecords[id]
		switch {
		case !wasPresent && isPresent:
			record := newRecord
			changes = append(changes, Change{Kind: ChangeAdded, ID: id, After: &record})
		case wasPresent && !isPresent:
			record := oldRecord
			changes = append(changes, Change{Kind: ChangeRemoved, ID: id, Before: &record})
		case wasPresent && isPresent && oldRecord != newRecord:
			oldCopy, newCopy := oldRecord, newRecord
			changes = append(changes, Change{Kind: ChangeUpdated, ID: id, Before: &oldCopy, After: &newCopy})
		}
	}
	return changes
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	snapshot.Records = append([]Record(nil), snapshot.Records...)
	return snapshot
}

func readStore(path string) (fileStore, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileStore{FormatVersion: formatVersion, Snapshots: []Snapshot{}}, nil
	}
	if err != nil {
		return fileStore{}, fmt.Errorf("open ledger: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return fileStore{}, fmt.Errorf("stat ledger: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fileStore{}, errors.New("ledger path must be a regular file")
	}
	if info.Size() > maxStoreSize {
		return fileStore{}, fmt.Errorf("ledger exceeds %d bytes", maxStoreSize)
	}

	decoder := json.NewDecoder(io.LimitReader(file, maxStoreSize+1))
	decoder.DisallowUnknownFields()
	var store fileStore
	if err := decoder.Decode(&store); err != nil {
		return fileStore{}, fmt.Errorf("decode ledger: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fileStore{}, errors.New("ledger contains more than one JSON value")
		}
		return fileStore{}, fmt.Errorf("decode ledger trailer: %w", err)
	}
	if store.FormatVersion != formatVersion {
		return fileStore{}, fmt.Errorf("unsupported ledger format version %d", store.FormatVersion)
	}
	if store.Snapshots == nil {
		return fileStore{}, errors.New("ledger snapshots must be an array")
	}

	lastByScope := make(map[string]time.Time)
	for i := range store.Snapshots {
		normalized, err := normalizeSnapshot(store.Snapshots[i], true)
		if err != nil {
			return fileStore{}, fmt.Errorf("validate stored snapshot %d: %w", i, err)
		}
		if !sameSnapshot(normalized, store.Snapshots[i]) {
			return fileStore{}, fmt.Errorf("stored snapshot %d is not normalized", i)
		}
		if last, ok := lastByScope[normalized.Scope]; ok && !normalized.ObservedAt.After(last) {
			return fileStore{}, fmt.Errorf("stored snapshots for scope %q are not strictly ordered", normalized.Scope)
		}
		lastByScope[normalized.Scope] = normalized.ObservedAt
	}
	return store, nil
}

func sameSnapshot(normalized, stored Snapshot) bool {
	a, errA := json.Marshal(normalized)
	b, errB := json.Marshal(stored)
	return errA == nil && errB == nil && bytes.Equal(a, b)
}

func writeStore(path string, store fileStore) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create ledger directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("ledger path must be a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect ledger path: %w", err)
	}

	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("encode ledger: %w", err)
	}
	data = append(data, '\n')
	if len(data) > maxStoreSize {
		return fmt.Errorf("ledger exceeds %d bytes", maxStoreSize)
	}

	temp, err := os.CreateTemp(dir, ".psr-ledger-*")
	if err != nil {
		return fmt.Errorf("create temporary ledger: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return fmt.Errorf("secure temporary ledger: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write ledger: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync ledger: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close ledger: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace ledger: %w", err)
	}
	return nil
}
