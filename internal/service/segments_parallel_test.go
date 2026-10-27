package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFetchSegmentsInOrderRunsParallelAndKeepsOrder(t *testing.T) {
	var active, peak atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/segment/", func(w http.ResponseWriter, r *http.Request) {
		now := active.Add(1)
		for old := peak.Load(); now > old; old = peak.Load() {
			if peak.CompareAndSwap(old, now) {
				break
			}
		}
		defer active.Add(-1)
		time.Sleep(50 * time.Millisecond)
		index, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/segment/"))
		fmt.Fprintf(w, "segment-%02d", index)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	const total, concurrency = 12, 4
	segments := make([]hlsSegment, total)
	for i := range segments {
		segments[i].URL = fmt.Sprintf("%s/segment/%d", server.URL, i)
	}
	processed := 0
	err := fetchSegmentsInOrder(context.Background(), server.Client(), segments, concurrency, func(index int, data []byte) error {
		if index != processed || string(data) != fmt.Sprintf("segment-%02d", index) {
			return fmt.Errorf("segments were not consumed in order: index=%d data=%q expected=%d", index, data, processed)
		}
		processed++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if processed != total {
		t.Fatalf("processed %d of %d segments", processed, total)
	}
	if max := peak.Load(); max < 2 || max > concurrency {
		t.Fatalf("HTTP concurrency = %d; expected 2..%d", max, concurrency)
	}
}

func TestFetchSegmentsInOrderCancelsWorkers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/0" {
			_, _ = w.Write([]byte("first"))
			return
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	segments := make([]hlsSegment, 6)
	for i := range segments {
		segments[i].URL = fmt.Sprintf("%s/%d", server.URL, i)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := fetchSegmentsInOrder(ctx, server.Client(), segments, 4, func(index int, data []byte) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestFetchSegmentsInOrderReportsHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/2" {
			http.Error(w, "failed", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	segments := []hlsSegment{
		{URL: server.URL + "/0"},
		{URL: server.URL + "/1"},
		{URL: server.URL + "/2"},
		{URL: server.URL + "/3"},
	}
	err := fetchSegmentsInOrder(context.Background(), server.Client(), segments, 3, func(int, []byte) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") || !strings.Contains(err.Error(), "3/4") {
		t.Fatalf("expected segment 3 HTTP error, got %v", err)
	}
}
