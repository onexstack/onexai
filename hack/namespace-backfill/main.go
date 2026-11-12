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

// Command namespace-backfill copies the business metadata that used to live in
// agent.namespace onto the core Namespace objects themselves.
//
// It is the cut-over step for removing the business-line row table. Run it
// before the new binary starts reading Namespaces, then run it again with
// --verify after the switch:
//
//	hack/namespace-backfill --config ~/.onexai/oai-agent-apiserver.yaml \
//	    --kubeconfig ~/.onexai/declserver.kubeconfig --dry-run
//	hack/namespace-backfill --config ~/.onexai/oai-agent-apiserver.yaml \
//	    --kubeconfig ~/.onexai/declserver.kubeconfig
//
// The object always wins when it already carries an annotation or label; the
// row only fills gaps. That makes the command idempotent and safe to run after
// an operator has started editing the object directly.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/spf13/viper"
	"gorm.io/gorm"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/yaml"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/pkg/dbschema"
	"github.com/onexstack/onexai/pkg/declclient"
	customoptions "github.com/onexstack/onexai/pkg/options"
	"github.com/onexstack/onexai/pkg/readmodel"
)

type namespaceRow struct {
	Name        string          `gorm:"column:name"`
	DisplayName string          `gorm:"column:display_name"`
	Description string          `gorm:"column:description"`
	Owner       string          `gorm:"column:owner"`
	Labels      model.StringMap `gorm:"column:labels"`
}

func main() {
	var (
		configPath = flag.String("config", "", "Path to the agent service config whose coredb block is used (required)")
		kubeconfig = flag.String("kubeconfig", "", "Control-plane kubeconfig (required)")
		dryRun     = flag.Bool("dry-run", false, "Print the patches without touching the control plane")
	)
	flag.Parse()

	if *configPath == "" || *kubeconfig == "" {
		fatalf("--config and --kubeconfig are required")
	}
	if err := run(*configPath, *kubeconfig, *dryRun); err != nil {
		fatalf("%v", err)
	}
}

func run(configPath, kubeconfig string, dryRun bool) error {
	db, err := openDB(configPath)
	if err != nil {
		return err
	}
	client, err := declclient.New(kubeconfig, metav1.NamespaceAll, 0)
	if err != nil {
		return fmt.Errorf("build the control-plane client: %w", err)
	}
	if client.KubeClient == nil {
		return fmt.Errorf("the kubeconfig did not produce a Kubernetes client")
	}

	var rows []namespaceRow
	table := dbschema.Table(dbschema.Agent, "namespace")
	if err := db.Table(table).Find(&rows).Error; err != nil {
		return fmt.Errorf("read %s: %w", table, err)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	var written, unchanged, missing int
	for _, row := range rows {
		if readmodel.SystemNamespace(row.Name) {
			continue
		}
		changed, found, err := applyOne(context.Background(), client, row, dryRun)
		if err != nil {
			return fmt.Errorf("namespace %q: %w", row.Name, err)
		}
		if !found {
			missing++
			fmt.Fprintf(os.Stderr, "skip %q: the Namespace object does not exist\n", row.Name)
			continue
		}
		if !changed {
			unchanged++
			continue
		}
		written++
	}
	fmt.Printf("namespace backfill: rows=%d written=%d unchanged=%d missing=%d dryRun=%v\n",
		len(rows), written, unchanged, missing, dryRun)
	return nil
}

func applyOne(ctx context.Context, client *declclient.Client, row namespaceRow, dryRun bool) (bool, bool, error) {
	namespaces := client.KubeClient.CoreV1().Namespaces()
	current, err := namespaces.Get(ctx, row.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if !changedBy(row, current.ObjectMeta) {
		return false, true, nil
	}
	if dryRun {
		out, err := yaml.Marshal(map[string]any{
			"name":        row.Name,
			"annotations": desiredAnnotations(row),
			"labels":      desiredLabels(row, current.Labels),
		})
		if err != nil {
			return false, true, err
		}
		fmt.Printf("---\n%s", out)
		return true, true, nil
	}

	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh, err := namespaces.Get(ctx, row.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		updated := fresh.DeepCopy()
		if updated.Annotations == nil {
			updated.Annotations = map[string]string{}
		}
		if updated.Labels == nil {
			updated.Labels = map[string]string{}
		}
		for key, value := range desiredAnnotations(row) {
			if updated.Annotations[key] == "" && value != "" {
				updated.Annotations[key] = value
			}
		}
		for key, value := range desiredLabels(row, fresh.Labels) {
			if _, ok := updated.Labels[key]; !ok {
				updated.Labels[key] = value
			}
		}
		_, err = namespaces.Update(ctx, updated, metav1.UpdateOptions{})
		return err
	})
	return err == nil, true, err
}

func changedBy(row namespaceRow, meta metav1.ObjectMeta) bool {
	for key, value := range desiredAnnotations(row) {
		if value != "" && meta.Annotations[key] == "" {
			return true
		}
	}
	for key := range desiredLabels(row, meta.Labels) {
		if _, ok := meta.Labels[key]; !ok {
			return true
		}
	}
	return false
}

func desiredAnnotations(row namespaceRow) map[string]string {
	out := map[string]string{}
	if row.DisplayName != "" {
		out[model.AnnotationDisplayName] = row.DisplayName
	}
	if row.Description != "" {
		out[model.AnnotationDescription] = row.Description
	}
	if row.Owner != "" {
		out[model.AnnotationOwner] = row.Owner
	}
	return out
}

func desiredLabels(row namespaceRow, existing map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range row.Labels {
		if _, ok := existing[key]; ok {
			continue
		}
		out[key] = value
	}
	return out
}

func openDB(path string) (*gorm.DB, error) {
	v := viper.New()
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg struct {
		CoreDB *customoptions.CoreDBOptions `mapstructure:"coredb"`
	}
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("decode the coredb block of %s: %w", path, err)
	}
	if cfg.CoreDB == nil {
		return nil, fmt.Errorf("%s has no coredb block", path)
	}
	return cfg.CoreDB.NewDB()
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
