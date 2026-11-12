package authn

import (
	"context"
	"ragflow/internal/common"
	"ragflow/internal/dao"
	"ragflow/internal/engine/kvrocks"
	"ragflow/internal/server"
)

// OpenRuntime initializes native identity dependencies once at process startup.
// No HTTP server, engine, worker, migration, signing key or membership is created.
func OpenRuntime(ctx context.Context, configPath string) (*Verifier, func(), error) {
	if configPath == "" || ctx == nil { return nil, nil, ErrUnverified }
	if err := common.InitLogger("error", common.FileOutput{}, "tob-auth"); err != nil { return nil, nil, ErrUnverified }
	if err := server.Init(configPath); err != nil { return nil, nil, ErrUnverified }
	if err := dao.InitIdentityDB(ctx); err != nil { return nil, nil, ErrUnverified }
	db, err := dao.DB.DB()
	if err != nil { return nil, nil, ErrUnverified }
	if err := kvrocks.Init(ctx); err != nil { db.Close(); return nil, nil, ErrUnverified }
	close := func() { kvrocks.Close(); db.Close() }
	return NewVerifier(), close, nil
}
