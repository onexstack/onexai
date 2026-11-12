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

package agent

import (
	"strings"
	"testing"

	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// step is one report of a tool invocation, the shape the engine streams.
func step(id, name, status string) agentapi.ToolStep {
	return agentapi.ToolStep{ID: id, Name: name, Status: status}
}

// TestToolTrailFoldsTheReportsOfOneCall pins the merge rule: the backend reports
// an invocation when it starts and again when it ends, under one call id, and
// the record has to hold one row — the later one, which carries the status the
// call actually ended in.
func TestToolTrailFoldsTheReportsOfOneCall(t *testing.T) {
	trail := newToolTrail(nil)
	trail.observe([]agentapi.ToolStep{step("call_1", "pipeline", "pending")})
	trail.observe([]agentapi.ToolStep{step("call_1", "pipeline", "ok")})

	got := trail.record()
	if len(got) != 1 {
		t.Fatalf("record = %v, want one row", got)
	}
	if got[0] != "pipeline(ok)" {
		t.Errorf("row = %q, want the terminal report to have replaced the pending one", got[0])
	}
}

// TestToolTrailKeepsTheOrderTheToolsRan covers why the record is not a map:
// iteration order would shuffle the rows between turns, and a record that
// reorders itself reads as a different sequence of events.
func TestToolTrailKeepsTheOrderTheToolsRan(t *testing.T) {
	trail := newToolTrail(nil)
	trail.observe([]agentapi.ToolStep{
		step("call_1", "Read", "ok"),
		step("call_2", "pipeline", "ok"),
		step("call_3", "Bash", "fail"),
	})

	got := trail.record()
	want := []string{"Read(ok)", "pipeline(ok)", "Bash(fail)"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("record = %v, want %v in the order the calls arrived", got, want)
	}
}

// TestToolTrailCarriesWhatRanBeforeAPause is the reason the record is stored
// with the suspended run. A resume replays none of the tools that already ran,
// so a record taken from the resumed half alone would describe half the turn and
// be indistinguishable from a whole one.
func TestToolTrailCarriesWhatRanBeforeAPause(t *testing.T) {
	before := newToolTrail(nil)
	before.observe([]agentapi.ToolStep{step("call_1", "pipeline", "ok")})

	// The run pauses, is answered, and continues on what it recorded.
	resumed := newToolTrail(before.record())
	resumed.observe([]agentapi.ToolStep{step("call_2", "Read", "ok")})

	got := resumed.record()
	want := []string{"pipeline(ok)", "Read(ok)"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("record = %v, want %v — the resumed run must not lose the calls made before it paused", got, want)
	}
}

// TestToolTrailKeepsStepsWithNoCallID pins the fallback for a backend that names
// no call: there is nothing to match a later report on, so each report is a row
// of its own. The local CLI backend reports steps this way.
func TestToolTrailKeepsStepsWithNoCallID(t *testing.T) {
	trail := newToolTrail(nil)
	trail.observe([]agentapi.ToolStep{
		step("", "exec", "pending"),
		step("", "exec", "ok"),
		step("", "exec", "fail"),
	})

	if got := trail.record(); len(got) != 3 {
		t.Errorf("record = %v, want each unnamed report kept", got)
	}
}

// TestRenderToolTrailSaysSoWhenNothingWasCalled is the case the whole record
// exists for: a run that called nothing and described its own success. The
// absence has to be written down, because a record kept only when there was
// something to report leaves its absence ambiguous — and an unrecorded turn and
// an empty one would look the same.
func TestRenderToolTrailSaysSoWhenNothingWasCalled(t *testing.T) {
	got := renderToolTrail(nil)
	if !strings.Contains(got, "没有调用任何工具") {
		t.Errorf("renderToolTrail(nil) = %q, want it to state that nothing was called", got)
	}
	if !strings.Contains(got, toolTrailMarker) {
		t.Errorf("renderToolTrail(nil) = %q, want the system-generated marker", got)
	}
}

// TestRenderToolTrailListsTheCalls pins the rendered form: a marker that
// identifies it as system-generated, one row per call, and no trailing blank
// line — the annotation sits above the next user turn, and a stray newline there
// is the sort of thing that gets copied into an answer.
func TestRenderToolTrailListsTheCalls(t *testing.T) {
	got := renderToolTrail([]string{"pipeline(ok)", "Read(fail)"})

	if !strings.HasPrefix(got, toolTrailMarker) {
		t.Errorf("renderToolTrail = %q, want it to lead with the marker", got)
	}
	for _, row := range []string{"pipeline(ok)", "Read(fail)"} {
		if !strings.Contains(got, "- "+row) {
			t.Errorf("renderToolTrail = %q, want a row for %q", got, row)
		}
	}
	if strings.HasSuffix(got, "\n") {
		t.Errorf("renderToolTrail = %q, want no trailing newline", got)
	}
}
