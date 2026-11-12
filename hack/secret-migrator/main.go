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

// Command secret-migrator copies the AK/SK table into core Secrets.
//
// It is idempotent: an existing core Secret is updated from the row, and a
// missing one is created. Run it with --dry-run first; the printed objects are
// built by the same function the store uses.
//
//	hack/secret-migrator --config ~/.onexai/oai-agent-apiserver.yaml \
//	    --kubeconfig ~/.onexai/declserver.kubeconfig --dry-run
//	hack/secret-migrator --config ~/.onexai/oai-agent-apiserver.yaml \
//	    --kubeconfig ~/.onexai/declserver.kubeconfig
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/spf13/viper"
	"gorm.io/gorm"
	"github.com/onexstack/onexstack/pkg/store/where"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store/controlplanestore"
	"github.com/onexstack/onexai/pkg/dbschema"
	"github.com/onexstack/onexai/pkg/declclient"
	customoptions "github.com/onexstack/onexai/pkg/options"
)

func main() {
	var (
		configPath = flag.String("config", "", "Path to the agent service config whose coredb block is used (required)")
		kubeconfig = flag.String("kubeconfig", "", "Control-plane kubeconfig (required)")
		namespace  = flag.String("namespace", "", "Namespace for the core Secrets (empty uses helper-public)")
		dryRun     = flag.Bool("dry-run", false, "Print the objects without touching the control plane")
	)
	flag.Parse()

	if *configPath == "" || *kubeconfig == "" {
		fatalf("--config and --kubeconfig are required")
	}
	if err := run(*configPath, *kubeconfig, *namespace, *dryRun); err != nil {
		fatalf("%v", err)
	}
}

func run(configPath, kubeconfig, namespace string, dryRun bool) error {
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
	if namespace == "" {
		namespace = model.DefaultNamespace
	}

	var rows []model.SecretM
	table := dbschema.Table(dbschema.Agent, "secret")
	if err := db.Table(table).Find(&rows).Error; err != nil {
		return fmt.Errorf("read %s: %w", table, err)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].SecretID < rows[j].SecretID })

	store := controlplanestore.NewSecretStore(client, namespace)
	var created, updated, unchanged int
	for i := range rows {
		row := rows[i]
		existing, err := store.Get(context.Background(), where.F("secret_id", row.SecretID))
		if err != nil {
			return fmt.Errorf("look up secret %q: %w", row.SecretID, err)
		}
		if existing != nil {
			row.CreatedAt = existing.CreatedAt
			row.UpdatedAt = existing.UpdatedAt
		}

		if dryRun {
			out, err := yaml.Marshal(controlplanestore.SecretObjectFor(&row, namespace))
			if err != nil {
				return fmt.Errorf("marshal secret %q: %w", row.SecretID, err)
			}
			fmt.Printf("---\n%s", out)
			if existing == nil {
				created++
			} else {
				unchanged++
			}
			continue
		}

		if err := store.Create(context.Background(), &row); err != nil {
			// AlreadyExists means the object appeared between the lookup and the
			// create; fall through to update so a concurrent run is harmless.
			if !apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("create secret %q: %w", row.SecretID, err)
			}
			if err := store.Update(context.Background(), &row); err != nil {
				return fmt.Errorf("update secret %q: %w", row.SecretID, err)
			}
			updated++
			continue
		}
		created++
	}
	fmt.Printf("secret migration: rows=%d created=%d updated=%d unchanged=%d dryRun=%v\n",
		len(rows), created, updated, unchanged, dryRun)
	return nil
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
