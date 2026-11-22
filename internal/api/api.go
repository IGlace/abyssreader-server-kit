// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

// Package api serves server contract v1 over HTTP: the fixed GET /api/status and the routes
// under /api/v1 (contract/openapi.yaml), plus /healthz for the container health check.
package api

import (
	"database/sql"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/accounts"
	"github.com/IGlace/abyssreader-server-kit/internal/catalog"
	"github.com/IGlace/abyssreader-server-kit/internal/config"
	"github.com/IGlace/abyssreader-server-kit/internal/covers"
	"github.com/IGlace/abyssreader-server-kit/internal/usersync"
)

// ContractVersion is the contract version this server implements for major 1. A test keeps it
// equal to contract/openapi.yaml's info.version.
const ContractVersion = "1.0"

// viewWindow is how long one client's reads of one title count as one view.
const viewWindow = time.Hour

// BasePath is where contract major 1's routes live.
const BasePath = "/api/v1"

// Options is everything the HTTP layer needs.
type Options struct {
	Config   config.Config
	DB       *sql.DB
	Accounts *accounts.Service
	Covers   *covers.Store // the stored covers, served at /covers/
	Pages    *covers.Store // the page images of hosted chapters, served at /pages/
	Logger   *slog.Logger
	// The optional features that are switched on are Config.Features (ABYSS_FEATURES): listed in
	// /api/status, and every route of one that is off answers 404 feature_disabled (feature()).
}

type server struct {
	Options
	signUps      *limiter // per client
	signIns      *limiter // password checks per client
	accountFails *limiter // wrong passwords per account, from any client
	views        *limiter // one counted view per client and title per viewWindow
	hostedReads  *limiter // the hosted chapter and page-list routes, per client
	pageImages   *limiter // page images, per client
	syncPushes   *limiter // sync pushes, per account
	syncPulls    *limiter // sync pulls, per account
	clientKey    func(*http.Request) string
	status       statusBody
	catalog      *catalog.Catalog
	backups      *backupLimits
	sync         *usersync.Service
}

// New returns the server's HTTP handler.
func New(o Options) http.Handler {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	s := &server{
		Options:      o,
		signUps:      newLimiter(o.Config.SignUpsPerHour, time.Hour),
		signIns:      newLimiter(o.Config.SignInsPerMinute, time.Minute),
		accountFails: newLimiter(o.Config.SignInFailuresPerAccount, 15*time.Minute),
		views:        newLimiter(1, viewWindow),
		hostedReads:  newLimiter(o.Config.HostedRequestsPerMinute, time.Minute),
		pageImages:   newLimiter(o.Config.PageImagesPerMinute, time.Minute),
		syncPushes:   newLimiter(o.Config.SyncPushesPerMinute, time.Minute),
		syncPulls:    newLimiter(o.Config.SyncPullsPerMinute, time.Minute),
		status:       newStatus(o.Config, o.Config.Features),
		catalog:      catalog.New(o.DB, nil, nil).ShowHosted(o.Config.FeatureOn(config.HostedChapters)),
		sync:         usersync.New(o.DB),
		backups:      newBackupLimits(),
	}
	s.clientKey = clientKey(o.Config.TrustedProxies, func(peer string) {
		o.Logger.Warn("requests arrive from a private address but ABYSS_TRUSTED_PROXIES trusts nothing: "+
			"every client behind that proxy shares one rate-limit budget. Leave ABYSS_TRUSTED_PROXIES empty "+
			"for the defaults (loopback and private ranges) or list your proxy.", "peer", peer)
	})

	v1 := http.NewServeMux()
	route(v1, BasePath+"/auth/sign-up", map[string]http.HandlerFunc{"POST": s.signUp})
	route(v1, BasePath+"/auth/sign-in", map[string]http.HandlerFunc{"POST": s.signIn})
	route(v1, BasePath+"/me", map[string]http.HandlerFunc{"GET": s.signedIn(s.getMe), "PUT": s.signedIn(s.updateMe)})
	route(v1, BasePath+"/me/password", map[string]http.HandlerFunc{"PUT": s.signedIn(s.changePassword)})
	route(v1, BasePath+"/me/deletion", map[string]http.HandlerFunc{"POST": s.signedIn(s.deleteAccount)})
	// Catalog, search and title details; sources and definitions.
	route(v1, BasePath+"/titles", map[string]http.HandlerFunc{"GET": s.searchTitles})
	route(v1, BasePath+"/titles/{id}", map[string]http.HandlerFunc{"GET": s.getTitle})
	route(v1, BasePath+"/titles/by-slug/{slug}", map[string]http.HandlerFunc{"GET": s.getTitleBySlug})
	route(v1, BasePath+"/genres", map[string]http.HandlerFunc{"GET": s.listGenres})
	route(v1, BasePath+"/sources", map[string]http.HandlerFunc{"GET": s.listSources})
	route(v1, BasePath+"/definitions/keys", map[string]http.HandlerFunc{"GET": s.listDefinitionKeys})
	route(v1, BasePath+"/definitions/manifest", map[string]http.HandlerFunc{"GET": s.getDefinitionManifest})
	route(v1, BasePath+"/definitions/{slug}", map[string]http.HandlerFunc{"GET": s.getDefinition})
	route(v1, BasePath+"/definitions/{slug}/health", map[string]http.HandlerFunc{"POST": s.reportDefinitionHealth})
	// Library and sync.
	route(v1, BasePath+"/me/library", map[string]http.HandlerFunc{"GET": s.signedIn(s.getLibrary)})
	route(v1, BasePath+"/sync/push", map[string]http.HandlerFunc{"POST": s.signedIn(s.perAccount(s.syncPushes, s.syncPush))})
	route(v1, BasePath+"/sync/pull", map[string]http.HandlerFunc{"GET": s.signedIn(s.perAccount(s.syncPulls, s.syncPull))})
	v1.HandleFunc(BasePath+"/", notFound)

	root := http.NewServeMux()
	route(root, "/api/status", map[string]http.HandlerFunc{"GET": s.getStatus})
	route(root, "/healthz", map[string]http.HandlerFunc{"GET": s.healthz})
	// Covers load with no headers: outside the access key and the session.
	route(root, coversPath+"{name}", map[string]http.HandlerFunc{"GET": s.serveCover})
	s.registerFeatures(v1, root)
	// Every v1 route needs the access key (when one is set), then a valid token or none.
	root.Handle(BasePath+"/", s.accessKey(s.session(v1)))
	root.HandleFunc("/", notFound)

	return s.logRequests(s.recoverPanics(root))
}

// route registers one handler per method on path, and a 405 with the error body for any other
// method (the mux's own 405 has a text body). It dispatches by method itself, so that a
// wildcard path ("/definitions/{slug}") and a literal sibling ("/definitions/keys") never
// conflict in the mux. A GET handler also answers HEAD, as the mux's GET patterns do.
func route(mux *http.ServeMux, path string, byMethod map[string]http.HandlerFunc) {
	mux.HandleFunc(path, methods(byMethod))
}

func methods(byMethod map[string]http.HandlerFunc) http.HandlerFunc {
	allowed := make([]string, 0, len(byMethod))
	for m := range byMethod {
		allowed = append(allowed, m)
	}
	slices.Sort(allowed)
	return func(w http.ResponseWriter, r *http.Request) {
		h, ok := byMethod[r.Method]
		if !ok && r.Method == http.MethodHead {
			h, ok = byMethod[http.MethodGet]
		}
		if ok {
			h(w, r)
			return
		}
		for _, m := range allowed {
			w.Header().Add("Allow", m)
		}
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "This method is not allowed here.")
	}
}

func notFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "not_found", "There is nothing at this address.")
}

func (s *server) healthz(w http.ResponseWriter, r *http.Request) {
	if err := s.DB.PingContext(r.Context()); err != nil {
		s.Logger.Error("health check: database", "err", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "The database is not reachable.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
