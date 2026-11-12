package readonly_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"ragflow/pkg/authn"
	"ragflow/pkg/readonly"
)

func TestReadonlyApprovalCannotBeRestoredOrSubmitted(t *testing.T) {
	var approval readonly.Approval
	if err:=json.Unmarshal([]byte(`{"readonly":true,"sql":"SELECT hidden","fingerprint":"forged"}`),&approval);!errors.Is(err,readonly.ErrDenied){t.Fatal("HTTP/JSON approval accepted")}
	var source *readonly.Source
	if _,err:=source.Execute(context.Background(),approval);!errors.Is(err,readonly.ErrDenied){t.Fatal("unsealed approval entered execution")}
}

func TestReadonlyMissingBusinessConnectionNeverFallsBack(t *testing.T) {
	directory:=t.TempDir()
	if _,err:=readonly.Open(context.Background(),filepath.Join(directory,"policy.json"),directory,authn.NewVerifier());!errors.Is(err,readonly.ErrUnverified){t.Fatal("missing explicit business credential accepted")}
}
