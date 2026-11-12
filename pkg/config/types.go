// Copyright 2022 Lingfei Kong <colin404@foxmail.com>
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
//
// The original repo for this file is https://github.com/onexstack/onex.
//

package config

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	componentbaseconfig "k8s.io/component-base/config"
)

// GenericControllerManagerConfiguration holds configuration for a generic controller-manager.
type GenericControllerManagerConfiguration struct {
	// leaderElection defines the configuration of leader election client.
	LeaderElection componentbaseconfig.LeaderElectionConfiguration

	// Namespace that the controller watches to reconcile onex-apiserver objects.
	Namespace string

	// bindAddress is the IP address for the proxy server to serve on (set to 0.0.0.0
	// for all interfaces)
	BindAddress string

	// MetricsBindAddress is the IP address and port for the metrics server to serve on,
	// defaulting to 127.0.0.1:20249 (set to 0.0.0.0 for all interfaces)
	MetricsBindAddress string

	// HealthzBindAddress is the IP address and port for the health check server to serve on,
	// defaulting to 0.0.0.0:20250
	HealthzBindAddress string

	// PprofBindAddress is the TCP address that the controller should bind to
	// for serving pprof.
	// It can be set to "" or "0" to disable the pprof serving.
	// Since pprof may contain sensitive information, make sure to protect it
	// before exposing it to public.
	PprofBindAddress string

	// Parallelism defines the amount of parallelism to process miners. Must be greater than 0. Defaults to 16.
	Parallelism int32

	// MinResyncPeriod is the resync period used by the informers backing the
	// Kubernetes controllers (garbage collector, clusterrole aggregation). Each
	// controller gets a period jittered between MinResyncPeriod and
	// 2*MinResyncPeriod, so a fleet does not resync in lockstep.
	MinResyncPeriod metav1.Duration

	// ClientConnection holds the settings used when talking to the apiserver.
	ClientConnection componentbaseconfig.ClientConnectionConfiguration

	// SyncPeriod determines the minimum frequency at which watched resources are
	// reconciled. A lower period will correct entropy more quickly, but reduce
	// responsiveness to change if there are many watched resources. Change this
	// value only if you know what you are doing. Defaults to 10 hours if unset.
	SyncPeriod metav1.Duration

	// Label value that the controller watches to reconcile cloud miner objects
	WatchFilterValue string

	// Controllers is the list of controllers to enable or disable
	// '*' means "all enabled by default controllers"
	// 'foo' means "enable 'foo'"
	// '-foo' means "disable 'foo'"
	// first item for a particular name wins
	Controllers []string
}

// GroupResource describes an group resource.
type GroupResource struct {
	// group is the group portion of the GroupResource.
	Group string
	// resource is the resource portion of the GroupResource.
	Resource string
}

// GarbageCollectorControllerConfiguration contains elements describing GarbageCollectorController.
type GarbageCollectorControllerConfiguration struct {
	// enables the generic garbage collector. MUST be synced with the
	// corresponding flag of the kube-apiserver. WARNING: the generic garbage
	// collector is an alpha feature.
	EnableGarbageCollector bool
	// concurrentGCSyncs is the number of garbage collector workers that are
	// allowed to sync concurrently.
	ConcurrentGCSyncs int32
	// gcIgnoredResources is the list of GroupResources that garbage collection should ignore.
	GCIgnoredResources []GroupResource
}

// CoreDBConfiguration defines the database every service and the controller
// manager connect to.
//
// It is the same block the runtime services already read, so an operator keeps
// one spelling of "where the database is" instead of one per binary. It replaces
// the mysql-only configuration this package used to carry: that type could not
// express the dialect, and the deployment it described runs PostgreSQL.
type CoreDBConfiguration struct {
	// Dialect selects the backend: "mysql" | "postgres". Empty means mysql.
	Dialect string
	// Addr is the database service address (host:port).
	Addr string
	// Username for access to the database.
	Username string
	// Password for access to the database.
	Password string
	// Database is the database (schema) name.
	Database string
	// Maximum idle connections allowed.
	MaxIdleConnections int
	// Maximum open connections allowed.
	MaxOpenConnections int
	// Maximum connection life time allowed.
	MaxConnectionLifeTime metav1.Duration
	// LogLevel is the gorm log level.
	LogLevel int
	// SSLMode is the libpq sslmode; PostgreSQL only.
	SSLMode string
	// SearchPath is the PostgreSQL schema search path; PostgreSQL only.
	SearchPath string
}

// RedisConfiguration defines the configuration of redis
// clients for components that can run with redis key-value database.
type RedisConfiguration struct {
	// Address of your Redis server(ip:port).
	Addr string
	// Username for access to redis service.
	Username string
	// Optional auth password for Redis db.
	Password string
	// Database to be selected after connecting to the server.
	Database int
	// Maximum number of retries before giving up.
	MaxRetries int
	// Timeout when connecting to redis service.
	Timeout metav1.Duration
}
