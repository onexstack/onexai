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

package customcmd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	utilvalidation "k8s.io/apimachinery/pkg/util/validation"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/pkg/clientconfig"
	"github.com/onexstack/onexai/internal/pkg/netguard"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// HTTPConfig bounds every outbound command call. The defaults are the
// deployment's, not the command author's.
type HTTPConfig struct {
	// AllowedHosts, when non-empty, is the only set of hosts a url target may
	// name. Empty means any host netguard considers public.
	AllowedHosts []string
	// AllowPlainHTTP permits http:// url targets. service targets are
	// cluster-internal and are allowed to stay on http regardless.
	AllowPlainHTTP bool
	// AllowPrivateHosts is the explicit opt-in to private/loopback targets.
	AllowPrivateHosts bool
	// MaxTimeout caps a command's own timeout. Zero uses DefaultMaxTimeout.
	MaxTimeout time.Duration
	// MaxResponseBytes caps the body. Zero uses DefaultMaxResponseBytes.
	MaxResponseBytes int64
	// MaxRedirects bounds the chain. Zero uses the netguard default.
	MaxRedirects int
}

const (
	DefaultMaxTimeout       = 60 * time.Second
	DefaultMaxResponseBytes = 1 << 20
)

// HTTPExecutor runs one http command.
type HTTPExecutor struct {
	cfg HTTPConfig
	// public serves url targets and applies the private-address policy.
	public *netguard.Fetcher
	// cluster serves service targets: an in-cluster Service resolves to a
	// private address by construction, so the public policy would block every
	// one of them.
	cluster *netguard.Fetcher
	// tlsClients caches the client built for one explicit TLS configuration.
	// Without it, every call to a command with a private CA clones the
	// transport — a new connection pool, and a reparsed bundle — per invocation.
	tlsClients sync.Map // map[string]*http.Client
}

// NewHTTPExecutor builds an executor around the process-wide netguard policy.
func NewHTTPExecutor(cfg HTTPConfig) *HTTPExecutor {
	if cfg.MaxTimeout <= 0 {
		cfg.MaxTimeout = DefaultMaxTimeout
	}
	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = DefaultMaxResponseBytes
	}
	fetcher := func(allowPrivate bool) *netguard.Fetcher {
		return netguard.NewFetcher(netguard.Options{
			AllowPrivateHosts: allowPrivate,
			MaxBytes:          cfg.MaxResponseBytes,
			MaxRedirects:      cfg.MaxRedirects,
			// A command call is not retried: a retried POST is a second side
			// effect, and the caller can decide to try again itself.
			Retries: -1,
		})
	}
	return &HTTPExecutor{
		cfg:     cfg,
		public:  fetcher(cfg.AllowPrivateHosts),
		cluster: fetcher(true),
	}
}

// HTTPResult is one completed call.
type HTTPResult struct {
	Status      int
	ContentType string
	Header      http.Header
	Body        []byte
	Rendered    string
}

// CallError separates "the call could not be made" from "the call was made and
// failed". The chat surface tells the user a different thing in each case.
type CallError struct {
	Message    string
	Evaluation bool
	StatusCode int
}

func (e *CallError) Error() string { return e.Message }

// Execute renders and performs one http command.
func (e *HTTPExecutor) Execute(
	ctx context.Context,
	spec *model.HTTPCommandM,
	output *model.CommandOutputM,
	params map[string]any,
	data TemplateData,
	timeout time.Duration,
	bearer string,
) (*HTTPResult, error) {
	if spec == nil {
		return nil, &CallError{Message: "command has no http body", Evaluation: false}
	}

	rawURL, inCluster, tlsConfig, err := e.endpointFor(spec, data)
	if err != nil {
		return nil, err
	}
	if err := e.checkTarget(rawURL, inCluster); err != nil {
		return nil, err
	}

	body, err := RenderTemplate("http.body", spec.Body, data)
	if err != nil {
		return nil, &CallError{Message: err.Error(), Evaluation: false}
	}

	if timeout <= 0 || timeout > e.cfg.MaxTimeout {
		timeout = e.cfg.MaxTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(callCtx, methodOf(spec.Method), rawURL, strings.NewReader(body))
	if err != nil {
		return nil, &CallError{Message: "building request: " + err.Error(), Evaluation: false}
	}
	contentType := spec.ContentType
	if contentType == "" {
		contentType = "application/json"
	}
	carriesBody := body != "" || (req.Method != http.MethodGet && req.Method != http.MethodHead)
	if carriesBody {
		req.Header.Set("Content-Type", contentType)
	}
	for _, h := range spec.Headers {
		value, err := RenderTemplate("http.header."+h.Name, h.Value, data)
		if err != nil {
			return nil, &CallError{Message: err.Error(), Evaluation: false}
		}
		req.Header.Set(h.Name, value)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if data.DryRun {
		req.Header.Set("X-OnexAI-Dry-Run", "true")
	}

	var client *http.Client
	if len(spec.ClientConfig) > 0 {
		client, err = e.clientForResolved(inCluster, tlsConfig, fmt.Sprintf("%t|%s", inCluster, spec.ClientConfig))
	} else {
		client, err = e.clientFor(spec, inCluster)
	}
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &CallError{Message: "command was cancelled", Evaluation: true}
		}
		return nil, &CallError{Message: "calling endpoint: " + err.Error(), Evaluation: true}
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, e.cfg.MaxResponseBytes+1)
	payload, err := io.ReadAll(limited)
	if err != nil {
		return nil, &CallError{Message: "reading response: " + err.Error(), Evaluation: true}
	}
	if int64(len(payload)) > e.cfg.MaxResponseBytes {
		return nil, &CallError{
			Message:    fmt.Sprintf("response exceeds %d bytes", e.cfg.MaxResponseBytes),
			Evaluation: false,
			StatusCode: resp.StatusCode,
		}
	}

	result := &HTTPResult{
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Header:      resp.Header,
		Body:        payload,
	}
	rendered, renderErr := RenderOutput(output, resp.StatusCode, resp.Header, payload, params)
	if renderErr != nil {
		return nil, &CallError{Message: renderErr.Error(), Evaluation: false, StatusCode: resp.StatusCode}
	}
	result.Rendered = rendered

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return result, &CallError{
			Message:    fmt.Sprintf("endpoint returned status %d: %s", resp.StatusCode, truncate(rendered, 512)),
			Evaluation: false,
			StatusCode: resp.StatusCode,
		}
	}
	return result, nil
}

func methodOf(method string) string {
	if method == "" {
		return http.MethodGet
	}
	return strings.ToUpper(method)
}

// endpointFor resolves the endpoint and its TLS configuration. A ClientConfig
// takes the shared Kubernetes-shaped path; the legacy fields keep the historical
// buildURL/clientFor path.
func (e *HTTPExecutor) endpointFor(
	spec *model.HTTPCommandM, data TemplateData,
) (string, bool, *tls.Config, error) {
	if len(spec.ClientConfig) == 0 {
		rawURL, inCluster, err := e.buildURL(spec, data)
		return rawURL, inCluster, nil, err
	}

	cfg := &agentv1alpha1.ClientConfig{}
	if err := json.Unmarshal(spec.ClientConfig, cfg); err != nil {
		return "", false, nil, &CallError{Message: "invalid clientConfig: " + err.Error(), Evaluation: false}
	}
	resolved, err := clientconfig.Resolve(*cfg)
	if err != nil {
		return "", false, nil, &CallError{Message: err.Error(), Evaluation: false}
	}
	u, err := url.Parse(resolved.URL)
	if err != nil {
		return "", false, nil, &CallError{Message: "invalid url: " + err.Error(), Evaluation: false}
	}
	if err := appendQuery(u, spec.Query, data); err != nil {
		return "", false, nil, &CallError{Message: err.Error(), Evaluation: false}
	}
	return u.String(), cfg.Service != nil, resolved.TLS, nil
}

// clientForResolved applies a resolved TLS configuration to the netguard client.
// It deliberately keeps the netguard transport: the shared clientconfig builder
// knows how to reach an endpoint, while netguard is what enforces this
// deployment's destination policy.
func (e *HTTPExecutor) clientForResolved(
	inCluster bool, tlsConfig *tls.Config, key string,
) (*http.Client, error) {
	base := e.public
	if inCluster {
		base = e.cluster
	}
	if tlsConfig == nil {
		return base.Client(), nil
	}
	if cached, ok := e.tlsClients.Load(key); ok {
		return cached.(*http.Client), nil
	}
	transport, ok := base.Client().Transport.(*http.Transport)
	if !ok {
		return nil, &CallError{Message: "cannot apply TLS settings to this transport", Evaluation: false}
	}
	cloned := transport.Clone()
	cloned.TLSClientConfig = tlsConfig
	client := *base.Client()
	client.Transport = cloned
	e.tlsClients.Store(key, &client)
	return &client, nil
}

func (e *HTTPExecutor) buildURL(spec *model.HTTPCommandM, data TemplateData) (string, bool, error) {
	if spec.URL != "" {
		rendered, err := RenderTemplate("http.url", spec.URL, data)
		if err != nil {
			return "", false, &CallError{Message: err.Error(), Evaluation: false}
		}
		u, err := url.Parse(rendered)
		if err != nil {
			return "", false, &CallError{Message: "invalid url: " + err.Error(), Evaluation: false}
		}
		if err := appendQuery(u, spec.Query, data); err != nil {
			return "", false, &CallError{Message: err.Error(), Evaluation: false}
		}
		return u.String(), false, nil
	}

	if spec.Service == nil {
		return "", false, &CallError{Message: "command has neither url nor service", Evaluation: false}
	}
	// A service target resolves inside the cluster, so both parts have to be DNS
	// labels. Without the check a value such as "169.254.169.254" renders as a
	// link-local address instead of a Service name, and a dotted value leaves
	// the cluster's DNS namespace — both defeat the url path's allowlist.
	if problems := serviceHostProblems(spec.Service); len(problems) > 0 {
		return "", false, &CallError{
			Message:    "invalid service reference: " + strings.Join(problems, "; "),
			Evaluation: false,
		}
	}
	scheme := "http"
	port := spec.Service.Port
	if spec.CABundle != "" || spec.InsecureSkipTLSVerify {
		scheme = "https"
		if port == 0 {
			port = 443
		}
	} else if port == 0 {
		port = 80
	}
	host := spec.Service.Name
	if spec.Service.Namespace != "" {
		host = host + "." + spec.Service.Namespace
	}
	// Always qualify with .svc. The Service may live in another namespace, and
	// relying on the resolver's search list would make the target depend on the
	// pod's DNS configuration.
	host = host + ".svc"
	u := &url.URL{
		Scheme: scheme,
		Host:   fmt.Sprintf("%s:%d", host, port),
		Path:   spec.Service.Path,
	}
	if err := appendQuery(u, spec.Query, data); err != nil {
		return "", false, &CallError{Message: err.Error(), Evaluation: false}
	}
	return u.String(), true, nil
}

func appendQuery(u *url.URL, query map[string]string, data TemplateData) error {
	if len(query) == 0 {
		return nil
	}
	q := u.Query()
	for name, tmpl := range query {
		value, err := RenderTemplate("http.query."+name, tmpl, data)
		if err != nil {
			return err
		}
		q.Set(name, value)
	}
	u.RawQuery = q.Encode()
	return nil
}

func (e *HTTPExecutor) checkTarget(rawURL string, inCluster bool) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return &CallError{Message: "invalid url: " + err.Error(), Evaluation: false}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return &CallError{Message: "url must use http or https", Evaluation: false}
	}
	if u.Scheme == "http" && !inCluster && !e.cfg.AllowPlainHTTP {
		return &CallError{Message: "plain http is not permitted for an external endpoint", Evaluation: false}
	}
	if inCluster {
		// A service reference is an explicit in-cluster address; the host
		// allowlist is about external destinations and does not apply.
		//
		// The suffix is checked again even though buildURL produced it from DNS
		// labels: this is the one place that decides whether the private-address
		// policy is skipped, so it must not be reachable with any other host.
		if !strings.HasSuffix(u.Hostname(), ".svc") {
			return &CallError{
				Message:    "service target must be an in-cluster Service name",
				Evaluation: false,
			}
		}
		return nil
	}
	if len(e.cfg.AllowedHosts) == 0 {
		return nil
	}
	host := u.Hostname()
	for _, allowed := range e.cfg.AllowedHosts {
		if matchHost(allowed, host) {
			return nil
		}
	}
	return &CallError{
		Message:    fmt.Sprintf("host %q is not in custom-command.allowed-hosts", host),
		Evaluation: false,
	}
}

// serviceHostProblems reports why a Service reference cannot be rendered as an
// in-cluster DNS name. Both parts must be DNS labels, so the joined host is a
// name the cluster's DNS can resolve and nowhere else.
func serviceHostProblems(spec *model.ServiceReferenceM) []string {
	if spec.Name == "" {
		return []string{"name is required"}
	}
	var problems []string
	if errs := utilvalidation.IsDNS1123Label(spec.Name); len(errs) > 0 {
		problems = append(problems, "name: "+strings.Join(errs, ", "))
	}
	if spec.Namespace != "" {
		if errs := utilvalidation.IsDNS1123Label(spec.Namespace); len(errs) > 0 {
			problems = append(problems, "namespace: "+strings.Join(errs, ", "))
		}
	}
	return problems
}

func matchHost(allowed, host string) bool {
	allowed = strings.TrimSpace(strings.ToLower(allowed))
	host = strings.ToLower(host)
	if allowed == host {
		return true
	}
	if strings.HasPrefix(allowed, "*.") {
		suffix := allowed[1:] // ".example.com"
		return strings.HasSuffix(host, suffix) && host != strings.TrimPrefix(allowed, "*")
	}
	return false
}

func (e *HTTPExecutor) clientFor(spec *model.HTTPCommandM, inCluster bool) (*http.Client, error) {
	base := e.public
	if inCluster {
		base = e.cluster
	}
	if spec.CABundle == "" && !spec.InsecureSkipTLSVerify {
		// The process-wide client already carries the right policy; sharing it
		// is what lets connections be reused across calls.
		return base.Client(), nil
	}

	// inCluster is part of the key: the two fetchers dial under different
	// policies, and a client built for one must never serve the other.
	key := fmt.Sprintf("%t|%t|%s", inCluster, spec.InsecureSkipTLSVerify, spec.CABundle)
	if cached, ok := e.tlsClients.Load(key); ok {
		return cached.(*http.Client), nil
	}

	client, err := buildTLSClient(base.Client(), spec)
	if err != nil {
		return nil, err
	}
	e.tlsClients.Store(key, client)
	return client, nil
}

// buildTLSClient is clientFor's uncached half.
func buildTLSClient(base *http.Client, spec *model.HTTPCommandM) (*http.Client, error) {
	transport, ok := base.Transport.(*http.Transport)
	if !ok {
		return nil, &CallError{Message: "cannot apply TLS settings to this transport", Evaluation: false}
	}
	cloned := transport.Clone()
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if spec.InsecureSkipTLSVerify {
		//nolint:gosec // explicit, opt-in, and warned about by validation.
		tlsConfig.InsecureSkipVerify = true
	}
	if spec.CABundle != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(spec.CABundle)) {
			return nil, &CallError{Message: "caBundle is not valid PEM", Evaluation: false}
		}
		tlsConfig.RootCAs = pool
	}
	cloned.TLSClientConfig = tlsConfig

	client := *base
	client.Transport = cloned
	return &client, nil
}

// OutputTemplateData is what an output template sees.
type OutputTemplateData struct {
	Status int
	Header http.Header
	Body   string
	JSON   any
	Params map[string]any
}

// RenderOutput turns a call result into the text the chat surface shows.
func RenderOutput(
	spec *model.CommandOutputM, status int, header http.Header, body []byte, params map[string]any,
) (string, error) {
	var parsed any
	if json.Valid(body) {
		_ = json.Unmarshal(body, &parsed)
	}
	if spec == nil {
		if parsed != nil {
			if pretty, err := json.MarshalIndent(parsed, "", "  "); err == nil {
				return truncate(string(pretty), ResultLimit), nil
			}
		}
		return truncate(string(body), ResultLimit), nil
	}
	if len(spec.Schema) > 0 {
		if parsed == nil {
			return "", fmt.Errorf("result does not match schema: result is not JSON")
		}
		if err := validateSchema(spec.Schema, parsed); err != nil {
			return "", fmt.Errorf("result does not match schema: %w", err)
		}
	}
	switch spec.Format {
	case "", "text":
		return truncate(string(body), ResultLimit), nil
	case "json":
		if parsed == nil {
			return truncate(string(body), ResultLimit), nil
		}
		pretty, err := json.MarshalIndent(parsed, "", "  ")
		if err != nil {
			return truncate(string(body), ResultLimit), nil
		}
		return truncate(string(pretty), ResultLimit), nil
	case "template":
		out, err := RenderWith("output.template", spec.Template, OutputTemplateData{
			Status: status,
			Header: header,
			Body:   string(body),
			JSON:   parsed,
			Params: params,
		})
		if err != nil {
			return "", err
		}
		return truncate(out, ResultLimit), nil
	default:
		return truncate(string(body), ResultLimit), nil
	}
}

// TruncateForChat bounds a tool or prompt result before it reaches a chat card.
func TruncateForChat(s string, limit int) string { return truncate(s, limit) }

// truncate keeps the tail off at a UTF-8 boundary. A byte slice that ends
// mid-rune is invalid UTF-8, and a chat card that receives one may reject the
// whole message rather than show a shortened one.
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := s[:limit]
	// Back off to a rune boundary. Only the last rune can be partial, and
	// DecodeLastRuneInString finds it in constant time.
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r != utf8.RuneError || size > 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return cut + "\n… (truncated)"
}
