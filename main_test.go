/*
Copyright 2019 Hammerspace

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"testing"

	log "github.com/sirupsen/logrus"
)

func TestParseLogLevel(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  log.Level
	}{
		{"unset falls back to default", "", DefaultLogLevel},
		{"whitespace only falls back to default", "   ", DefaultLogLevel},
		{"invalid value falls back to default", "chatty", DefaultLogLevel},
		{"info", "info", log.InfoLevel},
		{"debug", "debug", log.DebugLevel},
		{"warn", "warn", log.WarnLevel},
		{"error", "error", log.ErrorLevel},
		{"trace", "trace", log.TraceLevel},
		{"uppercase is accepted", "DEBUG", log.DebugLevel},
		{"surrounding whitespace is trimmed", "  debug  ", log.DebugLevel},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseLogLevel(tc.input); got != tc.want {
				t.Errorf("parseLogLevel(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// The driver must not default to debug: debug logs every Anvil REST call, which
// is far too verbose for steady-state operation.
func TestDefaultLogLevelIsNotDebug(t *testing.T) {
	if DefaultLogLevel >= log.DebugLevel {
		t.Errorf("DefaultLogLevel = %v, want a level less verbose than debug", DefaultLogLevel)
	}
}
