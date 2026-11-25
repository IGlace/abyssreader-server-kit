// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

// Package support interprets the app's published contract support dates. It
// only reads its embedded copy or bytes supplied by an explicit operator command.
package support

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const (
	maxScheduleSize = 64 << 10
	warningWindow   = 90 * 24 * time.Hour
)

//go:embed compat.json
var embedded []byte

type Schedule struct {
	entries map[int]entry
}

type entry struct {
	endDate time.Time
	minimum *threshold
}

type threshold struct {
	minor int
	date  time.Time
}

// Embedded loads the public schedule that shipped with this binary.
func Embedded() (Schedule, error) { return Parse(bytes.NewReader(embedded)) }

// Parse accepts the versioned public file and ignores fields the kit does not use.
func Parse(r io.Reader) (Schedule, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxScheduleSize+1))
	if err != nil || len(data) > maxScheduleSize {
		return Schedule{}, errors.New("could not read the app support schedule")
	}
	var wire struct {
		Version int `json:"formatVersion"`
		Entries []struct {
			Major   int     `json:"contractMajor"`
			EndDate *string `json:"endDate"`
			Minimum *struct {
				Minor int    `json:"minor"`
				Date  string `json:"date"`
			} `json:"upcomingLowestMinor"`
		} `json:"supportSchedule"`
	}
	if json.Unmarshal(data, &wire) != nil || wire.Version != 1 || wire.Entries == nil {
		return Schedule{}, errors.New("invalid app support schedule format")
	}
	invalidEntry := errors.New("invalid app support schedule entry")
	out := Schedule{entries: make(map[int]entry, len(wire.Entries))}
	for _, item := range wire.Entries {
		if _, seen := out.entries[item.Major]; seen || item.Major < 1 {
			return Schedule{}, invalidEntry
		}
		var e entry
		if item.EndDate != nil {
			e.endDate, err = parseDate(*item.EndDate)
			if err != nil {
				return Schedule{}, invalidEntry
			}
		}
		if item.Minimum != nil {
			date, err := parseDate(item.Minimum.Date)
			if err != nil || item.Minimum.Minor < 1 {
				return Schedule{}, invalidEntry
			}
			e.minimum = &threshold{minor: item.Minimum.Minor, date: date}
		}
		out.entries[item.Major] = e
	}
	return out, nil
}

func parseDate(s string) (time.Time, error) {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil || d.Year() < 1 {
		return time.Time{}, errors.New("invalid UTC date")
	}
	return d, nil
}

type effect struct {
	date  time.Time
	label string
}

func (s Schedule) effects(version string) []effect {
	majorPart, minorPart, _ := strings.Cut(version, ".")
	major, majorErr := strconv.Atoi(majorPart)
	minor, minorErr := strconv.Atoi(minorPart)
	got, exists := s.entries[major]
	if majorErr != nil || minorErr != nil || !exists || minor < 0 {
		return nil
	}
	var out []effect
	if !got.endDate.IsZero() {
		out = append(out, effect{got.endDate, fmt.Sprintf("App support for contract %s ends", version)})
	}
	if got.minimum != nil && got.minimum.minor > minor {
		out = append(out, effect{got.minimum.date, fmt.Sprintf("App minimum for this major rises to contract %d.%d (this kit serves %s)", major, got.minimum.minor, version)})
	}
	return out
}

func (e effect) message(now time.Time) string {
	when := "scheduled"
	if !now.Before(e.date) {
		when = "in effect"
	}
	return fmt.Sprintf("%s on %s UTC (%s).", e.label, e.date.Format(time.DateOnly), when)
}

// Report describes every date affecting a served version, including earlier dates
// whose requirements still apply.
func (s Schedule) Report(version string, now time.Time) []string {
	var out []string
	for _, e := range s.effects(version) {
		out = append(out, e.message(now))
	}
	return out
}

func (s Schedule) Warn(version string, now time.Time, report func(string)) {
	for _, e := range s.effects(version) {
		if e.date.Sub(now) < warningWindow {
			report(e.message(now))
		}
	}
}

// Watch checks the same embedded schedule on each tick until shutdown.
func (s Schedule) Watch(ctx context.Context, version string, tick <-chan time.Time, report func(string)) {
	for {
		select {
		case <-ctx.Done():
			return
		case now, open := <-tick:
			if !open {
				return
			}
			s.Warn(version, now, report)
		}
	}
}
