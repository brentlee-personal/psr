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

package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func TestParsePGVectorConfigFallbackAndOverrides(t *testing.T) {
	t.Setenv("DB_TYPE", "postgres")
	v := viper.New()
	v.Set("postgres", map[string]interface{}{
		"host":          "rds.internal",
		"port":          5432,
		"user":          "meta_user",
		"password":      "meta_password",
		"name":          "psr_db",
		"ssl_mode":      "verify-full",
		"ssl_root_cert": "/certs/metadata-ca.pem",
	})
	v.Set("pgvector", map[string]interface{}{
		"user":      "worker_user",
		"password":  "worker_password",
		"ts_config": "jiebacfg",
	})

	c := &Config{}
	if err := c.ParseGeneralConfig(v); err != nil {
		t.Fatal(err)
	}
	if err := c.ParseDatabaseConfig(v); err != nil {
		t.Fatal(err)
	}
	if err := c.ParseDocEngineConfig(v); err != nil {
		t.Fatal(err)
	}

	got := c.GetPGVectorConfig()
	if got.Host != "rds.internal" || got.Port != 5432 || got.DBName != "psr_db" || got.SSLMode != "verify-full" ||
		got.SSLRootCert != "/certs/metadata-ca.pem" ||
		got.User != "worker_user" || got.Password != "worker_password" || got.TSConfig != "jiebacfg" ||
		got.MaxConnections != 16 || got.MaxIdleConns != 4 {
		t.Fatalf("pgvector resolved config = %#v", got)
	}

	v.Set("pgvector.ssl_mode", "verify-ca")
	v.Set("pgvector.ssl_root_cert", "/certs/doc-engine-ca.pem")
	if err := c.ParseDocEngineConfig(v); err != nil {
		t.Fatal(err)
	}
	got = c.GetPGVectorConfig()
	if got.SSLMode != "verify-ca" || got.SSLRootCert != "/certs/doc-engine-ca.pem" {
		t.Fatalf("explicit pgvector TLS settings not preserved: mode=%q cert=%q", got.SSLMode, got.SSLRootCert)
	}
}

func TestParsePGVectorConfigEnvOverrides(t *testing.T) {
	t.Setenv("DOC_ENGINE", "pgvector")
	t.Setenv("RAGFLOW_PGVECTOR_HOST", "env-pg-host")
	t.Setenv("RAGFLOW_PGVECTOR_PORT", "15432")
	t.Setenv("RAGFLOW_PGVECTOR_TS_CONFIG", "simple")
	t.Setenv("RAGFLOW_PGVECTOR_MAX_CONNECTIONS", "24")

	v := viper.New()
	v.SetEnvPrefix("RAGFLOW")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	c := &Config{}
	if err := c.ParseDocEngineConfig(v); err != nil {
		t.Fatal(err)
	}
	if err := c.GetEnvironments(); err != nil {
		t.Fatal(err)
	}
	if c.DocEngineType() != "pgvector" {
		t.Fatalf("DocEngineType = %q, want pgvector", c.DocEngineType())
	}
	got := c.GetPGVectorConfig()
	if got.Host != "env-pg-host" || got.Port != 15432 || got.TSConfig != "simple" || got.MaxConnections != 24 {
		t.Fatalf("pgvector env config = %#v", got)
	}
}
