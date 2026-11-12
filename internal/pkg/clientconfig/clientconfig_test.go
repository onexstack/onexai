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

package clientconfig

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

func strPtr(s string) *string { return &s }

func int32Ptr(v int32) *int32 { return &v }

func TestResolveRequiresExactlyOneEndpoint(t *testing.T) {
	if _, err := Resolve(agentv1alpha1.ClientConfig{}); !errors.Is(err, ErrNoEndpoint) {
		t.Fatalf("got %v, want ErrNoEndpoint", err)
	}

	cfg := agentv1alpha1.ClientConfig{
		WebhookClientConfig: admissionregistrationv1.WebhookClientConfig{
			URL:     strPtr("https://example.com/hook"),
			Service: &admissionregistrationv1.ServiceReference{Namespace: "ns", Name: "svc"},
		},
	}
	if _, err := Resolve(cfg); !errors.Is(err, ErrBothEndpoints) {
		t.Fatalf("got %v, want ErrBothEndpoints", err)
	}
}

func TestResolveURL(t *testing.T) {
	resolved, err := Resolve(agentv1alpha1.ClientConfig{
		WebhookClientConfig: admissionregistrationv1.WebhookClientConfig{
			URL: strPtr("https://example.com/hook?x=1"),
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.URL != "https://example.com/hook?x=1" {
		t.Fatalf("got %q", resolved.URL)
	}
	if resolved.TLS != nil {
		t.Fatal("system trust roots should leave TLS nil")
	}
}

func TestResolveRejectsANonHTTPURL(t *testing.T) {
	_, err := Resolve(agentv1alpha1.ClientConfig{
		WebhookClientConfig: admissionregistrationv1.WebhookClientConfig{
			URL: strPtr("ftp://example.com/hook"),
		},
	})
	if err == nil {
		t.Fatal("expected an error for a non-http(s) URL")
	}
}

func TestResolveServiceUsesInClusterDNS(t *testing.T) {
	resolved, err := Resolve(agentv1alpha1.ClientConfig{
		WebhookClientConfig: admissionregistrationv1.WebhookClientConfig{
			Service: &admissionregistrationv1.ServiceReference{
				Namespace: "onexai",
				Name:      "policy",
				Port:      int32Ptr(8443),
				Path:      strPtr("/authorize"),
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.URL != "http://policy.onexai.svc:8443/authorize" {
		t.Fatalf("got %q", resolved.URL)
	}
}

func TestResolveServiceDefaultsThePortFromTheScheme(t *testing.T) {
	plain, err := Resolve(agentv1alpha1.ClientConfig{
		WebhookClientConfig: admissionregistrationv1.WebhookClientConfig{
			Service: &admissionregistrationv1.ServiceReference{Namespace: "ns", Name: "svc"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plain.URL != "http://svc.ns.svc:80" {
		t.Fatalf("got %q, want the http default port", plain.URL)
	}

	secure, err := Resolve(agentv1alpha1.ClientConfig{
		WebhookClientConfig: admissionregistrationv1.WebhookClientConfig{
			Service: &admissionregistrationv1.ServiceReference{Namespace: "ns", Name: "svc"},
		},
		InsecureSkipTLSVerify: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if secure.URL != "https://svc.ns.svc:443" {
		t.Fatalf("got %q, want the https default port", secure.URL)
	}
}

func TestResolveValidatesTheServiceReference(t *testing.T) {
	for _, ref := range []admissionregistrationv1.ServiceReference{
		{Namespace: "", Name: "svc"},
		{Namespace: "ns", Name: ""},
		{Namespace: "Bad_NS", Name: "svc"},
		{Namespace: "ns", Name: "svc", Port: int32Ptr(70000)},
	} {
		_, err := Resolve(agentv1alpha1.ClientConfig{
			WebhookClientConfig: admissionregistrationv1.WebhookClientConfig{Service: &ref},
		})
		if err == nil {
			t.Fatalf("expected an error for %+v", ref)
		}
	}
}

func TestResolveCABundle(t *testing.T) {
	cfg := agentv1alpha1.ClientConfig{
		WebhookClientConfig: admissionregistrationv1.WebhookClientConfig{
			URL:      strPtr("https://example.com/hook"),
			CABundle: caPEM(t),
		},
	}
	resolved, err := Resolve(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.TLS == nil || resolved.TLS.RootCAs == nil {
		t.Fatal("expected a certificate pool from caBundle")
	}
}

func TestResolveRejectsAnInvalidCABundle(t *testing.T) {
	_, err := Resolve(agentv1alpha1.ClientConfig{
		WebhookClientConfig: admissionregistrationv1.WebhookClientConfig{
			URL:      strPtr("https://example.com/hook"),
			CABundle: []byte("not a certificate"),
		},
	})
	if err == nil {
		t.Fatal("expected an error for an invalid caBundle")
	}
}

func TestHTTPClientUsesTheConfiguredTimeout(t *testing.T) {
	req := agentv1alpha1.ClientConfig{
		WebhookClientConfig: admissionregistrationv1.WebhookClientConfig{
			URL: strPtr("https://example.com/hook"),
		},
		TimeoutSeconds: 3,
	}
	client, err := HTTPClient(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.Timeout != 3*time.Second {
		t.Fatalf("got timeout %v, want 3s", client.Timeout)
	}
}

func TestHTTPClientIsCached(t *testing.T) {
	req := agentv1alpha1.ClientConfig{
		WebhookClientConfig: admissionregistrationv1.WebhookClientConfig{
			URL: strPtr("https://cached.example.com/hook"),
		},
	}
	first, err := HTTPClient(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, err := HTTPClient(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if first != second {
		t.Fatal("expected the same client for the same endpoint and TLS inputs")
	}
}

func caPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "clientconfig-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
