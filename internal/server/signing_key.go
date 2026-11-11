package server

import (
	"context"
	"errors"
)

// SigningKeyReader is the read-only portion of the native key authority.
type SigningKeyReader interface {
	Get(context.Context, string) (string, error)
}

var ErrSigningKeyUnavailable = errors.New("native signing key unavailable")

// GetCurrentSigningKey reads the authority used by native token issuance. It
// never initializes missing keys, generates a fallback, or logs backend errors.
func GetCurrentSigningKey(ctx context.Context, store SigningKeyReader) (string, error) {
	if ctx.Err() != nil {
		return "", ErrSigningKeyUnavailable
	}
	if cfg := GetConfig(); cfg != nil && cfg.GetSecretKey() != "" {
		return cfg.GetSecretKey(), nil
	}
	if store == nil {
		return "", ErrSigningKeyUnavailable
	}
	key, err := store.Get(ctx, SecretKeyRedisKey)
	if err != nil || key == "" {
		return "", ErrSigningKeyUnavailable
	}
	return key, nil
}
