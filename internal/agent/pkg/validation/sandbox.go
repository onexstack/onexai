// Copyright 2026 Lingfei Kong <colin404@foxmail.com>
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

package validation

import (
	"context"
	"strings"

	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// The sandbox requests fill Owner from the generated Default(); these methods
// check the identifiers and the command/path that the sandbox service needs.
// Negative ttlSeconds/timeoutSeconds are not rejected: the service interprets
// any non-positive value as "use the default".

// ValidateCreateSandboxRequest validates a sandbox creation request.
func (v *Validator) ValidateCreateSandboxRequest(_ context.Context, rq *v1.CreateSandboxRequest) error {
	return nil
}

// ValidateGetSandboxRequest validates a sandbox read.
func (v *Validator) ValidateGetSandboxRequest(_ context.Context, rq *v1.GetSandboxRequest) error {
	return requireSandboxID(rq.GetId())
}

// ValidateDeleteSandboxRequest validates a sandbox deletion.
func (v *Validator) ValidateDeleteSandboxRequest(_ context.Context, rq *v1.DeleteSandboxRequest) error {
	return requireSandboxID(rq.GetId())
}

// ValidateExecSandboxRequest validates a command execution.
func (v *Validator) ValidateExecSandboxRequest(_ context.Context, rq *v1.ExecSandboxRequest) error {
	if err := requireSandboxID(rq.GetId()); err != nil {
		return err
	}

	if strings.TrimSpace(rq.GetCommand()) == "" {
		return errno.ErrInvalidArgument.WithMessage("command is required")
	}

	return nil
}

// ValidateReadSandboxFileRequest validates a file read.
func (v *Validator) ValidateReadSandboxFileRequest(_ context.Context, rq *v1.ReadSandboxFileRequest) error {
	return requireSandboxPath(rq.GetId(), rq.GetPath())
}

// ValidateWriteSandboxFileRequest validates a file write.
func (v *Validator) ValidateWriteSandboxFileRequest(_ context.Context, rq *v1.WriteSandboxFileRequest) error {
	return requireSandboxPath(rq.GetId(), rq.GetPath())
}

func requireSandboxID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errno.ErrInvalidArgument.WithMessage("sandbox id is required")
	}

	return nil
}

func requireSandboxPath(id, path string) error {
	if err := requireSandboxID(id); err != nil {
		return err
	}

	if strings.TrimSpace(path) == "" {
		return errno.ErrInvalidArgument.WithMessage("path is required")
	}

	return nil
}
