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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/harness/gitness/app/pipeline/manager"
	"github.com/harness/gitness/livelog"
	"github.com/harness/gitness/types"
	"github.com/harness/gitness/types/enum"

	"github.com/rs/zerolog/log"
	"github.com/sirupsen/logrus"
)

// logrus fields and message markers emitted by the act job logger
// (see nektos/act pkg/runner step.go and job_executor.go).
const (
	fieldRawOutput  = "raw_output"
	fieldStepResult = "stepResult"
	fieldJobResult  = "jobResult"

	msgRunMainPrefix    = "⭐ Run Main " // "⭐ Run Main <step>"
	msgMainResultMarker = "- Main "     // "  ✅  Success - Main <step>"
)

var noContext = context.Background()

// reporter bridges the act job logger to the gitness execution manager: it
// implements runner.JobLoggerFactory and logrus.Hook, routing container
// output to livelog streams and translating act step/job results into
// step state transitions.
//
// stage.Steps is expected to contain a synthetic "Set up job" step first,
// the workflow steps in order, and a synthetic "Complete job" step last,
// all persisted (with IDs) by manager.BeforeStage.
type reporter struct {
	mu      sync.Mutex
	manager manager.ExecutionManager
	stage   *types.Stage

	// matchKeys mirror act's model.Step.String() per workflow step, used to
	// attribute "Run Main" boundaries by name (position is the fallback, as
	// steps skipped via if: emit no boundary at info level).
	matchKeys []string

	cur       int // index into stage.Steps currently receiving output
	started   []bool
	finished  []bool
	jobResult enum.CIStatus

	buffers map[int64][]livelog.Line
	starts  map[int64]time.Time
}

func newReporter(execManager manager.ExecutionManager, stage *types.Stage, matchKeys []string) *reporter {
	return &reporter{
		manager:   execManager,
		stage:     stage,
		matchKeys: matchKeys,
		started:   make([]bool, len(stage.Steps)),
		finished:  make([]bool, len(stage.Steps)),
		buffers:   map[int64][]livelog.Line{},
		starts:    map[int64]time.Time{},
	}
}

// WithJobLogger implements runner.JobLoggerFactory. The returned logger
// discards its own output; all routing happens in the hook.
func (r *reporter) WithJobLogger() *logrus.Logger {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	logger.SetLevel(logrus.InfoLevel)
	logger.AddHook(r)
	return logger
}

// Levels implements logrus.Hook.
func (r *reporter) Levels() []logrus.Level {
	return logrus.AllLevels
}

// Fire implements logrus.Hook.
func (r *reporter) Fire(entry *logrus.Entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// raw container output goes to the step currently running.
	if raw, ok := entry.Data[fieldRawOutput].(bool); ok && raw {
		r.writeLine(entry.Message)
		return nil
	}

	// job conclusion: remember it and route trailing output (container
	// cleanup, post steps) to the synthetic "Complete job" step.
	if result, ok := entry.Data[fieldJobResult]; ok {
		r.jobResult = mapActResult(stringify(result))
		r.moveTo(len(r.stage.Steps) - 1)
		r.writeLine(entry.Message)
		return nil
	}

	// main step start boundary.
	if key, ok := strings.CutPrefix(entry.Message, msgRunMainPrefix); ok {
		r.startWorkflowStep(key)
		r.writeLine(entry.Message)
		return nil
	}

	// main step conclusion.
	if result, ok := entry.Data[fieldStepResult]; ok {
		if strings.Contains(entry.Message, msgMainResultMarker) {
			r.writeLine(entry.Message)
			r.finishStep(r.cur, mapActResult(stringify(result)))
			return nil
		}
		// pre/post action results are plain log lines for us.
	}

	r.writeLine(entry.Message)
	return nil
}

// startWorkflowStep advances the current step to the workflow step matching
// the boundary key (act's step display string), falling back to the next
// not-yet-started workflow step when no name matches.
func (r *reporter) startWorkflowStep(key string) {
	next := -1
	for i := 1; i < len(r.stage.Steps)-1; i++ {
		if r.started[i] || r.finished[i] {
			continue
		}
		if r.matchKeys[i-1] == key {
			next = i
			break
		}
	}
	if next == -1 {
		for i := 1; i < len(r.stage.Steps)-1; i++ {
			if !r.started[i] && !r.finished[i] {
				next = i
				break
			}
		}
	}
	if next == -1 {
		// more boundaries than steps; keep writing to the current step.
		return
	}
	r.moveTo(next)
}

// moveTo finalizes the current step if it is still running without a result
// and makes the step at index the current output target.
func (r *reporter) moveTo(index int) {
	if index == r.cur {
		return
	}
	if r.started[r.cur] && !r.finished[r.cur] {
		r.finishStep(r.cur, enum.CIStatusSuccess)
	}
	r.cur = index
	r.startStep(index)
}

// startStep transitions the step at index to running and opens its log
// stream. No-op when the step already started.
func (r *reporter) startStep(index int) {
	if r.started[index] {
		return
	}
	step := r.stage.Steps[index]
	step.Status = enum.CIStatusRunning
	step.Started = time.Now().UnixMilli()
	r.started[index] = true
	r.starts[step.ID] = time.Now()
	if err := r.manager.BeforeStep(noContext, step); err != nil {
		log.Warn().Err(err).Str("step.name", step.Name).Msg("gha: cannot start step")
	}
}

// finishStep records the result of the step at index and tears its log
// stream down, uploading the buffered lines for permanent storage.
func (r *reporter) finishStep(index int, status enum.CIStatus) {
	if r.finished[index] {
		return
	}
	step := r.stage.Steps[index]
	step.Status = status
	step.ExitCode = exitCode(status)
	step.Stopped = time.Now().UnixMilli()
	if step.Started == 0 {
		step.Started = step.Stopped
	}
	r.finished[index] = true
	r.uploadLogs(step.ID)
	if err := r.manager.AfterStep(noContext, step); err != nil {
		log.Warn().Err(err).Str("step.name", step.Name).Msg("gha: cannot finish step")
	}
}

// start marks the synthetic "Set up job" step as running. Called once before
// the act executor starts.
func (r *reporter) start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.startStep(0)
}

// finish finalizes all step states once the act executor returned. It
// returns the stage status: the recorded job result when available,
// otherwise derived from the executor error / cancellation.
func (r *reporter) finish(execErr error, cancelled bool) enum.CIStatus {
	r.mu.Lock()
	defer r.mu.Unlock()

	stageStatus := r.jobResult
	switch {
	case cancelled:
		stageStatus = enum.CIStatusKilled
	case stageStatus == "":
		if execErr != nil {
			stageStatus = enum.CIStatusError
		} else {
			stageStatus = enum.CIStatusSuccess
		}
	}

	// the step the failure/cancellation surfaced in.
	currentStatus := enum.CIStatusSuccess
	if stageStatus != enum.CIStatusSuccess {
		currentStatus = stageStatus
	}
	if r.started[r.cur] && !r.finished[r.cur] {
		r.finishStep(r.cur, currentStatus)
	}

	// steps that never ran were skipped (if: conditions) or aborted.
	for i := range r.stage.Steps {
		if r.finished[i] {
			continue
		}
		status := enum.CIStatusSkipped
		if i == 0 || i == len(r.stage.Steps)-1 {
			// synthetic steps always resolve to the stage outcome.
			status = stageStatus
			if status == enum.CIStatusFailure && i == len(r.stage.Steps)-1 {
				// a failed job still completes its teardown.
				status = enum.CIStatusSuccess
			}
		}
		if cancelled {
			status = enum.CIStatusKilled
		}
		r.finishStep(i, status)
	}
	return stageStatus
}

// writeLine appends a log line to the current step stream and buffer.
func (r *reporter) writeLine(message string) {
	step := r.stage.Steps[r.cur]
	if r.finished[r.cur] || step.ID == 0 {
		return
	}
	if !r.started[r.cur] {
		// output for a step we did not explicitly start (e.g. setup output
		// before the first boundary); start it implicitly.
		r.startStep(r.cur)
	}
	elapsed := int64(0)
	if start, ok := r.starts[step.ID]; ok {
		elapsed = int64(time.Since(start).Seconds())
	}
	line := livelog.Line{
		Number:    len(r.buffers[step.ID]),
		Message:   strings.TrimSuffix(message, "\n") + "\n",
		Timestamp: elapsed,
	}
	r.buffers[step.ID] = append(r.buffers[step.ID], line)
	if err := r.manager.Write(noContext, step.ID, &line); err != nil {
		log.Debug().Err(err).Msg("gha: cannot write log line")
	}
}

// uploadLogs persists the buffered lines of a step to the log store.
func (r *reporter) uploadLogs(stepID int64) {
	lines := r.buffers[stepID]
	if lines == nil {
		lines = []livelog.Line{}
	}
	data, err := json.Marshal(lines)
	if err != nil {
		log.Warn().Err(err).Msg("gha: cannot marshal step logs")
		return
	}
	if err := r.manager.UploadLogs(noContext, stepID, bytes.NewReader(data)); err != nil {
		log.Warn().Err(err).Int64("step.id", stepID).Msg("gha: cannot upload step logs")
	}
}

// stringify renders a logrus field value (act uses stringer types for
// step/job results).
func stringify(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if s, ok := v.(interface{ String() string }); ok {
		return s.String()
	}
	return ""
}
