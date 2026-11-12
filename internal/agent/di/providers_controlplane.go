// Copyright 2026 Lingfei Kong <colin404@foxmail.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package di

import (
	"context"
	"log/slog"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/onexstack/onexai/internal/agent/pkg/checkpoint"
	"github.com/onexstack/onexai/internal/agent/pkg/controlplane"
	"github.com/onexstack/onexai/internal/agent/pkg/mem"
	"github.com/onexstack/onexai/internal/agent/pkg/runstop"
	"github.com/onexstack/onexai/internal/agent/pkg/runtime"
	"github.com/onexstack/onexai/internal/agent/pkg/runtime/inproc"
	runtimesync "github.com/onexstack/onexai/internal/agent/pkg/runtime/sync"
	"github.com/onexstack/onexai/internal/agent/pkg/settingstore"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"

	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/agent/store/batchstore"
	"github.com/onexstack/onexai/pkg/declclient"
	customoptions "github.com/onexstack/onexai/pkg/options"
)

// ProvideSettingStore serves the runtime-changeable settings from the control
// plane's ConfigMap, or returns nil when none is declared.
//
// There is no refresh loop: the source is an informer lister, so a lookup is a
// map read and an edit is visible as soon as the watch delivers it.
func ProvideSettingStore(cfg *Config, client *declclient.Client) *settingstore.Store {
	controlPlane := cfg.ControlPlaneOptions
	if client == nil || controlPlane == nil || controlPlane.SettingsConfigMap == "" {
		return nil
	}
	return settingstore.New(settingstore.NewConfigMapSource(
		client.ConfigMaps(settingsNamespace(controlPlane)), controlPlane.SettingsConfigMap,
	))
}

// settingsNamespace is the namespace the settings ConfigMap is looked up in.
//
// An unset namespace means the client watches every namespace, which leaves the
// ConfigMap's own namespace unsaid; default is the answer kubectl gives for the
// same omission.
func settingsNamespace(controlPlane *customoptions.ControlPlaneOptions) string {
	if controlPlane.Namespace == "" {
		return metav1.NamespaceDefault
	}
	return controlPlane.Namespace
}

// ProvideAgentBiz assembles the AI agent service, or nil when no engine exists.
// The app-lifetime ctx is used as the root for runs so in-flight runs are
// cancelled on server shutdown.
// ProvideRuntimeRegistry builds the runtime registry. The in-process eino
// adapter is always the fallback; remote profiles are registered by the
// profile syncer as they become ready.
func ProvideRuntimeRegistry(
	engine agentapi.Engine, sessions mem.SessionStore, checkpoints checkpoint.Store, stops runstop.Registry,
) *runtime.Registry {
	if engine == nil {
		return nil
	}
	adapter := &inproc.Adapter{
		Engine:      engine,
		Sessions:    sessions,
		Checkpoints: checkpoints,
		Stops:       stops,
	}
	if resumable, ok := engine.(agentapi.Resumable); ok {
		adapter.Resumable = resumable
	}
	return runtime.NewRegistry(adapter)
}

// ProvideScheduledExecutionStore serves occurrence history from the Jobs a
// batch CronJob owns. A missing control-plane client leaves the facade
// unavailable, which is the same condition that disables ScheduledRun itself.
func ProvideScheduledExecutionStore(cfg *Config, client *declclient.Client) store.ScheduledExecutionStore {
	if client == nil {
		return nil
	}
	namespace := ""
	if cfg.ControlPlaneOptions != nil {
		namespace = cfg.ControlPlaneOptions.Namespace
	}
	return batchstore.NewExecutionStore(client, namespace)
}

// ProvideRuntimeSyncer keeps remote runtime ports aligned with the profiles.
func ProvideRuntimeSyncer(registry *runtime.Registry, resources store.AIStore) *runtimesync.Syncer {
	if registry == nil || resources == nil {
		return nil
	}
	return &runtimesync.Syncer{
		Registry: registry,
		Profiles: resources.RuntimeProfiles(),
		Hostings: resources.RuntimeHostings(),
	}
}

// ProvideDeclClient builds the client every control-plane reader shares and
// starts its informers.
//
// A nil return means the service has no declarative resources at all: the
// kubeconfig is the switch. Callers treat nil as "that resource set is empty",
// which for the rule store is the fail-closed state an operator has to seed
// anyway.
func ProvideDeclClient(ctx context.Context, cfg *Config) *declclient.Client {
	controlPlane := cfg.ControlPlaneOptions
	if controlPlane == nil || !controlPlane.Enabled() {
		slog.WarnContext(ctx, "controlplane: not configured; the declarative resources are unavailable")
		return nil
	}

	client, err := declclient.New(controlPlane.Kubeconfig, controlPlane.Namespace, controlPlane.ResyncPeriod,
		declclient.WithInformerFilter(cfg.StoreOptions.InformerFilter()))
	if err != nil {
		slog.ErrorContext(ctx, "controlplane: could not build the client; the declarative resources are unavailable", "error", err)
		return nil
	}
	if err := client.Start(ctx, controlPlane.SyncTimeout); err != nil {
		slog.ErrorContext(ctx, "controlplane: caches did not sync; the declarative resources are unavailable", "error", err)
		return nil
	}
	return client
}

// ProvideHealthReporter publishes the health only the runtime can observe.
func ProvideHealthReporter(cfg *Config, client *declclient.Client, probers controlplane.Probers) *controlplane.HealthReporter {
	if client == nil || cfg.ControlPlaneOptions == nil {
		return nil
	}
	return controlplane.NewHealthReporter(client, cfg.ControlPlaneOptions.ProbeInterval, probers,
		cfg.ModelAPIKeyEncryptionKey)
}
