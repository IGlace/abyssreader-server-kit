// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import "net/http"

func featureDisabled(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "feature_disabled", "This server has switched this feature off.")
}

func disabledMethods(allowed ...string) http.HandlerFunc {
	handlers := make(map[string]http.HandlerFunc, len(allowed))
	for _, method := range allowed {
		handlers[method] = featureDisabled
	}
	return methods(handlers)
}

func routeActions(mux *http.ServeMux, pattern string, actions map[string]http.HandlerFunc) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		handler, ok := actions[r.PathValue("action")]
		if !ok {
			notFound(w, r)
			return
		}
		handler(w, r)
	})
}

// Keep contract operations registered even when the kit can't switch the
// feature on. All except Google's browser entry point go through v1 auth.
func registerUnavailableFeatures(v1, root *http.ServeMux) {
	for path, allowed := range map[string][]string{
		"/comments/{id}":                    {"PUT", "DELETE"},
		"/comments/{id}/replies":            {"GET", "POST"},
		"/comments/{id}/vote":               {"PUT"},
		"/comments/{id}/reports":            {"POST"},
		"/collections":                      {"GET", "POST"},
		"/me/collections":                   {"GET"},
		"/collections/{id}":                 {"GET", "PUT", "DELETE"},
		"/collections/by-slug/{slug}":       {"GET"},
		"/collection-comments/{id}":         {"PUT", "DELETE"},
		"/collection-comments/{id}/replies": {"GET", "POST"},
		"/collection-comments/{id}/vote":    {"PUT"},
		"/collection-comments/{id}/reports": {"POST"},
		"/me/stats":                         {"GET"},
		"/rankings":                         {"GET"},
		"/auth/google/token":                {"POST"},
	} {
		v1.HandleFunc(BasePath+path, disabledMethods(allowed...))
	}
	routeActions(v1, BasePath+"/collections/{id}/{action}", map[string]http.HandlerFunc{
		"titles":   disabledMethods("GET"),
		"vote":     disabledMethods("PUT"),
		"reports":  disabledMethods("POST"),
		"import":   disabledMethods("POST"),
		"comments": disabledMethods("GET", "POST"),
	})
	root.HandleFunc(BasePath+"/auth/google/start", disabledMethods("GET"))
}
