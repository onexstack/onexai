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
	"github.com/onexstack/onexstack/pkg/store/registry"

	"github.com/onexstack/onexai/pkg/dbschema"
)

func init() { registry.Register(&WebhookM{}) }

// Event names a run lifecycle moment a webhook can subscribe to.
const (
	// EventOnStart is delivered before a run begins, and its answer decides
	// whether the run happens at all. It is the only event that can refuse.
	EventOnStart = "onStart"
	// EventOnEnd is delivered after a run has finished answering. It is a
	// notification: nothing it says changes the run, and its delivery failing
	// changes nothing either.
	//
	// # Do not subscribe a hook to this while usage-report is configured
	//
	// Both deliver a finished run's usage to the platform. usagereport
	// (internal/agent/pkg/usagereport) has been doing it since before this
	// contract existed, and it is currently the only thing that does — so
	// removing it before a hook takes over would stop the ledger seeing Lark
	// runs at all, and adding a hook before it is removed would have the
	// platform record every run twice.
	//
	// The order is therefore: configure the hook, confirm what the platform
	// receives matches what usagereport sends, then empty
	// usage-report.endpoint.
	EventOnEnd = "onEnd"
)

// Webhook types. Empty means run, which is what every row written before the
// column existed is.
const (
	// WebhookTypeRun is a hook called around an Agent run.
	WebhookTypeRun = "run"
	// WebhookTypeAuthentication is a Kubernetes TokenReview endpoint that
	// authenticates callers of an Agent that names it in authn_webhooks.
	WebhookTypeAuthentication = "authentication"
	// WebhookTypeAuthorization is a Kubernetes SubjectAccessReview endpoint that
	// decides whether a caller may use an Agent that names it in authz_webhooks.
	WebhookTypeAuthorization = "authorization"
)

// EffectiveType returns the stored type, defaulting to run.
func (w WebhookM) EffectiveType() string {
	if w.Type == "" {
		return WebhookTypeRun
	}
	return w.Type
}

// Failure mode values for WebhookM.FailureMode.
const (
	// FailureModeIgnore lets a run through when the endpoint cannot be reached,
	// times out, or answers with a non-2xx status. It is the default: a webhook
	// that is down must not become an outage of the thing it observes.
	FailureModeIgnore = "ignore"
	// FailureModeFail refuses the run instead. It is for a deployment that
	// would rather stop than run unguarded, and it only means anything for
	// onStart — an onEnd webhook has no run left to refuse.
	FailureModeFail = "fail"
)

// WebhookM is one configured run-lifecycle hook.
//
// # Why this is a table and not a column on AgentM
//
// An agent names the hooks it uses (AgentM.Webhooks) rather than carrying their
// configuration, so the URL, the credential and the timeout live in one row
// that every agent naming it shares. An operator who moves an endpoint edits one
// row, not every agent that talks to it.
//
// # The contract it speaks
//
// A hook is called over HTTP with the event in a header (X-Onexai-Event) and a
// JSON body whose keys are lowerCamel, matching the rest of this service's wire
// types. It answers 2xx with a body carrying the verdict; a 2xx that says
// {"allowed": false} is a refusal, which is deliberately not an error status —
// see pkg/webhookclient for why.
type WebhookM struct {
	ID     uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	Name   string `gorm:"uniqueIndex;size:128" json:"name"`
	Status string `gorm:"size:32" json:"status"` // "enabled" | "disabled"

	// URL is the endpoint to call. It must carry a scheme; a bare host:port is
	// refused at write time rather than at call time, where it would look like
	// an unreachable endpoint. It is empty for a hook that addresses a Service
	// through ClientConfig.
	URL string `gorm:"type:text" json:"url"`

	// ClientConfigJSON is the Kubernetes-shaped endpoint configuration
	// (url or service + caBundle + the platform extensions), serialized. Empty
	// means the hook predates ClientConfig and uses URL alone.
	ClientConfigJSON string `gorm:"column:client_config;type:text" json:"client_config"`

	// SecretRef names a row in the secret store whose value is sent as the
	// webhook's bearer token (X-Onexai-Token). It is a reference rather than a
	// value because this table is read by anything that lists hooks, and a
	// credential stored beside a URL is a credential in every dump, log line
	// and console page that shows the row.
	//
	// Empty means the endpoint is called unauthenticated, which is a real
	// configuration for an endpoint on a trusted network.
	SecretRef string `gorm:"size:128" json:"secret_ref"`

	// AuthToken is the plaintext credential supplied on a write. It is never
	// persisted: the service moves it into the secret store, sets SecretRef to
	// the managed secret, and clears this field before the row is written.
	AuthToken string `gorm:"-" json:"-"`

	// Type is run, authentication or authorization. Empty means run, which is
	// what every row written before the column existed is. Run hooks fire
	// around a run; the other two are Kubernetes access-review endpoints called
	// per request by an Agent that names them in authn_webhooks/authz_webhooks.
	Type string `gorm:"column:type;size:32" json:"type"`

	// Events names the lifecycle moments this hook subscribes to. One hook may
	// take both, which is how a single URL can be the whole of an integration —
	// the contract's header is what tells the two deliveries apart. Auth hooks
	// leave it empty.
	Events StringList `gorm:"type:text" json:"events"`

	// Audiences is sent in TokenReview.spec.audiences for an authentication
	// hook. Empty sends none and leaves the audience decision to the receiver.
	Audiences StringList `gorm:"type:text" json:"audiences"`

	// TimeoutSeconds bounds one call. onStart is on a run's critical path, so
	// this is a ceiling on how long a slow endpoint can hold a run up.
	TimeoutSeconds int `json:"timeout_seconds"`

	// FailureMode is what an unreachable endpoint means: FailureModeIgnore
	// (default) or FailureModeFail.
	FailureMode string `gorm:"size:32" json:"failure_mode"`

	// Headers are sent with every delivery, for an endpoint that needs more
	// than the shared token — a tenant label, a routing key. They cannot
	// override the delivery headers the contract defines.
	Headers StringMap `gorm:"type:text" json:"headers"`

	// Template, when set, is rendered and sent as the request body instead of
	// the default one, so an endpoint can name the fields it wants in its own
	// vocabulary. Empty means the default body.
	Template string `gorm:"type:text" json:"template"`

	Owner     string `gorm:"size:256" json:"owner"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `gorm:"autoUpdateTime:milli" json:"updated_at"`
}

// TableName returns the webhook table name.
func (WebhookM) TableName() string { return dbschema.Table(dbschema.Agent, "webhook") }

// Enabled reports whether this hook should be called.
func (w WebhookM) Enabled() bool { return w.Status != StatusDisabled }

// Subscribes reports whether this hook takes the given event. An event value it
// does not name is not delivered to it, which is how an endpoint that only
// wants usage reports avoids being asked to admit anything.
func (w WebhookM) Subscribes(event string) bool {
	for _, e := range w.Events {
		if e == event {
			return true
		}
	}
	return false
}
