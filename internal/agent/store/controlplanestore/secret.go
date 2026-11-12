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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/utils/ptr"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// secretReader reads credentials from the Secret cache the control-plane client
// already maintains.
//
// It used to keep its own 30-second TTL cache and issue a GET per miss, which
// meant a rotated credential could stay stale for half a minute and every run
// paid an API call per model. The informer cache has neither problem: reads are
// local, and a rotation shows up as soon as the watch delivers it.
type secretReader struct {
	secrets corelisters.SecretLister
}

func newSecretReader(secrets corelisters.SecretLister) *secretReader {
	return &secretReader{secrets: secrets}
}

// get returns the named key's value, or "" when the Secret or the key is
// absent. A missing credential is a configuration state, not an error: the
// runtime reports it through the model's health conditions.
func (r *secretReader) get(_ context.Context, namespace, name, key string) (string, error) {
	secret, err := r.secrets.Secrets(namespace).Get(name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	return string(secret.Data[key]), nil
}

// all returns the Secret's data as strings, or nil when it does not exist.
func (r *secretReader) all(_ context.Context, namespace, name string) (map[string]string, error) {
	data, err := r.read(context.Background(), namespace, name)
	if err != nil || data == nil {
		return nil, err
	}
	out := make(map[string]string, len(data))
	for key, value := range data {
		out[key] = string(value)
	}
	return out, nil
}

// read returns the Secret's raw data.
func (r *secretReader) read(_ context.Context, namespace, name string) (map[string][]byte, error) {
	secret, err := r.secrets.Secrets(namespace).Get(name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return secret.Data, nil
}

// writeSecret creates or updates a Secret owned by the given object, so deleting
// the object reclaims the credential with it.
func writeSecret(ctx context.Context, kube kubernetes.Interface, namespace, name string, data map[string][]byte, ownerKind, ownerName string, ownerUID types.UID) error {
	owner := metav1.OwnerReference{
		APIVersion:         agentv1alpha1.SchemeGroupVersion.String(),
		Kind:               ownerKind,
		Name:               ownerName,
		UID:                ownerUID,
		Controller:         ptr.To(true),
		BlockOwnerDeletion: ptr.To(true),
	}

	secrets := kube.CoreV1().Secrets(namespace)
	existing, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if !isNotFound(err) {
			return err
		}
		_, err = secrets.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, OwnerReferences: []metav1.OwnerReference{owner}},
			Data:       data,
		}, metav1.CreateOptions{})
		return err
	}

	existing.Data = data
	existing.OwnerReferences = []metav1.OwnerReference{owner}
	_, err = secrets.Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func isNotFound(err error) bool { return apierrors.IsNotFound(err) }

// ignoreNotFound turns a NotFound error into success, which is what every
// delete caller means: the object must not exist afterwards.
func ignoreNotFound(err error) error {
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
