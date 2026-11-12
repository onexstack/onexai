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

package authreview

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/user"

	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/authz"
)

const testSecret = "test-jwt-secret"

func sign(t *testing.T, claims *bearer.Claims) string {
	t.Helper()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
	require.NoError(t, err)
	return token
}

func TestTokenReviewAuthenticatesPlatformToken(t *testing.T) {
	token := sign(t, &bearer.Claims{
		UserID:   "go.kong",
		Name:     "Kong",
		Provider: "feishu",
		IsAdmin:  true,
		IsSRE:    true,
	})

	b := New(Config{JWTSecret: testSecret}, nil, nil)
	out := b.TokenReview(context.Background(), &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{Token: token},
	})

	require.True(t, out.Status.Authenticated)
	assert.Empty(t, out.Status.Error)
	assert.Equal(t, "go.kong", out.Status.User.Username)
	assert.Equal(t, "go.kong", out.Status.User.UID)
	assert.Contains(t, out.Status.User.Groups, "system:authenticated")
	assert.Contains(t, out.Status.User.Groups, "system:masters")
	assert.Contains(t, out.Status.User.Groups, "onexai:admin")
	assert.Contains(t, out.Status.User.Groups, "onexai:sre")
	assert.Equal(t, authenticationv1.ExtraValue{"Kong"}, out.Status.User.Extra["onexai.io/name"])
	assert.Equal(t, authenticationv1.ExtraValue{"feishu"}, out.Status.User.Extra["onexai.io/provider"])
}

func TestTokenReviewRejectsInvalidToken(t *testing.T) {
	b := New(Config{JWTSecret: testSecret}, nil, nil)
	out := b.TokenReview(context.Background(), &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{Token: "not-a-token"},
	})

	assert.False(t, out.Status.Authenticated)
	assert.NotEmpty(t, out.Status.Error)
}

func TestTokenReviewRequiresToken(t *testing.T) {
	b := New(Config{JWTSecret: testSecret}, nil, nil)
	out := b.TokenReview(context.Background(), &authenticationv1.TokenReview{})
	assert.False(t, out.Status.Authenticated)
	assert.NotEmpty(t, out.Status.Error)
}

func TestTokenReviewEchoesAudiences(t *testing.T) {
	token := sign(t, &bearer.Claims{UserID: "go.kong"})
	b := New(Config{JWTSecret: testSecret}, nil, nil)
	out := b.TokenReview(context.Background(), &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{Token: token, Audiences: []string{"onexai"}},
	})
	assert.Equal(t, []string{"onexai"}, out.Status.Audiences)
}

func TestSubjectAccessReviewAllow(t *testing.T) {
	var seen authz.AuthorizationRequest
	coordinator := authz.NewCoordinator(
		authz.AuthorizerFunc(func(_ context.Context, req authz.AuthorizationRequest) (authz.Decision, string, error) {
			seen = req
			return authz.DecisionAllow, "matched allow rule", nil
		}),
		authz.AuthorizerFunc(func(context.Context, authz.AuthorizationRequest) (authz.Decision, string, error) {
			return authz.DecisionNoOpinion, "", nil
		}),
		true,
	)

	b := New(Config{JWTSecret: testSecret}, nil, coordinator)
	out := b.SubjectAccessReview(context.Background(), &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:   "go.kong",
			Groups: []string{"oai-chat"},
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Verb:     "get",
				Resource: "agents",
				Name:     "helper",
			},
		},
	})

	require.True(t, out.Status.Allowed)
	assert.False(t, out.Status.Denied)
	assert.Contains(t, out.Status.Reason, "matched allow rule")

	assert.Equal(t, authz.SubjectKindUser, seen.Subject.Kind)
	assert.Equal(t, "go.kong", seen.Subject.UserID)
	assert.Equal(t, "oai-chat", seen.Subject.Extra["group_codes"])
	assert.Equal(t, authz.VerbRead, seen.Verb)
	assert.Equal(t, authz.ResourceKindAgent, seen.Resource.Kind)
	assert.Equal(t, "helper", seen.Resource.Name)
	assert.Equal(t, "helper", seen.AgentName)
}

func TestSubjectAccessReviewWriteVerb(t *testing.T) {
	var seen authz.AuthorizationRequest
	coordinator := authz.NewCoordinator(
		authz.AuthorizerFunc(func(_ context.Context, req authz.AuthorizationRequest) (authz.Decision, string, error) {
			seen = req
			return authz.DecisionAllow, "", nil
		}),
		authz.AuthorizerFunc(func(context.Context, authz.AuthorizationRequest) (authz.Decision, string, error) {
			return authz.DecisionNoOpinion, "", nil
		}),
		true,
	)

	b := New(Config{JWTSecret: testSecret}, nil, coordinator)
	b.SubjectAccessReview(context.Background(), &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User: "go.kong",
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Verb:     "update",
				Resource: "mcpservers",
				Name:     "github",
			},
		},
	})

	assert.Equal(t, authz.VerbWrite, seen.Verb)
	assert.Equal(t, authz.ResourceKindMCP, seen.Resource.Kind)
}

func TestSubjectAccessReviewDeny(t *testing.T) {
	coordinator := authz.NewCoordinator(
		authz.AuthorizerFunc(func(context.Context, authz.AuthorizationRequest) (authz.Decision, string, error) {
			return authz.DecisionDeny, "denied by rule", nil
		}),
		authz.AuthorizerFunc(func(context.Context, authz.AuthorizationRequest) (authz.Decision, string, error) {
			return authz.DecisionNoOpinion, "", nil
		}),
		true,
	)
	b := New(Config{JWTSecret: testSecret}, nil, coordinator)
	out := b.SubjectAccessReview(context.Background(), &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User: "go.kong",
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Verb: "get", Resource: "agents", Name: "helper",
			},
		},
	})
	require.True(t, out.Status.Denied)
	assert.False(t, out.Status.Allowed)
	assert.Contains(t, out.Status.Reason, "denied by rule")
}

func TestSubjectAccessReviewNoOpinionFailsClosed(t *testing.T) {
	coordinator := authz.NewCoordinator(
		authz.AuthorizerFunc(func(context.Context, authz.AuthorizationRequest) (authz.Decision, string, error) {
			return authz.DecisionNoOpinion, "no matching rule", nil
		}),
		authz.AuthorizerFunc(func(context.Context, authz.AuthorizationRequest) (authz.Decision, string, error) {
			return authz.DecisionNoOpinion, "", nil
		}),
		true,
	)
	b := New(Config{JWTSecret: testSecret}, nil, coordinator)
	out := b.SubjectAccessReview(context.Background(), &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User: "go.kong",
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Verb: "get", Resource: "agents", Name: "helper",
			},
		},
	})
	require.True(t, out.Status.Denied)
	assert.Contains(t, out.Status.Reason, "NoOpinion (fail-closed)")
}

func TestSubjectAccessReviewNonResourceIsDenied(t *testing.T) {
	b := New(Config{JWTSecret: testSecret}, nil, nil)
	out := b.SubjectAccessReview(context.Background(), &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:                  "go.kong",
			NonResourceAttributes: &authorizationv1.NonResourceAttributes{Verb: "get", Path: "/healthz"},
		},
	})
	require.True(t, out.Status.Denied)
	assert.Contains(t, out.Status.Reason, "non-resource")
}

func TestSubjectAccessReviewUnsupportedVerb(t *testing.T) {
	b := New(Config{JWTSecret: testSecret}, nil, nil)
	out := b.SubjectAccessReview(context.Background(), &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User: "go.kong",
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Verb: "escalate", Resource: "agents", Name: "helper",
			},
		},
	})
	require.True(t, out.Status.Denied)
	assert.Contains(t, out.Status.Reason, "unsupported verb")
}

func TestSubjectAccessReviewRequiresUser(t *testing.T) {
	b := New(Config{JWTSecret: testSecret}, nil, nil)
	out := b.SubjectAccessReview(context.Background(), &authorizationv1.SubjectAccessReview{})
	require.True(t, out.Status.Denied)
	assert.Equal(t, "user is required", out.Status.Reason)
}

func TestSARResourceKindMapsPluralAndSingular(t *testing.T) {
	tests := []struct{ resource, want string }{
		{"agents", "agent"},
		{"agent", "agent"},
		{"mcpservers", "mcp"},
		{"mcps", "mcp"},
		{"mcp", "mcp"},
		{"knowledgebases", "knowledgebase"},
		{"memories", "memory"},
		{"models", "model"},
		{"customcommands", "customcommand"},
		{"scheduledruns", "scheduledrun"},
		{"webhooks", "webhook"},
		{"runtimeprofiles", "runtimeprofile"},
		{"sandboxtemplates", "sandboxtemplate"},
		{"namespaces", "namespace"},
		{"roles", "role"},
		{"clusters", "cluster"},
		// An unknown kind is passed through: the policy vocabulary is open, so
		// a new kind must not need a code change to be reviewable.
		{"futurekind", "futurekind"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, sarResourceKind(tt.resource), tt.resource)
	}
}

func TestSelfSubjectUsesAuthenticatedClaims(t *testing.T) {
	claims := &bearer.Claims{UserID: "go.kong", IsAdmin: false}
	b := New(Config{JWTSecret: testSecret}, nil, nil)
	user, groups := b.SelfSubject(context.Background(), claims)
	assert.Equal(t, "go.kong", user)
	assert.Equal(t, []string{"system:authenticated"}, groups)
}

func TestInfoReportsConfiguredToken(t *testing.T) {
	configured := New(Config{JWTSecret: testSecret, WebhookTokenConfigured: true}, nil, nil)
	assert.True(t, configured.Info().CallerAuthConfigured)
	assert.Equal(t, TokenReviewPath, configured.Info().TokenReviewPath)
	assert.Equal(t, SubjectAccessReviewPath, configured.Info().SubjectAccessReviewPath)

	notConfigured := New(Config{JWTSecret: testSecret}, nil, nil)
	assert.False(t, notConfigured.Info().CallerAuthConfigured)
}

func TestTokenReviewUsesTheKubernetesAuthenticator(t *testing.T) {
	b := New(Config{
		JWTSecret: testSecret,
		TokenAuthenticator: authenticator.RequestFunc(func(req *http.Request) (*authenticator.Response, bool, error) {
			if req.Header.Get("Authorization") != "Bearer oidc-token" {
				return nil, false, nil
			}
			return &authenticator.Response{User: &user.DefaultInfo{
				Name:   "oidc-user",
				UID:    "uid-1",
				Groups: []string{"system:authenticated"},
				Extra:  map[string][]string{"onexai.io/name": {"OIDC User"}},
			}}, true, nil
		}),
	}, nil, nil)

	out := b.TokenReview(context.Background(), &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{Token: "oidc-token"},
	})

	require.True(t, out.Status.Authenticated)
	assert.Equal(t, "oidc-user", out.Status.User.Username)
	assert.Equal(t, "uid-1", out.Status.User.UID)
	assert.Contains(t, out.Status.User.Groups, "system:authenticated")
	assert.Equal(t, authenticationv1.ExtraValue{"OIDC User"}, out.Status.User.Extra["onexai.io/name"])
}

func TestTokenReviewFallsBackToHS256(t *testing.T) {
	token := sign(t, &bearer.Claims{UserID: "go.kong"})
	b := New(Config{
		JWTSecret: testSecret,
		TokenAuthenticator: authenticator.RequestFunc(func(*http.Request) (*authenticator.Response, bool, error) {
			return nil, false, nil
		}),
	}, nil, nil)

	out := b.TokenReview(context.Background(), &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{Token: token},
	})
	require.True(t, out.Status.Authenticated)
	assert.Equal(t, "go.kong", out.Status.User.Username)
}

func TestTokenReviewExpiredToken(t *testing.T) {
	token := sign(t, &bearer.Claims{
		UserID: "go.kong",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
		},
	})
	b := New(Config{JWTSecret: testSecret}, nil, nil)
	out := b.TokenReview(context.Background(), &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{Token: token},
	})
	assert.False(t, out.Status.Authenticated)
	assert.NotEmpty(t, out.Status.Error)
}
