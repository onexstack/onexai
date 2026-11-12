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

// Package app wires oai-job-controller's options into a controller-runtime
// manager and runs the batch Job/CronJob controllers.
package app

import (
	"context"
	"fmt"

	"github.com/onexstack/onexstack/pkg/app"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/onexstack/onexai/cmd/oai-job-controller/app/options"
	cronjobcontroller "github.com/onexstack/onexai/internal/controller/job/cronjob"
	jobcontroller "github.com/onexstack/onexai/internal/controller/job/job"
	"github.com/onexstack/onexai/internal/controller/job/job/providers/agentrun"
	_ "github.com/onexstack/onexai/internal/controller/job/job/providers/all"
	batchv1beta1 "github.com/onexstack/onexai/pkg/apis/batch/v1beta1"
	agentclient "github.com/onexstack/onexai/pkg/clientset/agent"
)

const commandDesc = `oai-job-controller reconciles batch.onex.io Jobs and CronJobs. It creates
occurrences from CronJobs and dispatches each Job to the provider registered for
its spec.type (today: agentrun, which submits an asynchronous agent run).`

// NewApp creates the oai-job-controller application.
func NewApp() *app.App {
	opts := options.NewServerOptions()

	return app.NewApp(
		"oai-job-controller",
		"Batch job and cronjob controller for onexai",
		app.WithOptions(opts),
		app.WithRun(run(opts)),
		app.WithDirInHome(".onexai"),
		app.WithDescription(commandDesc),
	)
}

func run(opts *options.ServerOptions) app.RunFunc {
	return func(ctx context.Context) error {
		return Run(ctx, opts)
	}
}

// Run builds the manager and blocks until ctx is canceled.
func Run(ctx context.Context, opts *options.ServerOptions) error {
	restConfig, err := clientcmd.BuildConfigFromFlags(opts.Master, opts.Kubeconfig)
	if err != nil {
		return fmt.Errorf("build kubeconfig: %w", err)
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return fmt.Errorf("register client-go scheme: %w", err)
	}
	if err := batchv1beta1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("register batch scheme: %w", err)
	}

	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                  scheme,
		LeaderElection:          opts.LeaderElection,
		LeaderElectionID:        opts.LeaderElectionID,
		LeaderElectionNamespace: opts.LeaderElectionNamespace,
		Metrics:                 metricsserver.Options{BindAddress: opts.MetricsAddr},
		HealthProbeBindAddress:  opts.HealthProbeAddr,
	})
	if err != nil {
		return fmt.Errorf("create manager: %w", err)
	}

	agentClient := agentclient.NewWithSigning(
		opts.AgentOptions, opts.AgentAKSKOptions.APIKey, opts.AgentAKSKOptions.SecretKey,
	)

	jobTypes := make([]batchv1beta1.JobType, 0, len(opts.JobTypes))
	for _, t := range opts.JobTypes {
		jobTypes = append(jobTypes, batchv1beta1.JobType(t))
	}

	jobReconciler := (&jobcontroller.Reconciler{}).
		WithAgentRunClient(agentrun.NewHTTPClient(agentClient)).
		WithPollInterval(opts.ProviderPollInterval).
		WithJobTypes(jobTypes)
	if err := jobReconciler.SetupWithManager(ctx, mgr, controller.Options{
		MaxConcurrentReconciles: opts.MaxConcurrentReconciles,
	}); err != nil {
		return fmt.Errorf("set up job controller: %w", err)
	}

	if err := (&cronjobcontroller.Reconciler{}).SetupWithManager(ctx, mgr, controller.Options{
		MaxConcurrentReconciles: opts.MaxConcurrentReconciles,
	}); err != nil {
		return fmt.Errorf("set up cronjob controller: %w", err)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("add health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("add readiness check: %w", err)
	}

	return mgr.Start(ctx)
}
