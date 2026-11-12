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
	"fmt"

	"gorm.io/gorm"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/onexstack/onexai/internal/agent/model"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
	"github.com/onexstack/onexai/pkg/declclient/declstore"
	"github.com/onexstack/onexai/pkg/declclient/mirrorstore"
	"github.com/onexstack/onexai/pkg/options"
)

// MirrorConfig points a subset of the store's Lists at the PostgreSQL projection.
//
// Only List is redirected. Get keeps reading the control plane: it is already
// O(1), and it is where read-your-own-write matters, since an admin API call
// that writes and then reads back must not see a stale projection.
type MirrorConfig struct {
	DB      *gorm.DB
	Options *options.StoreOptions
	// Namespace is the scope the store was built for; a mirror-full Get reads the
	// API server directly and needs it.
	Namespace string
}

// attach installs the projection-backed List on every kind configured for it.
//
// Model and MCPServer are included, and they are why the decoder takes a
// context: their rows carry credential material that lives in a Secret, not in
// the object, so the projection is only correct if the Secret is read on the way
// out. It is the same enrichment the lister path applies.
func (m *MirrorConfig) attach(s *AIStore, client *declclient.Client) error {
	if m == nil || m.DB == nil || m.Options == nil {
		return nil
	}
	secrets := newSecretReader(client.Secrets())

	if err := attachKind(m, s.agents.Store, "Agent", agentFromRaw); err != nil {
		return err
	}
	dropInformer(m, &s.agents.Reader, "Agent", func(ctx context.Context, name string) (model.AgentM, error) {
		item, err := client.Clientset.AgentV1alpha1().Agents(m.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return model.AgentM{}, err
		}
		return agentFromCR(item), nil
	})
	if err := attachKind(m, s.models.Store, "Model", modelFromRaw(secrets)); err != nil {
		return err
	}
	dropInformer(m, &s.models.Reader, "Model", func(ctx context.Context, name string) (model.ModelM, error) {
		item, err := client.Clientset.AgentV1alpha1().Models(m.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return model.ModelM{}, err
		}
		row := modelFromCR(item)
		if item.Spec.CredentialRef != nil {
			key, err := secrets.get(ctx, item.Namespace, item.Spec.CredentialRef.Name, item.Spec.CredentialRef.Key)
			if err != nil {
				return model.ModelM{}, err
			}
			row.APIKey = key
		}
		return row, nil
	})
	if err := attachKind(m, s.mcps.Store, "MCPServer", mcpFromRaw(secrets)); err != nil {
		return err
	}
	dropInformer(m, &s.mcps.Reader, "MCPServer", func(ctx context.Context, name string) (model.MCPM, error) {
		item, err := client.Clientset.AgentV1alpha1().MCPServers(m.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return model.MCPM{}, err
		}
		row := mcpFromCR(item)
		if item.Spec.HeadersRef != nil {
			headers, err := secrets.all(ctx, item.Namespace, item.Spec.HeadersRef.Name)
			if err != nil {
				return model.MCPM{}, err
			}
			row.Headers = headersJSON(headers)
		}
		return row, nil
	})
	if err := attachKind(m, s.prompts.Store, "Prompt", promptFromRaw); err != nil {
		return err
	}
	dropInformer(m, &s.prompts.Reader, "Prompt", func(ctx context.Context, name string) (model.PromptM, error) {
		item, err := client.Clientset.AgentV1alpha1().Prompts(m.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return model.PromptM{}, err
		}
		return promptFromCR(item), nil
	})
	if err := attachKind(m, s.skills.Store, "Skill", skillFromRaw); err != nil {
		return err
	}
	dropInformer(m, &s.skills.Reader, "Skill", func(ctx context.Context, name string) (model.SkillM, error) {
		item, err := client.Clientset.AgentV1alpha1().Skills(m.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return model.SkillM{}, err
		}
		return skillFromCR(item), nil
	})
	if err := attachKind(m, s.customCommands.Store, "CustomCommand", customCommandFromRaw); err != nil {
		return err
	}
	dropInformer(m, &s.customCommands.Reader, "CustomCommand", func(ctx context.Context, name string) (model.CustomCommandM, error) {
		item, err := client.Clientset.AgentV1alpha1().CustomCommands(m.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return model.CustomCommandM{}, err
		}
		return customCommandFromCR(item), nil
	})
	if err := attachKind(m, s.knowledgeBases.Store, "KnowledgeBase", knowledgeBaseFromRaw); err != nil {
		return err
	}
	dropInformer(m, &s.knowledgeBases.Reader, "KnowledgeBase", func(ctx context.Context, name string) (model.KnowledgeBaseM, error) {
		item, err := client.Clientset.AgentV1alpha1().KnowledgeBases(m.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return model.KnowledgeBaseM{}, err
		}
		return knowledgeBaseFromCR(item), nil
	})
	if err := attachKind(m, s.webhooks.Store, "Webhook", webhookFromRaw); err != nil {
		return err
	}
	dropInformer(m, &s.webhooks.Reader, "Webhook", func(ctx context.Context, name string) (model.WebhookM, error) {
		item, err := client.Clientset.AgentV1alpha1().Webhooks(m.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return model.WebhookM{}, err
		}
		return webhookFromCR(item), nil
	})
	return nil
}

// dropInformer switches a kind's Get to the API server behind a bounded cache.
//
// It is what makes mirror-full mean what it says: the process holds no rows for
// this kind, so a Get that missed the cache must ask the API server rather than
// scan an empty indexer and call the object missing.
func dropInformer[M any](
	cfg *MirrorConfig, reader *Reader[M], kind string, get func(context.Context, string) (M, error),
) {
	if cfg.Options.BackendFor(kind) != options.BackendMirrorFull {
		return
	}
	reader.NoScan = true
	reader.GetByKey = declstore.APIGetByKey(declstore.NewGetCache[M](cfg.Options.GetCacheSize), get)
}

// attachKind wires one kind, if the configuration selects a mirror backend.
func attachKind[M any](
	cfg *MirrorConfig, store *Store[M], kind string, decode func(context.Context, mirrorstore.RawRow) (M, error),
) error {
	switch cfg.Options.BackendFor(kind) {
	case options.BackendMirrorList, options.BackendMirrorFull:
	default:
		return nil
	}

	reader, err := mirrorstore.New(mirrorstore.Config{
		DB:        cfg.DB,
		Schema:    cfg.Options.Mirror.Schema,
		Group:     agentv1alpha1.GroupName,
		Kind:      kind,
		MaxLag:    cfg.Options.Mirror.MaxLag,
		CountMode: mirrorstore.CountMode(cfg.Options.Mirror.CountMode),
	}, decode)
	if err != nil {
		return fmt.Errorf("controlplanestore: %s reads from the projection: %w", kind, err)
	}
	store.ListPage = reader.ListPage
	return nil
}

// The decoders below rebuild the control-plane object and hand it to the same
// conversion the lister path uses. Reusing it is the point: a projection that
// mapped fields on its own would be a second definition of what a row is.

func agentFromRaw(_ context.Context, raw mirrorstore.RawRow) (model.AgentM, error) {
	cr := &agentv1alpha1.Agent{ObjectMeta: mirrorstore.Meta(raw)}
	if err := mirrorstore.Unmarshal(raw, &cr.Spec, &cr.Status); err != nil {
		return model.AgentM{}, err
	}
	return agentFromCR(cr), nil
}

func promptFromRaw(_ context.Context, raw mirrorstore.RawRow) (model.PromptM, error) {
	cr := &agentv1alpha1.Prompt{ObjectMeta: mirrorstore.Meta(raw)}
	if err := mirrorstore.Unmarshal(raw, &cr.Spec, &cr.Status); err != nil {
		return model.PromptM{}, err
	}
	return promptFromCR(cr), nil
}

func skillFromRaw(_ context.Context, raw mirrorstore.RawRow) (model.SkillM, error) {
	cr := &agentv1alpha1.Skill{ObjectMeta: mirrorstore.Meta(raw)}
	if err := mirrorstore.Unmarshal(raw, &cr.Spec, &cr.Status); err != nil {
		return model.SkillM{}, err
	}
	return skillFromCR(cr), nil
}

func customCommandFromRaw(_ context.Context, raw mirrorstore.RawRow) (model.CustomCommandM, error) {
	cr := &agentv1alpha1.CustomCommand{ObjectMeta: mirrorstore.Meta(raw)}
	if err := mirrorstore.Unmarshal(raw, &cr.Spec, &cr.Status); err != nil {
		return model.CustomCommandM{}, err
	}
	return customCommandFromCR(cr), nil
}

func knowledgeBaseFromRaw(_ context.Context, raw mirrorstore.RawRow) (model.KnowledgeBaseM, error) {
	cr := &agentv1alpha1.KnowledgeBase{ObjectMeta: mirrorstore.Meta(raw)}
	if err := mirrorstore.Unmarshal(raw, &cr.Spec, &cr.Status); err != nil {
		return model.KnowledgeBaseM{}, err
	}
	return knowledgeBaseFromCR(cr), nil
}

func webhookFromRaw(_ context.Context, raw mirrorstore.RawRow) (model.WebhookM, error) {
	cr := &agentv1alpha1.Webhook{ObjectMeta: mirrorstore.Meta(raw)}
	if err := mirrorstore.Unmarshal(raw, &cr.Spec, &cr.Status); err != nil {
		return model.WebhookM{}, err
	}
	return webhookFromCR(cr), nil
}

// modelFromRaw rebuilds a Model row and resolves its credential from the Secret
// the object names. Without that step a projected Model has an empty API key —
// the one field where an empty value is not a valid answer.
func modelFromRaw(secrets *secretReader) func(context.Context, mirrorstore.RawRow) (model.ModelM, error) {
	return func(ctx context.Context, raw mirrorstore.RawRow) (model.ModelM, error) {
		cr := &agentv1alpha1.Model{ObjectMeta: mirrorstore.Meta(raw)}
		if err := mirrorstore.Unmarshal(raw, &cr.Spec, &cr.Status); err != nil {
			return model.ModelM{}, err
		}
		row := modelFromCR(cr)
		if cr.Spec.CredentialRef != nil {
			key, err := secrets.get(ctx, cr.Namespace, cr.Spec.CredentialRef.Name, cr.Spec.CredentialRef.Key)
			if err != nil {
				return model.ModelM{}, err
			}
			row.APIKey = key
		}
		return row, nil
	}
}

// mcpFromRaw does for MCP headers what modelFromRaw does for the API key.
func mcpFromRaw(secrets *secretReader) func(context.Context, mirrorstore.RawRow) (model.MCPM, error) {
	return func(ctx context.Context, raw mirrorstore.RawRow) (model.MCPM, error) {
		cr := &agentv1alpha1.MCPServer{ObjectMeta: mirrorstore.Meta(raw)}
		if err := mirrorstore.Unmarshal(raw, &cr.Spec, &cr.Status); err != nil {
			return model.MCPM{}, err
		}
		row := mcpFromCR(cr)
		if cr.Spec.HeadersRef != nil {
			headers, err := secrets.all(ctx, cr.Namespace, cr.Spec.HeadersRef.Name)
			if err != nil {
				return model.MCPM{}, err
			}
			row.Headers = headersJSON(headers)
		}
		return row, nil
	}
}
