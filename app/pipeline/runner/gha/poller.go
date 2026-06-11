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
	"fmt"
	"runtime/debug"
	"sync"

	"github.com/harness/gitness/app/pipeline/gha"
	"github.com/harness/gitness/app/pipeline/manager"

	"github.com/rs/zerolog/log"
)

// Poller requests pending gha stages from the execution manager and
// dispatches them to the runner, mirroring the drone runner-go poller.
type Poller struct {
	Manager manager.ExecutionManager
	Runner  *Runner
	Machine string
}

// NewPoller returns a poller for GitHub Actions stages.
func NewPoller(execManager manager.ExecutionManager, runner *Runner, machine string) *Poller {
	return &Poller{
		Manager: execManager,
		Runner:  runner,
		Machine: machine,
	}
}

// Poll launches n workers that each poll the queue for gha stages until the
// context is cancelled. It blocks until all workers returned.
func (p *Poller) Poll(ctx context.Context, n int) {
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					p.pollOnce(ctx, i)
				}
			}
		}()
	}
	wg.Wait()
}

// pollOnce requests, accepts and runs a single stage, recovering from any
// panic so a bad workflow cannot take the worker down.
func (p *Poller) pollOnce(ctx context.Context, worker int) {
	defer func() {
		if r := recover(); r != nil {
			log.Ctx(ctx).Error().
				Int("worker", worker).
				Msgf("gha: recovered from panic: %s, stack: %s", r, debug.Stack())
		}
	}()

	stage, err := p.Manager.Request(ctx, &manager.Request{
		Kind: "pipeline",
		Type: gha.StageType,
	})
	if err != nil {
		if ctx.Err() == nil {
			log.Ctx(ctx).Warn().Err(err).Msg("gha: cannot request stage")
		}
		return
	}
	if stage == nil || stage.ID == 0 {
		return
	}

	machine := p.Machine
	if machine == "" {
		machine = fmt.Sprintf("gha-worker-%d", worker)
	}
	if _, err := p.Manager.Accept(ctx, stage.ID, machine); err != nil {
		// the stage was picked up by another worker; move on.
		log.Ctx(ctx).Debug().Err(err).Int64("stage.id", stage.ID).Msg("gha: stage not accepted")
		return
	}

	if err := p.Runner.Run(ctx, stage.ID); err != nil {
		log.Ctx(ctx).Error().Err(err).Int64("stage.id", stage.ID).Msg("gha: stage execution failed")
	}
}
