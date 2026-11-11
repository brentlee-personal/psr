package kvrocks

import (
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"ragflow/internal/common"
	"testing"
)

func TestSensitiveGetDoesNotLogBackendFailure(t *testing.T) {
	core, entries := observer.New(zap.WarnLevel)
	original := common.Logger
	common.Logger = zap.New(core)
	t.Cleanup(func() { common.Logger = original })
	backend := redis.NewClient(&redis.Options{Addr: "unused.invalid:1"})
	backend.Close() // local closed-client error, no network connection
	client := &Client{client: backend}
	value, err := client.GetSensitive(t.Context(), "synthetic-signing-authority")
	if err == nil || value != "" {
		t.Fatal("closed backend was accepted")
	}
	if entries.Len() != 0 {
		t.Fatal("sensitive backend error was logged")
	}
	if _, err := client.Get(t.Context(), "ordinary-key"); err == nil {
		t.Fatal("ordinary error disappeared")
	}
	if entries.Len() != 1 {
		t.Fatal("ordinary Get logging changed")
	}

}
