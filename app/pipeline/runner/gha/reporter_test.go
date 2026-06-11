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
	"context"
	"io"
	"testing"

	"github.com/harness/gitness/app/pipeline/manager"
	"github.com/harness/gitness/livelog"
	"github.com/harness/gitness/types"
	"github.com/harness/gitness/types/enum"

	"github.com/sirupsen/logrus"
)

// fakeManager records the manager interactions of the reporter.
type fakeManager struct {
	manager.ExecutionManager

	beforeSteps []string
	afterSteps  map[string]enum.CIStatus
	lines       map[int64][]string
	uploads     map[int64]bool
}

func newFakeManager() *fakeManager {
	return &fakeManager{
		afterSteps: map[string]enum.CIStatus{},
		lines:      map[int64][]string{},
		uploads:    map[int64]bool{},
	}
}

func (f *fakeManager) Write(_ context.Context, step int64, line *livelog.Line) error {
	f.lines[step] = append(f.lines[step], line.Message)
	return nil
}

func (f *fakeManager) UploadLogs(_ context.Context, step int64, _ io.Reader) error {
	f.uploads[step] = true
	return nil
}

func (f *fakeManager) BeforeStep(_ context.Context, step *types.Step) error {
	f.beforeSteps = append(f.beforeSteps, step.Name)
	return nil
}

func (f *fakeManager) AfterStep(_ context.Context, step *types.Step) error {
	f.afterSteps[step.Name] = step.Status
	return nil
}

func testStage(stepNames ...string) *types.Stage {
	names := append([]string{"Set up job"}, stepNames...)
	names = append(names, "Complete job")
	stage := &types.Stage{ID: 1}
	for i, name := range names {
		stage.Steps = append(stage.Steps, &types.Step{
			ID:      int64(i + 100),
			StageID: stage.ID,
			Number:  int64(i + 1),
			Name:    name,
			Status:  enum.CIStatusPending,
		})
	}
	return stage
}

func entry(msg string, fields logrus.Fields) *logrus.Entry {
	e := logrus.NewEntry(logrus.New())
	e.Message = msg
	e.Data = fields
	return e
}

func TestReporterHappyPath(t *testing.T) {
	fake := newFakeManager()
	stage := testStage("compile", "test")
	r := newReporter(fake, stage, []string{"compile", "test"})
	r.start()

	_ = r.Fire(entry("docker pull image", nil))
	_ = r.Fire(entry("⭐ Run Main compile", nil))
	_ = r.Fire(entry("building...", logrus.Fields{fieldRawOutput: true}))
	_ = r.Fire(entry("  ✅  Success - Main compile", logrus.Fields{fieldStepResult: "success"}))
	_ = r.Fire(entry("⭐ Run Main test", nil))
	_ = r.Fire(entry("testing...", logrus.Fields{fieldRawOutput: true}))
	_ = r.Fire(entry("  ✅  Success - Main test", logrus.Fields{fieldStepResult: "success"}))
	_ = r.Fire(entry("🏁  Job succeeded", logrus.Fields{fieldJobResult: "success"}))

	status := r.finish(nil, false)
	if status != enum.CIStatusSuccess {
		t.Fatalf("expected stage success, got %s", status)
	}

	wantBefore := []string{"Set up job", "compile", "test", "Complete job"}
	if len(fake.beforeSteps) != len(wantBefore) {
		t.Fatalf("unexpected BeforeStep calls: %v", fake.beforeSteps)
	}
	for i, name := range wantBefore {
		if fake.beforeSteps[i] != name {
			t.Fatalf("BeforeStep %d: got %q, want %q", i, fake.beforeSteps[i], name)
		}
	}
	for _, name := range wantBefore {
		if fake.afterSteps[name] != enum.CIStatusSuccess {
			t.Fatalf("step %q: got status %q, want success", name, fake.afterSteps[name])
		}
	}
	// setup output routed to "Set up job" (ID 100), raw output to steps.
	if len(fake.lines[100]) == 0 {
		t.Fatal("expected setup log lines on Set up job")
	}
	if fake.lines[101][1] != "building...\n" {
		t.Fatalf("unexpected compile output: %v", fake.lines[101])
	}
	// all steps got their logs uploaded.
	for _, step := range stage.Steps {
		if !fake.uploads[step.ID] {
			t.Fatalf("step %q logs were not uploaded", step.Name)
		}
	}
}

func TestReporterFailureAndSkip(t *testing.T) {
	fake := newFakeManager()
	stage := testStage("compile", "deploy")
	r := newReporter(fake, stage, []string{"compile", "deploy"})
	r.start()

	_ = r.Fire(entry("⭐ Run Main compile", nil))
	_ = r.Fire(entry("boom", logrus.Fields{fieldRawOutput: true}))
	_ = r.Fire(entry("  ❌  Failure - Main compile", logrus.Fields{fieldStepResult: "failure"}))
	_ = r.Fire(entry("🏁  Job failed", logrus.Fields{fieldJobResult: "failure"}))

	status := r.finish(nil, false)
	if status != enum.CIStatusFailure {
		t.Fatalf("expected stage failure, got %s", status)
	}
	if fake.afterSteps["compile"] != enum.CIStatusFailure {
		t.Fatalf("compile: got %q, want failure", fake.afterSteps["compile"])
	}
	// deploy never ran: skipped.
	if fake.afterSteps["deploy"] != enum.CIStatusSkipped {
		t.Fatalf("deploy: got %q, want skipped", fake.afterSteps["deploy"])
	}
	if fake.afterSteps["Set up job"] != enum.CIStatusSuccess {
		t.Fatalf("Set up job: got %q, want success", fake.afterSteps["Set up job"])
	}
}

func TestReporterSkippedStepBoundaries(t *testing.T) {
	// step 1 is skipped via if:; the first Main boundary belongs to step 2
	// and must be attributed by name, not position.
	fake := newFakeManager()
	stage := testStage("lint", "compile")
	r := newReporter(fake, stage, []string{"lint", "compile"})
	r.start()

	_ = r.Fire(entry("⭐ Run Main compile", nil))
	_ = r.Fire(entry("  ✅  Success - Main compile", logrus.Fields{fieldStepResult: "success"}))
	_ = r.Fire(entry("🏁  Job succeeded", logrus.Fields{fieldJobResult: "success"}))

	status := r.finish(nil, false)
	if status != enum.CIStatusSuccess {
		t.Fatalf("expected success, got %s", status)
	}
	if fake.afterSteps["compile"] != enum.CIStatusSuccess {
		t.Fatalf("compile: got %q, want success", fake.afterSteps["compile"])
	}
	if fake.afterSteps["lint"] != enum.CIStatusSkipped {
		t.Fatalf("lint: got %q, want skipped", fake.afterSteps["lint"])
	}
}

func TestReporterCancellation(t *testing.T) {
	fake := newFakeManager()
	stage := testStage("compile")
	r := newReporter(fake, stage, []string{"compile"})
	r.start()

	_ = r.Fire(entry("⭐ Run Main compile", nil))
	_ = r.Fire(entry("working...", logrus.Fields{fieldRawOutput: true}))

	status := r.finish(context.Canceled, true)
	if status != enum.CIStatusKilled {
		t.Fatalf("expected killed, got %s", status)
	}
	if fake.afterSteps["compile"] != enum.CIStatusKilled {
		t.Fatalf("compile: got %q, want killed", fake.afterSteps["compile"])
	}
}

func TestReporterErrorBeforeAnyStep(t *testing.T) {
	// e.g. image pull failure: no step boundary ever fires.
	fake := newFakeManager()
	stage := testStage("compile")
	r := newReporter(fake, stage, []string{"compile"})
	r.start()

	_ = r.Fire(entry("pull access denied", nil))

	status := r.finish(context.DeadlineExceeded, false)
	if status != enum.CIStatusError {
		t.Fatalf("expected error, got %s", status)
	}
	if fake.afterSteps["Set up job"] != enum.CIStatusError {
		t.Fatalf("Set up job: got %q, want error", fake.afterSteps["Set up job"])
	}
	if fake.afterSteps["compile"] != enum.CIStatusSkipped {
		t.Fatalf("compile: got %q, want skipped", fake.afterSteps["compile"])
	}
}
