package cctv

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type archiveBackfillRoundTripper func(*http.Request) (*http.Response, error)

func (f archiveBackfillRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func archiveBackfillResponse(body string) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body: io.NopCloser(strings.NewReader(body)),
	}, nil
}

func TestResolveSeriesBackfillsHistoricalColumnEpisodesOnce(t *testing.T) {
	const (
		pageURL = "https://tv.cctv.com/lm/djldzg/index.shtml"
		columnID = "TOPC1453100395512779"
		currentGUID = "0123456789abcdef0123456789abcdef"
		historyGUID = "fedcba9876543210fedcba9876543210"
		currentURL = "https://tv.cctv.com/2026/10/29/VIDECurrentEpisode.shtml"
		historyURL = "https://tv.cctv.com/2024/04/29/VIDEHistoryEpisode.shtml"
	)
	now := time.Now()
	currentMonth := now.Format("200601")
	historyMonth := now.AddDate(0, -30, 0).Format("200601")
	pageHTML := `<html><head>
<meta property="og:title" content="典籍里的中国_央视网">
<script>var topicID = "` + columnID + `";</script>
</head><body><a href="` + currentURL + `">当前节目</a></body></html>`
	client := New()
	client.HTTPClient().Transport = archiveBackfillRoundTripper(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.Host == "tv.cctv.com" && req.URL.Path == "/lm/djldzg/index.shtml":
			return archiveBackfillResponse(pageHTML)
		case req.URL.Host == "tv.cctv.com" && req.URL.Path == "/lm/djldzg/videoset":
			return archiveBackfillResponse("{}")
		case req.URL.Host == "api.cntv.cn" && req.URL.Path == "/NewVideo/getVideoListByColumn":
			query := req.URL.Query()
			if query.Get("id") != columnID {
				t.Errorf("unexpected column ID: %s", req.URL)
			}
			switch query.Get("d") {
			case currentMonth:
				return archiveBackfillResponse(`{"data":{"total":1,"list":[{"guid":"` + currentGUID + `","title":"当前节目","time":"2026-10-29 20:00:00","url":"` + currentURL + `"}]}}`)
			case historyMonth:
				return archiveBackfillResponse(`{"data":{"total":1,"list":[{"guid":"` + historyGUID + `","title":"历史节目","time":"2024-04-29 20:00:00","url":"` + historyURL + `"}]}}`)
			default:
				return archiveBackfillResponse(`{"data":{"total":0,"list":[]}}`)
			}
		default:
			return nil, fmt.Errorf("unexpected request: %s", req.URL)
		}
	})

	full, err := client.ResolveSeries(context.Background(), pageURL)
	if err != nil {
		t.Fatalf("full archive scan failed: %v", err)
	}
	if len(full.Episodes) != 2 {
		t.Fatalf("expected current and historical episodes, got %d: %#v", len(full.Episodes), full.Episodes)
	}
	seen := map[string]bool{}
	for _, episode := range full.Episodes {
		seen[episode.VideoGUID] = true
	}
	if !seen[currentGUID] || !seen[historyGUID] {
		t.Fatalf("historical archive entry was not backfilled: %#v", full.Episodes)
	}

	recent, err := client.ResolveRecentSeries(context.Background(), pageURL)
	if err != nil {
		t.Fatalf("recent scan failed: %v", err)
	}
	if len(recent.Episodes) != 1 || recent.Episodes[0].VideoGUID != currentGUID {
		t.Fatalf("routine recent scan should not crawl the older archive: %#v", recent.Episodes)
	}
}
