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

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	componentbaseconfigv1alpha1 "k8s.io/component-base/config/v1alpha1"
)

const (
	// ControllerManagerDefaultLockObjectNamespace defines default onex controller manager lock object namespace ("kube-system").
	ControllerManagerDefaultLockObjectNamespace string = metav1.NamespaceSystem

	// ControllerManagerDefaultLockObjectName defines default onex controller manager lock object name ("onex-controller-manager").
	ControllerManagerDefaultLockObjectName = "controller-manager"
)

// GenericControllerManagerConfiguration holds configuration for a generic controller-manager.
type GenericControllerManagerConfiguration struct {
	// leaderElection defines the configuration of leader election client.
	LeaderElection componentbaseconfigv1alpha1.LeaderElectionConfiguration `json:"leaderElection,omitempty"`

	// Namespace that the controller watches to reconcile onex-apiserver objects.
	Namespace string `json:"namespace,omitempty"`

	// bindAddress is the IP address for the proxy server to serve on (set to 0.0.0.0
	// for all interfaces)
	BindAddress string `json:"bindAddress,omitempty"`

	// MetricsBindAddress is the IP address and port for the metrics server to serve on,
	// defaulting to 127.0.0.1:20249 (set to 0.0.0.0 for all interfaces)
	MetricsBindAddress string `json:"metricsBindAddress,omitempty"`

	// HealthzBindAddress is the IP address and port for the health check server to serve on,
	// defaulting to 0.0.0.0:20250
	HealthzBindAddress string `json:"healthzBindAddress,omitempty"`

	// PprofBindAddress is the TCP address that the controller should bind to
	// for serving pprof.
	// It can be set to "" or "0" to disable the pprof serving.
	// Since pprof may contain sensitive information, make sure to protect it
	// before exposing it to public.
	PprofBindAddress string `json:"pprofBindAddress,omitempty"`

	// Parallelism defines the amount of parallelism to process miners. Must be greater than 0. Defaults to 16
	Parallelism int32 `json:"parallelism,omitempty"`

	// MinResyncPeriod is the resync period used by the informers backing the
	// Kubernetes controllers (garbage collector, clusterrole aggregation). Each
	// controller gets a period jittered between MinResyncPeriod and
	// 2*MinResyncPeriod, so a fleet does not resync in lockstep.
	MinResyncPeriod metav1.Duration `json:"minResyncPeriod,omitempty"`

	// ClientConnection holds the settings used when talking to the apiserver.
	ClientConnection componentbaseconfigv1alpha1.ClientConnectionConfiguration `json:"clientConnection,omitempty"`

	// SyncPeriod determines the minimum frequency at which watched resources are
	// reconciled. A lower period will correct entropy more quickly, but reduce
	// responsiveness to change if there are many watched resources. Change this
	// value only if you know what you are doing. Defaults to 10 hours if unset.
	SyncPeriod metav1.Duration `json:"syncPeriod,omitempty"`

	// Label value that the controller watches to reconcile cloud miner objects
	WatchFilterValue string `json:"watchFilterValue,omitempty"`

	// Controllers is the list of controllers to enable or disable
	// '*' means "all enabled by default controllers"
	// 'foo' means "enable 'foo'"
	// '-foo' means "disable 'foo'"
	// first item for a particular name wins
	Controllers []string `json:"controllers,omitempty"`
}

// GroupResource describes an group resource.
type GroupResource struct {
	// group is the group portion of the GroupResource.
	Group string `json:"group,omitempty"`
	// resource is the resource portion of the GroupResource.
	Resource string `json:"resource,omitempty"`
}

// GarbageCollectorControllerConfiguration contains elements describing GarbageCollectorController.
type GarbageCollectorControllerConfiguration struct {
	// enables the generic garbage collector. MUST be synced with the
	// corresponding flag of the kube-apiserver. WARNING: the generic garbage
	// collector is an alpha feature.
	EnableGarbageCollector *bool `json:"enableGarbageCollector,omitempty"`
	// concurrentGCSyncs is the number of garbage collector workers that are
	// allowed to sync concurrently.
	ConcurrentGCSyncs int32 `json:"concurrentGCSyncs,omitempty"`
	// gcIgnoredResources is the list of GroupResources that garbage collection should ignore.
	GCIgnoredResources []GroupResource `json:"gcIgnoredResources,omitempty"`
}

// CoreDBConfiguration defines the database every service and the controller
// manager connect to. See pkg/config.CoreDBConfiguration.
type CoreDBConfiguration struct {
	// Dialect selects the backend: "mysql" | "postgres". Empty means mysql.
	Dialect string `json:"dialect,omitempty"`
	// Addr is the database service address (host:port).
	Addr string `json:"addr,omitempty"`
	// Username for access to the database.
	Username string `json:"username,omitempty"`
	// Password for access to the database.
	Password string `json:"password,omitempty"`
	// Database is the database (schema) name.
	Database string `json:"database,omitempty"`
	// Maximum idle connections allowed.
	MaxIdleConnections int `json:"maxIdleConnections,omitempty"`
	// Maximum open connections allowed.
	MaxOpenConnections int `json:"maxOpenConnections,omitempty"`
	// Maximum connection life time allowed.
	MaxConnectionLifeTime metav1.Duration `json:"maxConnectionLifeTime,omitempty"`
	// LogLevel is the gorm log level.
	LogLevel int `json:"logLevel,omitempty"`
	// SSLMode is the libpq sslmode; PostgreSQL only.
	SSLMode string `json:"sslmode,omitempty"`
	// SearchPath is the PostgreSQL schema search path; PostgreSQL only.
	SearchPath string `json:"searchPath,omitempty"`
}

// RedisConfiguration defines the configuration of redis
// clients for components that can run with redis key-value database.
type RedisConfiguration struct {
	// Address of your Redis server(ip:port).
	Addr string `json:"addr"`
	// Username for access to redis service.
	Username string `json:"username"`
	// Optional auth password for Redis db.
	Password string `json:"password"`
	// Database to be selected after connecting to the server.
	Database int `json:"database"`
	// Maximum number of retries before giving up.
	MaxRetries int `json:"maxRetries"`
	// Timeout when connecting to redis service.
	Timeout metav1.Duration `json:"timeout"`
}
