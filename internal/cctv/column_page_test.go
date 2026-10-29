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

type columnPageRoundTripper func(*http.Request) (*http.Response, error)

func (f columnPageRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func columnPageResponse(body string) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body: io.NopCloser(strings.NewReader(body)),
		Request: nil,
	}, nil
}

func TestResolveLinkHandlesCCTVColumnArchive(t *testing.T) {
	const (
		pageURL = "https://tv.cctv.com/lm/djldzg/index.shtml"
		columnID = "TOPC1453100395512779"
		guid = "0123456789abcdef0123456789abcdef"
		videoURL = "https://tv.cctv.com/2026/10/29/VIDE123456789012345678901234567890.shtml"
	)
	currentMonth := time.Now().Format("200601")
	pageHTML := `<!doctype html>
<html><head>
<meta property="og:title" content="典籍里的中国_央视网_央视网(cctv.com)">
<meta property="og:description" content="中华优秀文化典籍">
<meta property="og:image" content="//p1.img.cctvpic.com/poster.jpg">
<title>典籍里的中国_央视网_央视网(cctv.com)</title>
<script>var topicID = "` + columnID + `";</script>
</head><body><a href="` + videoURL + `">《典籍里的中国》最新一期</a></body></html>`
	client := New()
	client.HTTPClient().Transport = columnPageRoundTripper(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.Host == "tv.cctv.com" && req.URL.Path == "/lm/djldzg/index.shtml":
			return columnPageResponse(pageHTML)
		case req.URL.Host == "tv.cctv.com" && req.URL.Path == "/lm/djldzg/videoset":
			return columnPageResponse("{}")
		case req.URL.Host == "api.cntv.cn" && req.URL.Path == "/NewVideo/getVideoListByColumn":
			query := req.URL.Query()
			if query.Get("id") != columnID || query.Get("serviceId") != "tvcctv" || query.Get("mode") != "0" || query.Get("sort") != "desc" {
				t.Errorf("incorrect CCTV column request: %s", req.URL)
			}
			if query.Get("d") == currentMonth && query.Get("p") == "1" {
				return columnPageResponse(`{"data":{"total":1,"list":[{"guid":"` + guid + `","title":"《典籍里的中国》最新一期","time":"2026-10-29 20:00:00","image":"//p1.img.cctvpic.com/episode.jpg","url":"` + videoURL + `","brief":"本期节目简介"}]}}`)
			}
			return columnPageResponse(`{"data":{"total":0,"list":[]}}`)
		default:
			return nil, fmt.Errorf("unexpected CCTV request: %s", req.URL)
		}
	})

	series, err := client.ResolveLink(context.Background(), pageURL)
	if err != nil {
		t.Fatalf("resolve column page: %v", err)
	}
	if series.Title != "典籍里的中国" {
		t.Fatalf("unexpected column title: %q", series.Title)
	}
	if series.SourceURL != pageURL || series.IsSingle {
		t.Fatalf("column page was not treated as a series: %#v", series.SearchResult)
	}
	if series.EpisodeCount != 1 || len(series.Episodes) != 1 {
		t.Fatalf("expected one deduplicated video, got %d: %#v", series.EpisodeCount, series.Episodes)
	}
	episode := series.Episodes[0]
	if episode.VideoGUID != guid || episode.PageURL != videoURL || episode.PublishedAt != "2026-10-29" {
		t.Fatalf("unexpected episode metadata: %#v", episode)
	}
	if episode.Image != "https://p1.img.cctvpic.com/episode.jpg" {
		t.Fatalf("unexpected episode image: %q", episode.Image)
	}
}

func TestCCTVColumnIDAndMetadataFallbacks(t *testing.T) {
	html := `<meta content="栏目标题_央视网_央视网(cctv.com)" property="og:title">
<meta content="栏目简介" name="description">
<script>var lmtopId = 'TOPC1453100395512779';</script>`
	if got := extractCCTVColumnID(html); got != "TOPC1453100395512779" {
		t.Fatalf("failed to detect lmtopId: %q", got)
	}
	if got := cctvColumnTitle(html, "test"); got != "栏目标题" {
		t.Fatalf("failed to normalize column title: %q", got)
	}
	if got := cctvColumnMeta(html, "description"); got != "栏目简介" {
		t.Fatalf("failed to read description meta: %q", got)
	}
}

func TestParseColumnPageAnchorsOnlyAcceptCCTVVideoPages(t *testing.T) {
	html := `<a href="/2026/10/29/VIDE123456789012345678901234567890.shtml">可播放视频</a>
<a href="https://example.com/2026/10/29/VIDE123456789012345678901234567890.shtml">外部链接</a>
<a href="/lm/other/index.shtml">栏目入口</a>`
	got := parseColumnPageAnchors(html, "https://tv.cctv.com/lm/djldzg/index.shtml")
	if len(got) != 1 || got[0].Title != "可播放视频" || got[0].PublishedAt != "2026-10-29" {
		t.Fatalf("unexpected static column video parsing: %#v", got)
	}
}
