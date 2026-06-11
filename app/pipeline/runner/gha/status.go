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

package gha

import "github.com/harness/gitness/types/enum"

// exit codes mirrored from the drone runner conventions so the UI renders
// results consistently across engines.
const (
	exitCodeFailure = 1
	exitCodeKilled  = 130
)

// mapActResult maps an act job/step result (the jobResult/stepResult logrus
// field values: success, failure, cancelled, skipped) to a gitness CI status.
func mapActResult(result string) enum.CIStatus {
	switch result {
	case "success":
		return enum.CIStatusSuccess
	case "failure":
		return enum.CIStatusFailure
	case "cancelled":
		return enum.CIStatusKilled
	case "skipped":
		return enum.CIStatusSkipped
	default:
		return enum.CIStatusError
	}
}

// exitCode returns the exit code matching a CI status.
func exitCode(status enum.CIStatus) int {
	switch status {
	case enum.CIStatusFailure, enum.CIStatusError:
		return exitCodeFailure
	case enum.CIStatusKilled:
		return exitCodeKilled
	default:
		return 0
	}
}
