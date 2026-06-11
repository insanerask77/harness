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

// Package gha implements an embedded execution engine for pipelines whose
// config is a GitHub Actions workflow. Stages created by the triggerer with
// type "gha" are executed with nektos/act, reporting state and logs through
// the same execution manager the drone engine uses, so the API, SSE events
// and UI behave identically for both engines.
package gha

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/harness/gitness/app/pipeline/gha"
	"github.com/harness/gitness/app/pipeline/manager"
	"github.com/harness/gitness/encrypt"
	"github.com/harness/gitness/types"
	"github.com/harness/gitness/types/enum"

	"github.com/docker/docker/api/types/container"
	"github.com/nektos/act/pkg/model"
	actrunner "github.com/nektos/act/pkg/runner"
	"github.com/rs/zerolog/log"
)

// Runner executes a single GitHub Actions stage (= one workflow job, or one
// matrix combination) end to end.
type Runner struct {
	Config    *types.Config
	Manager   manager.ExecutionManager
	Encrypter encrypt.Encrypter

	platforms map[string]string
}

// NewRunner returns a runner for GitHub Actions stages.
func NewRunner(
	config *types.Config,
	execManager manager.ExecutionManager,
	encrypter encrypt.Encrypter,
) *Runner {
	// act resolves its docker client from the environment; propagate the
	// configured docker host so GITNESS_DOCKER_HOST applies to act too.
	if config.Docker.Host != "" && os.Getenv("DOCKER_HOST") == "" {
		_ = os.Setenv("DOCKER_HOST", config.Docker.Host)
	}
	return &Runner{
		Config:    config,
		Manager:   execManager,
		Encrypter: encrypter,
		platforms: parseRunnerImages(config.CI.GHA.RunnerImages),
	}
}

// Run executes an accepted gha stage. It mirrors the lifecycle the drone
// runner drives through the execution manager: BeforeStage with the step
// list, Before/AfterStep per step (from the log bridge), AfterStage with the
// final state.
//
//nolint:gocognit // the stage lifecycle is one linear flow.
func (r *Runner) Run(ctx context.Context, stageID int64) error {
	ectx, err := r.Manager.Details(ctx, stageID)
	if err != nil {
		return fmt.Errorf("gha: cannot fetch stage details: %w", err)
	}
	stage := ectx.Stage

	log := log.With().
		Int64("stage.id", stage.ID).
		Str("stage.name", stage.Name).
		Int64("execution.id", stage.ExecutionID).
		Logger()

	// re-derive the job behind this stage from the workflow file; stage
	// names are deterministic between the triggerer and the runner.
	runs, err := gha.ParseJobRunsForEvent(ectx.Config.Data, ectx.Execution.Event)
	if err != nil {
		return r.failStage(stage, fmt.Errorf("cannot parse workflow: %w", err))
	}
	jobRun, ok := gha.FindJobRun(runs, stage.Name)
	if !ok {
		return r.failStage(stage, fmt.Errorf("job for stage %q not found in workflow", stage.Name))
	}

	// transition the stage to running and create the step records: a
	// synthetic setup step, the workflow steps, and a synthetic teardown.
	now := time.Now().UnixMilli()
	stage.Status = enum.CIStatusRunning
	stage.Started = now
	stage.Steps = buildSteps(stage, jobRun)
	if err := r.Manager.BeforeStage(noContext, stage); err != nil {
		return fmt.Errorf("gha: cannot start stage: %w", err)
	}

	reporter := newReporter(r.Manager, stage, jobRun.StepKeys)
	reporter.start()

	status, runErr := r.runJob(ctx, ectx, jobRun, reporter)
	if runErr != nil {
		log.Warn().Err(runErr).Msg("gha: job execution finished with error")
	}

	stage.Status = status
	stage.Stopped = time.Now().UnixMilli()
	if status == enum.CIStatusError && runErr != nil {
		stage.Error = runErr.Error()
	}
	stage.ExitCode = exitCode(status)
	if err := r.Manager.AfterStage(noContext, stage); err != nil {
		return fmt.Errorf("gha: cannot complete stage: %w", err)
	}
	return runErr
}

// runJob prepares the workspace and event payload and drives the act
// executor for the single job, watching for cancellation.
func (r *Runner) runJob(
	ctx context.Context,
	ectx *manager.ExecutionContext,
	jobRun gha.JobRun,
	reporter *reporter,
) (enum.CIStatus, error) {
	workdir, err := prepareWorkspace(
		ctx, r.Config.CI.GHA.WorkdirRoot, ectx.Repo.GitURL, ectx.Execution.After, ectx.Netrc)
	if err != nil {
		reporter.finish(err, false)
		return enum.CIStatusError, err
	}
	defer os.RemoveAll(workdir)

	eventPath, eventName, err := writeEventFile(workdir, ectx.Repo, ectx.Execution)
	if err != nil {
		reporter.finish(err, false)
		return enum.CIStatusError, err
	}

	planner, err := model.NewSingleWorkflowPlanner("workflow.yml", bytes.NewReader(ectx.Config.Data))
	if err != nil {
		reporter.finish(err, false)
		return enum.CIStatusError, err
	}
	plan, err := planner.PlanJob(jobRun.JobID)
	if err != nil {
		reporter.finish(err, false)
		return enum.CIStatusError, err
	}

	cfg := &actrunner.Config{
		Workdir:       workdir,
		BindWorkdir:   false,
		EventName:     eventName,
		EventPath:     eventPath,
		DefaultBranch: ectx.Repo.DefaultBranch,
		Platforms:     r.platformsFor(jobRun.RunsOn),
		Secrets:       r.secretsMap(ectx),
		Env: map[string]string{
			"GITHUB_REPOSITORY": ectx.Repo.Path,
			"GITHUB_SHA":        ectx.Execution.After,
			"GITHUB_REF":        ectx.Execution.Ref,
		},
		Matrix:         matrixFilter(jobRun.Matrix),
		GitHubInstance: "github.com",
		AutoRemove:     true,
		// keep actions/checkout a no-op; the pre-cloned workdir is copied
		// into the job container instead.
		NoSkipCheckout: false,
	}
	if networks := r.Config.CI.ContainerNetworks; len(networks) > 0 {
		cfg.ContainerNetworkMode = container.NetworkMode(networks[0])
	}

	jobExecutor, err := actrunner.New(cfg)
	if err != nil {
		reporter.finish(err, false)
		return enum.CIStatusError, err
	}

	// watch for execution cancellation and abort act through its context.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cancelled := r.watchCancellation(runCtx, ectx.Stage.ExecutionID, cancel)

	runCtx = actrunner.WithJobLoggerFactory(runCtx, reporter)
	execErr := jobExecutor.NewPlanExecutor(plan)(runCtx)

	wasCancelled := *cancelled || errors.Is(runCtx.Err(), context.Canceled)
	status := reporter.finish(execErr, wasCancelled)
	if execErr != nil && status == enum.CIStatusSuccess {
		status = enum.CIStatusError
	}
	if status == enum.CIStatusError {
		return status, execErr
	}
	// job failures are a regular outcome, not an engine error.
	return status, nil
}

// watchCancellation polls the manager for cancellation requests and cancels
// the act context when the execution gets cancelled or completes elsewhere.
func (r *Runner) watchCancellation(
	ctx context.Context,
	executionID int64,
	cancel context.CancelFunc,
) *bool {
	cancelled := new(bool)
	go func() {
		for {
			if ctx.Err() != nil {
				return
			}
			watchCtx, watchCancel := context.WithTimeout(ctx, 30*time.Second)
			done, err := r.Manager.Watch(watchCtx, executionID)
			watchCancel()
			if err != nil {
				// poll timeout; resume watching.
				continue
			}
			if done {
				*cancelled = true
				cancel()
				return
			}
		}
	}()
	return cancelled
}

// failStage finalizes a stage that could not start executing at all.
func (r *Runner) failStage(stage *types.Stage, failure error) error {
	now := time.Now().UnixMilli()
	stage.Status = enum.CIStatusError
	stage.Error = failure.Error()
	stage.ExitCode = exitCodeFailure
	if stage.Started == 0 {
		stage.Started = now
	}
	stage.Stopped = now
	if err := r.Manager.AfterStage(noContext, stage); err != nil {
		return fmt.Errorf("gha: cannot fail stage: %w", err)
	}
	return failure
}

// buildSteps creates the persisted step list of a stage: setup, the workflow
// steps and teardown, all pending.
func buildSteps(stage *types.Stage, jobRun gha.JobRun) []*types.Step {
	names := make([]string, 0, len(jobRun.StepNames)+2)
	names = append(names, "Set up job")
	names = append(names, jobRun.StepNames...)
	names = append(names, "Complete job")

	steps := make([]*types.Step, len(names))
	for i, name := range names {
		steps[i] = &types.Step{
			StageID: stage.ID,
			Number:  int64(i + 1),
			Name:    name,
			Status:  enum.CIStatusPending,
		}
	}
	return steps
}

// platformsFor returns the runs-on label → image map for a job, applying the
// configured default image to unmapped labels.
func (r *Runner) platformsFor(runsOn []string) map[string]string {
	platforms := map[string]string{}
	for label, image := range r.platforms {
		platforms[label] = image
	}
	for _, label := range runsOn {
		key := strings.ToLower(label)
		if _, ok := platforms[key]; !ok {
			platforms[key] = r.Config.CI.GHA.DefaultImage
		}
	}
	return platforms
}

// secretsMap decrypts the space secrets and exposes them to the workflow,
// plus a GITHUB_TOKEN that authenticates git operations against gitness.
func (r *Runner) secretsMap(ectx *manager.ExecutionContext) map[string]string {
	secrets := map[string]string{}
	for _, secret := range ectx.Secrets {
		value, err := r.Encrypter.Decrypt([]byte(secret.Data))
		if err != nil {
			// data may predate encryption; fall back to the stored value.
			value = secret.Data
		}
		secrets[strings.ToUpper(secret.Identifier)] = value
	}
	if _, ok := secrets["GITHUB_TOKEN"]; !ok && ectx.Netrc != nil {
		secrets["GITHUB_TOKEN"] = ectx.Netrc.Password
	}
	return secrets
}

// matrixFilter converts a matrix combination into act's inclusion filter so
// only this stage's combination executes.
func matrixFilter(matrix map[string]any) map[string]map[string]bool {
	if len(matrix) == 0 {
		return nil
	}
	filter := map[string]map[string]bool{}
	for k, v := range matrix {
		filter[k] = map[string]bool{fmt.Sprintf("%v", v): true}
	}
	return filter
}

// parseRunnerImages parses the comma-separated label=image configuration.
func parseRunnerImages(raw string) map[string]string {
	images := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		label, image, ok := strings.Cut(pair, "=")
		if !ok {
			log.Warn().Str("pair", pair).Msg("gha: ignoring malformed runner image mapping")
			continue
		}
		images[strings.ToLower(strings.TrimSpace(label))] = strings.TrimSpace(image)
	}
	return images
}
