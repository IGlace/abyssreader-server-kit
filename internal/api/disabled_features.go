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

// Google's browser entry point cannot require an access key.
func registerUnavailableFeatures(v1, root *http.ServeMux) {
	v1.HandleFunc(BasePath+"/auth/google/token", disabledMethods("POST"))
	root.HandleFunc(BasePath+"/auth/google/start", disabledMethods("GET"))
}
