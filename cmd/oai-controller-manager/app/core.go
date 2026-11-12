package app

import (
	"context"
	"fmt"
	"time"

	"k8s.io/client-go/metadata"
	"k8s.io/kubernetes/pkg/controller/garbagecollector"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/onexstack/onexai/cmd/oai-controller-manager/names"
	"github.com/onexstack/onexai/internal/controller/mirror"
	namespacecontroller "github.com/onexstack/onexai/internal/controller/namespace"
	genericconfigoptions "github.com/onexstack/onexai/pkg/config/options"
)

func newGarbageCollectorControllerDescriptor() *ControllerDescriptor {
	return &ControllerDescriptor{
		name:    names.GarbageCollectorController,
		aliases: []string{"garbagecollector"},
		addFunc: addGarbageCollectorController,
	}
}

// newResourceMirrorControllerDescriptor registers the projection of control-plane
// resources into PostgreSQL.
//
// The periodic cleaner is part of this controller rather than a switch of its
// own: a projection that is written but never verified keeps rows for objects
// the control plane no longer has, forever. One switch, one behaviour.
func newResourceMirrorControllerDescriptor() *ControllerDescriptor {
	return &ControllerDescriptor{
		name:    mirror.ControllerName,
		aliases: []string{"mirror", "resource-clean", "mirror-clean"},
		addFunc: addResourceMirrorController,
	}
}

func addResourceMirrorController(ctx context.Context, mgr ctrl.Manager, cctx ControllerContext) (bool, error) {
	cfg := cctx.Config.ComponentConfig.Mirror
	if !cfg.Enabled {
		return false, nil
	}

	// The connection is opened here, once, and outlives the manager's start: the
	// projection writes for as long as the process runs.
	db, err := genericconfigoptions.NewCoreDBOptions(&cctx.Config.ComponentConfig.CoreDB).NewDB()
	if err != nil {
		return false, fmt.Errorf("open the database for the resource mirror: %w", err)
	}

	var namespaces []string
	if ns := cctx.Config.ComponentConfig.Generic.Namespace; ns != "" {
		namespaces = []string{ns}
	}

	return mirror.Add(ctx, mgr, cctx.ControllerManagerOptions, mirror.Config{
		DB:            db,
		Schema:        cfg.Schema,
		Kinds:         cfg.Kinds,
		BatchSize:     int(cfg.BatchSize),
		FlushInterval: cfg.FlushInterval.Duration,
		SweepInterval: cfg.SweepInterval.Duration,
		Namespaces:    namespaces,
	})
}

func newNamespacedResourcesDeleterControllerDescriptor() *ControllerDescriptor {
	return &ControllerDescriptor{
		name:    names.NamespacedResourcesDeleterController,
		aliases: []string{"namespaced-resource-deleter"},
		addFunc: addNamespacedResourcesDeleterController,
	}
}

// add functions
func addNamespacedResourcesDeleterController(ctx context.Context, mgr ctrl.Manager, cctx ControllerContext) (bool, error) {
	return true, namespacecontroller.NewNamespacedResourcesDeleter(mgr, cctx.Config.Client, cctx.MetadataClient).
		SetupWithManager(mgr, cctx.ControllerManagerOptions)
}

// garbageCollector used to defines a garbage collector controller.
type garbageCollector struct {
	cctx ControllerContext
}

// Start implement manager.Runnable interface.
func (gc *garbageCollector) Start(ctx context.Context) error {
	if _, err := startGarbageCollectorController(ctx, gc.cctx); err != nil {
		return err
	}

	// A Runnable must block until ctx is cancelled. Returning here would let the
	// manager treat the garbage collector as finished while the goroutines it
	// started keep deleting objects.
	<-ctx.Done()
	return nil
}

// NeedLeaderElection implements manager.LeaderElectionRunnable. Deleting
// dependents is destructive, so only the leader may run it.
func (gc *garbageCollector) NeedLeaderElection() bool { return true }

func addGarbageCollectorController(ctx context.Context, mgr ctrl.Manager, cctx ControllerContext) (bool, error) {
	return true, mgr.Add(&garbageCollector{cctx})
}

func startGarbageCollectorController(ctx context.Context, cctx ControllerContext) (bool, error) {
	if !cctx.Config.ComponentConfig.GarbageCollectorController.EnableGarbageCollector {
		return false, nil
	}

	gcClientset := cctx.ClientBuilder.ClientOrDie("generic-garbage-collector")
	discoveryClient := cctx.ClientBuilder.DiscoveryClientOrDie("generic-garbage-collector")

	config := cctx.ClientBuilder.ConfigOrDie("generic-garbage-collector")
	// Increase garbage collector controller's throughput: each object deletion takes two API calls,
	// so to get |config.QPS| deletion rate we need to allow 2x more requests for this controller.
	config.QPS *= 2
	metadataClient, err := metadata.NewForConfig(config)
	if err != nil {
		return true, err
	}

	garbageCollector, err := garbagecollector.NewComposedGarbageCollector(
		ctx,
		gcClientset,
		metadataClient,
		cctx.RESTMapper,
		cctx.GraphBuilder,
	)
	if err != nil {
		return true, fmt.Errorf("failed to start the generic garbage collector: %w", err)
	}

	// Start the garbage collector.
	workers := int(cctx.Config.ComponentConfig.GarbageCollectorController.ConcurrentGCSyncs)
	const syncPeriod = 30 * time.Second
	go garbageCollector.Run(ctx, workers, syncPeriod)

	// Periodically refresh the RESTMapper with new discovery information and sync
	// the garbage collector.
	go garbageCollector.Sync(ctx, discoveryClient, syncPeriod)

	return true, nil
}
