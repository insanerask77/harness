/*
 * Copyright 2023 Harness, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

export enum YamlVersion {
  V0,
  V1,
  GITHUB_ACTIONS
}

export const DEFAULT_YAML_PATH_PREFIX = '.harness/'
export const DEFAULT_YAML_PATH_SUFFIX = '.yaml'

export const DRONE_CONFIG_YAML_FILE_SUFFIXES = ['.drone.yml', '.drone.yaml']

// GitHub Actions workflows live under .github/workflows/ and are executed
// by the embedded act engine on the backend.
export const GHA_YAML_PATH_PREFIX = '.github/workflows/'
export const GHA_YAML_PATH_SUFFIX = '.yml'
export const GHA_CONFIG_YAML_FILE_REGEX = /^\.github\/workflows\/[^/]+\.ya?ml$/i
