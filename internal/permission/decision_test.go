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

package permission

import (
	"context"
	"errors"
	"testing"
)

type decisionTestSource struct {
	resources map[ResourceRef]Resource
}

func (s *decisionTestSource) GetMembership(context.Context, string, string) (*Membership, error) {
	return nil, ErrMembershipNotFound
}

func (s *decisionTestSource) GetResources(_ context.Context, refs []ResourceRef) ([]Resource, error) {
	resources := make([]Resource, 0, len(refs))
	for _, ref := range refs {
		if resource, exists := s.resources[ref]; exists {
			resources = append(resources, resource)
		}
	}
	return resources, nil
}

func (*decisionTestSource) ListResourceRefs(context.Context, ResourceKind, ResourceRef) ([]ResourceRef, error) {
	return nil, nil
}

func TestEvaluateResourceDecision(t *testing.T) {
	privateResource := Resource{
		Ref:         ResourceRef{Kind: ResourceKindDataset, ID: "dataset-1"},
		TenantID:    "tenant-1",
		OwnerUserID: "owner-1",
		Visibility:  VisibilityPrivate,
		Active:      true,
		OwnerOperations: []Operation{
			OperationRead,
		},
	}

	tests := []struct {
		name      string
		checker   *Checker
		ctx       context.Context
		resource  ResourceRef
		operation Operation
		want      DecisionStatus
		wantErr   error
	}{
		{
			name:      "allowed current owner",
			checker:   NewChecker(&decisionTestSource{resources: map[ResourceRef]Resource{privateResource.Ref: privateResource}}),
			ctx:       WithIdentity(context.Background(), "owner-1", "tenant-1"),
			resource:  privateResource.Ref,
			operation: OperationRead,
			want:      DecisionAllowed,
		},
		{
			name:      "known denial",
			checker:   NewChecker(&decisionTestSource{resources: map[ResourceRef]Resource{privateResource.Ref: privateResource}}),
			ctx:       WithIdentity(context.Background(), "other-user", "tenant-1"),
			resource:  privateResource.Ref,
			operation: OperationRead,
			want:      DecisionDenied,
		},
		{
			name:      "unavailable source fails closed",
			checker:   NewChecker(nil),
			ctx:       WithIdentity(context.Background(), "owner-1", "tenant-1"),
			resource:  privateResource.Ref,
			operation: OperationRead,
			want:      DecisionUnverified,
			wantErr:   ErrSourceUnavailable,
		},
		{
			name:      "invalid request remains an error",
			checker:   NewChecker(&decisionTestSource{}),
			ctx:       WithIdentity(context.Background(), "owner-1", "tenant-1"),
			resource:  ResourceRef{},
			operation: OperationRead,
			wantErr:   ErrInvalidPermission,
		},
		{
			name:      "unauthenticated request remains an error",
			checker:   NewChecker(&decisionTestSource{}),
			ctx:       context.Background(),
			resource:  privateResource.Ref,
			operation: OperationRead,
			wantErr:   ErrUnauthenticated,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, err := test.checker.EvaluateResource(test.ctx, test.resource, test.operation)
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if decision.Status != test.want {
				t.Fatalf("decision = %q, want %q", decision.Status, test.want)
			}
		})
	}
}
