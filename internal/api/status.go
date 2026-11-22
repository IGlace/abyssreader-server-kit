// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"net/http"

	"github.com/IGlace/abyssreader-server-kit/internal/config"
)

// The contract's ServerStatus. Every field is always sent; absent operator links are null.
type statusBody struct {
	Name              string          `json:"name"`
	AccessKeyRequired bool            `json:"accessKeyRequired"`
	SignUpOpen        bool            `json:"signUpOpen"`
	Operator          operatorBody    `json:"operator"`
	Contracts         []contractOffer `json:"contracts"`
}

type operatorBody struct {
	Name         string  `json:"name"`
	Contact      string  `json:"contact"`
	PrivacyURL   *string `json:"privacyUrl"`
	TermsURL     *string `json:"termsUrl"`
	CopyrightURL *string `json:"copyrightUrl"`
}

type contractOffer struct {
	Version  string   `json:"version"`
	BasePath string   `json:"basePath"`
	Features []string `json:"features"`
}

func newStatus(c config.Config, features []string) statusBody {
	if features == nil {
		features = []string{}
	}
	return statusBody{
		Name:              c.ServerName,
		AccessKeyRequired: c.AccessKey != "",
		SignUpOpen:        c.SignUpOpen,
		Operator: operatorBody{
			Name: c.Operator.Name, Contact: c.Operator.Contact,
			PrivacyURL: c.Operator.PrivacyURL, TermsURL: c.Operator.TermsURL, CopyrightURL: c.Operator.CopyrightURL,
		},
		Contracts: []contractOffer{{Version: ContractVersion, BasePath: BasePath, Features: features}},
	}
}

// getStatus answers GET /api/status: always public, at this fixed path, ignoring
// Authorization and X-Access-Key.
func (s *server) getStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.status)
}
