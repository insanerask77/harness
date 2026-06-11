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

import (
	"testing"

	"github.com/harness/gitness/app/pipeline/gha"
	"github.com/harness/gitness/types"
	"github.com/harness/gitness/types/enum"
)

func TestMapActResult(t *testing.T) {
	tests := map[string]enum.CIStatus{
		"success":   enum.CIStatusSuccess,
		"failure":   enum.CIStatusFailure,
		"cancelled": enum.CIStatusKilled,
		"skipped":   enum.CIStatusSkipped,
		"bogus":     enum.CIStatusError,
	}
	for in, want := range tests {
		if got := mapActResult(in); got != want {
			t.Errorf("mapActResult(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseRunnerImages(t *testing.T) {
	images := parseRunnerImages(
		"ubuntu-latest=ghcr.io/catthehacker/ubuntu:act-latest, Ubuntu-22.04=img:22 ,malformed,")
	if len(images) != 2 {
		t.Fatalf("expected 2 mappings, got %v", images)
	}
	if images["ubuntu-latest"] != "ghcr.io/catthehacker/ubuntu:act-latest" {
		t.Fatalf("unexpected mapping: %v", images)
	}
	// labels are lowercased.
	if images["ubuntu-22.04"] != "img:22" {
		t.Fatalf("expected lowercased label, got %v", images)
	}
}

func TestPlatformsFor(t *testing.T) {
	r := &Runner{platforms: map[string]string{"ubuntu-latest": "img:latest"}}
	r.Config = &types.Config{}
	r.Config.CI.GHA.DefaultImage = "img:default"

	platforms := r.platformsFor([]string{"ubuntu-latest", "Self-Hosted"})
	if platforms["ubuntu-latest"] != "img:latest" {
		t.Fatalf("mapped label lost: %v", platforms)
	}
	if platforms["self-hosted"] != "img:default" {
		t.Fatalf("default image not applied: %v", platforms)
	}
}

func TestMatrixFilter(t *testing.T) {
	if matrixFilter(nil) != nil {
		t.Fatal("expected nil filter for non-matrix jobs")
	}
	filter := matrixFilter(map[string]any{"go": "1.21", "fast": true})
	if !filter["go"]["1.21"] || !filter["fast"]["true"] {
		t.Fatalf("unexpected filter: %v", filter)
	}
}

func TestBuildSteps(t *testing.T) {
	stage := &types.Stage{ID: 7}
	steps := buildSteps(stage, gha.JobRun{StepNames: []string{"compile", "test"}})
	wantNames := []string{"Set up job", "compile", "test", "Complete job"}
	if len(steps) != len(wantNames) {
		t.Fatalf("expected %d steps, got %d", len(wantNames), len(steps))
	}
	for i, want := range wantNames {
		if steps[i].Name != want || steps[i].StageID != 7 || steps[i].Number != int64(i+1) {
			t.Fatalf("step %d: %+v, want name %q", i, steps[i], want)
		}
		if steps[i].Status != enum.CIStatusPending {
			t.Fatalf("step %d: status %q, want pending", i, steps[i].Status)
		}
	}
}

func TestPullReqNumber(t *testing.T) {
	if n := pullReqNumber("refs/pull/42/head"); n != 42 {
		t.Fatalf("got %d, want 42", n)
	}
	if n := pullReqNumber("refs/heads/main"); n != 0 {
		t.Fatalf("got %d, want 0", n)
	}
}
