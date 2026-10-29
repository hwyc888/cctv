package cctv

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLiveCCTVColumnArchive(t *testing.T) {
	if os.Getenv("CCTV_LIBRARY_INTEGRATION") != "1" {
		t.Skip("set CCTV_LIBRARY_INTEGRATION=1 to query the real CCTV column archive")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	series, err := New().ResolveLink(ctx, "https://tv.cctv.com/lm/djldzg/index.shtml")
	if err != nil {
		t.Fatalf("resolve 典籍里的中国 column page: %v", err)
	}
	if !strings.Contains(series.Title, "典籍里的中国") {
		t.Fatalf("unexpected column title: %q", series.Title)
	}
	if len(series.Episodes) == 0 {
		t.Fatal("CCTV column archive returned no videos")
	}
	apiVideos := 0
	for _, episode := range series.Episodes {
		if episode.VideoGUID != "" {
			apiVideos++
		}
	}
	if apiVideos == 0 {
		t.Fatalf("only static page links were found; official column API returned no videos: %#v", series.Episodes)
	}
	t.Logf("column=%q episodes=%d API-backed=%d", series.Title, len(series.Episodes), apiVideos)
	for i, episode := range series.Episodes {
		if i >= 5 {
			break
		}
		t.Logf("episode %d: title=%q date=%s guid=%s url=%s", i+1, episode.Title, episode.PublishedAt, episode.VideoGUID, episode.PageURL)
	}
}
