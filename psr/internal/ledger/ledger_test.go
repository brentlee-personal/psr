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
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestIngestCurrentAsOfAndDiff(t *testing.T) {
	ledger := newTestLedger(t)
	first := testSnapshot("2026-01-01T10:00:00Z",
		testRecord("decision-a", "Use the community baseline.", "rev-a", "L1-L3"),
		testRecord("decision-b", "Keep ToB data private.", "rev-a", "L4-L6"),
	)
	firstReceipt, err := ledger.Ingest(first)
	if err != nil {
		t.Fatalf("ingest first snapshot: %v", err)
	}
	if !firstReceipt.Recorded || len(firstReceipt.Changes) != 2 {
		t.Fatalf("unexpected first receipt: %+v", firstReceipt)
	}

	second := testSnapshot("2026-01-02T10:00:00Z",
		testRecord("decision-a", "Use the fixed Go core.", "rev-b", "L1-L3"),
		testRecord("decision-c", "Keep ToC differences on its branch.", "rev-b", "L7-L9"),
	)
	secondReceipt, err := ledger.Ingest(second)
	if err != nil {
		t.Fatalf("ingest second snapshot: %v", err)
	}
	if got, want := []ChangeKind{ChangeUpdated, ChangeRemoved, ChangeAdded}, changeKinds(secondReceipt.Changes); !equalKinds(got, want) {
		t.Fatalf("changes = %v, want %v", got, want)
	}

	current, err := ledger.Current("project")
	if err != nil {
		t.Fatalf("current snapshot: %v", err)
	}
	if current.Records[0].ID != "decision-a" || current.Records[0].Content != "Use the fixed Go core." {
		t.Fatalf("unexpected current records: %+v", current.Records)
	}
	if current.Records[0].State != "candidate" || current.Records[0].ContentSHA256 == "" {
		t.Fatalf("record lost candidate state or content hash: %+v", current.Records[0])
	}

	historical, err := ledger.AsOfObservation("project", mustTime(t, "2026-01-01T10:30:00Z"))
	if err != nil {
		t.Fatalf("historical snapshot: %v", err)
	}
	if len(historical.Records) != 2 || historical.Records[0].Content != "Use the community baseline." {
		t.Fatalf("unexpected historical records: %+v", historical.Records)
	}

	changes, err := ledger.Diff("project", mustTime(t, "2026-01-01T10:00:00Z"), mustTime(t, "2026-01-02T10:00:00Z"))
	if err != nil {
		t.Fatalf("diff snapshots: %v", err)
	}
	if !equalKinds(changeKinds(changes), []ChangeKind{ChangeUpdated, ChangeRemoved, ChangeAdded}) {
		t.Fatalf("unexpected diff: %+v", changes)
	}
}

func TestIngestRequiresCompleteAndMonotonicSnapshots(t *testing.T) {
	ledger := newTestLedger(t)
	incomplete := testSnapshot("2026-01-01T10:00:00Z", testRecord("decision-a", "fact", "rev-a", "L1"))
	incomplete.Complete = false
	if _, err := ledger.Ingest(incomplete); err == nil {
		t.Fatal("expected incomplete snapshot to be rejected")
	}

	first := testSnapshot("2026-01-02T10:00:00Z", testRecord("decision-a", "fact", "rev-a", "L1"))
	if _, err := ledger.Ingest(first); err != nil {
		t.Fatalf("ingest first snapshot: %v", err)
	}
	replayed, err := ledger.Ingest(first)
	if err != nil {
		t.Fatalf("replay identical snapshot: %v", err)
	}
	if replayed.Recorded {
		t.Fatal("identical replay should not append another snapshot")
	}

	conflict := testSnapshot("2026-01-02T10:00:00Z", testRecord("decision-a", "changed", "rev-b", "L1"))
	if _, err := ledger.Ingest(conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-time conflict error = %v, want ErrConflict", err)
	}
	outOfOrder := testSnapshot("2026-01-01T10:00:00Z", testRecord("decision-a", "older", "rev-0", "L1"))
	if _, err := ledger.Ingest(outOfOrder); !errors.Is(err, ErrConflict) {
		t.Fatalf("out-of-order error = %v, want ErrConflict", err)
	}
}

func TestIngestRejectsMissingEvidenceAndInvalidContentHash(t *testing.T) {
	ledger := newTestLedger(t)
	missingAnchor := testSnapshot("2026-01-01T10:00:00Z", testRecord("decision-a", "fact", "rev-a", ""))
	if _, err := ledger.Ingest(missingAnchor); err == nil {
		t.Fatal("expected missing evidence anchor to be rejected")
	}

	badHash := testRecord("decision-a", "fact", "rev-a", "L1")
	badHash.ContentSHA256 = "not-the-content-hash"
	if _, err := ledger.Ingest(testSnapshot("2026-01-01T10:00:00Z", badHash)); err == nil {
		t.Fatal("expected mismatched content hash to be rejected")
	}
}

func TestAsOfBeforeFirstObservationReturnsNotFound(t *testing.T) {
	ledger := newTestLedger(t)
	if _, err := ledger.AsOfObservation("project", mustTime(t, "2026-01-01T10:00:00Z")); !errors.Is(err, ErrNoSnapshot) {
		t.Fatalf("error = %v, want ErrNoSnapshot", err)
	}
}

func newTestLedger(t *testing.T) *Ledger {
	t.Helper()
	ledger, err := Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	return ledger
}

func testSnapshot(observedAt string, records ...Record) Snapshot {
	return Snapshot{
		Scope:      "project",
		ObservedAt: mustTimeNoTest(observedAt),
		Complete:   true,
		Records:    records,
	}
}

func testRecord(id, content, revision, anchor string) Record {
	return Record{
		ID:             id,
		Content:        content,
		SourceURI:      "https://example.test/decisions",
		SourceRevision: revision,
		Anchor:         anchor,
	}
}

func changeKinds(changes []Change) []ChangeKind {
	kinds := make([]ChangeKind, len(changes))
	for i, change := range changes {
		kinds[i] = change.Kind
	}
	return kinds
}

func equalKinds(a, b []ChangeKind) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse test time: %v", err)
	}
	return parsed
}

func mustTimeNoTest(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed
}
