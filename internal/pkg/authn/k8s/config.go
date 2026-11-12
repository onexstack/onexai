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

package k8s

import (
	"context"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	apiserverapi "k8s.io/apiserver/pkg/apis/apiserver"
	apiserverinstall "k8s.io/apiserver/pkg/apis/apiserver/install"
	apiserverconfigv1 "k8s.io/apiserver/pkg/apis/apiserver/v1"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/request/bearertoken"
	requestunion "k8s.io/apiserver/pkg/authentication/request/union"
	oidcauthenticator "k8s.io/apiserver/plugin/pkg/authenticator/token/oidc"
	"sigs.k8s.io/yaml"
)

var (
	authenticationScheme = runtime.NewScheme()
	authenticationCodecs serializer.CodecFactory
)

func init() {
	apiserverinstall.Install(authenticationScheme)
	authenticationCodecs = serializer.NewCodecFactory(authenticationScheme)
}

// LoadAuthenticationConfiguration reads an apiserver.config.k8s.io/v1
// AuthenticationConfiguration file.
//
// The file is the same one a kube-apiserver consumes: decoding it through the
// Kubernetes scheme (rather than a local struct) is what makes the platform read
// the exact fields, aliases and version rules the upstream type defines.
func LoadAuthenticationConfiguration(path string) (*apiserverconfigv1.AuthenticationConfiguration, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read authentication config %q: %w", path, err)
	}
	jsonData, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, fmt.Errorf("convert authentication config %q: %w", path, err)
	}
	obj, gvk, err := authenticationCodecs.UniversalDeserializer().Decode(jsonData, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("decode authentication config %q: %w", path, err)
	}
	cfg, ok := obj.(*apiserverconfigv1.AuthenticationConfiguration)
	if !ok {
		return nil, fmt.Errorf("authentication config %q decoded to %v, want AuthenticationConfiguration", path, gvk)
	}
	return cfg, nil
}

// NewTokenAuthenticator builds the request authenticator described by cfg.
//
// It reuses k8s.io/apiserver's OIDC token authenticator and request union: the
// discovery fetch, JWKS refresh, CEL claim mapping and validation all stay
// upstream. A nil config (OIDC not configured) returns a nil authenticator, and
// the caller falls back to its existing credential path.
func NewTokenAuthenticator(
	ctx context.Context, cfg *apiserverconfigv1.AuthenticationConfiguration, apiServerID string,
) (authenticator.Request, error) {
	if cfg == nil {
		return nil, nil
	}

	// The upstream OIDC plugin consumes the internal hub type; the file is
	// decoded into the versioned v1 type. The scheme performs the conversion, so
	// the two stay in lockstep with whatever the Kubernetes version defines.
	internal := &apiserverapi.AuthenticationConfiguration{}
	if err := authenticationScheme.Convert(cfg, internal, nil); err != nil {
		return nil, fmt.Errorf("convert authentication config: %w", err)
	}

	handlers := make([]authenticator.Request, 0, len(internal.JWT))
	for i := range internal.JWT {
		jwtCfg := internal.JWT[i]
		opts := oidcauthenticator.Options{
			JWTAuthenticator:     jwtCfg,
			APIServerID:          apiServerID,
			SupportedSigningAlgs: []string{"RS256"},
		}
		if jwtCfg.Issuer.CertificateAuthority != "" {
			opts.CAContentProvider = staticCA(jwtCfg.Issuer.CertificateAuthority)
		}
		tokenAuthenticator, err := oidcauthenticator.New(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("authentication config jwt[%d]: %w", i, err)
		}
		handlers = append(handlers, bearertoken.New(tokenAuthenticator))
	}
	if len(handlers) == 0 {
		return nil, nil
	}
	return requestunion.New(handlers...), nil
}

// staticCA is the CAContentProvider shape the upstream OIDC authenticator
// consumes. The issuer's certificateAuthority field is a static PEM block, so
// "dynamic" here is the same bytes every time.
type staticCA string

var _ interface{ CurrentCABundleContent() []byte } = staticCA("")

func (c staticCA) CurrentCABundleContent() []byte { return []byte(c) }
