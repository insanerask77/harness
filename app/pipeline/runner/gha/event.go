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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/harness/gitness/app/pipeline/gha"
	"github.com/harness/gitness/types"
)

var pullRefRegex = regexp.MustCompile(`^refs/pull/(\d+)/`)

// writeEventFile synthesizes a GitHub webhook-style event payload from the
// execution metadata and writes it into the workspace, so workflows see the
// github.event context they expect. Returns the file path and event name.
func writeEventFile(
	dir string,
	repo *types.Repository,
	execution *types.Execution,
) (string, string, error) {
	eventName := gha.MapTriggerEvent(execution.Event)

	repository := map[string]any{
		"name":           repo.Identifier,
		"full_name":      repo.Path,
		"html_url":       repo.GitURL,
		"clone_url":      repo.GitURL,
		"default_branch": repo.DefaultBranch,
	}

	var payload map[string]any
	switch eventName {
	case gha.EventPullRequest:
		number := pullReqNumber(execution.Ref)
		payload = map[string]any{
			"action": "synchronize",
			"number": number,
			"pull_request": map[string]any{
				"number": number,
				"title":  execution.Title,
				"head": map[string]any{
					"ref": execution.Source,
					"sha": execution.After,
				},
				"base": map[string]any{
					"ref": execution.Target,
					"sha": execution.Before,
				},
			},
			"repository": repository,
		}
	case gha.EventWorkflowDispatch:
		payload = map[string]any{
			"ref":        execution.Ref,
			"inputs":     map[string]any{},
			"repository": repository,
		}
	default: // push, schedule and anything else uses push-like metadata.
		payload = map[string]any{
			"ref":    execution.Ref,
			"before": execution.Before,
			"after":  execution.After,
			"head_commit": map[string]any{
				"id":      execution.After,
				"message": execution.Message,
				"url":     execution.Link,
				"author": map[string]any{
					"name":  execution.AuthorName,
					"email": execution.AuthorEmail,
				},
			},
			"pusher": map[string]any{
				"name":  execution.Author,
				"email": execution.AuthorEmail,
			},
			"repository": repository,
		}
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal event payload: %w", err)
	}
	path := filepath.Join(dir, ".gitness-event.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", "", fmt.Errorf("failed to write event payload: %w", err)
	}
	return path, eventName, nil
}

// pullReqNumber extracts the pull request number from refs like
// refs/pull/42/head; zero when the ref has another shape.
func pullReqNumber(ref string) int {
	m := pullRefRegex.FindStringSubmatch(ref)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}
