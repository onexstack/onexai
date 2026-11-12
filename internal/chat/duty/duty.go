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

// Package duty defines the duty/on-call domain shared by the chat commands.
//
// Service is the command-facing capability: the native on-call schedule (the
// same one the console page drives through REST) is its primary implementation.
// Store remains the in-memory legacy Regen fallback and the shape the commands'
// tests use; ticket records are owned by the store layer (store.Ticket), not
// here.
package duty

import (
	"context"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// Sink receives the group set after a successful refresh, so a caller can
// publish what only this store knows — the live roster — somewhere durable.
//
// It is called outside the store's lock and its failures are the sink's own: a
// roster that could not be published is a stale status field, while failing the
// refresh would throw away a roster that was read successfully.
type Sink interface {
	Report(ctx context.Context, groups []Group)
}

// Source loads the duty groups and their on-call watchers from an external
// system. It is what turns the store from an in-memory stub into a view of the
// real on-call platform.
type Source interface {
	// Load returns every known group with its current on-call watchers
	// (primary first). A group with no watcher is returned with an empty list.
	Load(ctx context.Context) ([]Group, error)
}

// rosterErrReporter is implemented by a Source that can say whether its roster
// read failed.
//
// Load deliberately reports the module metadata it could read even when the
// roster backend is unreachable, so a group with no watcher means either
// "nobody is on call" or "the roster could not be read". The duty commands need
// the difference to answer correctly, and only the source knows it. A source
// that cannot tell simply does not implement this, and its empty rosters are
// taken at face value.
type rosterErrReporter interface {
	// RosterError returns why the most recent Load could not read the on-call
	// rosters, or nil when it could.
	RosterError() error
}

// rosterErrorOf reports the roster verdict of src, or nil when src cannot give
// one.
func rosterErrorOf(src Source) error {
	if r, ok := src.(rosterErrReporter); ok {
		return r.RosterError()
	}
	return nil
}

// Group is one duty group as loaded from a Source.
//
// ID is the group id the commands take as an argument (lower-cased
// group name). The remaining metadata comes from the oncall-group table;
// Watchers come from the provider schedule named by ScheduleID.
type Group struct {
	ID   string
	Name string
	// RawName is the unique name column exactly as stored. Schedule lookups
	// need the stored spelling; ID is lower-cased and Name may be a display
	// name, so neither is a safe substitute.
	RawName  string
	Watchers []*v1.Watcher

	// Namespace is the namespace recorded on the group row. It is metadata
	// now: duty groups no longer have a control-plane status object to publish
	// a roster onto.
	Namespace string

	// ParentGroupID names the group a sub-group belongs to. It is only ever
	// set by a legacy source that models parent/child groups; the native group
	// table has no such column, so it stays empty there.
	ParentGroupID string

	// ScheduleID is the provider's on-call schedule backing this group's roster.
	// Empty means no rotation is configured, so the group is listed without a
	// watcher and does not answer /oncall.
	ScheduleID string
	// ShortDesc / LongDesc are used by /oncall --list and, historically, by AI
	// routing; ShortDesc wins when both are set.
	ShortDesc string
	LongDesc  string
	// WelcomeMessage is the group-join announcement, sent as Markdown to the
	// chat when it is created. It is sent verbatim: no placeholder substitution
	// happens anywhere.
	WelcomeMessage string
	// Agents are the oai-agent-apiserver agents attached to this module.
	Agents []string
	// Owner is the group's admin display name.
	Owner string
	// Order controls the display order in listings; ties break by ID.
	Order int
	// AllocationStrategy is the module's on-call strategy, e.g. "primary",
	// "primary_backup", "round_robin", "time_based".
	AllocationStrategy string

	// ScheduleSource says where the roster came from: "native" for a schedule
	// stored by this service, "regen" for a legacy provider schedule, and
	// "none" when no rotation is configured.
	ScheduleSource string
	// PrimaryUntil is when the current primary's shift ends, when the native
	// evaluator knows it.
	PrimaryUntil *time.Time
	// PrimaryIsOverride reports that the primary is covering an override
	// window.
	PrimaryIsOverride bool
	// PrimaryLayerName names the layer that supplied the primary.
	PrimaryLayerName string
}

// Store is an in-memory duty/on-call store. All methods are safe for
// concurrent use.
type Store struct {
	mu sync.RWMutex

	// sink, when set, receives each successfully refreshed group set so the
	// roster can be published to the control plane.
	sink Sink

	// groups maps group id → duty group metadata.
	groups map[string]*v1.OnDutyGroup
	// meta maps lower-cased group id → the richer module metadata loaded from
	// the source (display name, descriptions, welcome message, agents, order).
	// It is the substrate for /duty and /oncall --list; watchers stay in oncall.
	meta map[string]Group
	// oncall maps group id → ordered watcher list (primary first).
	oncall map[string][]*v1.Watcher
	// activeGroups is the set of lower-cased group ids that have watchers.
	activeGroups map[string]struct{}
	// overrides is the set of lower-cased group ids whose watchers were set
	// locally by /assign. Refresh leaves them alone: a local assignment is a
	// deliberate operator action and outranks the source until the process
	// restarts.
	overrides map[string]struct{}

	// source is the external duty data source; nil for a bare in-memory store.
	source Source
	// refreshInterval is how often runRefreshLoop reloads from source while the
	// store is healthy.
	refreshInterval time.Duration

	// loadErr is why the last Refresh failed outright; rosterErr is why its
	// roster read failed while the module metadata still came through. Either
	// one means the store cannot vouch for what it holds, which is both what
	// the commands report and what makes the refresh loop retry sooner.
	loadErr   error
	rosterErr error

	// firstLoadDone is closed by the refresh loop once its first pass has
	// finished, whatever that pass found. The constructor no longer loads, so
	// this is how a test that builds a store waits for the load instead of
	// sleeping and hoping. Nothing in production reads it.
	firstLoadDone chan struct{}
}

// New creates a new empty Store.
func New() *Store {
	return &Store{
		groups:        make(map[string]*v1.OnDutyGroup),
		meta:          make(map[string]Group),
		oncall:        make(map[string][]*v1.Watcher),
		activeGroups:  make(map[string]struct{}),
		overrides:     make(map[string]struct{}),
		firstLoadDone: make(chan struct{}),
	}
}

// NewWithSource creates a Store backed by src and starts the goroutine that
// loads it, once immediately and then every interval until ctx is cancelled.
//
// The first load happens inside that goroutine rather than here, and the
// difference is the whole point: this reader is a roster backend in another
// cluster, and waiting for it used to hold the server's start-up hostage —
// tens of seconds of a bot that was running but not listening, and in one case
// long enough for the launcher to declare it dead. A duty command asked before
// the first pass finishes answers "unavailable", which is the same answer it
// gives for a module it does not know; that is a better first second than a
// bot that is not there at all.
//
// The initial load is deliberately not fatal either: a source that is briefly
// down at start-up leaves the store empty — the same state this service was in
// before the source existed — rather than refusing to start. Contrast
// dnswhitelist.New, which does fail fast: there the whitelist is a security
// boundary and an empty one would mean "allow everything".
func NewWithSource(ctx context.Context, src Source, interval time.Duration) *Store {
	if interval <= 0 {
		// A non-positive interval would make runRefreshLoop spin: it would wait
		// nothing before every reload, and never back off from it.
		interval = refreshRetryInterval
	}

	s := New()
	s.source = src
	s.refreshInterval = interval
	go s.runRefreshLoop(ctx)
	return s
}

// refreshRetryInterval is the first wait runRefreshLoop uses after a refresh
// that came back incomplete, before backing off towards the configured
// interval. It is short because the state it reacts to is usually a blip — a
// roster backend that was not reachable the moment the service started — and
// until it clears the duty commands cannot tell a user who is on call.
const refreshRetryInterval = 30 * time.Second

// runRefreshLoop reloads the store until ctx is cancelled.
//
// The wait between refreshes is the configured interval while the store is
// healthy, and a backoff from refreshRetryInterval towards it while it is not,
// so a failure that leaves the duty commands degraded is repaired in seconds
// rather than at the next ordinary tick.
func (s *Store) runRefreshLoop(ctx context.Context) {
	// wait is the delay before the next refresh; retry is the backoff for
	// consecutive incomplete refreshes, reset by the next complete one.
	//
	// The first pass runs before any wait: the store starts empty, and sitting
	// out an interval — or a retry backoff, which is what "degraded" would say
	// about an empty store — before the first load would leave every duty
	// command answering "unavailable" for that whole tick.
	var retry, wait time.Duration
	for first := true; ; first = false {
		if !first {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}

		err := s.Refresh(ctx)
		if first {
			// Releases anyone waiting for the pass the constructor used to do.
			close(s.firstLoadDone)
		}
		if err != nil {
			slog.WarnContext(ctx, "duty: refresh failed, keeping the previous assignments", "error", err)
		} else if first && !s.HasKnownGroups() {
			// Logged once, on the first pass that succeeded, rather than on
			// every tick: an empty store is a standing condition, and repeating
			// it every interval with no state change is noise. This is the line
			// that says the duty commands are about to answer "unavailable" for
			// every module name.
			slog.WarnContext(ctx, "duty: no duty groups loaded at startup, the duty commands will report the service as unavailable")
		}

		if !s.degraded() {
			retry, wait = 0, s.refreshInterval
			continue
		}
		retry = s.nextRetry(retry)
		wait = retry
	}
}

// nextRetry returns the backoff for one more incomplete refresh: it starts at
// refreshRetryInterval and doubles, never past the configured interval. prev is
// the previous backoff, zero when the last refresh was complete.
func (s *Store) nextRetry(prev time.Duration) time.Duration {
	next := refreshRetryInterval
	if prev > 0 {
		next = 2 * prev
	}
	if next > s.refreshInterval {
		return s.refreshInterval
	}
	return next
}

// Refresh reloads every group and its watchers from the source. Groups whose
// watchers were assigned locally by /assign are left untouched, as are the
// previously read watchers of every group when the rosters could not be read at
// all this pass.
func (s *Store) Refresh(ctx context.Context) error {
	if s.source == nil {
		return nil
	}

	groups, err := s.source.Load(ctx)
	if err != nil {
		// Nothing is replaced, so the previous assignments — and the roster
		// verdict that came with them — still stand. Only the load failure is
		// recorded, and that alone is enough to mark the store degraded.
		s.mu.Lock()
		s.loadErr = err
		s.mu.Unlock()
		return err
	}
	rosterErr := rosterErrorOf(s.source)

	s.mu.Lock()
	s.loadErr = nil
	s.rosterErr = rosterErr

	// A pass that could not read the rosters says nothing about who is on call,
	// so it must not overwrite what the last readable pass found: the watcher
	// lists it reports are unknown, not empty. Keeping the previous ones makes a
	// failure on the path to the roster backend invisible — the duty commands go
	// on answering with the last rotation they did read, which beats both
	// paging nobody and refusing the ticket. A group the store has never seen
	// has nothing to keep and stays inactive, and the next readable pass
	// replaces all of it with the authoritative lists, empty ones included.
	keepRosters := rosterErr != nil

	seen := make(map[string]struct{}, len(groups))
	for _, g := range groups {
		if g.ID == "" {
			continue
		}
		if _, ok := s.overrides[strings.ToLower(g.ID)]; ok {
			continue
		}
		seen[g.ID] = struct{}{}
		s.groups[g.ID] = &v1.OnDutyGroup{
			GroupId:        g.ID,
			GroupName:      g.Name,
			AssignStrategy: AssignStrategyOf(g.AllocationStrategy),
		}
		s.meta[strings.ToLower(g.ID)] = g
		if !keepRosters {
			s.applyOncall(g.ID, g.Watchers)
		}
	}

	// Drop groups the source no longer knows about. Without this a schedule
	// deleted upstream would keep answering /duty list for the life of the
	// process, since the refresh would only ever add.
	for id := range s.groups {
		if _, ok := seen[id]; ok {
			continue
		}
		if _, ok := s.overrides[strings.ToLower(id)]; ok {
			continue
		}
		delete(s.groups, id)
		delete(s.meta, strings.ToLower(id))
		delete(s.oncall, id)
		delete(s.activeGroups, strings.ToLower(id))
	}
	s.mu.Unlock()

	// Publish outside the lock: the sink writes to the API server, and holding
	// the store's lock across that call would stall every duty command.
	if s.sink != nil && !keepRosters {
		s.sink.Report(ctx, groups)
	}
	return nil
}

// RegisterGroup adds or updates a duty group.
func (s *Store) RegisterGroup(group *v1.OnDutyGroup) {
	if group == nil || group.GroupId == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.groups[group.GroupId] = group
	// Seed the metadata entry only when the source did not provide one, so
	// registering an unknown group does not blank a configured module.
	if _, ok := s.meta[strings.ToLower(group.GroupId)]; !ok {
		s.meta[strings.ToLower(group.GroupId)] = Group{
			ID:        group.GroupId,
			Name:      group.GroupName,
			ShortDesc: group.ShortDesc,
			LongDesc:  group.LongDesc,
		}
	}
}

// GetGroup returns a copy of the duty group with the given id, or nil.
func (s *Store) GetGroup(groupID string) *v1.OnDutyGroup {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g := s.groups[groupID]
	if g == nil {
		return nil
	}
	return proto.Clone(g).(*v1.OnDutyGroup)
}

// ListGroup returns all duty groups sorted by group id.
func (s *Store) ListGroup() []*v1.OnDutyGroup {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*v1.OnDutyGroup, 0, len(s.groups))
	for _, g := range s.groups {
		out = append(out, proto.Clone(g).(*v1.OnDutyGroup))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GroupId < out[j].GroupId })
	return out
}

// GroupInfos returns every known group's metadata with its current watchers,
// sorted by (Order, ID). It is the substrate for /duty with no arguments.
func (s *Store) GroupInfos() []Group {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Group, 0, len(s.meta))
	for _, g := range s.meta {
		g.Watchers = cloneWatchers(s.oncall[g.ID])
		out = append(out, g)
	}
	sortGroups(out)
	return out
}

// GetGroupInfo returns the metadata and watchers for a group id
// (case-insensitive). The bool is false when no such group is known.
func (s *Store) GetGroupInfo(groupID string) (Group, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.meta[strings.ToLower(groupID)]
	if !ok {
		return Group{}, false
	}
	g.Watchers = cloneWatchers(s.oncall[g.ID])
	return g, true
}

// ActiveGroupInfos returns the groups that currently have at least one watcher,
// sorted by (Order, ID). It backs /oncall --list and argument-less /duty: a
// module with no rotation is deliberately absent from both.
func (s *Store) ActiveGroupInfos() []Group {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Group, 0, len(s.activeGroups))
	for _, g := range s.meta {
		if _, ok := s.activeGroups[strings.ToLower(g.ID)]; !ok {
			continue
		}
		g.Watchers = cloneWatchers(s.oncall[g.ID])
		out = append(out, g)
	}
	sortGroups(out)
	return out
}

// FilterGroups returns the active groups whose id, name, short description or
// long description matches filter, a case-insensitive regular expression. An
// empty filter returns every active group. The bool reports whether the pattern
// compiled; on a bad pattern the slice is nil and the caller surfaces the
// invalid-regex error, which is what /oncall --list does.
func (s *Store) FilterGroups(filter string) ([]Group, bool) {
	groups := s.ActiveGroupInfos()
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return groups, true
	}
	re, err := regexp.Compile("(?i)" + filter)
	if err != nil {
		return nil, false
	}
	out := make([]Group, 0, len(groups))
	for _, g := range groups {
		if re.MatchString(g.ID) || re.MatchString(g.Name) ||
			re.MatchString(g.ShortDesc) || re.MatchString(g.LongDesc) {
			out = append(out, g)
		}
	}
	return out, true
}

// sortGroups orders groups by their module Order, breaking ties by id so the
// listing is stable rather than map-iteration order.
func sortGroups(groups []Group) {
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Order != groups[j].Order {
			return groups[i].Order < groups[j].Order
		}
		return groups[i].ID < groups[j].ID
	})
}

// SetOncall sets the on-call watcher list for a group (primary first).
//
// This is the operator path (/assign): the group is marked as locally
// overridden so a later Refresh from the source does not undo the assignment.
func (s *Store) SetOncall(groupID string, watchers []*v1.Watcher) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.overrides[strings.ToLower(groupID)] = struct{}{}
	s.applyOncall(groupID, watchers)
}

// applyOncall writes the watcher list for a group without marking it as a local
// override. The caller must hold s.mu.
func (s *Store) applyOncall(groupID string, watchers []*v1.Watcher) {
	s.oncall[groupID] = cloneWatchers(watchers)
	if len(watchers) > 0 {
		s.activeGroups[strings.ToLower(groupID)] = struct{}{}
	} else {
		delete(s.activeGroups, strings.ToLower(groupID))
	}
}

// GetOnDutys returns a copy of the on-call watcher list for a group.
func (s *Store) GetOnDutys(groupID string) []*v1.Watcher {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneWatchers(s.oncall[groupID])
}

// GetPrimaryOncaller returns the primary on-call watcher for a group, or a
// default placeholder if none is assigned.
func (s *Store) GetPrimaryOncaller(groupID string) *v1.Watcher {
	s.mu.RLock()
	watchers := s.oncall[groupID]
	s.mu.RUnlock()
	if len(watchers) == 0 || watchers[0] == nil {
		return &v1.Watcher{UserId: "default_user", UserName: "Default User"}
	}
	return proto.Clone(watchers[0]).(*v1.Watcher)
}

// cloneWatchers deep-copies a watcher slice so callers cannot mutate internal
// state or hold a reference past the lock.
func cloneWatchers(watchers []*v1.Watcher) []*v1.Watcher {
	if watchers == nil {
		return nil
	}
	out := make([]*v1.Watcher, len(watchers))
	for i, w := range watchers {
		if w == nil {
			continue
		}
		out[i] = proto.Clone(w).(*v1.Watcher)
	}
	return out
}

// GetAssignStrategy returns the assignment strategy for a group.
func (s *Store) GetAssignStrategy(groupID string) v1.AssignStrategy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if g, ok := s.groups[groupID]; ok && g.AssignStrategy != v1.AssignStrategy_ASSIGN_STRATEGY_UNSPECIFIED {
		return g.AssignStrategy
	}
	return v1.AssignStrategy_ASSIGN_PRIMARY
}

// ActiveGroupIDs returns the set of lower-cased group ids with watchers.
func (s *Store) ActiveGroupIDs() map[string]struct{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]struct{}, len(s.activeGroups))
	for k := range s.activeGroups {
		out[k] = struct{}{}
	}
	return out
}

// HasKnownGroups reports whether the store holds any duty group at all, whether
// or not it currently has a watcher. It is how the command layer tells "nothing
// was loaded — the data source is unavailable" apart from "the name you typed
// is not one of the groups".
//
// ActiveGroupInfos cannot answer this: with the rotation source unreachable it
// is empty even though the module metadata loaded, and so is HasGroup. A group
// registered only through a local /assign counts too, which is why activeGroups
// is consulted alongside meta.
func (s *Store) HasKnownGroups() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.meta) > 0 || len(s.activeGroups) > 0
}

// RosterError returns why the most recent refresh could not read the on-call
// rosters from the source, or nil when it could.
//
// It is what lets a caller tell a group with no rotation ("nobody is on call")
// from a roster backend that could not be reached. The two differ in what the
// store holds: on a failure the previous rosters are kept (see Refresh), so a
// non-empty active set is stale rather than wrong, and only a store that never
// managed to read a roster is empty — that is the case the commands answer with
// the service-unavailable message, because no group is known to be staffed and
// the module list alone is not something a user can raise a ticket against. A
// store with no source — a bare in-memory store — reports nil, because it has
// no roster to fail to read.
func (s *Store) RosterError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rosterErr
}

// degraded reports whether the last refresh came back incomplete, either
// because the load failed or because the rosters could not be read. It is what
// makes runRefreshLoop retry sooner than the configured interval.
func (s *Store) degraded() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadErr != nil || s.rosterErr != nil
}

// HasGroup reports whether the given group id (case-insensitive) is known and
// currently has at least one watcher.
func (s *Store) HasGroup(groupID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.activeGroups[strings.ToLower(groupID)]
	return ok
}

// SetSink installs the roster publisher and publishes what has already been
// read.
//
// It is called once at wiring time — after the constructor's first refresh, and
// before the first tick — so publishing immediately is what keeps a restarted
// process from leaving the objects showing the previous roster for one interval.
// That window is exactly when somebody is looking at them.
func (s *Store) SetSink(ctx context.Context, sink Sink) {
	if s == nil || sink == nil {
		return
	}
	s.sink = sink

	s.mu.RLock()
	groups := make([]Group, 0, len(s.meta))
	for _, group := range s.meta {
		groups = append(groups, group)
	}
	s.mu.RUnlock()

	if len(groups) > 0 {
		sink.Report(ctx, groups)
	}
}
