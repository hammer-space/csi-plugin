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

package common

import (
	"os"
	"strings"

	log "github.com/sirupsen/logrus"
)

// DefaultLogLevel is used when LOG_LEVEL is unset or invalid.
const DefaultLogLevel = log.InfoLevel

// ParseLogLevel resolves LOG_LEVEL, falling back to info for empty or invalid
// values. It lives in common so the level is active before other packages emit
// initialization logs.
func ParseLogLevel(level string) log.Level {
	level = strings.TrimSpace(level)
	if level == "" {
		return DefaultLogLevel
	}
	parsed, err := log.ParseLevel(level)
	if err != nil {
		log.Warnf("invalid LOG_LEVEL %q, defaulting to %s", level, DefaultLogLevel)
		return DefaultLogLevel
	}
	return parsed
}

// ConfigureJSONLogging installs the process-wide single-line formatter. It is
// safe to call more than once and is invoked before package initialization logs
// as well as from main.
func ConfigureJSONLogging() {
	log.SetFormatter(&log.JSONFormatter{
		PrettyPrint:      false,
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05",
	})
	log.SetOutput(os.Stdout)
	log.SetLevel(ParseLogLevel(os.Getenv("LOG_LEVEL")))
}
