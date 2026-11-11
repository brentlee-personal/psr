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
)

// DecisionStatus is the result of checking one operation against current
// permission state. Unverified is not an authorization grant.
type DecisionStatus string

const (
	DecisionAllowed    DecisionStatus = "allowed"
	DecisionDenied     DecisionStatus = "denied"
	DecisionUnverified DecisionStatus = "unverified"
)

// Decision exposes only the authorization outcome, not internal resource or
// permission details.
type Decision struct {
	Status DecisionStatus `json:"status"`
}

// EvaluateResource checks current access and separates a known denial from an
// unavailable or otherwise untrustworthy permission source. Invalid requests
// and missing authenticated identity remain errors for the caller to handle.
func (c *Checker) EvaluateResource(ctx context.Context, resource ResourceRef, operation Operation) (Decision, error) {
	err := c.CheckResource(ctx, resource, operation)
	switch {
	case err == nil:
		return Decision{Status: DecisionAllowed}, nil
	case errors.Is(err, ErrPermissionDenied), errors.Is(err, ErrMembershipNotFound), errors.Is(err, ErrResourceNotFound):
		return Decision{Status: DecisionDenied}, nil
	case errors.Is(err, ErrUnauthenticated), errors.Is(err, ErrInvalidPermission):
		return Decision{}, err
	default:
		return Decision{Status: DecisionUnverified}, err
	}
}
