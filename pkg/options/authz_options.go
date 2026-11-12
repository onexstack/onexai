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

package options

import (
	"fmt"
	"time"

	genericoptions "github.com/onexstack/onexstack/pkg/options"
	"github.com/spf13/pflag"
)

var _ genericoptions.IOptions = (*AuthzOptions)(nil)

// AuthzOptions defines options for the multi-level (Agent/Tool/Resource)
// authorization subsystem.
type AuthzOptions struct {
	// Enabled is the master switch. When false, the authz Coordinator is a
	// no-op (always Allow).
	Enabled bool `json:"enabled" mapstructure:"enabled"`

	// There is deliberately no fail-open switch. An Authorizer that errors out
	// (network failure, policy compile error — not a business Deny) is treated
	// as Deny, and a backend outage can never grant access.

	// RuleTableRefreshInterval controls how often the authz rule rows are
	// re-synced from the database into the OPA rule store.
	RuleTableRefreshInterval time.Duration `json:"rule-table-refresh-interval" mapstructure:"rule-table-refresh-interval"`

	// DecisionCallers, when non-empty, is the set of service accounts allowed to
	// ask POST /v1/authz/decisions. The endpoint answers "may this subject use
	// this agent", so an unrestricted caller can enumerate the policy; naming the
	// callers turns that into a question only the services that need it can ask.
	// Empty keeps the behaviour every deployment had: any signed caller may ask.
	// The value is a service name — the AK/SK api-key id the request was signed
	// with (authz.SubjectFromService).
	DecisionCallers []string `json:"decision-callers" mapstructure:"decision-callers"`

	// AuthorizationConfig is the path to an apiserver.config.k8s.io/v1
	// AuthorizationConfiguration. When set, its webhook authorizers replace the
	// legacy webhook.endpoints list and are evaluated in the configured order;
	// the platform's own OPA/CMDB/Casdoor backends stay in the union.
	AuthorizationConfig string `json:"authorization-config" mapstructure:"authorization-config"`

	OPA     OPAOptions          `json:"opa" mapstructure:"opa"`
	CMDB    CMDBAuthzOptions    `json:"cmdb" mapstructure:"cmdb"`
	Webhook WebhookAuthzOptions `json:"webhook" mapstructure:"webhook"`
	Casdoor CasdoorAuthzOptions `json:"casdoor" mapstructure:"casdoor"`

	// ResourceGuard applies policy decisions to the console's resource CRUD.
	// It is separate from the run/tool authorizers because it guards a different
	// surface and rolls out on its own schedule.
	ResourceGuard ResourceGuardOptions `json:"resource-guard" mapstructure:"resource-guard"`
}

// ResourceGuardOptions controls the console resource guard.
type ResourceGuardOptions struct {
	// WriteMode is off | dry-run | additive | authoritative.
	//
	//   off:            no policy check; the legacy admin/owner rules stand.
	//   dry-run:        evaluate and log, change nothing.
	//   additive:       an explicit Deny refuses; an explicit Allow grants; no
	//                   rule keeps the legacy owner/shared behaviour.
	//   authoritative: no rule on a shared resource refuses; owner-scoped
	//                   resources still let the owner through.
	//
	// The default is additive: it makes explicit rules effective without
	// locking out a deployment that has not written any yet.
	WriteMode string `json:"write-mode" mapstructure:"write-mode"`

	// ReadFilterEnabled hides list rows and item reads that a Deny rule covers
	// (and, in authoritative mode, rows with no covering rule). In additive
	// mode it is a deny-list: everything the legacy product showed stays
	// visible until an operator writes a Deny.
	ReadFilterEnabled bool `json:"read-filter-enabled" mapstructure:"read-filter-enabled"`

	// ReadFilterKinds narrows the rows that are filtered. Empty means every
	// guarded kind. Naming kinds keeps a large list (agents) on the legacy path
	// while a smaller one (namespaces) is already filtered.
	ReadFilterKinds []string `json:"read-filter-kinds" mapstructure:"read-filter-kinds"`
}

// DefaultWriteMode is what an unset write-mode means.
const DefaultWriteMode = "additive"

// OPAOptions configures the embedded OPA Authorizer (Agent-level + Tool-level).
type OPAOptions struct {
	Enabled bool `json:"enabled" mapstructure:"enabled"`
}

// CMDBAuthzOptions configures the CMDB resource Authorizer. Per-backend cache
// TTL is not configurable: every backend is cached with authz.DefaultCacheTTL,
// and a knob that no code reads is worse than no knob.
type CMDBAuthzOptions struct {
	Enabled bool `json:"enabled" mapstructure:"enabled"`
}

// WebhookAuthzOptions configures the Webhook Authorizer. Endpoints is a slice
// from day one (even though a deployment may configure only one) so
// registering a second backend never requires a breaking config format change.
//
// Each endpoint speaks Kubernetes authorization.k8s.io/v1 SubjectAccessReview:
// the request body is a SubjectAccessReview and the response's status
// (allowed/denied/reason/evaluationError) is the decision. Any policy center
// that already implements that contract — OPA, Casbin, a kube-apiserver — can
// be used without an adapter. The optional secret-ref/HMAC header is a platform
// extension a Kubernetes-native receiver simply ignores.
type WebhookAuthzOptions struct {
	Enabled   bool              `json:"enabled" mapstructure:"enabled"`
	Endpoints []WebhookEndpoint `json:"endpoints" mapstructure:"endpoints"`
}

// WebhookEndpoint configures a single webhook backend consulted by the
// webhook Authorizer.
type WebhookEndpoint struct {
	Name    string        `json:"name" mapstructure:"name"`
	URL     string        `json:"url" mapstructure:"url"`
	Timeout time.Duration `json:"timeout" mapstructure:"timeout"`
	// SecretRef names a row in the secret store whose value signs the request
	// body (X-Onex-Signature). Empty sends unsigned requests, which is what every
	// deployment had before the option existed; a ref that cannot be resolved is
	// a startup error rather than a silent downgrade to unsigned.
	SecretRef string `json:"secret-ref" mapstructure:"secret-ref"`
	// FailureMode controls the Decision used when this endpoint's call fails
	// (timeout, network error, non-2xx) or returns "Pending".
	// "no_opinion" (default, recommended) lets other Union'd Authorizers
	// decide; "deny" fails closed on this endpoint alone.
	FailureMode string `json:"failure-mode" mapstructure:"failure-mode"`
}

// CasdoorAuthzOptions configures the Casdoor-backed Authorizer, which delegates
// Agent-level, Tool-level, and Resource-level decisions to a Casdoor
// (https://casdoor.org) authorization center via the official casdoor-go-sdk.
type CasdoorAuthzOptions struct {
	Enabled bool `json:"enabled" mapstructure:"enabled"`

	Endpoint         string `json:"endpoint" mapstructure:"endpoint"`
	ClientID         string `json:"client-id" mapstructure:"client-id"`
	ClientSecret     string `json:"client-secret" mapstructure:"client-secret"`
	Certificate      string `json:"certificate" mapstructure:"certificate"`
	OrganizationName string `json:"organization-name" mapstructure:"organization-name"`
	ApplicationName  string `json:"application-name" mapstructure:"application-name"`

	// Organization is prepended to a Subject's PrimaryID() to build the Casbin
	// sub Casdoor expects (e.g. "built-in/u_alice") — see the casdoor package's
	// Config.Organization doc for why this is a separate, explicitly configured
	// field rather than reusing OrganizationName.
	Organization string `json:"organization" mapstructure:"organization"`

	// PermissionID/ModelID/ResourceID/EnforcerID/Owner scope which Casdoor
	// enforcer(s)/policy set the Enforce calls are evaluated against. All may
	// be left empty to consult every enforcer Casdoor has configured for this
	// organization/application.
	PermissionID string `json:"permission-id" mapstructure:"permission-id"`
	ModelID      string `json:"model-id" mapstructure:"model-id"`
	ResourceID   string `json:"resource-id" mapstructure:"resource-id"`
	EnforcerID   string `json:"enforcer-id" mapstructure:"enforcer-id"`
	Owner        string `json:"owner" mapstructure:"owner"`

	// Timeout bounds every HTTP call this Authorizer makes to Casdoor. Note
	// this is applied process-wide (casdoorsdk.SetHttpClient has no
	// per-instance scope) — see the casdoor package's Config.Timeout doc.
	Timeout time.Duration `json:"timeout" mapstructure:"timeout"`
}

// NewAuthzOptions creates a zero value instance for Authz options. The builder
// ships enabled with the OPA backend on: every Agent run and every Tool call is
// authorized out of the box, with the CMDB placeholder deliberately off (the
// upstream production deployment does the same).
func NewAuthzOptions() *AuthzOptions {
	return &AuthzOptions{
		Enabled:                  true,
		RuleTableRefreshInterval: 60 * time.Second,
		OPA:                      OPAOptions{Enabled: true},
		CMDB:                     CMDBAuthzOptions{Enabled: false},
		Webhook:                  WebhookAuthzOptions{Enabled: false},
		Casdoor:                  CasdoorAuthzOptions{Enabled: false, Timeout: 3 * time.Second},
		ResourceGuard: ResourceGuardOptions{
			WriteMode:         DefaultWriteMode,
			ReadFilterEnabled: true,
		},
	}
}

// Validate verifies flags passed to AuthzOptions.
func (o *AuthzOptions) Validate() []error {
	var errs []error
	if o.Enabled && !o.OPA.Enabled && !o.CMDB.Enabled && !o.Webhook.Enabled && !o.Casdoor.Enabled {
		errs = append(errs, fmt.Errorf("authz.enabled=true but no authorizer backend (opa/cmdb/webhook/casdoor) is enabled"))
	}
	for _, ep := range o.Webhook.Endpoints {
		if ep.URL == "" {
			errs = append(errs, fmt.Errorf("authz.webhook.endpoints[%s].url cannot be empty", ep.Name))
		}
		if ep.FailureMode != "" && ep.FailureMode != "no_opinion" && ep.FailureMode != "deny" {
			errs = append(errs, fmt.Errorf("authz.webhook.endpoints[%s].failure-mode must be \"no_opinion\" or \"deny\", got %q", ep.Name, ep.FailureMode))
		}
	}
	if o.ResourceGuard.WriteMode == "" {
		o.ResourceGuard.WriteMode = DefaultWriteMode
	}
	switch o.ResourceGuard.WriteMode {
	case "off", "dry-run", "additive", "authoritative":
	default:
		errs = append(errs, fmt.Errorf(
			"authz.resource-guard.write-mode must be off, dry-run, additive or authoritative, got %q",
			o.ResourceGuard.WriteMode))
	}
	if o.ResourceGuard.ReadFilterEnabled && !o.Enabled {
		errs = append(errs, fmt.Errorf(
			"authz.resource-guard.read-filter-enabled=true requires authz.enabled=true"))
	}
	if o.ResourceGuard.WriteMode != "off" && !o.Enabled {
		errs = append(errs, fmt.Errorf(
			"authz.resource-guard.write-mode=%q requires authz.enabled=true", o.ResourceGuard.WriteMode))
	}
	for _, kind := range o.ResourceGuard.ReadFilterKinds {
		if kind == "" {
			errs = append(errs, fmt.Errorf("authz.resource-guard.read-filter-kinds must not contain an empty entry"))
		}
	}
	if o.Casdoor.Enabled && o.Casdoor.Endpoint == "" {
		errs = append(errs, fmt.Errorf("authz.casdoor.endpoint cannot be empty when authz.casdoor.enabled=true"))
	}
	return errs
}

// AddFlags adds flags related to authorization to the specified FlagSet.
// Webhook.Endpoints (a slice of structs) is intentionally not flag-bindable; it
// is only configurable via the YAML/viper config file.
func (o *AuthzOptions) AddFlags(fs *pflag.FlagSet, fullPrefix string) {
	fs.BoolVar(&o.Enabled, fullPrefix+".enabled", o.Enabled,
		"Enable multi-level (agent/tool/resource) authorization")
	fs.StringVar(&o.AuthorizationConfig, fullPrefix+".authorization-config", o.AuthorizationConfig,
		"Path to an apiserver.config.k8s.io/v1 AuthorizationConfiguration; replaces the legacy webhook.endpoints list")
	fs.DurationVar(&o.RuleTableRefreshInterval, fullPrefix+".rule-table-refresh-interval", o.RuleTableRefreshInterval,
		"How often to re-sync the authorization rule table")
	fs.BoolVar(&o.OPA.Enabled, fullPrefix+".opa.enabled", o.OPA.Enabled,
		"Enable the embedded OPA authorizer (agent-level and tool-level)")
	fs.BoolVar(&o.CMDB.Enabled, fullPrefix+".cmdb.enabled", o.CMDB.Enabled,
		"Enable the CMDB resource authorizer")
	fs.BoolVar(&o.Webhook.Enabled, fullPrefix+".webhook.enabled", o.Webhook.Enabled,
		"Enable the webhook authorizer")
	fs.BoolVar(&o.Casdoor.Enabled, fullPrefix+".casdoor.enabled", o.Casdoor.Enabled,
		"Enable the Casdoor authorization center authorizer")
	fs.StringVar(&o.Casdoor.Endpoint, fullPrefix+".casdoor.endpoint", o.Casdoor.Endpoint,
		"Casdoor server endpoint (e.g. https://casdoor.example.com)")
	fs.StringVar(&o.Casdoor.ClientID, fullPrefix+".casdoor.client-id", o.Casdoor.ClientID,
		"Casdoor application client ID")
	fs.StringVar(&o.Casdoor.ClientSecret, fullPrefix+".casdoor.client-secret", o.Casdoor.ClientSecret,
		"Casdoor application client secret")
	fs.StringVar(&o.Casdoor.Certificate, fullPrefix+".casdoor.certificate", o.Casdoor.Certificate,
		"Casdoor JWT verification certificate (PEM)")
	fs.StringVar(&o.Casdoor.OrganizationName, fullPrefix+".casdoor.organization-name", o.Casdoor.OrganizationName,
		"Casdoor organization name")
	fs.StringVar(&o.Casdoor.ApplicationName, fullPrefix+".casdoor.application-name", o.Casdoor.ApplicationName,
		"Casdoor application name")
	fs.StringVar(&o.Casdoor.Organization, fullPrefix+".casdoor.organization", o.Casdoor.Organization,
		"Prefix prepended to Subject.PrimaryID() to build the Casbin sub (e.g. \"built-in\")")
	fs.DurationVar(&o.Casdoor.Timeout, fullPrefix+".casdoor.timeout", o.Casdoor.Timeout,
		"Timeout for Casdoor Enforce HTTP calls (applied process-wide, see CasdoorAuthzOptions.Timeout doc)")
}
