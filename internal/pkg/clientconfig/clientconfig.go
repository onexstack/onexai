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

// Package clientconfig turns the platform's Kubernetes-shaped ClientConfig into
// an endpoint and an HTTP client.
//
// It is the one implementation of "where is this external API and how do I
// reach it". Every consumer — run-lifecycle webhooks, custom commands, MCP
// servers, A2A peers, runtime profiles — should use it, so a fix to Service
// resolution, CA handling or timeout semantics applies everywhere instead of
// to the one caller somebody remembered.
package clientconfig

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

const (
	// defaultTimeout bounds one request when ClientConfig.TimeoutSeconds is 0.
	defaultTimeout = 30 * time.Second
	// serviceSuffix is the in-cluster DNS suffix a Service reference resolves to.
	serviceSuffix = ".svc"
)

// ErrNoEndpoint reports a ClientConfig that names neither a URL nor a Service.
var ErrNoEndpoint = errors.New("clientconfig: exactly one of url or service is required")

// ErrBothEndpoints reports a ClientConfig that names both a URL and a Service.
var ErrBothEndpoints = errors.New("clientconfig: url and service are mutually exclusive")

// Resolved is the answer a ClientConfig resolves to.
type Resolved struct {
	// URL is the absolute endpoint a request is sent to.
	URL string
	// TLS is nil for a plain-HTTP endpoint or when the system trust roots are
	// sufficient.
	TLS *tls.Config
}

// Resolve validates cfg and returns the endpoint it addresses.
//
// The rules mirror admissionregistration.k8s.io/v1 WebhookClientConfig where
// they overlap: exactly one of url/service, a Service reference is
// namespace/name[/port][/path], and caBundle is PEM. The platform adds one
// deliberate difference: a Service without a CA bundle resolves to http (the
// in-cluster default every existing caller used), while a CA bundle or
// insecureSkipTLSVerify selects https.
func Resolve(cfg agentv1alpha1.ClientConfig) (Resolved, error) {
	rawURL := ""
	if cfg.URL != nil {
		rawURL = strings.TrimSpace(*cfg.URL)
	}
	hasURL := rawURL != ""
	hasService := cfg.Service != nil

	switch {
	case hasURL && hasService:
		return Resolved{}, ErrBothEndpoints
	case !hasURL && !hasService:
		return Resolved{}, ErrNoEndpoint
	}

	tlsConfig, err := buildTLS(cfg)
	if err != nil {
		return Resolved{}, err
	}

	if hasURL {
		u, err := url.Parse(rawURL)
		if err != nil {
			return Resolved{}, fmt.Errorf("clientconfig: parse url: %w", err)
		}
		if u.Scheme != "https" && u.Scheme != "http" {
			return Resolved{}, fmt.Errorf("clientconfig: url %q must use http or https", rawURL)
		}
		if u.Host == "" {
			return Resolved{}, fmt.Errorf("clientconfig: url %q has no host", rawURL)
		}
		return Resolved{URL: u.String(), TLS: tlsConfig}, nil
	}

	svc := cfg.Service
	if errs := validation.IsDNS1123Label(svc.Namespace); len(errs) > 0 {
		return Resolved{}, fmt.Errorf("clientconfig: service namespace %q is invalid: %s", svc.Namespace, strings.Join(errs, "; "))
	}
	if errs := validation.IsDNS1123Label(svc.Name); len(errs) > 0 {
		return Resolved{}, fmt.Errorf("clientconfig: service name %q is invalid: %s", svc.Name, strings.Join(errs, "; "))
	}
	port := 0
	if svc.Port != nil {
		port = int(*svc.Port)
	}
	if port < 0 || port > 65535 {
		return Resolved{}, fmt.Errorf("clientconfig: service port %d is out of range", port)
	}

	scheme := "http"
	if tlsConfig != nil {
		scheme = "https"
	}
	if port == 0 {
		if scheme == "https" {
			port = 443
		} else {
			port = 80
		}
	}
	path := ""
	if svc.Path != nil {
		path = *svc.Path
	}
	host := net.JoinHostPort(svc.Name+"."+svc.Namespace+serviceSuffix, strconv.Itoa(port))
	u := url.URL{Scheme: scheme, Host: host, Path: path}
	return Resolved{URL: u.String(), TLS: tlsConfig}, nil
}

// HTTPClient returns a client for cfg, cached by the endpoint and TLS inputs so
// repeated calls do not rebuild certificate pools.
func HTTPClient(cfg agentv1alpha1.ClientConfig) (*http.Client, error) {
	resolved, err := Resolve(cfg)
	if err != nil {
		return nil, err
	}

	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	key := cacheKey(resolved, cfg, timeout)
	if cached, ok := clients.Load(key); ok {
		return cached.(*http.Client), nil
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = resolved.TLS
	client := &http.Client{Transport: transport, Timeout: timeout}
	clients.Store(key, client)
	return client, nil
}

// Validate checks the parts of cfg that do not require building a client.
func Validate(cfg agentv1alpha1.ClientConfig) error {
	_, err := Resolve(cfg)
	return err
}

var clients sync.Map // map[string]*http.Client

func cacheKey(resolved Resolved, cfg agentv1alpha1.ClientConfig, timeout time.Duration) string {
	sum := sha256.Sum256(cfg.CABundle)
	return fmt.Sprintf("%s|%x|%t|%s", resolved.URL, sum, cfg.InsecureSkipTLSVerify, timeout)
}

func buildTLS(cfg agentv1alpha1.ClientConfig) (*tls.Config, error) {
	if len(cfg.CABundle) == 0 && !cfg.InsecureSkipTLSVerify {
		return nil, nil
	}
	out := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.InsecureSkipTLSVerify,
	}
	if len(cfg.CABundle) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(cfg.CABundle) {
			return nil, errors.New("clientconfig: caBundle is not valid PEM")
		}
		out.RootCAs = pool
	}
	return out, nil
}
