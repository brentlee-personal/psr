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
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"ragflow/internal/common"
	"ragflow/internal/permission"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const maxPermissionCheckBody = 1 << 20

type permissionDecisionChecker interface {
	EvaluateResource(ctx context.Context, resource permission.ResourceRef, operation permission.Operation) (permission.Decision, error)
}

// PermissionHandler exposes the current authenticated user's resource decision.
type PermissionHandler struct {
	checker permissionDecisionChecker
}

// NewPermissionHandler creates a handler backed by the shared permission checker.
func NewPermissionHandler(checker permissionDecisionChecker) *PermissionHandler {
	return &PermissionHandler{checker: checker}
}

type permissionCheckRequest struct {
	Resource  permissionCheckResource `json:"resource"`
	Operation permission.Operation    `json:"operation"`
}

type permissionCheckResource struct {
	Kind permission.ResourceKind `json:"kind"`
	ID   string                  `json:"id"`
}

type permissionCheckError struct {
	Status string `json:"status"`
	Code   string `json:"code"`
}

// CheckResource handles POST /api/v1/permissions/check.
func (h *PermissionHandler) CheckResource(c *gin.Context) {
	var request permissionCheckRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxPermissionCheckBody)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		c.JSON(http.StatusBadRequest, permissionCheckError{Status: "error", Code: "INVALID_REQUEST"})
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, permissionCheckError{Status: "error", Code: "INVALID_REQUEST"})
		return
	}
	if request.Resource.Kind == "" || request.Resource.ID == "" || request.Operation == "" {
		c.JSON(http.StatusBadRequest, permissionCheckError{Status: "error", Code: "INVALID_REQUEST"})
		return
	}
	if h == nil || h.checker == nil {
		c.JSON(http.StatusServiceUnavailable, permissionCheckError{Status: string(permission.DecisionUnverified), Code: "PERMISSION_SOURCE_UNAVAILABLE"})
		return
	}

	decision, err := h.checker.EvaluateResource(c.Request.Context(), permission.ResourceRef{
		Kind: request.Resource.Kind,
		ID:   request.Resource.ID,
	}, request.Operation)
	if errors.Is(err, permission.ErrInvalidPermission) {
		c.JSON(http.StatusBadRequest, permissionCheckError{Status: "error", Code: "INVALID_REQUEST"})
		return
	}
	if errors.Is(err, permission.ErrUnauthenticated) {
		c.JSON(http.StatusUnauthorized, permissionCheckError{Status: string(permission.DecisionUnverified), Code: "IDENTITY_UNAVAILABLE"})
		return
	}
	if err != nil {
		common.Warn("permission check could not verify current access",
			zap.Error(err),
			zap.String("method", c.Request.Method),
			zap.String("path", c.Request.URL.Path),
		)
		c.JSON(http.StatusServiceUnavailable, permissionCheckError{Status: string(permission.DecisionUnverified), Code: "PERMISSION_SOURCE_UNAVAILABLE"})
		return
	}
	c.JSON(http.StatusOK, decision)
}
