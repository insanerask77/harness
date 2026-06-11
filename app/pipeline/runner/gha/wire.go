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
	"github.com/harness/gitness/app/pipeline/manager"
	"github.com/harness/gitness/encrypt"
	"github.com/harness/gitness/types"

	"github.com/google/wire"
)

// WireSet provides a wire set for this package.
var WireSet = wire.NewSet(
	ProvideGHARunner,
	ProvideGHAPoller,
)

// ProvideGHARunner provides the executor for GitHub Actions stages.
func ProvideGHARunner(
	config *types.Config,
	execManager manager.ExecutionManager,
	encrypter encrypt.Encrypter,
) *Runner {
	return NewRunner(config, execManager, encrypter)
}

// ProvideGHAPoller provides the poller for GitHub Actions stages.
func ProvideGHAPoller(
	config *types.Config,
	execManager manager.ExecutionManager,
	runner *Runner,
) *Poller {
	return NewPoller(execManager, runner, config.InstanceID)
}
