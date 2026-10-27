package cctv

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestParseSearchResults(t *testing.T) {
	html := `<div class="ind01" id="ind01_01">
<div class="image"><img src="https://img.example/poster.jpg" id="img_1"></div>
<h3><a href="https://tv.cctv.com/2026/06/25/VIDA.shtml" id="video_playlist_cont_1">《<font color="red">测试节目</font> 第一季》</a></h3>
<p class="bre">节目 &amp; 简介</p><div class="clear"></div></div>`
	results := parseSearchResults(html)
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if results[0].Title != "《测试节目 第一季》" {
		t.Fatalf("unexpected title: %q", results[0].Title)
	}
	if results[0].Description != "节目 & 简介" {
		t.Fatalf("unexpected description: %q", results[0].Description)
	}
}

func TestParseSeriesSortsEpisodesAndReadsMetadata(t *testing.T) {
	html := `<meta property="og:title" content="《测试剧》"><meta property="og:description" content="简介">
<div class="image"><img src="//img.example/poster.jpg" width="200" height="254" /></div>
<p><span>年份：</span>2026</p><p><span>类别：</span>纪录片</p><p><span>产地：</span>中国</p>
<script>var jsonData2=[{
'title':'《测试剧》 第2集','img':'//img.example/2.jpg','brief':'第二集','url':'https://tv.cctv.com/2026/08/02/VIDE2.shtml'
},{'title':'《测试剧》 第1集','img':'//img.example/1.jpg','brief':'第一集','url':'https://tv.cctv.com/2026/08/01/VIDE1.shtml'}];</script>`
	series := parseSeries(html, "https://tv.cctv.com/series.shtml")
	if series.Title != "《测试剧》" || series.EpisodeCount != 2 {
		t.Fatalf("unexpected series: %#v", series.SearchResult)
	}
	if series.Poster != "https://img.example/poster.jpg" {
		t.Fatalf("unexpected poster: %q", series.Poster)
	}
	if series.Episodes[0].Number != 1 || series.Episodes[0].PublishedAt != "2026-08-01" {
		t.Fatalf("episodes were not normalized: %#v", series.Episodes)
	}
}

func TestNormalizeCCTVURL(t *testing.T) {
	got, err := normalizeCCTVURL("https://tv.cctv.com/2026/06/25/VIDA.shtml?spm=test#part")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://tv.cctv.com/2026/06/25/VIDA.shtml" {
		t.Fatalf("unexpected URL: %q", got)
	}
	if _, err := normalizeCCTVURL("https://example.com/video"); err == nil {
		t.Fatal("expected non-CCTV host to be rejected")
	}
}

func TestUniqueURLsPrefersPlayerDRMStream(t *testing.T) {
	got := uniqueURLs("https://cdn/h5e.m3u8", "https://cdn/enc2.m3u8", "", "https://cdn/plain.m3u8", "https://cdn/h5e.m3u8")
	if len(got) != 3 || got[0] != "https://cdn/h5e.m3u8" || got[2] != "https://cdn/plain.m3u8" {
		t.Fatalf("unexpected stream order: %#v", got)
	}
}

func TestMaximizeHLSURLRaisesExistingLimit(t *testing.T) {
	got := maximizeHLSURL("https://cdn.example/main.m3u8?maxbr=2048&contentid=abc")
	if !strings.Contains(got, "maxbr=100000") || !strings.Contains(got, "contentid=abc") {
		t.Fatalf("unexpected maximized URL: %q", got)
	}
	plain := "https://cdn.example/main.m3u8?contentid=abc"
	if got := maximizeHLSURL(plain); got != plain {
		t.Fatalf("URL without maxbr should not change: %q", got)
	}
}

func TestResolveVideoPrefersWebPlayerStreamIntegration(t *testing.T) {
	if os.Getenv("CCTV_INTEGRATION") != "1" {
		t.Skip("set CCTV_INTEGRATION=1 to test the live player API")
	}
	info, err := New().ResolveVideo(context.Background(), "https://tv.cctv.com/2026/06/25/VIDEGipU6v9vmmKqmAbtQ4pk260625.shtml")
	if err != nil {
		t.Fatal(err)
	}
	if len(info.HLSURLs) < 2 || !strings.Contains(info.HLSURLs[0], "/h5e/") {
		t.Fatalf("web player stream was not preferred: %#v", info.HLSURLs)
	}
}
