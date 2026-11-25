// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package definitions

// IsPublicHTTPSURL applies the definition linter's HTTPS and hostname rules.
// They reject IP literals and reserved local hostnames without making a DNS request.
func IsPublicHTTPSURL(raw string) bool {
	_, err := httpsURL(raw)
	return err == nil
}
