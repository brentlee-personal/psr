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

package component

import (
	"compress/flate"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"ragflow/internal/ingestion/component/schema"
)

type scriptedEmbedder struct {
	*stubEmbedder
	outcomes map[int]error
	callSeq  int
}

func (s *scriptedEmbedder) Encode(ctx context.Context, texts []string) ([]EmbeddingResult, error) {
	s.callSeq++
	if err := s.outcomes[s.callSeq]; err != nil {
		s.calls.Add(1)
		s.callInputs = append(s.callInputs, append([]string(nil), texts...))
		return nil, err
	}
	return s.stubEmbedder.Encode(ctx, texts)
}

func TestEmbedChunksRetriesTransientEncodeEOF(t *testing.T) {
	origDelay := embedRetryBaseDelay
	embedRetryBaseDelay = time.Millisecond
	defer func() { embedRetryBaseDelay = origDelay }()

	// Call 1 is the title encode (succeeds); calls 2 and 3 fail the content
	// batch with io.ErrUnexpectedEOF; call 4 succeeds on the third content attempt.
	embedder := &scriptedEmbedder{
		stubEmbedder: newStubEmbedder(4),
		outcomes: map[int]error{
			2: fmt.Errorf("failed to read response: %w", io.ErrUnexpectedEOF),
			3: fmt.Errorf("failed to read response: %w", io.ErrUnexpectedEOF),
		},
	}
	comp, err := NewTokenizerComponentWithResolver(nil, func(ctx context.Context, _, _ string) (Embedder, string, error) {
		return embedder, "embd-test", nil
	})
	if err != nil {
		t.Fatalf("NewTokenizerComponentWithResolver: %v", err)
	}
	tc := comp.(*TokenizerComponent)
	tc.param.Fields = []string{"text"}
	chunks := []schema.ChunkDoc{chunkWithID("c1", "hello retry world")}

	out, _, err := tc.embedChunks(context.Background(), "tenant", "kb", "doc.md", chunks, newMemCacheStore())
	if err != nil {
		t.Fatalf("embedChunks failed after transient EOFs: %v", err)
	}
	if !hasEmbeddingVector(out[0]) {
		t.Fatal("expected vector attached after retry succeeded")
	}
	if got := embedder.calls.Load(); got != 4 {
		t.Fatalf("embedder calls = %d, want 4 (1 title + 3 content attempts)", got)
	}
}

func TestEncodeWithRetryDoesNotRetryNonTransientError(t *testing.T) {
	origDelay := embedRetryBaseDelay
	embedRetryBaseDelay = time.Millisecond
	defer func() { embedRetryBaseDelay = origDelay }()

	embedder := &scriptedEmbedder{
		stubEmbedder: newStubEmbedder(4),
		outcomes: map[int]error{
			1: errors.New("400 invalid input"),
		},
	}
	_, err := encodeWithRetry(context.Background(), embedder, []string{"bad"})
	if err == nil || !strings.Contains(err.Error(), "400 invalid input") {
		t.Fatalf("err = %v, want 400 invalid input", err)
	}
	if got := embedder.calls.Load(); got != 1 {
		t.Fatalf("embedder calls = %d, want 1 (no retry on non-transient 400)", got)
	}
}

func TestEncodeWithRetryAbortsOnCanceledContext(t *testing.T) {
	origDelay := embedRetryBaseDelay
	embedRetryBaseDelay = time.Millisecond
	defer func() { embedRetryBaseDelay = origDelay }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	embedder := &scriptedEmbedder{
		stubEmbedder: newStubEmbedder(4),
		outcomes: map[int]error{
			1: fmt.Errorf("failed to read response: %w", io.ErrUnexpectedEOF),
		},
	}
	_, err := encodeWithRetry(ctx, embedder, []string{"x"})
	if err == nil {
		t.Fatal("expected error on canceled context")
	}
	if got := embedder.calls.Load(); got != 1 {
		t.Fatalf("embedder calls = %d, want 1 (canceled context must abort without retrying)", got)
	}
}

func TestEncodeWithRetryRetriesFlateCorruptInput(t *testing.T) {
	origDelay := embedRetryBaseDelay
	embedRetryBaseDelay = time.Millisecond
	defer func() { embedRetryBaseDelay = origDelay }()

	embedder := &scriptedEmbedder{
		stubEmbedder: newStubEmbedder(4),
		outcomes: map[int]error{
			1: fmt.Errorf("failed to read response: %w", flate.CorruptInputError(52767)),
		},
	}
	res, err := encodeWithRetry(context.Background(), embedder, []string{"compressed-glitch"})
	if err != nil {
		t.Fatalf("encodeWithRetry failed on transient flate corrupt input: %v", err)
	}
	if len(res) != 1 || len(res[0].Vector) != 4 {
		t.Fatalf("unexpected result = %#v", res)
	}
	if got := embedder.calls.Load(); got != 2 {
		t.Fatalf("embedder calls = %d, want 2", got)
	}
}
