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

package controlplanestore

import (
	"context"
	"strings"
	"testing"

	"github.com/onexstack/onexstack/pkg/store/where"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/pkg/declclient"
)

func TestSecretStoreCreateAndGet(t *testing.T) {
	kube := fake.NewSimpleClientset()
	store := NewSecretStore(&declclient.Client{KubeClient: kube}, "helper-public")

	row := &model.SecretM{SecretID: "svc-chat", SecretKey: "sk-1", Description: "chat service"}
	if err := store.Create(context.Background(), row); err != nil {
		t.Fatalf("Create: %v", err)
	}

	secret, err := kube.CoreV1().Secrets("helper-public").Get(context.Background(), "svc-chat", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get core secret: %v", err)
	}
	if string(secret.Data[secretKeyDataKey]) != "sk-1" {
		t.Fatalf("data = %q", secret.Data[secretKeyDataKey])
	}
	if secret.Labels[secretKindLabel] != secretKindAKSK {
		t.Fatalf("labels = %v", secret.Labels)
	}

	got, err := store.Get(context.Background(), where.F("secret_id", "svc-chat"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || got.SecretKey != "sk-1" || got.Description != "chat service" {
		t.Fatalf("row = %+v", got)
	}
}

func TestSecretStoreHidesWebhookManagedTokens(t *testing.T) {
	kube := fake.NewSimpleClientset()
	store := NewSecretStore(&declclient.Client{KubeClient: kube}, "helper-public")

	row := &model.SecretM{
		SecretID:    "webhook-hook-token",
		SecretKey:   "tok",
		Description: "onexai-managed webhook token: hook",
		Kind:        model.SecretKindWebhook,
	}
	if err := store.Create(context.Background(), row); err != nil {
		t.Fatalf("Create: %v", err)
	}

	secret, err := kube.CoreV1().Secrets("helper-public").Get(context.Background(), "webhook-hook-token", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get core secret: %v", err)
	}
	if secret.Labels[secretKindLabel] != model.SecretKindWebhook {
		t.Fatalf("labels = %v, want kind %q", secret.Labels, model.SecretKindWebhook)
	}

	// The write-through cache must not leak it into the AK/SK list.
	total, list, err := store.List(context.Background(), where.NewWhere().O(0).L(10))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 0 || len(list) != 0 {
		t.Fatalf("List = %d/%+v, want no rows", total, list)
	}

	// The dispatcher still resolves it by id.
	got, err := store.Get(context.Background(), where.F("secret_id", "webhook-hook-token"))
	if err != nil || got == nil || got.SecretKey != "tok" || got.Kind != model.SecretKindWebhook {
		t.Fatalf("Get = %+v, %v", got, err)
	}
}

func TestSecretStoreListReturnsOnlyAKSKSecrets(t *testing.T) {
	kube := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "model-api-key",
			Namespace: "helper-public",
			Labels:    map[string]string{"onexai.io/secret-kind": "model"},
		},
		Data: map[string][]byte{"api-key": []byte("should-not-leak")},
	})
	store := NewSecretStore(&declclient.Client{KubeClient: kube}, "helper-public")
	if err := store.Create(context.Background(), &model.SecretM{SecretID: "svc-chat", SecretKey: "sk-1"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	total, list, err := store.List(context.Background(), where.NewWhere().O(0).L(10))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(list) != 1 || list[0].SecretID != "svc-chat" {
		t.Fatalf("List = %d/%+v, want only svc-chat", total, list)
	}
}

func TestSecretStoreEncodesInvalidID(t *testing.T) {
	kube := fake.NewSimpleClientset()
	store := NewSecretStore(&declclient.Client{KubeClient: kube}, "helper-public")

	if err := store.Create(context.Background(), &model.SecretM{SecretID: "Svc_Chat", SecretKey: "sk-1"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	list, err := kube.CoreV1().Secrets("helper-public").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("List core secrets: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("created %d secrets, want 1", len(list.Items))
	}
	secret := list.Items[0]
	if secret.Name == "Svc_Chat" {
		t.Fatal("an invalid ID must not be used as the Secret name")
	}
	if secret.Annotations[secretIDAnnotation] != "Svc_Chat" {
		t.Fatalf("original ID annotation = %q", secret.Annotations[secretIDAnnotation])
	}
	if !strings.Contains(secret.Name, "svc-chat") {
		t.Fatalf("encoded name = %q, want a sanitized prefix", secret.Name)
	}

	got, err := store.Get(context.Background(), where.F("secret_id", "Svc_Chat"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || got.SecretKey != "sk-1" {
		t.Fatalf("row = %+v", got)
	}
}

func TestSecretStoreUpdateKeepsKeyWhenOmitted(t *testing.T) {
	kube := fake.NewSimpleClientset()
	store := NewSecretStore(&declclient.Client{KubeClient: kube}, "helper-public")
	if err := store.Create(context.Background(), &model.SecretM{SecretID: "svc-chat", SecretKey: "old"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	row, err := store.Get(context.Background(), where.F("secret_id", "svc-chat"))
	if err != nil || row == nil {
		t.Fatalf("Get: %v/%v", row, err)
	}
	row.SecretKey = ""
	row.Description = "rotated description"
	if err := store.Update(context.Background(), row); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := store.Get(context.Background(), where.F("secret_id", "svc-chat"))
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got.SecretKey != "old" {
		t.Fatalf("key = %q, want the stored one", got.SecretKey)
	}
	if got.Description != "rotated description" {
		t.Fatalf("description = %q", got.Description)
	}
}

func TestSecretStoreDeleteIsIdempotent(t *testing.T) {
	kube := fake.NewSimpleClientset()
	store := NewSecretStore(&declclient.Client{KubeClient: kube}, "helper-public")
	if err := store.Create(context.Background(), &model.SecretM{SecretID: "svc-chat", SecretKey: "sk-1"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := store.Delete(context.Background(), where.F("secret_id", "svc-chat")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Delete(context.Background(), where.F("secret_id", "svc-chat")); err != nil {
		t.Fatalf("second Delete should be a no-op: %v", err)
	}
	got, err := store.Get(context.Background(), where.F("secret_id", "svc-chat"))
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if got != nil {
		t.Fatalf("deleted secret is still readable: %+v", got)
	}
}
