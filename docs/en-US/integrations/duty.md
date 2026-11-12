# Duty integration

Duty is the on-call and incident capability behind `/oncall`, `/duty`, `/open`,
`/close`, `/save`, `/hand`, `/mark`, and related ChatOps commands.

## The contract

`pkg/duty` defines the provider contract:

```go
type Provider interface {
    Name() string
    Configured() bool
    Roster
    Incidents
}

type Roster interface {
    Schedules(ctx context.Context) ([]Schedule, error)
    OnCall(ctx context.Context, scheduleID string) (Person, error)
}

type Incidents interface {
    CreateIncident(ctx context.Context, in CreateIncidentRequest) (Incident, error)
    GetIncident(ctx context.Context, id string) (Incident, error)
    UpdateIncident(ctx context.Context, id string, in UpdateIncidentRequest) (Incident, error)
}
```

A provider can additionally implement `duty.TransitionChecker` to describe a one-way
incident state machine; the ticket mirror asks it before pushing a status change.

## Selecting a provider

```yaml
duty:
  provider: regen   # none | regen
```

- `none` (or a missing block with no `backend.regen-endpoint`): no external rotation is
  read; native schedules still load from the `chat.oncall_*` tables. Groups that have
  not been migrated have no watcher, and tickets stay local.
- `regen`: `pkg/duty/regen` logs in with `backend.regen-username` /
  `backend.regen-password`, lists `backend.regen-endpoint` schedules, and mirrors tickets
  as incidents. `backend.regen-user-map` maps a rotation's free-text name to a Lark
  `user_id`.

The legacy inference still works: setting `backend.regen-endpoint` without `duty.provider`
selects Regen.

## Native schedules (default)

A group's rotation layers, overrides, unavailabilities, holidays, and escalation policy
now live in the `chat.oncall_*` tables and are managed by
`/v1/oncall-groups/:name/schedule` on `oai-chat-apiserver`. On each refresh the duty store:

1. evaluates groups with a native schedule locally and never calls the provider;
2. falls back to the provider only for a group that still has a `schedule_id` and no
   native schedule;
3. keeps native schedules working when no provider is configured; legacy groups report
   no watcher.

Cut-over: run `hack/crd2pg` to copy group metadata from the control plane into
`chat.oncall_group`, then `hack/regen-import` to copy Regen rotations into the native
tables. Both support `--dry-run`.

## Implementing another provider

1. Implement `duty.Provider` in a new package (for example `pkg/duty/mysystem`).
2. Map your statuses to `duty.Status*` and `duty.Severity*`.
3. Return `duty.ErrTransitionNotAllowed` from `UpdateIncident` when your state machine
   rejects the move, and implement `CanTransition` if the caller should decide before
   calling.
4. Add a case to `ProvideDutyProvider` in `internal/chat/providers.go`.
5. Add mapping tests; do not change the persisted columns (`schedule_id`, `incident_id`,
   `source`) without a migration.

## Behavior guarantees

- A provider outage never stops the chat service from starting; the duty store stays
  empty and the commands report the roster as unavailable.
- Tickets created before a provider was configured have no incident and stay local.
- `source` keeps the provider name; existing rows keep `regen`.
- Closing and resolving share the provider's resolved state; reopening creates a fresh
  incident when the provider's state machine is one-way.
