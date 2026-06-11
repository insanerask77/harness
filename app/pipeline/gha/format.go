// Copyright 2023 Harness, Inc.
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

// Package gha provides parsing and planning helpers for pipelines whose
// config is a GitHub Actions workflow file, executed by the embedded
// nektos/act engine.
package gha

import "regexp"

// StageType is the stage type used to route GitHub Actions stages to the
// act-based runner instead of the drone docker runner.
const StageType = "gha"

var configPathRegex = regexp.MustCompile(`^\.github/workflows/[^/]+\.ya?ml$`)

// IsGHAConfigPath reports whether the pipeline config path points to a
// GitHub Actions workflow file.
func IsGHAConfigPath(path string) bool {
	return configPathRegex.MatchString(path)
}
