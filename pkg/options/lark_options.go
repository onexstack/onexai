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
	"net/url"

	"github.com/spf13/pflag"
)

// LarkOptions holds Lark/Feishu application credentials.
type LarkOptions struct {
	// AppID is the Lark application ID.
	AppID string `json:"app-id" mapstructure:"app-id"`
	// AppSecret is the Lark application secret.
	AppSecret string `json:"app-secret" mapstructure:"app-secret"`
	// URL is the Lark API base URL.
	URL string `json:"url" mapstructure:"url"`
	// EventMode is "websocket" or "webhook".
	EventMode string `json:"event-mode" mapstructure:"event-mode"`
	// VerificationToken used in webhook mode.
	VerificationToken string `json:"verification-token" mapstructure:"verification-token"`
	// OAuthRedirectURI is the browser callback registered with the Lark app. When
	// it, AppID and AppSecret are all set, Feishu OAuth login is available.
	//
	// It must match the console's callback URL exactly (including scheme, host,
	// port and path): Feishu rejects a mismatch with 20029, and the console is
	// usually served on a different port from this API in development.
	OAuthRedirectURI string `json:"oauth-redirect-uri" mapstructure:"oauth-redirect-uri"`
	// EncryptKey is the Lark event encryption key used to verify and decrypt
	// webhook event bodies. Empty means webhook events are plaintext and verified
	// against VerificationToken instead.
	EncryptKey string `json:"encrypt-key" mapstructure:"encrypt-key"`
	// BotName is the substring used to detect an @-mention of this bot in group
	// chats (e.g. "Helper"). When a group message @-mentions a user whose name
	// contains this value, the bot treats it as directed at itself.
	BotName string `json:"bot-name" mapstructure:"bot-name"`

	// AllowFrom is the comma-separated allow-list of sender identities the bot
	// responds to — Lark user_ids, like the role lists, not open_ids. "*" means
	// everyone.
	AllowFrom string `json:"allow-from" mapstructure:"allow-from"`
	// AllowChat is the comma-separated allow-list of chat ids the bot responds
	// in. "*" means every chat.
	AllowChat string `json:"allow-chat" mapstructure:"allow-chat"`
	// GroupOnly, when true, makes the bot ignore private (P2P) chats.
	GroupOnly bool `json:"group-only" mapstructure:"group-only"`
	// ReplyToTrigger, when true (default), replies to the triggering message
	// (threading the reply) instead of sending a new message.
	ReplyToTrigger bool `json:"reply-to-trigger" mapstructure:"reply-to-trigger"`
	// ThreadIsolation, when true, derives a separate session per topic/thread so
	// replies stay within the thread.
	ThreadIsolation bool `json:"thread-isolation" mapstructure:"thread-isolation"`
	// ImageBatchWindowMs is the window (ms) in which consecutive image messages
	// are buffered and merged into a single batch before dispatch.
	ImageBatchWindowMs int `json:"image-batch-window-ms" mapstructure:"image-batch-window-ms"`
	// OutboundRateLimit caps outbound Lark messages per second (0 disables).
	OutboundRateLimit float64 `json:"outbound-rate-limit" mapstructure:"outbound-rate-limit"`
	// OutboundRateBurst is the burst size of the outbound rate limiter.
	OutboundRateBurst int `json:"outbound-rate-burst" mapstructure:"outbound-rate-burst"`
}

// NewLarkOptions creates a LarkOptions with default values.
func NewLarkOptions() *LarkOptions {
	return &LarkOptions{
		EventMode:          "websocket",
		URL:                "https://open.feishu.cn",
		AllowFrom:          "*",
		AllowChat:          "*",
		ReplyToTrigger:     true,
		ImageBatchWindowMs: 500,
	}
}

// Validate checks the LarkOptions fields.
func (o *LarkOptions) Validate() []error {
	var errs []error
	switch o.EventMode {
	case "", "websocket", "webhook":
	default:
		errs = append(errs, fmt.Errorf("lark.event-mode %q is invalid (want websocket|webhook)", o.EventMode))
	}
	if o.EventMode == "webhook" && o.VerificationToken == "" && o.EncryptKey == "" {
		errs = append(errs, fmt.Errorf("lark.verification-token or lark.encrypt-key is required when lark.event-mode=webhook"))
	}
	if (o.AppID == "") != (o.AppSecret == "") {
		errs = append(errs, fmt.Errorf("lark.app-id and lark.app-secret must be set together"))
	}
	if o.OAuthRedirectURI != "" {
		if o.AppID == "" || o.AppSecret == "" {
			errs = append(errs, fmt.Errorf(
				"lark.oauth-redirect-uri requires lark.app-id and lark.app-secret"))
		}
		parsed, err := url.Parse(o.OAuthRedirectURI)
		if err != nil || !parsed.IsAbs() || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			errs = append(errs, fmt.Errorf(
				"lark.oauth-redirect-uri must be an absolute http(s) URL, got %q", o.OAuthRedirectURI))
		}
	}
	return errs
}

// AddFlags adds Lark-related flags to the given FlagSet.
func (o *LarkOptions) AddFlags(fs *pflag.FlagSet, fullPrefix string) {
	fs.StringVar(&o.AppID, fullPrefix+".app-id", o.AppID, "Lark/Feishu application ID.")
	fs.StringVar(&o.AppSecret, fullPrefix+".app-secret", o.AppSecret, "Lark/Feishu application secret.")
	fs.StringVar(&o.URL, fullPrefix+".url", o.URL, "Lark/Feishu API base URL.")
	fs.StringVar(&o.EventMode, fullPrefix+".event-mode", o.EventMode, "Lark event mode: websocket | webhook.")
	fs.StringVar(&o.VerificationToken, fullPrefix+".verification-token", o.VerificationToken, ""+
		"Lark verification token for webhook mode.")
	fs.StringVar(&o.OAuthRedirectURI, fullPrefix+".oauth-redirect-uri", o.OAuthRedirectURI, ""+
		"Browser callback URL for Feishu OAuth login, registered with the Lark app.")
	fs.StringVar(&o.EncryptKey, fullPrefix+".encrypt-key", o.EncryptKey, ""+
		"Lark event encryption key for webhook mode (verifies and decrypts event bodies).")
	fs.StringVar(&o.BotName, fullPrefix+".bot-name", o.BotName, ""+
		"Bot display-name substring used to detect @-mentions of this bot in group chats.")
	fs.StringVar(&o.AllowFrom, fullPrefix+".allow-from", o.AllowFrom, ""+
		"Comma-separated allow-list of sender user_ids the bot responds to; '*' means everyone.")
	fs.StringVar(&o.AllowChat, fullPrefix+".allow-chat", o.AllowChat, ""+
		"Comma-separated allow-list of chat ids the bot responds in; '*' means every chat.")
	fs.BoolVar(&o.GroupOnly, fullPrefix+".group-only", o.GroupOnly, ""+
		"Only respond in group chats, ignore private (P2P) chats.")
	fs.BoolVar(&o.ReplyToTrigger, fullPrefix+".reply-to-trigger", o.ReplyToTrigger, ""+
		"Reply to the triggering message (thread the reply) instead of sending a new message.")
	fs.BoolVar(&o.ThreadIsolation, fullPrefix+".thread-isolation", o.ThreadIsolation, ""+
		"Derive a separate session per topic/thread so replies stay within the thread.")
	fs.IntVar(&o.ImageBatchWindowMs, fullPrefix+".image-batch-window-ms", o.ImageBatchWindowMs, ""+
		"Window (ms) to buffer consecutive image messages into a single batch.")
	fs.Float64Var(&o.OutboundRateLimit, fullPrefix+".outbound-rate-limit", o.OutboundRateLimit, ""+
		"Max outbound Lark messages per second (0 disables rate limiting).")
	fs.IntVar(&o.OutboundRateBurst, fullPrefix+".outbound-rate-burst", o.OutboundRateBurst, ""+
		"Burst size of the outbound rate limiter.")
}
