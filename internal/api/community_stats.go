// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"net/http"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/community"
	"github.com/IGlace/abyssreader-server-kit/internal/features"
)

func (s *server) statsFeatures() community.StatsFeatures {
	return community.StatsFeatures{
		Comments:     s.Config.FeatureOn(features.Comments),
		Collections:  s.Config.FeatureOn(features.Collections),
		TitleReports: s.Config.FeatureOn(features.Reports),
	}
}

func (s *server) getStats(w http.ResponseWriter, r *http.Request, c caller) {
	out, err := s.community.Stats(r.Context(), c.account.ID, s.statsFeatures(), time.Now())
	s.replyCommunity(w, r, 200, out, err)
}

func (s *server) listRankings(w http.ResponseWriter, r *http.Request) {
	q := queryReader{v: r.URL.Query()}
	p := communityPage(&q)
	period := q.option("period", "", "monthly", "overall")
	p.Text = q.searchText()
	p.Sort = q.option("sort", "popular", "popular", "alphabetical")
	def := "desc"
	if p.Sort == "alphabetical" {
		def = "asc"
	}
	p.Asc = q.option("order", def, "asc", "desc") == "asc"
	if !q.valid(w) {
		return
	}
	out, err := s.community.Rankings(r.Context(), p, s.statsFeatures(), period == "monthly")
	s.replyCommunity(w, r, 200, out, err)
}
