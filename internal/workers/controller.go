package workers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	kubernetes "github.com/krun-dev/krun/internal/adapters/kubernetes"
	objectstore "github.com/krun-dev/krun/internal/adapters/objectstore"
	runtimeclient "github.com/krun-dev/krun/internal/adapters/runtimeclient"
	workerartifact "github.com/krun-dev/krun/internal/artifact"
	appconfig "github.com/krun-dev/krun/internal/config"
	domain "github.com/krun-dev/krun/internal/domain"
	securetoken "github.com/krun-dev/krun/internal/securetoken"
	storage "github.com/krun-dev/krun/internal/storage"
	"golang.org/x/sync/singleflight"
)

// workerPutObject is the narrow object-write seam used by source bundles and
// per-file asset copies. Production wires it to the S3 client; tests inject it.
type WorkerPutObject func(ctx context.Context, bucket, object string, reader io.Reader, size int64, opts objectstore.PutObjectOptions) (objectstore.UploadInfo, error)

type Controller struct {
	ValidateHyperdrive func(context.Context, domain.WorkerVersion, appconfig.WorkerRuntimePool) error
	ReadCredentials    ReadCredentialSource
	Service            *Service
	Config             *appconfig.WorkersConfig
	S3                 objectstore.Client
	S3LogRead          objectstore.Client
	PutObject          WorkerPutObject
	// Builder uploads can use a different network view of the same bucket.
	S3Builder objectstore.Client
	Kube      *kubernetes.Client
	Http      runtimeclient.Doer
	// Cache immutable manifests independently of the scoped read grants.
	assetFlights singleflight.Group
	assetMu      sync.RWMutex
	AssetCache   map[string]WorkerCachedAssets
	// Edge config publication is coordinated by a durable revision CAS; every
	// replica may race without electing a process-wide leader.
	configMu sync.Mutex

	configBucket bool

	routingReady   map[string]bool
	routingChecked time.Time
	objectGCAfter  map[string]string
	// Local build executor state for builder_mode: local.
	localMu      sync.Mutex
	localBuilds  map[string]*WorkerLocalBuild
	localWG      sync.WaitGroup
	logPublished map[string]time.Time
	lifecycle    context.Context
	// replica identifies this process in claimed_by columns. It only needs to be
	// unique among live replicas, not stable across restarts.
	Replica string
}

type WorkerLocalBuild struct{ Running bool }

type WorkerCachedAssets struct {
	Used   time.Time
	Config *domain.WorkerAssetsConfig
	Files  []workerartifact.WorkerManifestFile
}

func NewController(s *Service) (*Controller, error) {
	s3, e := NewWorkerS3(s.Config.Workers)
	if e != nil {
		return nil, e
	}
	s3Builder := s3
	if s.Config.Workers.S3BuilderEndpoint != s.Config.Workers.S3Endpoint {
		s3Builder, e = NewWorkerS3Endpoint(s.Config.Workers, s.Config.Workers.S3BuilderEndpoint)
		if e != nil {
			return nil, e
		}
	}
	s3LogRead := s3
	if endpoint := s.Config.Workers.S3LogReadEndpoint; endpoint != "" && endpoint != s.Config.Workers.S3Endpoint {
		s3LogRead, e = NewWorkerS3Endpoint(s.Config.Workers, endpoint)
		if e != nil {
			return nil, e
		}
	}
	k, e := kubernetes.New(s.Config.Workers)
	if e != nil {
		return nil, e
	}
	controller := &Controller{Service: s, Config: s.Config.Workers, S3: s3, S3LogRead: s3LogRead, PutObject: s3.PutObject, S3Builder: s3Builder, Kube: k, Http: runtimeclient.New(), AssetCache: map[string]WorkerCachedAssets{}, localBuilds: map[string]*WorkerLocalBuild{}, Replica: WorkerReplicaID()}
	s.Controller = controller
	return controller, nil
}

// workerReplicaID names this process in claimed_by columns.
func WorkerReplicaID() string {
	host, _ := os.Hostname()
	return host + "/" + strconv.FormatInt(time.Now().UnixNano()%1e9, 36)
}

func (w *Controller) Run(ctx context.Context) {
	gcDone := make(chan struct{})
	go func() {
		defer close(gcDone)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cycle, cancel := context.WithTimeout(ctx, 8*time.Second)
				if err := w.CollectWorkerObjects(cycle); err != nil && ctx.Err() == nil {
					fmt.Println("workers cleanup:", err)
				}
				cancel()
			}
		}
	}()
	defer func() { <-gcDone }()
	w.lifecycle = ctx
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			w.localWG.Wait()
			return
		case <-ticker.C:

			cycle, cancel := context.WithTimeout(ctx, 25*time.Second)
			e := w.Reconcile(cycle)
			cancel()
			if e != nil && ctx.Err() == nil {
				fmt.Println("workers reconcile:", e)
			}
		}
	}
}

// reconcile runs one cycle. It reads only the routing topology plus unfinished
// work, so an idle control plane scans no build or operation rows and never
// deserializes a whole document.
func (w *Controller) Reconcile(ctx context.Context) error {
	var failures []error

	if _, e := w.PublishConfig(ctx); e != nil {
		failures = append(failures, e)
	}

	pending, e := w.Service.Store.PendingWork(context.WithValue(ctx, storage.WorkerQueueReplicaKey{}, w.Replica), 100)
	if e != nil {
		return e
	}
	builds := make([]domain.WorkerBuild, 0, len(pending.Builds))
	for _, b := range pending.Builds {
		builds = append(builds, b)
	}
	sort.Slice(builds, func(i, j int) bool { return builds[i].Created.Before(builds[j].Created) })
	limit := w.Config.ReconcileConcurrency
	if limit < 1 {
		limit = 8
	}
	work := make(chan func() error, len(builds)+len(pending.Operations))
	for _, b := range builds {
		b := b
		work <- func() error {
			claimed, err := w.Service.Store.ClaimBuild(ctx, b.ID, w.Replica)
			if err != nil || !claimed {
				return err
			}
			if err = w.Service.Store.ScheduleWorkerRetry(ctx, "build", b.ID); err != nil {
				return err
			}
			b.State = "building"
			return w.ProcessClaimedBuild(ctx, b, pending.BuildPrivate[b.ID])
		}
	}

	routesReady := w.routingReady
	routeInterval := 30 * time.Second
	for _, ready := range routesReady {
		if !ready {
			routeInterval = time.Second
			break
		}
	}
	if routesReady == nil || w.routingChecked.IsZero() || time.Now().UTC().Sub(w.routingChecked) >= routeInterval {
		var routeErr error
		routesReady, routeErr = w.ReconcileRoutes(ctx)
		if routeErr != nil {
			failures = append(failures, routeErr)
		} else {
			w.routingReady = routesReady
			w.routingChecked = time.Now().UTC()
		}
	}

	ops := make([]domain.WorkerOperation, 0, len(pending.Operations))
	for _, op := range pending.Operations {
		ops = append(ops, op)
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].Created.Before(ops[j].Created) })
	for _, op := range ops {
		op := op
		work <- func() error {
			if err := w.Service.Store.ScheduleWorkerRetry(ctx, "operation", op.ID); err != nil {
				return err
			}
			ready := false
			if p, ok := domain.ActiveWorkerPlacement(&pending, op.Worker); ok {
				ready = routesReady["pool:"+p.Pool]
			}

			return w.ProcessOperation(ctx, op, pending, ready)
		}
	}
	close(work)
	var wg sync.WaitGroup
	errs := make(chan error, len(builds)+len(ops))
	for i := 0; i < limit; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range work {
				if ctx.Err() != nil {
					errs <- ctx.Err()
					continue
				}
				if err := task(); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		failures = append(failures, err)
	}

	return errors.Join(failures...)
}

// The namespace inventory is read once, with pagination, and reused for every
// stale-route check. Route repair is independent of the one-second work queue.
func (w *Controller) ReconcileRoutes(ctx context.Context) (map[string]bool, error) {
	var failures []error
	names, err := w.Kube.ListRoutes(ctx)
	if err != nil {
		return nil, err
	}
	ctx = context.WithValue(ctx, kubernetes.WorkerRouteInventoryKey{}, names)

	desiredStatic := map[string]bool{}
	for _, d := range w.Config.Domains {
		desiredStatic["workers-static-"+d.ID] = true
		if e := w.Kube.EnsureStaticRoute(ctx, w.Config, d.ID); e != nil {
			failures = append(failures, e)
		}
	}

	staleStatic, staleErr := kubernetes.StaleWorkerRoutes(ctx, w.Kube, "workers-static-", desiredStatic)
	if staleErr != nil {
		failures = append(failures, staleErr)
	} else {
		for _, n := range staleStatic {
			if e := w.Kube.DeleteRoute(ctx, n); e != nil {
				failures = append(failures, e)
			}
		}
	}
	routesReady := map[string]bool{}
	desiredRoutes := map[string]bool{}
	for name := range desiredStatic {
		desiredRoutes[name] = true
	}
	for _, pool := range w.Config.ConfiguredRuntimePools() {
		ready := true
		for _, domain := range w.Config.Domains {
			owner, err := w.Config.DomainRuntimePool(domain.ID)
			if err != nil {
				failures = append(failures, err)
				ready = false
				continue
			}
			if owner.ID != pool.ID {
				continue
			}
			gateway, ok := w.Config.Gateway(domain.Gateway)
			if !ok {
				continue
			}
			name := "workers-shared-" + securetoken.Digest(pool.ID + "/" + domain.ID)[:24]
			desiredRoutes[name] = true
			adopted, err := w.Kube.EnsureRoute(ctx, name, pool.RuntimeService, gateway, []string{"*." + domain.Domain})
			ready = ready && adopted && err == nil
			if err != nil {
				failures = append(failures, err)
			}
		}
		routesReady["pool:"+pool.ID] = ready
	}

	staleRoutes, staleErr := kubernetes.StaleWorkerRoutes(ctx, w.Kube, "workers-", desiredRoutes)
	if staleErr != nil {
		failures = append(failures, staleErr)
	} else {
		for _, name := range staleRoutes {
			if e := w.Kube.DeleteRoute(ctx, name); e != nil {
				failures = append(failures, e)
			}
		}
	}
	return routesReady, errors.Join(failures...)
}
