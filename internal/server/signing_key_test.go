package server

import (
	"context"
	"errors"
	"testing"
)

type signingKeyFixture struct {
	value string
	err   error
	calls int
}

func (s *signingKeyFixture) Get(context.Context, string) (string, error) {
	s.calls++
	return s.value, s.err
}

func TestCurrentSigningKeyReadOnlyFailClosed(t *testing.T) {
	original := globalConfig
	globalConfig = nil
	t.Cleanup(func() { globalConfig = original })
	for _, tc := range []struct {
		name, value string
		err         error
		wantOK      bool
	}{
		{"present", "synthetic-key", nil, true},
		{"missing", "", nil, false},
		{"failed", "must-not-use", errors.New("private backend details"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := &signingKeyFixture{value: tc.value, err: tc.err}
			key, err := GetCurrentSigningKey(t.Context(), fixture)
			if fixture.calls != 1 {
				t.Fatal("authority was not read exactly once")
			}
			if tc.wantOK {
				if err != nil || key != tc.value {
					t.Fatal("current key not returned")
				}
			} else if !errors.Is(err, ErrSigningKeyUnavailable) || key != "" {
				t.Fatal("unreadable key accepted")
			}
		})
	}
	if _, err := GetCurrentSigningKey(t.Context(), nil); !errors.Is(err, ErrSigningKeyUnavailable) {
		t.Fatal("nil source accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	fixture := &signingKeyFixture{value: "synthetic-key"}
	if _, err := GetCurrentSigningKey(ctx, fixture); !errors.Is(err, ErrSigningKeyUnavailable) || fixture.calls != 0 {
		t.Fatal("cancelled read proceeded")
	}
}
