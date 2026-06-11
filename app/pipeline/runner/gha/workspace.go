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
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/harness/gitness/app/pipeline/manager"
)

// prepareWorkspace clones the repository at the given commit into a fresh
// temporary directory. act copies this directory into the job container
// (BindWorkdir=false), which keeps actions/checkout a no-op the same way it
// works for local act runs.
//
// The caller must remove the returned directory when done.
func prepareWorkspace(
	ctx context.Context,
	root string,
	cloneURL string,
	sha string,
	netrc *manager.Netrc,
) (string, error) {
	if root == "" {
		root = os.TempDir()
	}
	dir, err := os.MkdirTemp(root, "gitness-gha-*")
	if err != nil {
		return "", fmt.Errorf("failed to create workspace dir: %w", err)
	}

	authURL, err := injectCredentials(cloneURL, netrc)
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}

	// shallow fetch of the single commit; credentials live only in the
	// remote URL of the throwaway clone and are never logged.
	cmds := [][]string{
		{"init", "--quiet", "."},
		{"remote", "add", "origin", authURL},
		{"fetch", "--quiet", "--depth=1", "origin", sha},
		{"checkout", "--quiet", "--force", sha},
	}
	for _, args := range cmds {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			os.RemoveAll(dir)
			return "", fmt.Errorf("git %s failed: %s: %w", args[0], sanitize(string(out), netrc), err)
		}
	}
	return dir, nil
}

// injectCredentials returns the clone URL with the netrc credentials
// embedded as userinfo.
func injectCredentials(cloneURL string, netrc *manager.Netrc) (string, error) {
	u, err := url.Parse(cloneURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse clone url: %w", err)
	}
	if netrc != nil && netrc.Login != "" {
		u.User = url.UserPassword(netrc.Login, netrc.Password)
	}
	return u.String(), nil
}

// sanitize strips credentials from git output before it lands in errors.
func sanitize(out string, netrc *manager.Netrc) string {
	if netrc != nil && netrc.Password != "" {
		out = strings.ReplaceAll(out, netrc.Password, "******")
	}
	return strings.TrimSpace(out)
}
