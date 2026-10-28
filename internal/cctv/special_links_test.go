package cctv

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	specialSeriesURL = "https://tv.cctv.com/2025/05/29/VIDAofwleevTNcsQH3PjpAy0250529.shtml"
	specialClosingURL = "https://tv.cctv.com/2025/08/06/VIDACeLVEwlatefxEv4B3A4Z250806.shtml"
)

func specialMockClient(t *testing.T) *Client {
	t.Helper()
	client := New()
	client.HTTPClient().Transport = mockCCTVTransport(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.String() {
		case specialSeriesURL:
			body = `<meta property="og:title" content="CMG世界机器人大赛 系列赛"><meta property="og:description" content="机器人赛事">`
		case specialClosingURL:
			body = `<meta property="og:title" content="智绘光谷·CMG世界机器人技能大赛-闭幕式">`
		default:
			switch req.URL.Path {
			case "/NewVideo/getVideoListByAlbumIdNew":
				if req.URL.Query().Get("mode") != "2" || req.URL.Query().Get("pub") != "1" {
					t.Errorf("wrong CCTV API mode/pub: %s", req.URL)
				}
				switch req.URL.Query().Get("id") {
				case "VIDAofwleevTNcsQH3PjpAy0250529":
					body = `{"data":{"list":[{"guid":"0123456789abcdef0123456789abcdef","title":"机甲格斗擂台赛","url":"https://tv.cctv.com/2025/05/29/VIDEfirst.shtml"},{"guid":"fedcba9876543210fedcba9876543210","title":"智能觉醒","url":"https://tv.cctv.com/2025/08/07/VIDEsecond.shtml"}]}}`
				case "VIDACeLVEwlatefxEv4B3A4Z250806":
					body = `{"data":{"list":[{"guid":"abcdefabcdefabcdefabcdefabcdefab","title":"机器人技能大赛闭幕式","url":"https://tv.cctv.com/2025/08/10/VIDEclosing.shtml"}]}}`
				default:
					t.Errorf("unexpected album ID %s", req.URL.Query().Get("id"))
				}
			case "/search.php":
				body = `<div class="ind01" id="ind01_01"><div class="image"><img src="https://img.cctv.com/a.jpg" id="img_1"></div><h3><a href="`+specialSeriesURL+`" id="video_playlist_cont_1">CMG世界机器人大赛 系列赛</a></h3><p class="bre">机器人赛事</p><div class="clear"></div></div>
				<div class="ind01" id="ind01_02"><div class="image"><img src="https://img.cctv.com/b.jpg" id="img_2"></div><h3><a href="`+specialClosingURL+`" id="video_playlist_cont_2">机器人闭幕式</a></h3><p class="bre">闭幕式</p><div class="clear"></div></div>`
			default:
				t.Errorf("unexpected URL %s", req.URL)
			}
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	return client
}

func TestResolveSpecialProgramPagesByAlbumAPI(t *testing.T) {
	client := specialMockClient(t)
	for _, tc := range []struct{ pageURL string; count int; single bool }{
		{specialSeriesURL, 2, false}, {specialClosingURL, 1, true},
	} {
		series, err := client.ResolveLink(context.Background(), tc.pageURL)
		if err != nil || series.EpisodeCount != tc.count || series.IsSingle != tc.single || len(series.Episodes) != tc.count {
			t.Fatalf("resolve %s: %#v %v", tc.pageURL, series, err)
		}
		for _, episode := range series.Episodes {
			if len(episode.VideoGUID) != 32 || !strings.HasPrefix(episode.PageURL, "https://tv.cctv.com/") {
				t.Fatalf("invalid playable episode: %#v", episode)
			}
		}
	}
}

func TestSearchEnrichesSpecialProgramResults(t *testing.T) {
	client := specialMockClient(t)
	results, err := client.Search(context.Background(), "机器人")
	if err != nil || len(results) != 2 || results[0].EpisodeCount != 2 ||
		results[0].IsSingle || results[1].EpisodeCount != 1 || !results[1].IsSingle {
		t.Fatalf("search must recognize single and multi-video programs: %#v %v", results, err)
	}
}

func TestLiveSpecialProgramLinks(t *testing.T) {
	if os.Getenv("CCTV_LIBRARY_INTEGRATION") != "1" {
		t.Skip("enable the real CCTV API integration check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
	defer cancel()
	client := New()
	for _, pageURL := range []string{specialSeriesURL, specialClosingURL} {
		series, err := client.ResolveLink(ctx, pageURL)
		if err != nil {
			t.Fatalf("real CCTV special page %s: %v", pageURL, err)
		}
		t.Logf("title=%s episodeCount=%d single=%v", series.Title, len(series.Episodes), series.IsSingle)
		if len(series.Episodes) == 0 {
			t.Fatalf("still no selectable episodes at %s", pageURL)
		}
		for i, episode := range series.Episodes {
			if i >= 3 {
				break
			}
			t.Logf("  episode %s: guid=%s url=%s", episode.Title, episode.VideoGUID, episode.PageURL)
		}
		if len(series.Episodes[0].VideoGUID) != 32 {
			t.Fatalf("video GUID missing for %s", pageURL)
		}
	}
}

