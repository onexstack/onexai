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

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/pkg/larkkit"
	"github.com/onexstack/onexai/pkg/dbschema"
)

// target is one column that holds a person, and may still hold their open_id.
type target struct {
	table  string
	column string
	// where narrows the rows in scope. A private-chat session is keyed by the
	// person it belongs to; a group session is keyed by its chat id, which is
	// not a person and must not be rewritten.
	where string
	// jsonPeople marks a column holding a JSON array of Watcher objects
	// ({user_id, user_name, open_id}), where the ids are inside the values.
	jsonPeople bool
}

func (t target) name() string { return t.table + "." + t.column }

// targets returns every column the identity move has to walk. It is derived
// from the writers rather than guessed: each entry is a place that stored, or
// still stores, a Lark id for a person.
//
// It is a function, not a slice literal, because the physical table names
// depend on the dialect the process configured (see pkg/dbschema).
func targets() []target {
	return []target{
		// Written by the bot for every archived message; historically the sender's
		// open_id, because that is what the opsassist import put there.
		{table: dbschema.Table(dbschema.Chat, "chat_message"), column: "sender_id"},
		// A private-chat session is keyed by the person, and was keyed by their
		// open_id — a key no lookup can find, since a message now resolves to a
		// user_id. chat_id repeats the same value.
		{table: dbschema.Table(dbschema.Chat, "session"), column: "session_id", where: "session_type = 'private_chat'"},
		{table: dbschema.Table(dbschema.Chat, "session"), column: "chat_id", where: "session_type = 'private_chat'"},
		// Tickets: the scalar reporter/assignee columns the opsassist import
		// filled from Bitable person fields. The two JSON watcher lists that
		// used to sit beside them (assignees, follower_list) are gone: nothing
		// ever read them, and a ticket's handling log is now chat.ticket_note.
		{table: dbschema.Table(dbschema.Chat, "ticket"), column: "reporter_id"},
		{table: dbschema.Table(dbschema.Chat, "ticket"), column: "assignee_id"},
		// Job and CronJob records are owned by the caller identity that created
		// them (cmdutil.ResolveUsername), so a record made from Lark is owned by
		// whatever that sent.
		{table: dbschema.Table(dbschema.JobServer, "job"), column: "username"},
		{table: dbschema.Table(dbschema.JobServer, "cronjob"), column: "username"},
	}
}

func targetNames() []string {
	all := targets()
	out := make([]string, 0, len(all))
	for _, t := range all {
		out = append(out, t.name())
	}
	return out
}

// watcher mirrors the JSON shape stored in the ticket lists. user_id is the
// identity; open_id is the pre-migration shape and is what this tool moves.
type watcher struct {
	UserID   string `json:"user_id,omitempty"`
	UserName string `json:"user_name,omitempty"`
	OpenID   string `json:"open_id,omitempty"`
}

// backfill walks the targets, resolves each open_id, and (when apply is set)
// rewrites it.
type backfill struct {
	db       *gorm.DB
	resolver *larkkit.IdentityResolver
	apply    bool
	limit    int
}

// plan is what one column turned out to need.
type plan struct {
	target target
	// values maps each open_id found in this column to the number of rows
	// holding it. For a JSON column it counts occurrences inside the values.
	values map[string]int
	// edges are the rows of a JSON column, by primary key.
	edges []jsonRow
	// unresolved are the ids this column holds that Lark could not translate.
	unresolved map[string]int
	// skipped counts values the tool declined to touch (malformed JSON, or an
	// id that is not a Lark user id at all).
	skipped map[string]int
	// rewritable and blocking are row counts, filled in once the ids have been
	// resolved: rows this run can fix, and rows it cannot.
	rewritable int
	blocking   int
	// written counts rows the UPDATEs changed; failed counts statements that
	// errored.
	written int
	failed  int
}

// rows is how many rows the column holds that carry an open_id at all.
func (p *plan) rows() int {
	if p.target.jsonPeople {
		return len(p.edges)
	}
	return sum(p.values)
}

// countRows splits the column's rows into what this run can rewrite and what it
// cannot, using the resolutions already performed. A JSON row counts as
// blocking when any one of the people in it is unresolvable — the whole value
// is left alone then, because a half-rewritten list is worse than an old one.
func (p *plan) countRows(resolved func(string) string) {
	p.rewritable, p.blocking = 0, 0

	if !p.target.jsonPeople {
		for id, n := range p.values {
			if resolved(id) != "" {
				p.rewritable += n
			} else {
				p.blocking += n
			}
		}
		return
	}

	for _, row := range p.edges {
		ws, ok := decodeWatchers(row.value)
		if !ok {
			p.blocking++
			continue
		}
		blocked := false
		for _, w := range ws {
			if isOpenID(w.OpenID) && resolved(w.OpenID) == "" {
				blocked = true
				break
			}
		}
		if blocked {
			p.blocking++
		} else {
			p.rewritable++
		}
	}
}

type jsonRow struct {
	id    int64
	value string
}

// report is the whole run, in column order.
type report struct {
	plans          []*plan
	rowsRewritten  int
	rowsUnresolved int
	mapping        map[string]string // open_id -> user_id, for the sample
	calls          int               // contact API calls made
}

func (r *report) print(w io.Writer) {
	fmt.Fprintf(w, "\n%-40s %8s %8s %11s\n", "column", "ids", "rows", "rewritable")
	fmt.Fprintln(w, strings.Repeat("-", 70))
	for _, p := range r.plans {
		fmt.Fprintf(w, "%-40s %8d %8d %11d\n", p.target.name(), len(p.values), p.rows(), p.rewritable)
		if n := len(p.skipped); n > 0 {
			fmt.Fprintf(w, "%-40s   %d value(s) skipped (not a watcher list)\n", "", n)
		}
		if p.failed > 0 {
			fmt.Fprintf(w, "%-40s   %d statement(s) failed\n", "", p.failed)
		}
	}

	if len(r.mapping) > 0 {
		fmt.Fprintf(w, "\nsample of the mapping (of %d resolved):\n", len(r.mapping))
		keys := make([]string, 0, len(r.mapping))
		for k := range r.mapping {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i, k := range keys {
			if i == 5 {
				fmt.Fprintf(w, "  … and %d more\n", len(keys)-i)
				break
			}
			fmt.Fprintf(w, "  %s -> %s\n", k, r.mapping[k])
		}
	}

	r.printUnresolved(w)
}

// printUnresolved lists the ids that could not be translated, with how many
// rows each one is holding back. They are the run's residual: the rows keep
// their open_id, and the reason is almost always one of two — the person left
// the tenant, or the app may not read user IDs.
func (r *report) printUnresolved(w io.Writer) {
	merged := map[string]int{}
	for _, p := range r.plans {
		for id, n := range p.unresolved {
			merged[id] += n
		}
	}
	if len(merged) == 0 {
		return
	}

	ids := make([]string, 0, len(merged))
	for id := range merged {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	fmt.Fprintf(w, "\nunresolved (%d ids) — left as-is; check that the app may read user IDs:\n", len(ids))
	for _, id := range ids {
		fmt.Fprintf(w, "  %s (%d occurrences)\n", id, merged[id])
	}
}

func (b *backfill) run(ctx context.Context, selected []target) (*report, error) {
	rep := &report{mapping: map[string]string{}}

	for _, t := range selected {
		p, err := b.plan(ctx, t)
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", t.name(), err)
		}
		rep.plans = append(rep.plans, p)
	}

	// Resolve everything once: the same open_id appears in several columns, and
	// the contact API is rate-limited.
	for _, p := range rep.plans {
		for id := range p.values {
			if _, err := b.resolver.UserID(ctx, id); err != nil {
				p.unresolved[id] = p.values[id]
				continue
			}
			rep.mapping[id] = b.resolver.Cached(id)
		}
		p.countRows(b.resolver.Cached)
	}
	rep.calls = b.resolver.Calls()

	if !b.apply {
		for _, p := range rep.plans {
			rep.rowsRewritten += p.rewritable
			rep.rowsUnresolved += p.blocking
		}
		return rep, nil
	}

	for _, p := range rep.plans {
		if err := b.write(ctx, p); err != nil {
			return nil, fmt.Errorf("rewrite %s: %w", p.target.name(), err)
		}
		rep.rowsRewritten += p.written
		rep.rowsUnresolved += p.blocking
	}
	return rep, nil
}

// plan finds every open_id one column still holds, keeping the row counts so
// the dry run can say how much a rewrite would touch.
func (b *backfill) plan(ctx context.Context, t target) (*plan, error) {
	p := &plan{
		target:     t,
		values:     map[string]int{},
		unresolved: map[string]int{},
		skipped:    map[string]int{},
	}

	if t.jsonPeople {
		return p, b.planJSON(ctx, t, p)
	}

	query := fmt.Sprintf(
		"SELECT `%s` AS v, COUNT(*) AS n FROM `%s` WHERE `%s` LIKE 'ou!_%%' ESCAPE '!'",
		t.column, t.table, t.column)
	if t.where != "" {
		query += " AND " + t.where
	}
	query += fmt.Sprintf(" GROUP BY `%s` ORDER BY n DESC", t.column)
	if b.limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", b.limit)
	}

	var rows []struct {
		V string
		N int
	}
	if err := b.db.WithContext(ctx).Raw(query).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		p.values[r.V] = r.N
	}
	return p, nil
}

// planJSON reads the rows holding a watcher list and records which of them
// carry an open_id inside the JSON.
func (b *backfill) planJSON(ctx context.Context, t target, p *plan) error {
	query := fmt.Sprintf(
		"SELECT id, `%s` AS v FROM `%s` WHERE `%s` LIKE '%%ou!_%%' ESCAPE '!'",
		t.column, t.table, t.column)
	if t.where != "" {
		query += " AND " + t.where
	}
	if b.limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", b.limit)
	}

	var rows []struct {
		ID int64
		V  string
	}
	if err := b.db.WithContext(ctx).Raw(query).Scan(&rows).Error; err != nil {
		return err
	}

	for _, r := range rows {
		ws, ok := decodeWatchers(r.V)
		if !ok {
			p.skipped[r.V]++
			continue
		}
		p.edges = append(p.edges, jsonRow{id: r.ID, value: r.V})
		// A row can hold several people; each id counts as one occurrence of the
		// value, which is what the report's row count is standing in for.
		for _, w := range ws {
			if isOpenID(w.OpenID) {
				p.values[w.OpenID]++
			}
		}
	}
	return nil
}

// write performs the rewrites a plan describes. Every statement is independent:
// a row that cannot be written (a unique key that would collide, say) is
// counted and reported rather than aborting the column.
func (b *backfill) write(ctx context.Context, p *plan) error {
	if p.target.jsonPeople {
		return b.writeJSON(ctx, p)
	}

	for old, n := range p.values {
		newID := b.resolver.Cached(old)
		if newID == "" {
			continue
		}
		if old == newID {
			continue
		}

		query := fmt.Sprintf("UPDATE `%s` SET `%s` = ? WHERE `%s` = ?", p.target.table, p.target.column, p.target.column)
		args := []any{newID, old}
		if p.target.where != "" {
			query += " AND " + p.target.where
		}

		res := b.db.WithContext(ctx).Exec(query, args...)
		if res.Error != nil {
			p.failed += n
			continue
		}
		p.written += int(res.RowsAffected)
	}
	return nil
}

func (b *backfill) writeJSON(ctx context.Context, p *plan) error {
	for _, row := range p.edges {
		ws, ok := decodeWatchers(row.value)
		if !ok {
			continue
		}

		// A row with an unresolvable person in it is left whole: a list half in
		// one namespace and half in the other is worse than one that is
		// uniformly stale, and the report lists it as blocking either way.
		blocked := false
		for _, w := range ws {
			if isOpenID(w.OpenID) && b.resolver.Cached(w.OpenID) == "" {
				blocked = true
				break
			}
		}
		if blocked {
			continue
		}

		changed := false
		for i := range ws {
			if !isOpenID(ws[i].OpenID) {
				continue
			}
			newID := b.resolver.Cached(ws[i].OpenID)
			if newID == "" {
				continue
			}
			ws[i].UserID = newID
			ws[i].OpenID = ""
			changed = true
		}
		if !changed {
			continue
		}

		blob, err := json.Marshal(ws)
		if err != nil {
			p.failed++
			continue
		}
		res := b.db.WithContext(ctx).Exec(
			fmt.Sprintf("UPDATE `%s` SET `%s` = ? WHERE id = ?", p.target.table, p.target.column),
			string(blob), row.id)
		if res.Error != nil {
			p.failed++
			continue
		}
		p.written += int(res.RowsAffected)
	}
	return nil
}

// decodeWatchers parses a watcher list. A column that does not hold one is
// reported as skipped rather than rewritten: the ticket lists are round-tripped
// through Bitable, so anything could be in there.
func decodeWatchers(raw string) ([]watcher, bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, false
	}
	var ws []watcher
	if err := json.Unmarshal([]byte(raw), &ws); err != nil || len(ws) == 0 {
		return nil, false
	}
	return ws, true
}

// isOpenID reports whether an id is in Lark's open_id namespace. The prefixes
// are Lark's own, so there is nothing to guess.
func isOpenID(id string) bool {
	return strings.HasPrefix(id, "ou_") || strings.HasPrefix(id, "on_")
}

func sum(m map[string]int) int {
	total := 0
	for _, n := range m {
		total += n
	}
	return total
}
