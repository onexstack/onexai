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

package agentstream

import (
	"context"
	"testing"

	agent "github.com/onexstack/onexai/pkg/agentapi"
	"github.com/onexstack/onexai/pkg/media"
)

// usageSink records only what the dedupe tests care about.
type usageSink struct {
	usage []*agent.Usage
	done  int
}

func (s *usageSink) Progress(context.Context, []agent.ToolStep) error    { return nil }
func (s *usageSink) Reasoning(context.Context, string) error             { return nil }
func (s *usageSink) Answer(context.Context, string) error                { return nil }
func (s *usageSink) Interrupted(context.Context, *agent.Interrupt) error { return nil }
func (s *usageSink) Image(context.Context, []media.Ref) error            { return nil }
func (s *usageSink) Sources(context.Context, []agent.SourceRef) error    { return nil }
func (s *usageSink) Failed(context.Context, error) error                 { return nil }
func (s *usageSink) Stopped(context.Context) error                       { return nil }
func (s *usageSink) Suspended(context.Context) error                     { return nil }
func (s *usageSink) Done(context.Context, string) error                  { s.done++; return nil }

func (s *usageSink) Usage(_ context.Context, u *agent.Usage) error {
	s.usage = append(s.usage, u)
	return nil
}

func pumpEvents(t *testing.T, events []agent.Event, sink StreamSink) {
	t.Helper()
	ch := make(chan agent.Event, len(events))
	for _, ev := range events {
		ch <- ev
	}
	close(ch)
	if err := Pump(context.Background(), ch, sink); err != nil {
		t.Fatalf("Pump: %v", err)
	}
}

// The ordinary shape: the engine reports the run's total on its own Usage
// event, then Done carries the same total. Forwarding both made every surface
// render the numbers twice.
func TestPumpDoesNotRepeatTheFinalUsage(t *testing.T) {
	u := &agent.Usage{PromptTokens: 4, CompletionTokens: 2, TotalTokens: 6, Calls: 1}
	sink := &usageSink{}
	pumpEvents(t, []agent.Event{
		{Type: agent.EventUsage, Usage: u},
		{Type: agent.EventDone, Final: "ok", Usage: u},
	}, sink)

	if len(sink.usage) != 1 {
		t.Fatalf("usage delivered %d times, want 1", len(sink.usage))
	}
	if sink.done != 1 {
		t.Fatalf("Done called %d times, want 1", sink.done)
	}
}

// The fallback the duplicate existed for: an engine that only reports usage on
// Done still gets it delivered.
func TestPumpDeliversUsageCarriedByDone(t *testing.T) {
	u := &agent.Usage{PromptTokens: 4, CompletionTokens: 2, TotalTokens: 6, Calls: 1}
	sink := &usageSink{}
	pumpEvents(t, []agent.Event{{Type: agent.EventDone, Final: "ok", Usage: u}}, sink)

	if len(sink.usage) != 1 {
		t.Fatalf("usage delivered %d times, want 1", len(sink.usage))
	}
}

// A Done total that differs from the last reported one is new information, not
// a repeat, and must be delivered.
func TestPumpDeliversAChangedFinalUsage(t *testing.T) {
	first := &agent.Usage{PromptTokens: 4, CompletionTokens: 2, TotalTokens: 6, Calls: 1}
	last := &agent.Usage{PromptTokens: 4, CompletionTokens: 9, TotalTokens: 13, Calls: 2}
	sink := &usageSink{}
	pumpEvents(t, []agent.Event{
		{Type: agent.EventUsage, Usage: first},
		{Type: agent.EventDone, Final: "ok", Usage: last},
	}, sink)

	if len(sink.usage) != 2 {
		t.Fatalf("usage delivered %d times, want 2", len(sink.usage))
	}
	if sink.usage[1] != last {
		t.Error("the changed total was not the one delivered last")
	}
}
