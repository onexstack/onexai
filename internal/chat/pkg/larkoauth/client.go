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

// Package larkoauth implements the browser-facing half of Feishu login: the
// authorization URL, the code exchange, and the user_info read.
//
// It is separate from internal/chat/im/lark because that package is about the
// bot's message surface. The credentials are the same application, but this
// flow needs neither the SDK's message client nor its event machinery, and
// keeping it to net/http makes the endpoints and the error shapes explicit.
package larkoauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Identity is what Feishu says about the person who authorized the app.
type Identity struct {
	OpenID    string
	UnionID   string
	UserID    string
	Name      string
	AvatarURL string
	Email     string
}

// Config is the subset of lark configuration this flow needs.
type Config struct {
	AppID       string
	AppSecret   string
	OpenBase    string // https://open.feishu.cn or https://open.larksuite.com
	RedirectURI string
}

// Client talks to Feishu's OAuth endpoints.
type Client struct {
	cfg  Config
	http *http.Client
}

// New returns a client. A client with missing credentials is disabled rather
// than invalid: Enabled reports that, and callers use it to hide the login
// button instead of offering one that always fails.
func New(cfg Config) *Client {
	if cfg.OpenBase == "" {
		cfg.OpenBase = "https://open.feishu.cn"
	}
	cfg.OpenBase = strings.TrimRight(cfg.OpenBase, "/")
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

// Enabled reports whether this deployment can use Feishu OAuth.
func (c *Client) Enabled() bool {
	return c != nil &&
		c.cfg.AppID != "" && c.cfg.AppSecret != "" && c.cfg.RedirectURI != ""
}

// RedirectURI returns the callback registered with the app.
func (c *Client) RedirectURI() string {
	if c == nil {
		return ""
	}
	return c.cfg.RedirectURI
}

// accountsBase is the OAuth host. Feishu's current authorization endpoint lives
// on the accounts host, which is a different host from the OpenAPI one.
func (c *Client) accountsBase() string {
	if strings.Contains(c.cfg.OpenBase, "larksuite.com") {
		return "https://accounts.larksuite.com"
	}
	return "https://accounts.feishu.cn"
}

// AuthorizeURL builds the URL the browser is sent to.
//
// state is opaque to Feishu and comes back on the callback; it is the signed
// value StateSigner produces, which is what makes the callback verifiable even
// though the exchange happens in the SPA.
func (c *Client) AuthorizeURL(state string) string {
	q := url.Values{}
	q.Set("client_id", c.cfg.AppID)
	q.Set("redirect_uri", c.cfg.RedirectURI)
	q.Set("response_type", "code")
	q.Set("state", state)
	return c.accountsBase() + "/open-apis/authen/v1/authorize?" + q.Encode()
}

// tokenResponse covers both shapes the token endpoint has used: the v2 OAuth
// response puts the token at the top level, while some deployments wrap it in
// data. Accepting both is what keeps a version difference from looking like
// "the code was invalid".
type tokenResponse struct {
	Code         int    `json:"code"`
	Msg          string `json:"msg"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Data         *struct {
		AccessToken string `json:"access_token"`
	} `json:"data"`
}

type userInfoResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data *struct {
		OpenID    string `json:"open_id"`
		UnionID   string `json:"union_id"`
		UserID    string `json:"user_id"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
		Email     string `json:"email"`
	} `json:"data"`
}

// Exchange turns an authorization code into the identity behind it.
func (c *Client) Exchange(ctx context.Context, code string) (*Identity, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("飞书登录未配置")
	}
	if code == "" {
		return nil, fmt.Errorf("缺少飞书授权码")
	}

	token, err := c.exchangeCode(ctx, code)
	if err != nil {
		return nil, err
	}
	return c.userInfo(ctx, token)
}

func (c *Client) exchangeCode(ctx context.Context, code string) (string, error) {
	body := map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     c.cfg.AppID,
		"client_secret": c.cfg.AppSecret,
		"code":          code,
		"redirect_uri":  c.cfg.RedirectURI,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("编码飞书授权请求失败: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.cfg.OpenBase+"/open-apis/authen/v2/oauth/token", bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("创建飞书授权请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求飞书授权服务失败: %w", err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("读取飞书授权响应失败: %w", err)
	}
	var out tokenResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		return "", fmt.Errorf("解析飞书授权响应失败（HTTP %d）", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || out.Code != 0 {
		return "", fmt.Errorf("飞书授权失败: %s（code=%d）", firstNonEmpty(out.Msg, "未知错误"), out.Code)
	}
	if out.AccessToken == "" && out.Data != nil {
		out.AccessToken = out.Data.AccessToken
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("飞书授权响应没有 access_token")
	}
	return out.AccessToken, nil
}

func (c *Client) userInfo(ctx context.Context, accessToken string) (*Identity, error) {
	req, err := http.NewRequestWithContext(
		ctx, http.MethodGet, c.cfg.OpenBase+"/open-apis/authen/v1/user_info", nil)
	if err != nil {
		return nil, fmt.Errorf("创建飞书用户信息请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求飞书用户信息失败: %w", err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("读取飞书用户信息失败: %w", err)
	}
	var out userInfoResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("解析飞书用户信息失败（HTTP %d）", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || out.Code != 0 || out.Data == nil {
		return nil, fmt.Errorf("读取飞书用户信息失败: %s（code=%d）",
			firstNonEmpty(out.Msg, "未知错误"), out.Code)
	}
	if out.Data.OpenID == "" {
		return nil, fmt.Errorf("飞书用户信息没有 open_id")
	}
	return &Identity{
		OpenID:    out.Data.OpenID,
		UnionID:   out.Data.UnionID,
		UserID:    out.Data.UserID,
		Name:      out.Data.Name,
		AvatarURL: out.Data.AvatarURL,
		Email:     out.Data.Email,
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
