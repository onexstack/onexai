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

// Command crd2pg backfills on-call group metadata from the declarative control
// plane into chat.oncall_group.
//
// It is the cut-over step for the change that made duty groups a chat-owned
// imperative resource. Run it after the new binary has created the schedule
// tables and before that binary starts serving reads:
//
//	hack/crd2pg --config ~/.onexai/oai-chat-apiserver.yaml \
//	            --kubeconfig ~/.onexai/declserver.kubeconfig --dry-run
//	hack/crd2pg --config ~/.onexai/oai-chat-apiserver.yaml \
//	            --kubeconfig ~/.onexai/declserver.kubeconfig
//
// The conversion is controlplanestore.DutyGroupFromCR, the same function the
// old read path used, so a migrated row is indistinguishable from one the old
// path would have produced. Group names are globally unique in SQL; a name that
// exists in two namespaces is reported and skipped rather than guessed at.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/spf13/viper"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/onexstack/onexstack/pkg/store/registry"

	"github.com/onexstack/onexai/internal/chat/model"
	chatcps "github.com/onexstack/onexai/internal/chat/store/controlplanestore"
	chatv1alpha1 "github.com/onexstack/onexai/pkg/apis/chat/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
	customoptions "github.com/onexstack/onexai/pkg/options"
)

func main() {
	var (
		configPath = flag.String("config", "", "Path to the chat service config whose coredb block is used (required)")
		kubeconfig = flag.String("kubeconfig", "", "Control-plane kubeconfig (required)")
		dryRun     = flag.Bool("dry-run", false, "Print what would be written without touching the database")
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
	if !dryRun {
		// The schedule tables must exist before the new binary serves reads.
		// AutoMigrate only adds; it never drops.
		if err := registry.Migrate(db); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}

	client, err := declclient.New(kubeconfig, metav1.NamespaceAll, 0)
	if err != nil {
		return fmt.Errorf("build the control-plane client: %w", err)
	}
	items, err := client.Clientset.ChatV1alpha1().
		DutyGroups(metav1.NamespaceAll).
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list duty groups: %w", err)
	}

	// SQL keys a group by name alone. A control-plane object is namespaced, so
	// two same-named objects cannot both survive the move; report and skip
	// rather than letting the last one win.
	byName := make(map[string][]*chatv1alpha1.DutyGroup, len(items.Items))
	for i := range items.Items {
		group := &items.Items[i]
		byName[group.Name] = append(byName[group.Name], group)
	}

	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)

	written, skipped := 0, 0
	for _, name := range names {
		groups := byName[name]
		if len(groups) > 1 {
			fmt.Fprintf(os.Stderr, "skip %q: %d namespaced objects share this name\n", name, len(groups))
			skipped++
			continue
		}
		row := chatcps.DutyGroupFromCR(groups[0])
		if dryRun {
			if err := printRow(row); err != nil {
				return err
			}
			written++
			continue
		}
		if err := upsert(db, &row); err != nil {
			return fmt.Errorf("write %q: %w", name, err)
		}
		written++
	}

	fmt.Printf("duty groups: %d written, %d skipped\n", written, skipped)
	return nil
}

func upsert(db *gorm.DB, row *model.OncallGroupM) error {
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"display_name", "status", "short_desc", "long_desc", "agents",
			"schedule_id", "allocation_strategy", "owner", "is_split_by_biz_line",
			"generate_weekly_rpt", "source", "welcome_message", "namespace",
			"order", "updated_at",
		}),
	}).Create(row).Error
}

func printRow(row model.OncallGroupM) error {
	encoded, err := json.MarshalIndent(row, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
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
