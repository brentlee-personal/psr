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

package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ragflow/internal/permission"

	"github.com/gin-gonic/gin"
)

type permissionDecisionCheckerStub struct {
	decision permission.Decision
	err      error
	calls    int
	resource permission.ResourceRef
	action   permission.Operation
}

func (s *permissionDecisionCheckerStub) EvaluateResource(_ context.Context, resource permission.ResourceRef, operation permission.Operation) (permission.Decision, error) {
	s.calls++
	s.resource = resource
	s.action = operation
	return s.decision, s.err
}

func TestPermissionHandlerCheckResource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		body       string
		checker    *permissionDecisionCheckerStub
		wantStatus int
		wantBody   string
		wantCalls  int
	}{
		{
			name:       "allowed",
			body:       `{"resource":{"kind":"dataset","id":"dataset-1"},"operation":"read"}`,
			checker:    &permissionDecisionCheckerStub{decision: permission.Decision{Status: permission.DecisionAllowed}},
			wantStatus: http.StatusOK,
			wantBody:   `{"status":"allowed"}`,
			wantCalls:  1,
		},
		{
			name:       "denied",
			body:       `{"resource":{"kind":"dataset","id":"dataset-1"},"operation":"read"}`,
			checker:    &permissionDecisionCheckerStub{decision: permission.Decision{Status: permission.DecisionDenied}},
			wantStatus: http.StatusOK,
			wantBody:   `{"status":"denied"}`,
			wantCalls:  1,
		},
		{
			name:       "unverified source",
			body:       `{"resource":{"kind":"dataset","id":"dataset-1"},"operation":"read"}`,
			checker:    &permissionDecisionCheckerStub{decision: permission.Decision{Status: permission.DecisionUnverified}, err: errors.New("private database error")},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   `{"status":"unverified","code":"PERMISSION_SOURCE_UNAVAILABLE"}`,
			wantCalls:  1,
		},
		{
			name:       "caller cannot choose identity",
			body:       `{"principal":"other-user","resource":{"kind":"dataset","id":"dataset-1"},"operation":"read"}`,
			checker:    &permissionDecisionCheckerStub{decision: permission.Decision{Status: permission.DecisionAllowed}},
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"status":"error","code":"INVALID_REQUEST"}`,
		},
		{
			name:       "malformed request",
			body:       `{"resource":{"kind":"dataset"}}`,
			checker:    &permissionDecisionCheckerStub{decision: permission.Decision{Status: permission.DecisionAllowed}},
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"status":"error","code":"INVALID_REQUEST"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/v1/permissions/check", strings.NewReader(test.body))
			context, _ := gin.CreateTestContext(recorder)
			context.Request = request

			NewPermissionHandler(test.checker).CheckResource(context)

			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if body := strings.TrimSpace(recorder.Body.String()); body != test.wantBody {
				t.Fatalf("body = %s, want %s", body, test.wantBody)
			}
			if test.checker.calls != test.wantCalls {
				t.Fatalf("checker calls = %d, want %d", test.checker.calls, test.wantCalls)
			}
			if test.wantCalls == 1 && (test.checker.resource != (permission.ResourceRef{Kind: "dataset", ID: "dataset-1"}) || test.checker.action != permission.OperationRead) {
				t.Fatalf("checker received resource=%+v operation=%q", test.checker.resource, test.checker.action)
			}
		})
	}
}

func TestPermissionHandlerRejectsOversizedRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &permissionDecisionCheckerStub{decision: permission.Decision{Status: permission.DecisionAllowed}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/permissions/check", strings.NewReader(strings.Repeat(" ", maxPermissionCheckBody+1)))
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request

	NewPermissionHandler(stub).CheckResource(context)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if stub.calls != 0 {
		t.Fatalf("checker called %d times for oversized input", stub.calls)
	}
}
