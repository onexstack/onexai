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

package di

import (
	"cmp"
	"context"
	"log/slog"
	"os"

	"github.com/a2aproject/a2a-go/v2/a2asrv/eventqueue"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
	"github.com/a2aproject/a2a-go/v2/a2asrv/workqueue"
	"github.com/google/uuid"
	"github.com/onexstack/onexstack/pkg/version"
	"github.com/redis/go-redis/v9"

	"github.com/onexstack/onexai/internal/agent/biz"
	agentbiz "github.com/onexstack/onexai/internal/agent/biz/v1/agent"
	"github.com/onexstack/onexai/internal/agent/dispatcher"
	"github.com/onexstack/onexai/internal/agent/handler"
	agent2a "github.com/onexstack/onexai/internal/agent/pkg/a2a"
	"github.com/onexstack/onexai/internal/agent/pkg/interactionlog"
	"github.com/onexstack/onexai/internal/agent/store/runstore"
)

// ProvideRunDispatcher resumes asynchronous run admissions left pending by a
// crash. The HTTP handler executes fresh submissions directly; this loop is the
// recovery path, and both share RunBiz.Execute.
func ProvideRunDispatcher(b biz.IBiz, runs runstore.Store) *dispatcher.RunDispatcher {
	if b == nil || runs == nil {
		return nil
	}
	return dispatcher.New(runs, b.RunV1().Execute, dispatcher.DefaultInterval, dispatcher.DefaultBatch)
}

// ProvideA2AHandlers builds the Agent-to-Agent transports.
//
// The surface is mounted in every deployment: the routes exist so a client can
// tell "not offered" from "wrong URL", and a deployment with no agent engine
// refuses each task the way every other agent surface refuses (see
// Handler.agentOrFail). What a deployment decides is what the card advertises
// and which agent a request gets by default.
func ProvideA2AHandlers(
	ctx context.Context, cfg *Config, b biz.IBiz, screen handler.Screener,
	interactions *interactionlog.Writer, rdb *redis.Client,
) (*handler.A2AHandlers, error) {
	// cfg and b are what Wire builds: ServerOptions.Config always allocates every
	// options block, and NewBiz always returns a value. The one dependency that
	// can be absent is the engine itself — a deployment with no model — and that
	// shows up as a nil agent biz.
	agentOptions := cfg.AgentOptions

	runner := agent2a.Runner(b.Agent())
	if runner == nil {
		slog.Warn("agent.a2a has no agent engine behind it; every task will be refused")
	}

	// Everything a task needs in order to outlive one replica lives in Redis
	// whenever there is a Redis at all: the task itself, its events, and the
	// queue that hands the execution to whichever replica claims it. Without it
	// the surface still works — one replica at a time, no resubscribe across
	// pods, and tasks lost on restart — which is what the SDK's in-memory
	// defaults give.
	var (
		taskStore  taskstore.Store
		eventQueue eventqueue.Manager
		workQueue  workqueue.Queue
	)
	if rdb != nil {
		prefix := redisPrefix(cfg)
		taskStore = agent2a.NewRedisTaskStore(rdb, prefix)
		eventQueue = agent2a.NewRedisEventQueue(rdb, prefix)
		workQueue = agent2a.NewRedisWorkQueue(ctx, rdb, prefix, a2aConsumer())
		slog.Info("a2a: tasks are distributed across replicas",
			"event_queue", "redis", "work_queue", "redis", "task_store", "redis")
	} else {
		// Worth a line at startup: on one replica everything works, and the
		// difference only shows up as a resubscribe that reaches nothing.
		slog.Info("a2a: tasks run on the replica that receives them; resubscribe reaches only that replica")
	}

	handlers, err := agent2a.NewHandlers(runner, agent2a.Options{
		DefaultAgent: agentOptions.A2ADefaultAgent,
		// The A2A-specific setting wins over the general one: a deployment that
		// wrote a sentence for A2A peers meant it for them.
		Description:  cmp.Or(agentOptions.A2ADescription, agentOptions.Description),
		PublicURL:    agentOptions.A2APublicURL,
		TrustedProxy: agentOptions.A2ATrustedProxy,
		Version:      version.Get().GitVersion,
		// The inbound audit and the per-caller output ceiling are edge policy
		// that lives in the handler package; the adapter asks, the edge answers,
		// so an A2A call is screened exactly like a chat message.
		Guard:        handler.NewA2AGuard(screen, interactions),
		Lookup:       a2aAgentLookup{agents: b.AgentV1()},
		TaskStore:    taskStore,
		QueueManager: eventQueue,
		WorkQueue:    workQueue,
		ContextCodec: agent2a.CallContextCodec{},
	})
	if err != nil {
		return nil, err
	}

	return &handler.A2AHandlers{
		JSONRPC: handlers.JSONRPC,
		REST:    handlers.REST,
		Card:    handlers.Card,
	}, nil
}

// a2aConsumer names this replica in the work queue's consumer group.
//
// It has to be unique per process: two replicas sharing a name would split the
// acknowledgements between them, and entries would sit pending under a name
// nobody claims. The hostname is enough to tell pods apart, with a random
// suffix because a restarted pod keeps its name but is a new consumer.
func a2aConsumer() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return uuid.NewString()
	}
	return host + "-" + uuid.NewString()[:8]
}

// a2aAgentLookup answers whether an agent a caller named exists.
//
// It is an adapter rather than a method on the executor so the adapter package
// keeps knowing nothing about the resource store.
type a2aAgentLookup struct {
	agents agentbiz.AgentResourceBiz
}

var _ agent2a.AgentLookup = a2aAgentLookup{}

// Exists implements agent2a.AgentLookup.
func (l a2aAgentLookup) Exists(ctx context.Context, name string) (bool, error) {
	return l.agents.Exists(ctx, name)
}
