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
	"fmt"
	"github.com/spf13/viper"
)

// PostgreSQLConfig is the native PostgreSQL metadata store, separate from
// Vastbase's MySQL compatibility mode and from any document-engine database.
type PostgreSQLConfig struct {
	DatabaseName   string `mapstructure:"name"`
	Host           string `mapstructure:"host"`
	Port           int    `mapstructure:"port"`
	User           string `mapstructure:"user"`
	Password       string `mapstructure:"password"`
	MaxConnections int    `mapstructure:"max_connections"`
	StaleTimeout   int    `mapstructure:"stale_timeout"`
	SSLMode        string `mapstructure:"ssl_mode"`
	SSLRootCert    string `mapstructure:"ssl_root_cert"`
}

func (c *Config) parsePostgreSQLConfig(v *viper.Viper) error {
	cfg := PostgreSQLConfig{DatabaseName: "rag_flow", Host: "localhost", Port: 5432,
		User: "ragflow", MaxConnections: 6, StaleTimeout: 300}
	strings := map[string]*string{"name": &cfg.DatabaseName, "host": &cfg.Host,
		"user": &cfg.User, "password": &cfg.Password, "ssl_mode": &cfg.SSLMode, "ssl_root_cert": &cfg.SSLRootCert}
	for key, target := range strings {
		if v.IsSet("postgres." + key) {
			*target = v.GetString("postgres." + key)
		}
	}
	ints := map[string]*int{"port": &cfg.Port, "max_connections": &cfg.MaxConnections, "stale_timeout": &cfg.StaleTimeout}
	for key, target := range ints {
		if v.IsSet("postgres." + key) {
			*target = v.GetInt("postgres." + key)
		}
	}
	if cfg.Port < 1 || cfg.Port > 65535 || cfg.MaxConnections < 1 || cfg.StaleTimeout < 1 {
		return fmt.Errorf("invalid PostgreSQL port or connection pool limits")
	}
	switch cfg.SSLMode {
	case "", "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
	default:
		return fmt.Errorf("invalid PostgreSQL ssl_mode")
	}
	c.database.Postgres = cfg
	return nil
}

func (c *Config) GetPostgreSQLConfig() PostgreSQLConfig { return c.database.Postgres }

// ExportConfigs follows the existing superuser-only database inspection
// contract. PrintAll masks the password key before any configuration logging.
func (c *PostgreSQLConfig) ExportConfigs() map[string]interface{} {
	return map[string]interface{}{"database_name": c.DatabaseName, "username": c.User,
		"password": c.Password, "host": c.Host, "port": c.Port, "ssl_mode": c.SSLMode,
		"max_connections": c.MaxConnections, "stale_timeout": c.StaleTimeout}
}
