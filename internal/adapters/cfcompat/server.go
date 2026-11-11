// Package cfcompat adapts the Cloudflare Workers API surface to krun.
//
// Authentication, CSRF, request scoping and business services stay in the
// control plane. This package owns only the Cloudflare wire protocol: the
// success/error envelope, numeric error codes, route catalog and the
// Cloudflare-specific request/response shapes.
package cfcompat

import (
	"context"
	"net/http"

	"github.com/krun-dev/krun/internal/authorization"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/iam"
	"github.com/krun-dev/krun/internal/storage"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

// Prefix is the single Cloudflare-compatible API prefix.
const Prefix = "/api/v1/cloudflare/client/v4"

type Endpoint func(*domain.State, authorization.Principal, *http.Request) (any, int, error)

type Route struct {
	Pattern      string
	Handler      http.HandlerFunc
	AccountScope bool
}

// Credentials is the request credential summary the Cloudflare adapter passes
// back to the control plane's authentication and scoping pipeline.
type Credentials struct {
	// Value is the control plane's opaque credential snapshot. cfcompat only
	// inspects HasSession and KeyID; the complete session is handed back
	// unchanged to the authentication pipeline.
	Value      any
	KeyID      string
	HasSession bool
}

// Deferred lets a handler resolve its response after the state transaction.
type Deferred interface {
	Run(context.Context) (any, int, error)
}

type deferred struct {
	run func(context.Context) (any, int, error)
}

func (d deferred) Run(ctx context.Context) (any, int, error) { return d.run(ctx) }

func workerRef(r *http.Request) workerapp.WorkerRef {
	ref := workerapp.WorkerRef{Organization: r.PathValue("org"), Worker: r.PathValue("resource"), Environment: r.PathValue("environment")}
	for _, name := range []string{"build", "operation", "artifact", "version", "secret"} {
		if id := r.PathValue(name); id != "" {
			ref.Item = id
			break
		}
	}
	return ref
}

// Server contains the control-plane capabilities the protocol adapter needs.
// It is assembled by internal/control; cfcompat never imports the HTTP layer.
type Server struct {
	Config  *appconfig.Config
	Store   *storage.Store
	Workers *workerapp.Controller

	Application func(*http.Request) *workerapp.Service
	IAM         func() iam.Service

	DecodeBody         func(*http.Request, any) error
	ReadCredentials    func(*http.Request) (Credentials, error)
	ResolveAccount     func(*http.Request) error
	ScopedRequest      func(*http.Request, Credentials) (*http.Request, error)
	ProtectedPrincipal func(*domain.State, Credentials, *http.Request, bool) (authorization.Principal, error)
	ProtectedWorkers   func(bool, int64, Endpoint) http.HandlerFunc
	Require            func(*domain.State, authorization.Principal, *http.Request, string, authorization.Target) error
	WorkerTarget       func(*domain.State, *http.Request, string, string, authorization.Principal) (authorization.Target, error)
	OrgTarget          func(*domain.State, *http.Request) (authorization.Target, error)
	ResourceTarget     func(*domain.State, *http.Request) (authorization.Target, error)
	OrgList            Endpoint
	CheckRateLimit     func(*http.Request) error

	JSON       func(http.ResponseWriter, int, any)
	WriteError func(http.ResponseWriter, *http.Request, error)
}
