// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package support

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestStartupAndDailyWarningsAt89And91Days(t *testing.T) {
	for _, days := range []int{89, 91} {
		t.Run(fmt.Sprint(days), func(t *testing.T) {
			start := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
			end := start.AddDate(0, 0, days).Format(time.DateOnly)
			s, err := Parse(strings.NewReader(fmt.Sprintf(
				`{"formatVersion":1,"supportSchedule":[{"contractMajor":1,"endDate":%q}]}`, end)))
			if err != nil {
				t.Fatal(err)
			}
			ticks := make(chan time.Time, 1)
			ticks <- start.AddDate(0, 0, 1)
			close(ticks)
			var messages []string
			report := func(msg string) { messages = append(messages, msg) }
			s.Warn("1.0", start, report)
			s.Watch(context.Background(), "1.0", ticks, report)
			want := 0
			if days == 89 {
				want = 2
			}
			if len(messages) != want {
				t.Fatalf("startup and daily warnings: %v; want %d warnings", messages, want)
			}
		})
	}
}

func TestReportDistinguishesMinorThresholdAndOtherMajors(t *testing.T) {
	s, err := Parse(strings.NewReader(`{
		"formatVersion":1,"supportSchedule":[
			{"contractMajor":1,"upcomingLowestMinor":{"minor":2,"date":"2097-01-01"}},
			{"contractMajor":2,"endDate":"2027-01-01"}
		]}`))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC)
	got := s.Report("1.1", now)
	if len(got) != 1 || !strings.Contains(got[0], "2097-01-01") || !strings.Contains(got[0], "1.2") {
		t.Fatalf("report: %v", got)
	}
	if got := s.Report("1.2", now); len(got) != 0 {
		t.Fatalf("already current: %v", got)
	}
	var warnings []string
	s.Warn("1.1", now, func(msg string) { warnings = append(warnings, msg) })
	if len(warnings) != 0 {
		t.Fatalf("distant date: %v", warnings)
	}
	s.Warn("1.1", time.Date(2097, 2, 1, 0, 0, 0, 0, time.UTC), func(msg string) { warnings = append(warnings, msg) })
	if len(warnings) != 1 || !strings.Contains(warnings[0], "in effect") {
		t.Fatalf("expired support needs warning: %v", warnings)
	}
}

func TestParseRejectsDamagedSchedules(t *testing.T) {
	for _, input := range []string{
		`{"formatVersion":2,"supportSchedule":[]}`,
		`{"formatVersion":1}`,
		`{"formatVersion":1,"supportSchedule":[{"contractMajor":1,"endDate":"2026-02-30"}]}`,
		`{"formatVersion":1,"supportSchedule":[{"contractMajor":1,"upcomingLowestMinor":{"minor":0,"date":"2026-12-01"}}]}`,
		`{"formatVersion":1,"supportSchedule":[{"contractMajor":1},{"contractMajor":1}]}`,
		`{"formatVersion":1,"supportSchedule":[]}{}`,
	} {
		if _, err := Parse(strings.NewReader(input)); err == nil {
			t.Fatalf("accepted invalid schedule: %s", input)
		}
	}
	if _, err := Embedded(); err != nil {
		t.Fatal(err)
	}
}
