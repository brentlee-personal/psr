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

package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ragflow/internal/common"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestStartupLogMasksNATSCredentials(t *testing.T) {
	for _, host := range []string{
		"canary-user:canary-password@127.0.0.1",
		"canary-token@127.0.0.1",
		"canary-user:canary-password%zz@127.0.0.1",
	} {
		t.Run(strings.Split(host, "@")[1]+fmt.Sprint(len(host)), func(t *testing.T) {
			oldV, oldC, oldL := globalViper, globalConfig, common.Logger
			t.Cleanup(func() { globalViper, globalConfig, common.Logger = oldV, oldC, oldL })
			core, logs := observer.New(zapcore.InfoLevel)
			common.Logger = zap.New(core)
			t.Setenv("DB_TYPE", "mysql")
			cfg := "postgres: {password: canary-pg-private}\nkvrocks: {password: canary-kv-private}\ns3: {access_key: canary-s3-ak-private, secret_key: canary-s3-sk-private, session_token: canary-s3-st-private}\nsecret_key: canary-signing-private\nnats: {host: '" + host + "', port: 4222}\nbackends: [{config: {host: '" + host + "'}}]\n"
			path := filepath.Join(t.TempDir(), "canary.yaml")
			if err := os.WriteFile(path, []byte(cfg), 0600); err != nil {
				t.Fatal(err)
			}
			if err := Init(path); err != nil {
				t.Fatal("synthetic configuration did not load")
			}
			PrintAll()
			output := fmt.Sprint(logs.All())
			for _, secret := range []string{"canary-user", "canary-password", "canary-token", "canary-pg-private", "canary-kv-private", "canary-s3-ak-private", "canary-s3-sk-private", "canary-s3-st-private", "canary-signing-private"} {
				if strings.Contains(output, secret) {
					t.Error("credential reached actual PrintAll Info sink")
				}
			}
			if !strings.Contains(output, "127.0.0.1") {
				t.Error("safe endpoint missing from configuration log")
			}
			if GetConfig().GetNATSConfig().Host != host || globalViper.GetString("nats.host") != host {
				t.Error("logging changed the runtime connection input")
			}
		})
	}
}
