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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"

	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/pkg/declclient"
)

// The annotations and labels that separate AK/SK credentials from the Secrets
// Model, MCP server and Agent create for themselves. Without the label, List
// would return every credential in the namespace, including the model API keys.
const (
	secretKindLabel      = "onexai.io/secret-kind"
	secretKindAKSK       = "aksk"
	secretManagedByLabel = "app.kubernetes.io/managed-by"
	secretManagedBy      = "onexai"
	secretIDAnnotation   = "onexai.io/secret-id"
	secretCreatedAtKey   = "onexai.io/created-at"
	secretKeyDataKey     = "secret-key"
)

// secretCacheTTL bounds the write-through cache. It exists for the window
// between a write and the informer's watch delivery: during that window the
// lister still holds the previous generation, and a signed request must see the
// key its writer just set.
const secretCacheTTL = 30 * time.Second

// secretStore serves AK/SK credentials from core Secrets.
//
// # Why core Secrets
//
// The SQL table stored the key in clear text and every signed request paid a
// database lookup. A core Secret gives the platform encryption at rest, RBAC
// and audit, and the informer the runtime already runs makes the lookup local.
//
// # Identity
//
// SecretID is the API's identity and the AK. A Secret name must be a DNS-1123
// subdomain, so an ID that is not one is encoded deterministically as
// `<sanitized>-<hash>`; the original value is kept in an annotation. The same
// function is used for every read and write, so an encoded name is never a
// second identity.
type secretStore struct {
	client    kubernetes.Interface
	lister    corelisters.SecretLister
	namespace string

	recent sync.Map // secretID -> *secretCacheEntry
}

type secretCacheEntry struct {
	row     model.SecretM
	deleted bool
	expires time.Time
}

var _ agentstore.SecretStore = (*secretStore)(nil)

// NewSecretStore builds the AK/SK store. namespace is the control plane's
// configured namespace; empty falls back to the platform's default business
// line, which is where the SQL migration placed credentials without a namespace.
func NewSecretStore(client *declclient.Client, namespace string) *secretStore {
	var kube kubernetes.Interface
	var lister corelisters.SecretLister
	if client != nil {
		kube = client.KubeClient
		if client.KubeFactory != nil {
			lister = corelisters.NewSecretLister(
				client.KubeFactory.Core().V1().Secrets().Informer().GetIndexer(),
			)
		}
	}
	if namespace == "" {
		namespace = model.DefaultNamespace
	}
	return &secretStore{client: kube, lister: lister, namespace: namespace}
}

// Create writes one AK/SK credential as a core Secret.
func (s *secretStore) Create(ctx context.Context, obj *model.SecretM) error {
	if s.client == nil {
		return errSecretNeedsControlPlane
	}
	secret := secretObject(obj, s.namespace)
	created, err := s.client.CoreV1().Secrets(s.namespace).Create(ctx, secret, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	applySecret(created, obj)
	s.remember(obj, false)
	return nil
}

// Update refreshes the writable fields. An empty key preserves the stored one,
// so the console's edit-description path does not erase a credential it never
// read.
func (s *secretStore) Update(ctx context.Context, obj *model.SecretM) error {
	if s.client == nil {
		return errSecretNeedsControlPlane
	}
	name, _ := secretNameForID(obj.SecretID)
	secrets := s.client.CoreV1().Secrets(s.namespace)
	current, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	updated := current.DeepCopy()
	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}
	if updated.Data == nil {
		updated.Data = map[string][]byte{}
	}
	mergeSecretAnnotations(updated.Annotations, obj)
	if obj.SecretKey != "" {
		updated.Data[secretKeyDataKey] = []byte(obj.SecretKey)
	}
	if len(updated.Labels) == 0 {
		updated.Labels = secretLabels(obj.Kind)
	}
	_, err = secrets.Update(ctx, updated, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	// Remember the effective row, not the request: an update that omitted the
	// key preserves the stored one, and the cache must reflect that.
	effective := *obj
	if effective.SecretKey == "" {
		effective.SecretKey = string(updated.Data[secretKeyDataKey])
	}
	s.remember(&effective, false)
	return nil
}

// Delete removes the credential. A second delete is success.
func (s *secretStore) Delete(ctx context.Context, opts *where.Options) error {
	if s.client == nil {
		return errSecretNeedsControlPlane
	}
	id, err := secretIDFromOptions(opts)
	if err != nil {
		return err
	}
	name, _ := secretNameForID(id)
	if err := s.client.CoreV1().Secrets(s.namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil &&
		!apierrors.IsNotFound(err) {
		return err
	}
	s.rememberDeleted(id)
	return nil
}

// Get resolves one secret ID. The write-through cache is consulted first so a
// create or rotation is visible to the request that follows it, before the
// informer's watch has delivered the new generation.
func (s *secretStore) Get(ctx context.Context, opts *where.Options) (*model.SecretM, error) {
	id, err := secretIDFromOptions(opts)
	if err != nil {
		return nil, err
	}
	if row, found, deleted := s.cached(id); found {
		if deleted {
			return nil, nil
		}
		return row, nil
	}
	return s.get(ctx, id)
}

func (s *secretStore) get(ctx context.Context, id string) (*model.SecretM, error) {
	name, _ := secretNameForID(id)
	if s.lister != nil {
		secret, err := s.lister.Secrets(s.namespace).Get(name)
		if err == nil {
			return secretModel(secret), nil
		}
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
	}
	if s.client == nil {
		return nil, nil
	}
	secret, err := s.client.CoreV1().Secrets(s.namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return secretModel(secret), nil
}

// List returns only the AK/SK credentials, sorted by secret ID. A write that
// has not reached the informer yet is merged from the write-through cache so a
// list immediately after a create does not look like the create failed.
func (s *secretStore) List(ctx context.Context, opts *where.Options) (int64, []*model.SecretM, error) {
	rows := map[string]*model.SecretM{}
	items, err := s.listSecrets(ctx)
	if err != nil {
		return 0, nil, err
	}
	for _, secret := range items {
		row := secretModel(secret)
		rows[row.SecretID] = row
	}
	s.recent.Range(func(_, value any) bool {
		entry := value.(*secretCacheEntry)
		if entry.deleted || time.Now().After(entry.expires) {
			return true
		}
		// The write-through cache holds every kind, but this list is the AK/SK
		// credential list. Without this filter a webhook's managed token would
		// show up here for the cache TTL even though the lister query excludes
		// it.
		if entry.row.Kind != "" && entry.row.Kind != secretKindAKSK {
			return true
		}
		row := entry.row
		rows[row.SecretID] = &row
		return true
	})

	out := make([]*model.SecretM, 0, len(rows))
	for _, row := range rows {
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SecretID < out[j].SecretID })

	offset, limit := 0, -1
	if opts != nil {
		offset, limit = opts.Offset, opts.Limit
	}
	if offset < 0 {
		offset = 0
	}
	if offset > len(out) {
		offset = len(out)
	}
	end := len(out)
	if limit >= 0 && offset+limit < end {
		end = offset + limit
	}
	return int64(len(out)), out[offset:end], nil
}

func (s *secretStore) listSecrets(ctx context.Context) ([]*corev1.Secret, error) {
	selector := labels.SelectorFromSet(labels.Set{secretKindLabel: secretKindAKSK})
	var items []*corev1.Secret
	if s.lister != nil {
		listed, err := s.lister.Secrets(s.namespace).List(selector)
		if err == nil {
			items = listed
		}
	}
	if items == nil {
		if s.client == nil {
			return nil, nil
		}
		list, err := s.client.CoreV1().Secrets(s.namespace).List(ctx, metav1.ListOptions{
			LabelSelector: selector.String(),
		})
		if err != nil {
			return nil, err
		}
		items = make([]*corev1.Secret, 0, len(list.Items))
		for i := range list.Items {
			items = append(items, &list.Items[i])
		}
	}

	// Filter again in-process: a fake or a projection backend might ignore the
	// selector, and returning a Model/MCP credential here would leak a key.
	out := make([]*corev1.Secret, 0, len(items))
	for _, item := range items {
		if item.Labels[secretKindLabel] != secretKindAKSK {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

// Key resolves a secret ID to its key. It is the one reader on the signed
// request path; the lister makes it a map lookup rather than a query.
func (s *secretStore) Key(ctx context.Context, secretID string) (string, bool) {
	if secretID == "" {
		return "", false
	}
	row, err := s.Get(ctx, where.F("secret_id", secretID))
	if err != nil || row == nil || row.SecretKey == "" {
		return "", false
	}
	return row.SecretKey, true
}

func (s *secretStore) remember(obj *model.SecretM, deleted bool) {
	copied := *obj
	s.recent.Store(obj.SecretID, &secretCacheEntry{
		row:     copied,
		deleted: deleted,
		expires: time.Now().Add(secretCacheTTL),
	})
}

func (s *secretStore) rememberDeleted(id string) {
	s.recent.Store(id, &secretCacheEntry{deleted: true, expires: time.Now().Add(secretCacheTTL)})
}

func (s *secretStore) cached(id string) (*model.SecretM, bool, bool) {
	value, ok := s.recent.Load(id)
	if !ok {
		return nil, false, false
	}
	entry := value.(*secretCacheEntry)
	if time.Now().After(entry.expires) {
		s.recent.Delete(id)
		return nil, false, false
	}
	if entry.deleted {
		return nil, true, true
	}
	row := entry.row
	return &row, true, false
}

func secretModel(secret *corev1.Secret) *model.SecretM {
	row := &model.SecretM{
		SecretID:    secretIDOf(secret),
		SecretKey:   string(secret.Data[secretKeyDataKey]),
		Description: secret.Annotations[model.AnnotationDescription],
		Kind:        secret.Labels[secretKindLabel],
		CreatedAt:   parseMillis(secret.Annotations[secretCreatedAtKey]),
		UpdatedAt:   parseMillis(secret.Annotations[model.AnnotationUpdatedAt]),
	}
	if row.CreatedAt == 0 {
		row.CreatedAt = secret.CreationTimestamp.UnixMilli()
	}
	if row.UpdatedAt == 0 {
		row.UpdatedAt = row.CreatedAt
	}
	return row
}

func applySecret(secret *corev1.Secret, obj *model.SecretM) {
	view := secretModel(secret)
	obj.CreatedAt = view.CreatedAt
	obj.UpdatedAt = view.UpdatedAt
}

func secretIDOf(secret *corev1.Secret) string {
	if value := secret.Annotations[secretIDAnnotation]; value != "" {
		return value
	}
	return secret.Name
}

func secretLabels(kind string) map[string]string {
	if kind == "" {
		kind = secretKindAKSK
	}
	return map[string]string{
		secretKindLabel:      kind,
		secretManagedByLabel: secretManagedBy,
	}
}

func secretAnnotations(obj *model.SecretM) map[string]string {
	annotations := map[string]string{
		secretIDAnnotation: obj.SecretID,
	}
	mergeSecretAnnotations(annotations, obj)
	return annotations
}

func mergeSecretAnnotations(annotations map[string]string, obj *model.SecretM) {
	if obj.SecretID != "" {
		annotations[secretIDAnnotation] = obj.SecretID
	}
	if obj.Description == "" {
		delete(annotations, model.AnnotationDescription)
	} else {
		annotations[model.AnnotationDescription] = obj.Description
	}
	createdAt := obj.CreatedAt
	if createdAt == 0 {
		createdAt = time.Now().UnixMilli()
	}
	annotations[secretCreatedAtKey] = strconv.FormatInt(createdAt, 10)
	updatedAt := obj.UpdatedAt
	if updatedAt == 0 {
		updatedAt = time.Now().UnixMilli()
	}
	annotations[model.AnnotationUpdatedAt] = strconv.FormatInt(updatedAt, 10)
}

// secretObject builds the core Secret a credential maps to. Create and the
// one-shot migration tool share it, so the object a dry-run prints is the object
// the store would write.
func secretObject(obj *model.SecretM, namespace string) *corev1.Secret {
	name, _ := secretNameForID(obj.SecretID)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Labels:      secretLabels(obj.Kind),
			Annotations: secretAnnotations(obj),
		},
		Data: map[string][]byte{},
	}
	if obj.SecretKey != "" {
		secret.Data[secretKeyDataKey] = []byte(obj.SecretKey)
	}
	return secret
}

// SecretNameForID exposes the deterministic name mapping to the migration tool.
func SecretNameForID(id string) (string, bool) { return secretNameForID(id) }

// SecretObjectFor exposes the object builder to the migration tool's dry-run.
func SecretObjectFor(obj *model.SecretM, namespace string) *corev1.Secret {
	return secretObject(obj, namespace)
}

// secretNameForID maps the API's identity to a DNS-1123 Secret name. The second
// result reports whether the ID had to be encoded; the original is always kept
// in the secret-id annotation.
func secretNameForID(id string) (string, bool) {
	if len(k8svalidation.IsDNS1123Subdomain(id)) == 0 {
		return id, false
	}
	sanitized := sanitizeDNS1123(id)
	sum := sha256.Sum256([]byte(id))
	name := sanitized + "-" + hex.EncodeToString(sum[:4])
	if len(name) > 253 {
		name = name[:253]
	}
	return name, true
}

func sanitizeDNS1123(id string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(id) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	out = strings.ReplaceAll(out, "..", ".")
	if out == "" {
		out = "secret"
	}
	if len(out) > 200 {
		out = strings.Trim(out[:200], "-.")
	}
	return out
}

func secretIDFromOptions(opts *where.Options) (string, error) {
	if opts != nil {
		if id, ok := opts.Filters["secret_id"].(string); ok && id != "" {
			return id, nil
		}
	}
	return "", errSecretIDRequired
}

var (
	errSecretNeedsControlPlane = errors.New(
		"AK/SK credentials need the declarative control plane; set controlplane.kubeconfig",
	)
	errSecretIDRequired = errors.New("secret id is required")
)
