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

// oai-scheduledrun-migrator copies agent.onexai.io ScheduledRun objects into
// batch.onex.io CronJobs. It is idempotent: a CronJob that carries the
// migrated-from annotation is left alone, and an existing object the migrator
// did not create is reported rather than overwritten.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/ptr"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	batchv1beta1 "github.com/onexstack/onexai/pkg/apis/batch/v1beta1"
	versioned "github.com/onexstack/onexai/pkg/generated/clientset/versioned"
)

const migratedFromAnnotation = "onexai.io/migrated-from"

func main() {
	kubeconfig := flag.String("kubeconfig", "", "Path to the oai-declserver kubeconfig.")
	namespace := flag.String("namespace", "", "Namespace to migrate. Empty migrates every namespace.")
	dryRun := flag.Bool("dry-run", false, "Print the CronJobs instead of creating them.")
	timeout := flag.Duration("timeout", 60*time.Second, "Overall operation timeout.")
	flag.Parse()

	if *kubeconfig == "" {
		fmt.Fprintln(os.Stderr, "oai-scheduledrun-migrator: --kubeconfig is required")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	restConfig, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load kubeconfig: %v\n", err)
		os.Exit(1)
	}
	client, err := versioned.NewForConfig(restConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "build clientset: %v\n", err)
		os.Exit(1)
	}

	list, err := client.AgentV1alpha1().ScheduledRuns(*namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "list scheduled runs: %v\n", err)
		os.Exit(1)
	}

	var created, skipped, failed int
	for i := range list.Items {
		sr := &list.Items[i]
		cronJob, err := convert(sr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "convert %s/%s: %v\n", sr.Namespace, sr.Name, err)
			failed++
			continue
		}

		if *dryRun {
			raw, _ := json.MarshalIndent(cronJob, "", "  ")
			fmt.Printf("--- %s/%s\n%s\n", sr.Namespace, sr.Name, raw)
			skipped++
			continue
		}

		existing, err := client.BatchV1beta1().CronJobs(sr.Namespace).Get(ctx, cronJob.Name, metav1.GetOptions{})
		switch {
		case err == nil:
			if existing.Annotations[migratedFromAnnotation] == migrationKey(sr) {
				fmt.Printf("skip %s/%s: already migrated\n", sr.Namespace, sr.Name)
				skipped++
				continue
			}
			fmt.Fprintf(os.Stderr, "skip %s/%s: a CronJob with that name already exists and was not created by the migrator\n", sr.Namespace, sr.Name)
			failed++
			continue
		case !apierrors.IsNotFound(err):
			fmt.Fprintf(os.Stderr, "get %s/%s: %v\n", sr.Namespace, sr.Name, err)
			failed++
			continue
		}

		if _, err := client.BatchV1beta1().CronJobs(sr.Namespace).Create(ctx, cronJob, metav1.CreateOptions{}); err != nil {
			if apierrors.IsAlreadyExists(err) {
				fmt.Printf("skip %s/%s: created concurrently\n", sr.Namespace, sr.Name)
				skipped++
				continue
			}
			fmt.Fprintf(os.Stderr, "create %s/%s: %v\n", sr.Namespace, sr.Name, err)
			failed++
			continue
		}
		fmt.Printf("created %s/%s\n", sr.Namespace, sr.Name)
		created++
	}

	fmt.Printf("\nmigrated: %d created, %d skipped, %d failed\n", created, skipped, failed)
	if failed > 0 {
		os.Exit(1)
	}
}

func migrationKey(sr *agentv1alpha1.ScheduledRun) string {
	return fmt.Sprintf("ScheduledRun/%s/%s", sr.Namespace, sr.Name)
}

func convert(sr *agentv1alpha1.ScheduledRun) (*batchv1beta1.CronJob, error) {
	subject := subjectOf(sr)
	if subject == "" {
		return nil, fmt.Errorf("no owner and no %s annotation", agentv1alpha1.AnnotationCreatedBy)
	}
	if sr.Spec.Agent.Name == "" {
		return nil, fmt.Errorf("spec.agent is empty")
	}
	if sr.Spec.Schedule == "" {
		return nil, fmt.Errorf("spec.schedule is empty")
	}
	if sr.Spec.Prompt == "" {
		return nil, fmt.Errorf("spec.prompt is empty")
	}

	providerSpec := batchv1beta1.AgentRunProviderSpec{
		Agent:            sr.Spec.Agent.Name,
		Prompt:           sr.Spec.Prompt,
		SubjectID:        subject,
		SubjectName:      subject,
		Metering:         "none",
		ApprovalFallback: "deny",
		SessionPolicy:    "new",
		Source:           "scheduled-run",
	}
	if sr.Spec.ExecutionTimeout != nil {
		providerSpec.TimeoutSeconds = ptr.To(int64(sr.Spec.ExecutionTimeout.Duration.Seconds()))
	}
	raw, err := json.Marshal(providerSpec)
	if err != nil {
		return nil, err
	}

	timeZone := sr.Spec.TimeZone
	if timeZone == "" {
		timeZone = "UTC"
	}

	annotations := copyMap(sr.Annotations)
	annotations[migratedFromAnnotation] = migrationKey(sr)
	annotations["onexai.io/managed-by"] = "scheduledrun"
	if sr.Spec.DisplayName != "" {
		annotations["onexai.io/display-name"] = sr.Spec.DisplayName
	}
	if sr.Spec.Description != "" {
		annotations["onexai.io/description"] = sr.Spec.Description
	}

	return &batchv1beta1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:        sr.Name,
			Namespace:   sr.Namespace,
			Labels:      copyMap(sr.Labels),
			Annotations: annotations,
		},
		Spec: batchv1beta1.CronJobSpec{
			Schedule:                sr.Spec.Schedule,
			TimeZone:                ptr.To(timeZone),
			StartingDeadlineSeconds: ptr.To[int64](300),
			ConcurrencyPolicy:       batchv1beta1.ForbidConcurrent,
			Suspend:                 ptr.To(sr.Spec.Paused || !sr.Spec.Enabled),
			JobTemplate: batchv1beta1.JobTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      copyMap(sr.Labels),
					Annotations: map[string]string{"onexai.io/migrated-from": migrationKey(sr)},
				},
				Spec: batchv1beta1.JobSpec{
					Type:                  batchv1beta1.JobTypeAgentRun,
					ActiveDeadlineSeconds: timeoutSeconds(sr),
					BackoffLimit:          ptr.To[int32](0),
					ProviderSpec:          &runtime.RawExtension{Raw: raw},
				},
			},
			SuccessfulJobsHistoryLimit: ptr.To[int32](10),
			FailedJobsHistoryLimit:     ptr.To[int32](5),
		},
	}, nil
}

func subjectOf(sr *agentv1alpha1.ScheduledRun) string {
	if sr.Spec.Owner != nil && sr.Spec.Owner.Name != "" {
		return sr.Spec.Owner.Name
	}
	if v := sr.Annotations[agentv1alpha1.AnnotationCreatedBy]; v != "" {
		return v
	}
	return "scheduled-run"
}

func timeoutSeconds(sr *agentv1alpha1.ScheduledRun) *int64 {
	if sr.Spec.ExecutionTimeout == nil {
		return nil
	}
	return ptr.To(int64(sr.Spec.ExecutionTimeout.Duration.Seconds()))
}

func copyMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}
