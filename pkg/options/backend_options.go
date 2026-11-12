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
	"strings"

	"github.com/spf13/pflag"
)

// BackendOptions holds the endpoints for the auxiliary HTTP-backed clients
// (CMDB, Zadig, Prometheus) consumed by the resource-query and platform
// commands.
type BackendOptions struct {
	// DNSWhitelistURL is the raw URL of the dnsmasq forward.conf file.
	DNSWhitelistURL string `json:"dns-whitelist-url" mapstructure:"dns-whitelist-url"`
	// DNSWhitelistToken is the GitLab PRIVATE-TOKEN for fetching forward.conf.
	DNSWhitelistToken string `json:"dns-whitelist-token" mapstructure:"dns-whitelist-token"`
	// CMDBEndpoint is the base URL of the CMDB API.
	CMDBEndpoint string `json:"cmdb-endpoint" mapstructure:"cmdb-endpoint"`
	// CMDBSecretID is the CMDB AK/SK access key ID used for HMAC signing.
	CMDBSecretID string `json:"cmdb-secret-id" mapstructure:"cmdb-secret-id"`
	// CMDBSecretKey is the CMDB AK/SK secret key used for HMAC signing.
	CMDBSecretKey string `json:"cmdb-secret-key" mapstructure:"cmdb-secret-key"`
	// CMDBEnabled turns on CMDB enrichment inside the resource commands. It
	// defaults to false: the CMDB code is kept, but /pod answers from metrics
	// alone until CMDB is ready.
	CMDBEnabled bool `json:"cmdb-enabled" mapstructure:"cmdb-enabled"`
	// PrometheusEndpoint is the base URL of the Prometheus HTTP API backing the
	// /pod ChatOps command. Access is anonymous; an empty value disables metric
	// queries and makes /pod report that no data source is configured.
	PrometheusEndpoint string `json:"prometheus-endpoint" mapstructure:"prometheus-endpoint"`
	// ZadigEndpoint is the base URL of the Zadig DevOps platform, used by the
	// /pipeline command to trigger custom workflows.
	ZadigEndpoint string `json:"zadig-endpoint" mapstructure:"zadig-endpoint"`
	// ZadigToken is the Zadig API token (account settings -> API Token), sent as
	// "Authorization: Bearer <token>". It is a secret: it must never be logged,
	// echoed in an error message, or committed to the repository.
	ZadigToken string `json:"zadig-token" mapstructure:"zadig-token"`
	// ZadigProject is the default Zadig project key the /pipeline command
	// resolves a workflow in; the command's --project flag overrides it.
	ZadigProject string `json:"zadig-project" mapstructure:"zadig-project"`
	// RegenEndpoint is the base URL of the Regen on-call platform, used by the
	// duty commands to resolve the on-call person of a module and by the ticket
	// store to mirror onexai_chat_ticket rows as Regen incidents. An empty value turns
	// both off: the duty store stays empty and tickets are local-only.
	RegenEndpoint string `json:"regen-endpoint" mapstructure:"regen-endpoint"`
	// RegenUsername is the Regen account's login email. Regen has no API key, so
	// the client authenticates with an email/password login and keeps the
	// resulting session cookie. The account must already exist in Regen: a
	// renamed service (oai-chatserver -> oai-chat-apiserver) whose login email
	// was not renamed with it fails the login with a 401, which leaves the duty
	// store empty and every /oncall and /duty module answering "不存在".
	RegenUsername string `json:"regen-username" mapstructure:"regen-username"`
	// RegenPassword is the Regen account's password. It is a secret: it must
	// never be logged, echoed in an error message, or committed to the
	// repository.
	RegenPassword string `json:"regen-password" mapstructure:"regen-password"`
	// RegenUserMap is the explicit on-call name -> Lark user_id override, as a
	// comma-separated "name=lark_id,name=lark_id" list. Names are usually
	// emails, but a platform login name works too.
	//
	// It is no longer the only source: a name that misses this map is resolved
	// through the platform account and its Feishu binding, so a person selected
	// in the console can be paged without editing this file. Keep an entry here
	// for people without a platform account, or to pin a user_id explicitly.
	RegenUserMap string `json:"regen-user-map" mapstructure:"regen-user-map"`
	// DutyMaintainerUserID is the Lark user_id shown as the default/fallback
	// on-caller on the /duty card. It is display-only.
	DutyMaintainerUserID string `json:"duty-maintainer-user-id" mapstructure:"duty-maintainer-user-id"`
	// P4DeferGroups lists the duty module codes whose P4 tickets do not page the
	// on-call up front: the group is created without a watcher, and /level
	// escalating the ticket to P1/P2/P3 invites them. Comma-separated.
	P4DeferGroups string `json:"p4-defer-groups" mapstructure:"p4-defer-groups"`
}

// NewBackendOptions creates a BackendOptions with default values.
func NewBackendOptions() *BackendOptions {
	return &BackendOptions{}
}

// AddFlags binds BackendOptions to command-line flags.
func (o *BackendOptions) AddFlags(fs *pflag.FlagSet, fullPrefix string) {
	fs.StringVar(&o.DNSWhitelistURL, fullPrefix+".dns-whitelist-url", o.DNSWhitelistURL, "Raw URL of the dnsmasq forward.conf file.")
	fs.StringVar(&o.DNSWhitelistToken, fullPrefix+".dns-whitelist-token", o.DNSWhitelistToken, "GitLab PRIVATE-TOKEN for fetching forward.conf.")
	fs.StringVar(&o.CMDBEndpoint, fullPrefix+".cmdb-endpoint", o.CMDBEndpoint, "CMDB API base URL.")
	fs.StringVar(&o.CMDBSecretID, fullPrefix+".cmdb-secret-id", o.CMDBSecretID, "CMDB AK/SK access key ID.")
	fs.StringVar(&o.CMDBSecretKey, fullPrefix+".cmdb-secret-key", o.CMDBSecretKey, "CMDB AK/SK secret key.")
	fs.BoolVar(&o.CMDBEnabled, fullPrefix+".cmdb-enabled", o.CMDBEnabled, "Enable CMDB enrichment in resource commands.")
	fs.StringVar(&o.PrometheusEndpoint, fullPrefix+".prometheus-endpoint", o.PrometheusEndpoint, "Prometheus HTTP API base URL backing /pod.")
	fs.StringVar(&o.ZadigEndpoint, fullPrefix+".zadig-endpoint", o.ZadigEndpoint, "Zadig platform base URL.")
	fs.StringVar(&o.ZadigToken, fullPrefix+".zadig-token", o.ZadigToken, "Zadig API token (secret).")
	fs.StringVar(&o.ZadigProject, fullPrefix+".zadig-project", o.ZadigProject, "Default Zadig project key.")
	fs.StringVar(&o.RegenEndpoint, fullPrefix+".regen-endpoint", o.RegenEndpoint, "Regen on-call platform base URL.")
	fs.StringVar(&o.RegenUsername, fullPrefix+".regen-username", o.RegenUsername, "Regen account login email.")
	fs.StringVar(&o.RegenPassword, fullPrefix+".regen-password", o.RegenPassword, "Regen account password (secret).")
	fs.StringVar(&o.RegenUserMap, fullPrefix+".regen-user-map", o.RegenUserMap, "Regen on-call name to Lark user_id map, e.g. \"a@x.com=ou_1,b@x.com=ou_2\".")
	fs.StringVar(&o.DutyMaintainerUserID, fullPrefix+".duty-maintainer-user-id", o.DutyMaintainerUserID, "Lark user_id shown as the fallback on-caller on the /duty card.")
	fs.StringVar(&o.P4DeferGroups, fullPrefix+".p4-defer-groups", o.P4DeferGroups, "Comma-separated duty module codes whose P4 tickets defer paging the on-call until /level escalates.")
}

// ParseP4DeferGroups parses P4DeferGroups into a set of lower-cased module
// codes. Blank entries are skipped, so a trailing separator is not an error.
func (o *BackendOptions) ParseP4DeferGroups() map[string]struct{} {
	out := make(map[string]struct{})
	for _, code := range strings.Split(o.P4DeferGroups, ",") {
		code = strings.ToLower(strings.TrimSpace(code))
		if code != "" {
			out[code] = struct{}{}
		}
	}
	return out
}

// ParseRegenUserMap parses RegenUserMap into a lookup table.
//
// The entries are "name=user_id" pairs separated by commas or semicolons;
// surrounding whitespace is ignored and blank entries are skipped, so a
// trailing separator is not an error. An entry that is not a pair, or a pair
// with an empty side, is an error: silently dropping it would turn a typo into
// a ticket group that cannot be created.
func (o *BackendOptions) ParseRegenUserMap() (map[string]string, error) {
	out := make(map[string]string)
	for _, entry := range strings.FieldsFunc(o.RegenUserMap, func(r rune) bool {
		return r == ',' || r == ';'
	}) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, userID, ok := strings.Cut(entry, "=")
		name, userID = strings.TrimSpace(name), strings.TrimSpace(userID)
		if !ok || name == "" || userID == "" {
			return nil, fmt.Errorf("invalid regen user map entry %q, want \"name=user_id\"", entry)
		}
		out[name] = userID
	}
	return out, nil
}
