// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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
	"testing"

	"k8s.io/apimachinery/pkg/util/validation/field"

	chat "github.com/onexstack/onexai/pkg/apis/chat"
)

func TestValidateCommandRequiresGroup(t *testing.T) {
	errs := ValidateCommand(&chat.CommandSpec{}, field.NewPath("spec"))
	if len(errs) == 0 {
		t.Fatal("a command without a group must be rejected")
	}
}

func TestValidateCommandAcceptsTheCatalogueShape(t *testing.T) {
	spec := &chat.CommandSpec{
		Group:   "builtin-agent",
		Aliases: []string{"chat-ai"},
		Usage:   "<service>",
		Source:  "builtin",
	}
	if errs := ValidateCommand(spec, field.NewPath("spec")); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
}

func TestValidateCommandRejectsBadAlias(t *testing.T) {
	spec := &chat.CommandSpec{Group: "core", Aliases: []string{"not a name"}}
	if errs := ValidateCommand(spec, field.NewPath("spec")); len(errs) == 0 {
		t.Fatal("an alias that is not a DNS label must be rejected")
	}
}
