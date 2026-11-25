// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"net/http"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/features"
)

// Every route registered here is gated by its owning feature, including public images.
type featureRoutes struct {
	s            *server
	v1, root     *http.ServeMux
	name         string
	titleActions map[string]http.HandlerFunc
}

func (s *server) registerFeatures(v1, root *http.ServeMux) {
	titleActions := make(map[string]http.HandlerFunc)
	for _, feature := range features.Built {
		feature.Register(featureRoutes{s, v1, root, feature.Name, titleActions})
	}
	// ServeMux prefers the existing literal by-slug/{slug} route, including
	// when a slug is "comments" or "reports". Separate wildcard routes for
	// the title actions would conflict with that by-slug pattern.
	routeActions(v1, BasePath+"/titles/{id}/{action}", titleActions)
	registerUnavailableFeatures(v1, root)
}

func (f featureRoutes) HostedRoutes() {
	s := f.s
	route(f.v1, BasePath+"/source-titles/{id}/chapters", map[string]http.HandlerFunc{"GET": s.feature(f.name, s.limited(s.hostedReads, s.listChapters))})
	route(f.v1, BasePath+"/chapters/{id}/pages", map[string]http.HandlerFunc{"GET": s.feature(f.name, s.limited(s.hostedReads, s.listChapterPages))})
	route(f.root, pagesPath+"{name}", map[string]http.HandlerFunc{"GET": s.feature(f.name, s.limited(s.pageImages, s.servePage))})
}

func (f featureRoutes) ReportRoutes() {
	s := f.s
	submit := s.signedIn(s.perAccount(newLimiter(10, time.Minute), s.reportTitle))
	f.titleActions["reports"] = methods(map[string]http.HandlerFunc{"POST": s.feature(f.name, submit)})
}
