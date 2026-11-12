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

package a2aclient

import "testing"

func TestParseCardV03(t *testing.T) {
	raw := []byte(`{
	  "protocolVersion":"0.3.0",
	  "name":"k8s-triage",
	  "description":"triages alerts",
	  "url":"https://agent.example.com/a2a",
	  "preferredTransport":"JSONRPC",
	  "version":"1.2.3",
	  "capabilities":{"streaming":true},
	  "defaultInputModes":["text/plain"],
	  "defaultOutputModes":["text/plain"],
	  "skills":[{"id":"triage","name":"Triage","description":"triage an alert","tags":["k8s"]}]
	}`)
	card, err := ParseCard(raw)
	if err != nil {
		t.Fatalf("ParseCard: %v", err)
	}
	if card.ProtocolVersion != ProtocolVersion03 {
		t.Fatalf("protocol = %q, want %q", card.ProtocolVersion, ProtocolVersion03)
	}
	if card.Name != "k8s-triage" || card.Version != "1.2.3" {
		t.Fatalf("unexpected card: %+v", card)
	}
	if got := card.Transport(); got != TransportJSONRPC {
		t.Fatalf("transport = %q", got)
	}
	if got := card.Endpoint(); got != "https://agent.example.com/a2a" {
		t.Fatalf("endpoint = %q", got)
	}
	if len(card.Skills) != 1 || card.Skills[0].ID != "triage" {
		t.Fatalf("skills = %+v", card.Skills)
	}
	if !card.Capabilities.Streaming {
		t.Fatal("streaming capability was lost")
	}
	if card.Digest == "" {
		t.Fatal("digest is empty")
	}
}

func TestParseCardV10(t *testing.T) {
	raw := []byte(`{
	  "name":"onexai",
	  "description":"onexai agent",
	  "version":"0.0.1",
	  "supportedInterfaces":[
	    {"url":"https://onex.example.com/v1/chat/a2a","protocolBinding":"JSONRPC","protocolVersion":"1.0"},
	    {"url":"https://onex.example.com/v1/chat/a2a/rest","protocolBinding":"HTTP+JSON","protocolVersion":"1.0"}
	  ],
	  "capabilities":{"streaming":true,"extendedAgentCard":true},
	  "defaultInputModes":["text/plain"],
	  "defaultOutputModes":["text/plain"],
	  "skills":[]
	}`)
	card, err := ParseCard(raw)
	if err != nil {
		t.Fatalf("ParseCard: %v", err)
	}
	if card.ProtocolVersion != ProtocolVersion10 {
		t.Fatalf("protocol = %q, want %q", card.ProtocolVersion, ProtocolVersion10)
	}
	if len(card.Interfaces) != 2 {
		t.Fatalf("interfaces = %d", len(card.Interfaces))
	}
	if got := card.Transport(); got != TransportJSONRPC {
		t.Fatalf("transport = %q", got)
	}
	if !card.Capabilities.ExtendedAgentCard {
		t.Fatal("extendedAgentCard was lost")
	}
}

func TestParseCardRejectsEmpty(t *testing.T) {
	if _, err := ParseCard([]byte(`{}`)); err == nil {
		t.Fatal("expected an error")
	}
}

func TestResolveRelativeInterface(t *testing.T) {
	card := &Card{Interfaces: []Interface{{URL: "/v1/chat/a2a", Transport: TransportJSONRPC}}}
	card.ResolveRelative("http://127.0.0.1:9181/.well-known/agent-card.json")
	if got := card.Endpoint(); got != "http://127.0.0.1:9181/v1/chat/a2a" {
		t.Fatalf("endpoint = %q", got)
	}
}

func TestCandidateCardURLsPrefersOriginRoot(t *testing.T) {
	got := CandidateCardURLs("https://host.example.com/v1/chat/a2a", "")
	want := "https://host.example.com/.well-known/agent-card.json"
	if len(got) == 0 || got[0] != want {
		t.Fatalf("candidates = %v, want first %q", got, want)
	}
}
