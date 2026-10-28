package cctv

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type mockCCTVTransport func(*http.Request) (*http.Response, error)

func (f mockCCTVTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestResolveLinkSingleEpisodeIgnoresEmbeddedSuggestions(t *testing.T) {
	const videoURL = "https://tv.cctv.com/2026/06/30/VIDEtest123.shtml"
	client := New()
	client.HTTPClient().Transport = mockCCTVTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != videoURL {
			t.Errorf("unexpected request %s", req.URL)
		}
		body := `<meta property="og:title" content="《测试动画》 第8集 出发"><meta property="og:image" content="//img.cctv.com/8.jpg"><meta property="og:description" content="测试简介"><script>var guid = "abcdef1234567890"; var jsonData2 = [{'title':'《测试动画》 第7集','img':'','brief':'','url':'https://tv.cctv.com/2026/06/29/VIDE7.shtml'}];</script>`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	series, err := client.ResolveLink(context.Background(), videoURL+"?spm=foo#section")
	if err != nil {
		t.Fatal(err)
	}
	if !series.IsSingle || series.Title != "《测试动画》" || series.EpisodeCount != 1 || len(series.Episodes) != 1 {
		t.Fatalf("single page parsed incorrectly: %#v", series)
	}
	if episode := series.Episodes[0]; episode.Number != 8 || episode.PageURL != videoURL || episode.Image != "https://img.cctv.com/8.jpg" {
		t.Fatalf("wrong episode metadata: %#v", episode)
	}
	results, err := client.Search(context.Background(), videoURL)
	if err != nil || len(results) != 1 || !results[0].IsSingle {
		t.Fatalf("direct episode search should work: %#v %v", results, err)
	}
	if _, err := client.ResolveSeries(context.Background(), videoURL); err == nil {
		t.Fatal("a single episode cannot create an auto-tracking subscription")
	}
}

func TestResolveLinkSeriesSupportsSelectionCatalog(t *testing.T) {
	client := New()
	client.HTTPClient().Transport = mockCCTVTransport(func(req *http.Request) (*http.Response, error) {
		source := `<meta property="og:title" content="《测试动画》"><script>
		var jsonData2 = [
		{'title':'《测试动画》 第2集','img':'','brief':'第二集','url':'https://tv.cctv.com/2026/06/30/VIDE2.shtml'},
		{'title':'《测试动画》 第1集','img':'','brief':'第一集','url':'https://tv.cctv.com/2026/06/29/VIDE1.shtml'}
		];</script>`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(source)), Header: make(http.Header)}, nil
	})
	series, err := client.ResolveLink(context.Background(), "https://tv.cctv.com/album/index.shtml")
	if err != nil {
		t.Fatal(err)
	}
	if series.IsSingle || len(series.Episodes) != 2 || series.Episodes[0].Number != 1 {
		t.Fatalf("series catalog not parsed: %#v", series)
	}
}

func TestResolveLinkRejectsInvalidURLSchemesAndMissingVideoGuid(t *testing.T) {
	for _, bad := range []string{"file://tv.cctv.com/a", "http://tv.cctv.com:9000/a", "https://user@tv.cctv.com/a", "https://evil.tv.cctv.com/a"} {
		if _, err := normalizeCCTVURL(bad); err == nil {
			t.Errorf("unsafe URL was accepted: %s", bad)
		}
	}
	client := New()
	client.HTTPClient().Transport = mockCCTVTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`<meta property="og:title" content="测试视频">`)), Header: make(http.Header)}, nil
	})
	if _, err := client.ResolveLink(context.Background(), "https://tv.cctv.com/2026/06/30/VIDEmissing.shtml"); err == nil {
		t.Fatal("a video without a guid must be rejected")
	}
}
