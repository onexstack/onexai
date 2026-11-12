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

package authz

import (
	"context"
	"reflect"
	"testing"
)

func TestContextWithSubjectRoundTrips(t *testing.T) {
	want := Subject{Kind: SubjectKindUser, UserID: "u_alice", UserName: "Alice"}

	got, ok := SubjectFromContext(ContextWithSubject(context.Background(), want))
	if !ok {
		t.Fatal("expected SubjectFromContext to report true for an attached subject")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestSubjectFromContextReportsMissingIdentity(t *testing.T) {
	got, ok := SubjectFromContext(context.Background())
	if ok {
		t.Fatal("expected SubjectFromContext to report false when nothing was attached")
	}
	if got.Kind != "" {
		t.Fatalf("got %+v, want a zero-value Subject", got)
	}
}

func TestSubjectFromUserBuildsAUserSubject(t *testing.T) {
	got := SubjectFromUser("u_alice", "Alice")
	want := Subject{Kind: SubjectKindUser, UserID: "u_alice", UserName: "Alice"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if got.PrimaryID() != "u_alice" {
		t.Fatalf("got PrimaryID %q, want u_alice", got.PrimaryID())
	}
}

func TestSubjectFromServiceBuildsAServiceAccountSubject(t *testing.T) {
	got := SubjectFromService("oai-chat-apiserver")
	if got.Kind != SubjectKindServiceAccount || got.ServiceName != "oai-chat-apiserver" {
		t.Fatalf("got %+v, want a serviceaccount subject named oai-chat-apiserver", got)
	}
	if got.PrimaryID() != "oai-chat-apiserver" {
		t.Fatalf("got PrimaryID %q, want oai-chat-apiserver", got.PrimaryID())
	}
}
