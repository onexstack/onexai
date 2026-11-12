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

package model

import (
	"testing"

	"github.com/onexstack/onexai/pkg/dbschema"
)

// TestTableNamesResolvePerDialect pins the job tables in both dialects. They
// are shared with oai-chat-apiserver (which reuses these models for its /job and
// /cronjob commands), so a drift here breaks two services at once.
func TestTableNamesResolvePerDialect(t *testing.T) {
	tables := map[string]func() string{
		"job":     TableNameJobM,
		"cronjob": TableNameCronJobM,
	}

	t.Cleanup(func() { dbschema.Configure(dbschema.MySQL) })

	dbschema.Configure(dbschema.MySQL)
	for what, table := range tables {
		if got, want := table(), "onexai_jobserver_"+what; got != want {
			t.Errorf("%s table name on mysql = %q, want %q", what, got, want)
		}
	}

	dbschema.Configure(dbschema.Postgres)
	for what, table := range tables {
		if got, want := table(), "job."+what; got != want {
			t.Errorf("%s table name on postgres = %q, want %q", what, got, want)
		}
	}
}
