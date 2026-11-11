// Copyright 2026 The InfiniFlow Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ragflow/internal/common"

	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestIndependentRuntimeMinIOCredentialLogging(t *testing.T) {
	oldViper, oldConfig, oldLogger := globalViper, globalConfig, common.Logger
	t.Cleanup(func() { globalViper, globalConfig, common.Logger = oldViper, oldConfig, oldLogger })
	core, logs := observer.New(zapcore.InfoLevel)
	common.Logger = zap.New(core)
	t.Setenv("DB_TYPE", "postgres")
	const access = "synthetic-minio-access-key-canary"
	const secret = "synthetic-minio-secret-key-canary"
	path := filepath.Join(t.TempDir(), "runtime.yaml")
	cfg := "general: {database: postgres, storage_engine: minio}\nminio: {host: minio.example:9000, user: " + access + ", password: " + secret + ", bucket: fixture-bucket, prefix_path: fixture/}\npostgres: {password: synthetic-pg-secret-canary}\n"
	if err := os.WriteFile(path, []byte(cfg), 0600); err != nil {
		t.Fatal("fixture write failed")
	}
	if err := Init(path); err != nil {
		t.Fatal("synthetic configuration load failed")
	}
	PrintAll()
	output := fmt.Sprint(logs.All())
	t.Run("password_and_PG_stay_masked", func(t *testing.T) {
		if strings.Contains(output, secret) || strings.Contains(output, "synthetic-pg-secret-canary") {
			t.Error("private credential reached Info sink")
		}
	})
	t.Run("MinIO_access_key_alias_user_stays_masked", func(t *testing.T) {
		if strings.Contains(output, access) {
			t.Error("MinIO access-key alias minio.user reached actual PrintAll Info sink")
		}
	})
	t.Run("runtime_access_key_preserved", func(t *testing.T) {
		if globalViper.GetString("minio.user") != access || GetConfig().GetMinioConfig().User != access {
			t.Error("logger changed real runtime credential input")
		}
	})
}

func TestPrintAllMasksMinIOAliasContext(t *testing.T) {
	oldViper, oldLogger := globalViper, common.Logger
	t.Cleanup(func() { globalViper, common.Logger = oldViper, oldLogger })
	globalViper = viper.New()
	globalViper.SetConfigType("yaml")
	if err := globalViper.ReadConfig(strings.NewReader(`
minio: {user: synthetic-minio-alias, password: synthetic-minio-password, host: minio.example:9000, bucket: fixture-bucket, region: fixture-region}
postgres: {user: visible-postgres-user, password: synthetic-postgres-password}
s3: {access_key: synthetic-s3-access, secret_key: synthetic-s3-secret, session_token: synthetic-s3-session}
backends:
  - minio: {user: synthetic-nested-minio-alias, host: nested-minio.example:9000}
  - config: {user: visible-backend-user}
user: visible-root-user
`)); err != nil {
		t.Fatal(err)
	}
	original := globalViper.AllSettings()
	core, logs := observer.New(zapcore.InfoLevel)
	common.Logger = zap.New(core)
	PrintAll()
	settings := make(map[string]any)
	for _, entry := range logs.FilterMessage("config").All() {
		fields := entry.ContextMap()
		settings[fields["key"].(string)] = fields["value"]
	}
	want := map[string]any{
		"minio":    map[string]any{"user": "******", "password": "******", "host": "minio.example:9000", "bucket": "fixture-bucket", "region": "fixture-region"},
		"postgres": map[string]any{"user": "visible-postgres-user", "password": "******"},
		"s3":       map[string]any{"access_key": "******", "secret_key": "******", "session_token": "******"},
		"backends": []any{
			map[string]any{"minio": map[string]any{"user": "******", "host": "nested-minio.example:9000"}},
			map[string]any{"config": map[string]any{"user": "visible-backend-user"}},
		},
		"user": "visible-root-user",
	}
	if !reflect.DeepEqual(settings, want) {
		t.Error("actual PrintAll sink did not mask only the object-storage credentials while retaining safe settings")
	}
	if !reflect.DeepEqual(globalViper.AllSettings(), original) {
		t.Error("logging mutated the loaded configuration")
	}
}

func TestMaskConfigCredentialsMinIOAliasCase(t *testing.T) {
	input := map[string]any{
		"MiNiO": map[string]any{"UsEr": "synthetic-minio-alias", "HOST": "minio.example:9000"},
		"other": map[string]any{"UsEr": "visible-other-user"},
	}
	want := map[string]any{
		"MiNiO": map[string]any{"UsEr": "******", "HOST": "minio.example:9000"},
		"other": map[string]any{"UsEr": "visible-other-user"},
	}
	if !reflect.DeepEqual(maskConfigCredentials(input), want) {
		t.Error("MinIO access-key alias masking must be case insensitive and scoped to the MinIO section")
	}
	if input["MiNiO"].(map[string]any)["UsEr"] != "synthetic-minio-alias" {
		t.Error("masking changed the original access key")
	}
}
