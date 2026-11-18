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
	"github.com/spf13/viper"
	"strings"
	"testing"
)

func postgresTestViper() *viper.Viper {
	v := viper.New()
	v.SetEnvPrefix("RAGFLOW")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	return v
}
func TestPostgreSQLConfigEnvironmentAndDatabaseGate(t *testing.T) {
	t.Setenv("DB_TYPE", "postgres")
	t.Setenv("RAGFLOW_POSTGRES_HOST", "postgres.internal")
	t.Setenv("RAGFLOW_POSTGRES_NAME", "psr_go_test")
	t.Setenv("RAGFLOW_POSTGRES_MAX_CONNECTIONS", "3")
	c := &Config{}
	if err := c.GetEnvironments(); err != nil {
		t.Fatal(err)
	}
	if err := c.ParseDatabaseConfig(postgresTestViper()); err != nil {
		t.Fatal(err)
	}
	got := c.GetPostgreSQLConfig()
	if c.DatabaseType() != "postgres" || got.Host != "postgres.internal" || got.DatabaseName != "psr_go_test" || got.MaxConnections != 3 || got.Port != 5432 || got.Password != "" {
		t.Fatal("PostgreSQL environment configuration did not match")
	}
}
func TestPostgreSQLConfigRejectsInvalidLimits(t *testing.T) {
	t.Setenv("DB_TYPE", "postgres")
	for _, key := range []string{"port", "max_connections", "stale_timeout"} {
		t.Run(key, func(t *testing.T) {
			v := viper.New()
			v.Set("postgres."+key, 0)
			if err := (&Config{}).ParseDatabaseConfig(v); err == nil {
				t.Fatal("invalid limit accepted")
			}
		})
	}
	v := viper.New()
	v.Set("postgres.ssl_mode", "insecure-anything")
	if err := (&Config{}).ParseDatabaseConfig(v); err == nil {
		t.Fatal("invalid ssl mode accepted")
	}
}
