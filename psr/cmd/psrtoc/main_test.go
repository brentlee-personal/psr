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

package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"ragflow/psr/internal/ledger"
)

func TestCaptureAsOfAndDiffCommands(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "ledger.json")
	first := `{"scope":"project","observed_at":"2026-01-01T10:00:00Z","complete":true,"records":[{"id":"decision","content":"Use release A.","source_uri":"https://example.test/decisions","source_revision":"rev-a","anchor":"L1-L2"}]}`
	second := `{"scope":"project","observed_at":"2026-01-02T10:00:00Z","complete":true,"records":[{"id":"decision","content":"Use release B.","source_uri":"https://example.test/decisions","source_revision":"rev-b","anchor":"L1-L2"}]}`

	for _, input := range []string{first, second} {
		if err := run([]string{"capture", "--store", storePath}, strings.NewReader(input), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatalf("capture: %v", err)
		}
	}

	var historical bytes.Buffer
	if err := run([]string{"as-of", "--store", storePath, "--scope", "project", "--time", "2026-01-01T10:30:00Z"}, strings.NewReader(""), &historical, &bytes.Buffer{}); err != nil {
		t.Fatalf("as-of: %v", err)
	}
	var got ledger.Snapshot
	if err := json.Unmarshal(historical.Bytes(), &got); err != nil {
		t.Fatalf("decode as-of output: %v", err)
	}
	if got.Records[0].Content != "Use release A." {
		t.Fatalf("as-of content = %q", got.Records[0].Content)
	}

	var diffOutput bytes.Buffer
	if err := run([]string{"diff", "--store", storePath, "--scope", "project", "--from", "2026-01-01T10:00:00Z", "--to", "2026-01-02T10:00:00Z"}, strings.NewReader(""), &diffOutput, &bytes.Buffer{}); err != nil {
		t.Fatalf("diff: %v", err)
	}
	if !strings.Contains(diffOutput.String(), `"kind": "updated"`) {
		t.Fatalf("diff output missing updated change: %s", diffOutput.String())
	}

	if err := run([]string{"rollback", "--store", storePath, "--scope", "project", "--to", "2026-01-01T10:00:00Z", "--at", "2026-01-03T10:00:00Z"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	var currentOutput bytes.Buffer
	if err := run([]string{"current", "--store", storePath, "--scope", "project"}, strings.NewReader(""), &currentOutput, &bytes.Buffer{}); err != nil {
		t.Fatalf("current after rollback: %v", err)
	}
	if err := json.Unmarshal(currentOutput.Bytes(), &got); err != nil {
		t.Fatalf("decode current output: %v", err)
	}
	if got.Records[0].Content != "Use release A." || got.ObservedAt.Format("2006-01-02T15:04:05Z07:00") != "2026-01-03T10:00:00Z" {
		t.Fatalf("rollback did not restore old content as a new observation: %+v", got)
	}
}
