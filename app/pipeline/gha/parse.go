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
	"fmt"
	"sort"
	"strings"

	"github.com/harness/gitness/types/enum"

	"github.com/nektos/act/pkg/model"
)

// GitHub Actions event names.
const (
	EventPush             = "push"
	EventPullRequest      = "pull_request"
	EventSchedule         = "schedule"
	EventWorkflowDispatch = "workflow_dispatch"
)

// JobRun is one schedulable unit of a workflow: a job, or a single matrix
// combination of a job. The slice returned by ParseJobRunsForEvent is
// deterministic for a given workflow + event, which allows the runner to
// re-derive the job behind a stage created by the triggerer.
type JobRun struct {
	// JobID is the job key in the workflow file (jobs.<id>).
	JobID string
	// StageName is the unique display name used for the gitness stage and
	// for DependsOn references (e.g. "build" or "build (1.21, ubuntu)").
	StageName string
	// DependsOn holds the expanded stage names of all `needs` jobs.
	DependsOn []string
	// Matrix is the matrix combination for this run; nil for non-matrix jobs.
	Matrix map[string]any
	// RunsOn holds the raw runs-on labels of the job.
	RunsOn []string
	// StepNames holds the display names of the job steps, in workflow order.
	StepNames []string
	// StepKeys holds the act-style step strings (model.Step.String()) used
	// by the runner to attribute step boundaries in the act log output.
	StepKeys []string
}

// MapTriggerEvent maps a gitness trigger event to the GitHub Actions
// event name used to plan the workflow.
func MapTriggerEvent(event enum.TriggerEvent) string {
	switch event {
	case enum.TriggerEventPullRequest:
		return EventPullRequest
	case enum.TriggerEventCron:
		return EventSchedule
	case enum.TriggerEventManual:
		return EventWorkflowDispatch
	case enum.TriggerEventTag, enum.TriggerEventPush:
		return EventPush
	default:
		return EventPush
	}
}

// ParseJobRunsForEvent parses a workflow and returns the job runs to execute
// for the given gitness trigger event. Manual triggers fall back from
// workflow_dispatch to push to all jobs, so that "Run pipeline" from the UI
// always executes something. An empty result means the workflow defines no
// jobs for the event and the execution should be skipped.
func ParseJobRunsForEvent(data []byte, event enum.TriggerEvent) ([]JobRun, error) {
	eventName := MapTriggerEvent(event)
	runs, err := parseJobRuns(data, eventName)
	if err != nil {
		return nil, err
	}
	if len(runs) > 0 || event != enum.TriggerEventManual {
		return runs, nil
	}

	// manual fallback: workflow_dispatch -> push -> all jobs.
	runs, err = parseJobRuns(data, EventPush)
	if err != nil {
		return nil, err
	}
	if len(runs) > 0 {
		return runs, nil
	}
	return parseJobRuns(data, "")
}

// parseJobRuns plans the workflow for the given event name (empty plans all
// jobs) and flattens the plan into a deterministic, topologically ordered
// list of job runs.
func parseJobRuns(data []byte, eventName string) ([]JobRun, error) {
	planner, err := model.NewSingleWorkflowPlanner("workflow", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to parse workflow: %w", err)
	}

	var plan *model.Plan
	if eventName == "" {
		plan, err = planner.PlanAll()
	} else {
		plan, err = planner.PlanEvent(eventName)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to plan workflow: %w", err)
	}

	// flatten the plan level by level. Within a level act derives run order
	// from a map, so sort by job ID to keep the slice deterministic across
	// processes - the runner relies on this to re-derive jobs by stage name.
	type plannedJob struct {
		jobID string
		job   *model.Job
	}
	var planned []plannedJob
	for _, stage := range plan.Stages {
		levelRuns := make([]*model.Run, len(stage.Runs))
		copy(levelRuns, stage.Runs)
		sort.Slice(levelRuns, func(i, j int) bool { return levelRuns[i].JobID < levelRuns[j].JobID })
		for _, run := range levelRuns {
			job := run.Workflow.GetJob(run.JobID)
			if job == nil {
				return nil, fmt.Errorf("job %q not found in workflow", run.JobID)
			}
			if job.Uses != "" {
				return nil, fmt.Errorf("job %q uses a reusable workflow, which is not supported", run.JobID)
			}
			planned = append(planned, plannedJob{jobID: run.JobID, job: job})
		}
	}

	// stage display names use the job name when unique, the job ID otherwise -
	// DependsOn matching requires stage names to be unique.
	nameCount := map[string]int{}
	for _, p := range planned {
		nameCount[jobDisplayName(p.jobID, p.job)]++
	}

	// first pass: expand matrix combinations and map job IDs to the stage
	// names they produce, so needs can reference them in the second pass.
	stageNames := map[string][]string{}
	jobsByID := map[string]*model.Job{}
	var runs []JobRun
	for _, p := range planned {
		jobsByID[p.jobID] = p.job
		base := jobDisplayName(p.jobID, p.job)
		if nameCount[base] > 1 {
			base = p.jobID
		}

		matrixes, err := p.job.GetMatrixes()
		if err != nil {
			return nil, fmt.Errorf("failed to expand matrix of job %q: %w", p.jobID, err)
		}
		sortMatrixes(matrixes)

		stepNames := make([]string, len(p.job.Steps))
		stepKeys := make([]string, len(p.job.Steps))
		for i, step := range p.job.Steps {
			stepNames[i] = stepDisplayName(step, i)
			stepKeys[i] = step.String()
		}

		for _, matrix := range matrixes {
			name := base
			if len(matrix) > 0 {
				name = fmt.Sprintf("%s (%s)", base, matrixValues(matrix))
			}
			run := JobRun{
				JobID:     p.jobID,
				StageName: name,
				RunsOn:    p.job.RunsOn(),
				StepNames: stepNames,
				StepKeys:  stepKeys,
			}
			if len(matrix) > 0 {
				run.Matrix = matrix
			}
			runs = append(runs, run)
			stageNames[p.jobID] = append(stageNames[p.jobID], name)
		}
	}

	// second pass: expand needs into the stage names created above.
	for i := range runs {
		job := jobsByID[runs[i].JobID]
		for _, need := range job.Needs() {
			expanded, ok := stageNames[need]
			if !ok {
				return nil, fmt.Errorf("job %q needs unknown job %q", runs[i].JobID, need)
			}
			runs[i].DependsOn = append(runs[i].DependsOn, expanded...)
		}
	}

	return runs, nil
}

// FindJobRun returns the job run matching the given stage name.
func FindJobRun(runs []JobRun, stageName string) (JobRun, bool) {
	for _, run := range runs {
		if run.StageName == stageName {
			return run, true
		}
	}
	return JobRun{}, false
}

func jobDisplayName(jobID string, job *model.Job) string {
	if job.Name != "" {
		return job.Name
	}
	return jobID
}

// stepDisplayName mirrors how GitHub labels steps: explicit name, the used
// action, or the first line of the run script.
func stepDisplayName(step *model.Step, index int) string {
	switch {
	case step.Name != "":
		return step.Name
	case step.Uses != "":
		return fmt.Sprintf("Run %s", step.Uses)
	case step.Run != "":
		line := strings.SplitN(strings.TrimSpace(step.Run), "\n", 2)[0]
		return fmt.Sprintf("Run %s", line)
	default:
		return fmt.Sprintf("Step %d", index+1)
	}
}

// sortMatrixes orders matrix combinations by their canonical key so the
// order is stable across processes (act derives it from map iteration).
func sortMatrixes(matrixes []map[string]any) {
	sort.Slice(matrixes, func(i, j int) bool {
		return matrixKey(matrixes[i]) < matrixKey(matrixes[j])
	})
}

func matrixKey(matrix map[string]any) string {
	keys := make([]string, 0, len(matrix))
	for k := range matrix {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%v", k, matrix[k])
	}
	return strings.Join(parts, ",")
}

// matrixValues renders the matrix combination values the way GitHub does in
// job names: "job (val1, val2)", with keys in sorted order.
func matrixValues(matrix map[string]any) string {
	keys := make([]string, 0, len(matrix))
	for k := range matrix {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%v", matrix[k])
	}
	return strings.Join(parts, ", ")
}
