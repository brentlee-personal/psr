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
	"net/url"
	"strings"
)

// RedactURLUserInfo removes the entire userinfo from a URL or host authority
// used for logging. Token authentication occupies the username, so URL.Redacted
// alone is insufficient. Never use the result as a connection address.
func RedactURLUserInfo(value string) string {
	if !strings.Contains(value, "@") {
		return value
	}
	authority := !strings.Contains(value, "://")
	input := value
	if authority {
		input = "//" + value
	}
	parsed, err := url.Parse(input)
	if err != nil {
		// A malformed credential can make url.Parse reject the whole URI. Recover
		// only a separately parsed endpoint; neither the URI nor parse error is safe.
		endpoint, endpointErr := url.Parse("//" + value[strings.LastIndex(value, "@")+1:])
		if endpointErr == nil && endpoint.Host != "" {
			return "[invalid URL; endpoint " + endpoint.Host + "]"
		}
		return "[invalid URL]"
	}
	if parsed.User == nil {
		return "[invalid URL]"
	}
	parsed.User = nil
	if authority {
		return strings.TrimPrefix(parsed.String(), "//")
	}
	return parsed.String()
}
