package cctv

import (
	"context"
	"io"
	"net/http"
	"os"
	"time"
	"strings"
	"testing"
)

func libraryMockResponse(value string) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(value))}, nil
}

func TestLibraryBrowseAnimationAndColumnChannels(t *testing.T) {
	client := New()
	client.HTTPClient().Transport = mockCCTVTransport(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/list/getVideoAlbumList":
			if req.URL.Query().Get("channelid") != "CHAL1460955899450127" || req.URL.Query().Get("fc") != "动画片" || req.URL.Query().Get("p") != "2" {
				t.Errorf("incorrect animation request: %s", req.URL)
			}
			return libraryMockResponse(`{"data":{"list":[{"id":"VIDA123456789abc","title":"动画测试","url":"https://tv.cctv.com/album/VIDA.shtml","image":"//img.cctv.com/poster.jpg","brief":"简介"}]}}`)
		case "/lanmu/columnSearch":
			if req.URL.Query().Get("cid") != "EPGC1386744804340117" || req.URL.Query().Get("cb") != "ko" {
				t.Errorf("incorrect channel filter: %s", req.URL)
			}
			return libraryMockResponse(`ko({"response":{"docs":[{"column_name":"测试栏目","column_website":"https://tv.cctv.com/lm/test","column_logo":"https://img.cctv.com/test.jpg","column_brief":"栏目介绍","lastVIDE":{"videoSharedCode":"0123456789abcdef0123456789abcdef"}}]}});`)
		}
		t.Errorf("unexpected endpoint: %s", req.URL)
		return libraryMockResponse("{}")
	})
	animation, err := client.BrowseLibrary(context.Background(), "animation", "", 2)
	if err != nil || len(animation.Programs) != 1 {
		t.Fatalf("animation catalog: %#v %v", animation, err)
	}
	if animation.Programs[0].ID != "VIDA123456789abc" || animation.Programs[0].Poster != "https://img.cctv.com/poster.jpg" {
		t.Fatalf("invalid animation card: %#v", animation.Programs[0])
	}
	columns, err := client.BrowseLibrary(context.Background(), "columns", "EPGC1386744804340117", 1)
	if err != nil || len(columns.Programs) != 1 || columns.Programs[0].Title != "测试栏目" {
		t.Fatalf("CCTV-14 column browse failed: %#v %v", columns, err)
	}
	if _, err := client.BrowseLibrary(context.Background(), "columns", "invalid-channel", 1); err == nil {
		t.Fatal("unlisted channel must be rejected")
	}
	if _, err := client.BrowseLibrary(context.Background(), "animation", "EPGC1386744804340117", 1); err == nil {
		t.Fatal("album browse must not allow channel filter")
	}
}

func TestLibraryEpisodesUseOfficialGUIDAndPagination(t *testing.T) {
	const guid = "0123456789abcdef0123456789abcdef"
	client := New()
	client.HTTPClient().Transport = mockCCTVTransport(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/video/videoinfoByGuid":
			if req.URL.Query().Get("guid") != guid {
				t.Errorf("incorrect column GUID: %s", req.URL)
			}
			return libraryMockResponse(`{"ctid":"TOPC1451559180488841"}`)
		case "/NewVideo/getVideoListByColumn":
			if req.URL.Query().Get("id") != "TOPC1451559180488841" || req.URL.Query().Get("p") != "2" {
				t.Errorf("incorrect column episodes request: %s", req.URL)
			}
		case "/NewVideo/getVideoListByAlbumIdNew":
			if req.URL.Query().Get("id") != "VIDA123456789abc" || req.URL.Query().Get("sort") != "asc" {
				t.Errorf("incorrect album episodes request: %s", req.URL)
			}
		default:
			t.Errorf("unexpected episode endpoint: %s", req.URL)
		}
		return libraryMockResponse(`{"data":{"total":1,"list":[{"guid":"fedcba9876543210fedcba9876543210","title":"第一集","time":"2026-10-20 20:00:00","image":"https://img.cctv.com/ep.jpg","url":"https://tv.cctv.com/2026/10/20/VIDEtest.shtml"}]}}`)
	})
	columns, err := client.BrowseLibraryEpisodes(context.Background(), "columns", guid, 2)
	if err != nil || len(columns.Episodes) != 1 {
		t.Fatalf("column episodes: %#v %v", columns, err)
	}
	if got := columns.Episodes[0]; got.VideoGUID != "fedcba9876543210fedcba9876543210" || got.Number != 101 || got.PublishedAt != "2026-10-20" {
		t.Fatalf("invalid episode record: %#v", got)
	}
	album, err := client.BrowseLibraryEpisodes(context.Background(), "animation", "VIDA123456789abc", 1)
	if err != nil || len(album.Episodes) != 1 || album.Episodes[0].Number != 1 {
		t.Fatalf("album episodes: %#v %v", album, err)
	}
	for _, tc := range []struct{ kind, id string; page int }{
		{"unknown", "VIDA123456789abc", 1}, {"animation", "https://evil.example", 1}, {"columns", "not-a-guid", 1}, {"animation", "VIDA123456789abc", 201},
	} {
		if _, err := client.BrowseLibraryEpisodes(context.Background(), tc.kind, tc.id, tc.page); err == nil {
			t.Errorf("invalid catalog query accepted: %#v", tc)
		}
	}
}

func TestLibraryJSONPRejectsMalformedResponses(t *testing.T) {
	for _, value := range []string{"ko({broken);", "ko({", "callback({});"} {
		if _, err := unwrapCCTVJSONP([]byte(value)); err == nil {
			t.Errorf("invalid JSONP accepted: %s", value)
		}
	}
	valid, err := unwrapCCTVJSONP([]byte("ko({\"response\":{\"docs\":[]}});"))
	if err != nil || !strings.Contains(string(valid), "docs") {
		t.Fatalf("valid JSONP rejected: %q %v", valid, err)
	}
}

func TestResolveVideoFromOfficialGUID(t *testing.T) {
	client := New()
	client.HTTPClient().Transport = mockCCTVTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "vdn.apps.cntv.cn" || req.URL.Query().Get("pid") != "0123456789abcdef0123456789abcdef" {
			t.Errorf("unexpected video lookup: %s", req.URL)
		}
		return libraryMockResponse(`{"ack":"yes","title":"测试视频","hls_url":"https://cdn.example/low.m3u8","manifest":{"hls_h5e_url":"https://cdn.example/high.m3u8"},"video":{"totalLength":"600"}}`)
	})
	video, err := client.ResolveVideoByGUID(context.Background(), "0123456789abcdef0123456789abcdef")
	if err != nil || len(video.HLSURLs) != 2 || video.Duration != 600 {
		t.Fatalf("video GUID resolution: %#v %v", video, err)
	}
	if _, err := client.ResolveVideoByGUID(context.Background(), "http://evil.example"); err == nil {
		t.Fatal("arbitrary URL must not be accepted as video GUID")
	}
}

func TestLiveCCTVLibraryIntegration(t *testing.T) {
	if os.Getenv("CCTV_LIBRARY_INTEGRATION") != "1" {
		t.Skip("set CCTV_LIBRARY_INTEGRATION=1 to query the live CCTV program catalog")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	client := New()
	for _, kind := range []string{"animation", "columns"} {
		list, err := client.BrowseLibrary(ctx, kind, "", 1)
		if err != nil {
			t.Fatalf("fetch real %s catalog: %v", kind, err)
		}
		t.Logf("category=%s items=%d hasMore=%v", kind, len(list.Programs), list.HasMore)
		for i, program := range list.Programs {
			if i >= 3 {
				break
			}
			t.Logf("program %d: id=%s title=%s source=%s", i, program.ID, program.Title, program.SourceURL)
		}
		if len(list.Programs) == 0 {
			t.Fatalf("CCTV %s catalog returned no usable program cards", kind)
		}
		// Some upstream albums are empty or unavailable; try a few programs.
		loaded := false
		for i, program := range list.Programs {
			if i >= 5 {
				break
			}
			episodes, err := client.BrowseLibraryEpisodes(ctx, kind, program.ID, 1)
			if err != nil {
				t.Logf("program %s has no accessible episodes: %v", program.Title, err)
				continue
			}
			t.Logf("program=%s episode-count=%d", program.Title, len(episodes.Episodes))
			if kind == "animation" && len(episodes.Episodes) == 0 && i == 0 {
				for _, mode := range []string{"0", "1", "2"} {
					query := "https://api.cntv.cn/NewVideo/getVideoListByAlbumIdNew?id=" + program.ID +
						"&serviceId=tvcctv&p=1&n=5&sort=asc&pub=1&mode=" + mode
					raw, probeErr := client.fetch(ctx, query)
					if len(raw) > 700 {
						raw = raw[:700]
					}
					t.Logf("live album mode=%s response=%q error=%v", mode, string(raw), probeErr)
				}
				raw, probeErr := client.fetch(ctx, "https://api.cntv.cn/NewVideoset/getVideoAlbumInfoByVideoId?id="+program.ID+"&serviceId=tvcctv")
				if len(raw) > 700 {
					raw = raw[:700]
				}
				t.Logf("live album lookup response=%q error=%v", string(raw), probeErr)
			}
			if len(episodes.Episodes) != 0 {
				loaded = true
				break
			}
		}
		if !loaded {
			t.Fatalf("none of the first five %s programs returned selectable videos", kind)
		}
	}
}
