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

package nats

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"ragflow/internal/common"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natsclient "github.com/nats-io/nats.go"
	pkgerrors "github.com/pkg/errors"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func startupLogSink(t *testing.T) *observer.ObservedLogs {
	t.Helper()
	old, oldSugar := common.Logger, common.Sugar
	if err := common.InitLogger("debug", common.FileOutput{}, "startup-test"); err != nil {
		t.Fatal(err)
	}
	core, logs := observer.New(zapcore.DebugLevel)
	common.Logger = zap.New(core, zap.WithFatalHook(zapcore.WriteThenGoexit))
	t.Cleanup(func() { common.Logger, common.Sugar = old, oldSugar })
	return logs
}

func assertStartupLogsSafe(t *testing.T, output string) {
	t.Helper()
	for _, secret := range []string{"canary-user", "canary-password", "canary-token"} {
		if strings.Contains(output, secret) {
			t.Error("NATS startup output contains userinfo")
		}
	}
	if !strings.Contains(output, "127.0.0.1") {
		t.Error("safe endpoint missing")
	}
}

func TestNATSStartupFailureLogsRedactUserInfo(t *testing.T) {
	for _, userinfo := range []string{"canary-user:canary-password", "canary-token", "canary-user:canary-password%zz"} {
		t.Run(fmt.Sprint(len(userinfo)), func(t *testing.T) {
			logs := startupLogSink(t)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			listener.Close()
			engine := NewNatsEngine(userinfo+"@127.0.0.1", port)
			err = engine.Init()
			if err == nil {
				t.Fatal("closed port unexpectedly accepted")
			}
			wrapped := pkgerrors.WithStack(fmt.Errorf("startup: %w", err))
			assertStartupLogsSafe(t, fmt.Sprintf("%v\n%+v", wrapped, wrapped))
			common.Error("failed to initialize NATS engine", err)
			done := make(chan struct{})
			go func() {
				defer close(done)
				common.Fatal("Failed to initialize NATS engine", zap.Error(err))
			}()
			<-done
			assertStartupLogsSafe(t, fmt.Sprint(logs.All()))
			if strings.Contains(userinfo, "%zz") {
				var parseErr *url.Error
				if !errors.As(err, &parseErr) {
					t.Error("safe URL error category was lost")
				}
				if !strings.Contains(err.Error(), "invalid") {
					t.Error("parse error category missing")
				}
			} else if !errors.Is(err, natsclient.ErrNoServers) {
				t.Error("NATS no-servers error chain was lost")
			}
		})
	}
}

func TestNATSStartupSuccessLogsRedactUserInfo(t *testing.T) {
	for _, tokenOnly := range []bool{false, true} {
		t.Run(fmt.Sprint(tokenOnly), func(t *testing.T) {
			logs := startupLogSink(t)
			opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true}
			userinfo := "canary-user:canary-password"
			if tokenOnly {
				opts.Authorization = "canary-token"
				userinfo = "canary-token"
			} else {
				opts.Username, opts.Password = "canary-user", "canary-password"
			}
			ns, err := natsserver.NewServer(opts)
			if err != nil {
				t.Fatal("create embedded NATS server")
			}
			ns.Start()
			t.Cleanup(func() { ns.Shutdown(); ns.WaitForShutdown() })
			if !ns.ReadyForConnections(5 * time.Second) {
				t.Fatal("embedded NATS did not start")
			}
			addr := ns.Addr().(*net.TCPAddr)
			engine := NewNatsEngine(userinfo+"@127.0.0.1", addr.Port)
			if err := engine.Init(); err != nil {
				t.Fatal("credential-bearing NATS connection failed")
			}
			t.Cleanup(engine.nc.Close)
			if engine.host != userinfo+"@127.0.0.1" {
				t.Error("connection input changed")
			}
			unauthenticated, err := natsclient.Connect(fmt.Sprintf("nats://127.0.0.1:%d", addr.Port), natsclient.Timeout(time.Second))
			if err == nil {
				unauthenticated.Close()
				t.Error("fixture did not enforce authentication")
			}
			if logs.FilterMessageSnippet("NATS stream RAGFLOW_TASKS ready at").Len() != 1 ||
				logs.FilterMessageSnippet("NATS stream create successfully at").Len() != 1 {
				t.Error("real success log paths were not exercised")
			}
			assertStartupLogsSafe(t, fmt.Sprint(logs.All()))
		})
	}
}
