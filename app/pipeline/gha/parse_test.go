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
	"reflect"
	"testing"

	"github.com/harness/gitness/types/enum"
)

func TestIsGHAConfigPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{".github/workflows/ci.yml", true},
		{".github/workflows/ci.yaml", true},
		{".github/workflows/build-and-test.yml", true},
		{".github/workflows/nested/ci.yml", false},
		{".harness/ci.yaml", false},
		{".drone.yml", false},
		{"github/workflows/ci.yml", false},
		{".github/workflows/", false},
		{".github/workflows/ci.json", false},
	}
	for _, tt := range tests {
		if got := IsGHAConfigPath(tt.path); got != tt.want {
			t.Errorf("IsGHAConfigPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

const workflowBasic = `
name: ci
on:
  push:
    branches: [main]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: compile
        run: make build
  test:
    runs-on: ubuntu-latest
    needs: build
    steps:
      - run: |
          make test
          make lint
`

func TestParseJobRunsBasic(t *testing.T) {
	runs, err := ParseJobRunsForEvent([]byte(workflowBasic), enum.TriggerEventPush)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	if runs[0].StageName != "build" || runs[1].StageName != "test" {
		t.Fatalf("unexpected stage order: %q, %q", runs[0].StageName, runs[1].StageName)
	}
	if !reflect.DeepEqual(runs[1].DependsOn, []string{"build"}) {
		t.Fatalf("unexpected depends_on: %v", runs[1].DependsOn)
	}
	wantSteps := []string{"Run actions/checkout@v4", "compile"}
	if !reflect.DeepEqual(runs[0].StepNames, wantSteps) {
		t.Fatalf("unexpected step names: %v", runs[0].StepNames)
	}
	// multi-line run commands are labelled with their first line.
	if runs[1].StepNames[0] != "Run make test" {
		t.Fatalf("unexpected step name: %q", runs[1].StepNames[0])
	}
	if !reflect.DeepEqual(runs[0].RunsOn, []string{"ubuntu-latest"}) {
		t.Fatalf("unexpected runs-on: %v", runs[0].RunsOn)
	}
}

func TestParseJobRunsEventFilter(t *testing.T) {
	// workflow only reacts to push; a pull request event plans nothing.
	runs, err := ParseJobRunsForEvent([]byte(workflowBasic), enum.TriggerEventPullRequest)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("expected no runs for pull_request, got %d", len(runs))
	}
}

func TestParseJobRunsManualFallback(t *testing.T) {
	// manual triggers fall back to the push plan when the workflow has no
	// workflow_dispatch trigger.
	runs, err := ParseJobRunsForEvent([]byte(workflowBasic), enum.TriggerEventManual)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected fallback to push plan with 2 runs, got %d", len(runs))
	}
}

const workflowMatrix = `
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        go: ["1.21", "1.22"]
        db: [sqlite, postgres]
    steps:
      - run: make test
  notify:
    runs-on: ubuntu-latest
    needs: build
    steps:
      - run: echo done
`

func TestParseJobRunsMatrix(t *testing.T) {
	runs, err := ParseJobRunsForEvent([]byte(workflowMatrix), enum.TriggerEventPush)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 5 {
		t.Fatalf("expected 5 runs (4 matrix + notify), got %d", len(runs))
	}
	wantNames := []string{
		"build (postgres, 1.21)",
		"build (postgres, 1.22)",
		"build (sqlite, 1.21)",
		"build (sqlite, 1.22)",
		"notify",
	}
	for i, want := range wantNames {
		if runs[i].StageName != want {
			t.Fatalf("run %d: got stage name %q, want %q", i, runs[i].StageName, want)
		}
	}
	if !reflect.DeepEqual(runs[4].DependsOn, wantNames[:4]) {
		t.Fatalf("notify should depend on all matrix stages, got %v", runs[4].DependsOn)
	}
	if runs[0].Matrix["db"] != "postgres" || runs[0].Matrix["go"] != "1.21" {
		t.Fatalf("unexpected matrix combination: %v", runs[0].Matrix)
	}

	// determinism: parsing twice yields the same order.
	for range 20 {
		again, err := ParseJobRunsForEvent([]byte(workflowMatrix), enum.TriggerEventPush)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(runs, again) {
			t.Fatal("ParseJobRunsForEvent is not deterministic")
		}
	}
}

const workflowReusable = `
on: push
jobs:
  call:
    uses: ./.github/workflows/other.yml
`

func TestParseJobRunsReusableWorkflowRejected(t *testing.T) {
	_, err := ParseJobRunsForEvent([]byte(workflowReusable), enum.TriggerEventPush)
	if err == nil {
		t.Fatal("expected error for reusable workflow job")
	}
}

func TestParseJobRunsDuplicateDisplayNames(t *testing.T) {
	const wf = `
on: push
jobs:
  a:
    name: build
    runs-on: ubuntu-latest
    steps: [{run: echo a}]
  b:
    name: build
    runs-on: ubuntu-latest
    steps: [{run: echo b}]
`
	runs, err := ParseJobRunsForEvent([]byte(wf), enum.TriggerEventPush)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	// duplicate display names fall back to job IDs to keep stage names unique.
	if runs[0].StageName != "a" || runs[1].StageName != "b" {
		t.Fatalf("expected job IDs as stage names, got %q, %q", runs[0].StageName, runs[1].StageName)
	}
}

func TestMapTriggerEvent(t *testing.T) {
	tests := map[enum.TriggerEvent]string{
		enum.TriggerEventPush:        EventPush,
		enum.TriggerEventTag:         EventPush,
		enum.TriggerEventPullRequest: EventPullRequest,
		enum.TriggerEventCron:        EventSchedule,
		enum.TriggerEventManual:      EventWorkflowDispatch,
	}
	for event, want := range tests {
		if got := MapTriggerEvent(event); got != want {
			t.Errorf("MapTriggerEvent(%q) = %q, want %q", event, got, want)
		}
	}
}
