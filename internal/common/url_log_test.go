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

package common

import (
	"strings"
	"testing"
)

func TestRedactURLUserInfo(t *testing.T) {
	cases := []struct{ name, input, want string }{
		{"plain host", "nats.internal:4222", "nats.internal:4222"},
		{"plain URL", "nats://nats.internal:4222", "nats://nats.internal:4222"},
		{"password", "nats://canary-user:canary-password@nats.internal:4222", "nats://nats.internal:4222"},
		{"token", "nats://canary-token@nats.internal:4222", "nats://nats.internal:4222"},
		{"authority", "canary-user:canary-password@nats.internal", "nats.internal"},
		{"encoded", "nats://canary%2Duser:canary%2Dpassword@[::1]:4222", "nats://[::1]:4222"},
		{"malformed credential", "nats://canary-user:canary-password%zz@nats.internal:4222", "[invalid URL; endpoint nats.internal:4222]"},
		{"malformed endpoint", "nats://canary-token@nats.internal:invalid", "[invalid URL]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedactURLUserInfo(tc.input); got != tc.want {
				t.Errorf("safe URL = %q, want %q", got, tc.want)
			}
			if got := RedactURLUserInfo(tc.input); strings.Contains(got, "canary") {
				t.Error("credential remains")
			}
		})
	}
}
