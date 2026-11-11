package authn_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"ragflow/pkg/authn"
)

func TestStrictUserRequestRejectsFallbacksAndUnprovenIdentities(t *testing.T) {
	verifier := authn.NewVerifier()
	for _, test := range []struct{ name, authorization, cookie string }{
		{"cookie-only", "", "native-user-cookie"},
		{"api-scheme", "ApiKey ragflow-key", ""},
		{"bare-beta", "0123456789abcdef0123456789abcdef", ""},
		{"empty-bearer", "Bearer", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/", strings.NewReader(`{"user_id":"forged","tenant_id":"forged"}`))
			if test.authorization != "" {
				request.Header.Set("Authorization", test.authorization)
			}
			request.Header.Set("Cookie", test.cookie)
			request.Header.Set("X-API-Key", "ragflow-key")
			_, err := verifier.VerifyUserRequest(context.Background(), request, authn.UserTokenPolicy{})
			if !errors.Is(err, authn.ErrDenied) {
				t.Fatalf("got %v", err)
			}
		})
	}
	request := httptest.NewRequest("POST", "/", nil)
	request.Header.Add("Authorization", "Bearer first")
	request.Header.Add("Authorization", "Bearer second")
	if _, err := verifier.VerifyUserRequest(context.Background(), request, authn.UserTokenPolicy{}); !errors.Is(err, authn.ErrDenied) {
		t.Fatal("duplicate auth accepted")
	}
	if err := verifier.CheckCurrentIdentity(context.Background(), authn.VerifiedIdentity{}); !errors.Is(err, authn.ErrDenied) {
		t.Fatal("snapshot/zero identity accepted")
	}
}
