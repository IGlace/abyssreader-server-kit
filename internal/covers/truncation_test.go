// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package covers

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCheckRefusesTruncatedCoverBodies(t *testing.T) {
	png := pngOf(t, 200, 300)
	jpeg := jpegOf(t, 40, 60)
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"PNG cut in image data", png[:100]},
		{"PNG missing IEND", png[:len(png)-12]},
		{"JPEG missing EOI", jpeg[:len(jpeg)-2]},
		{"WebP missing RIFF padding", webp1x1[:len(webp1x1)-1]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := Sniff(tc.body); err != nil {
				t.Fatalf("test body must have a readable header: %v", err)
			}
			if _, err := Check(tc.body); err == nil || !strings.Contains(err.Error(), "incomplete") {
				t.Fatalf("incomplete cover should be refused, got %v", err)
			}
		})
	}
}

func TestFetchRefusesCoverWhenHTTPBodyEndsCleanlyButImageIsTruncated(t *testing.T) {
	cover := pngOf(t, 200, 300)
	srv, fetcher := coverServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(cover[:100])
	}))
	if _, err := fetcher.Fetch(context.Background(), srv.URL+"/truncated.png"); err == nil {
		t.Fatal("Fetch accepted a PNG cut off after a complete header")
	}
}

// A request-scoped body isolates the deadline from TLS and handler scheduling.
type awaitingCoverBody struct{ context.Context }

func (b awaitingCoverBody) Read(_ []byte) (int, error) {
	<-b.Done()
	return 0, b.Err()
}

func (awaitingCoverBody) Close() error { return nil }

type awaitingCoverTransport struct{}

func (awaitingCoverTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"image/png"}},
		Body:       awaitingCoverBody{r.Context()},
	}, nil
}

func TestFetchTimeoutCoversTheResponseBody(t *testing.T) {
	f := NewFetcher(FetchOptions{})
	f.client.Transport = awaitingCoverTransport{}
	f.timeout = 10 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := f.Fetch(ctx, "https://covers.example.org/slow.png"); err == nil || !strings.Contains(err.Error(), "within 10ms") || ctx.Err() != nil {
		t.Fatalf("expected the fetch timeout during the body read, got %v", err)
	}
}
