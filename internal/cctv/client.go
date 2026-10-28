package cctv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cctv-sub/internal/model"
)

const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/138 Safari/537.36"

var (
	searchCardRE      = regexp.MustCompile(`(?s)<div class="ind01" id="ind01_\d+">(.*?)<div class="clear"></div>\s*</div>`)
	searchLinkRE      = regexp.MustCompile(`(?s)<a href="([^"]+)"[^>]+id="video_playlist_cont_\d+"[^>]*>(.*?)</a>`)
	searchImageRE     = regexp.MustCompile(`(?s)<img[^>]+src="([^"]+)"[^>]+id="img_\d+"`)
	searchBriefRE     = regexp.MustCompile(`(?s)<p class="bre">(.*?)</p>`)
	metaTitleRE       = regexp.MustCompile(`(?i)<meta\s+property="og:title"\s+content="([^"]*)"`)
	metaDescriptionRE = regexp.MustCompile(`(?i)<meta\s+property="og:description"\s+content="([^"]*)"`)
	seriesPosterRE    = regexp.MustCompile(`(?s)<div class="image"><img src="([^"]+)" width="200" height="254"`)
	seriesYearRE      = regexp.MustCompile(`(?s)<p><span>年份：</span>\s*([^<]*)</p>`)
	seriesCategoryRE  = regexp.MustCompile(`(?s)<p><span>类别：</span>\s*([^<]*)</p>`)
	seriesRegionRE    = regexp.MustCompile(`(?s)<p><span>产地：</span>\s*([^<]*)</p>`)
	episodeRE         = regexp.MustCompile(`(?s)\{\s*'title'\s*:\s*'((?:\\.|[^'])*)'\s*,\s*'img'\s*:\s*'((?:\\.|[^'])*)'\s*,\s*'brief'\s*:\s*'((?:\\.|[^'])*)'\s*,\s*'url'\s*:\s*'((?:\\.|[^'])*)'\s*\}`)
	episodeNumberRE   = regexp.MustCompile(`第\s*(\d+)\s*集`)
	episodePageRE     = regexp.MustCompile(`(?i)/VIDE[^/]*\.shtml$`)
	seriesInTitleRE   = regexp.MustCompile(`^(.+?)\s*第\s*\d+\s*集`)
	metaImageRE       = regexp.MustCompile(`(?i)<meta\s+property="og:image"\s+content="([^"]*)"`)
	publishedRE       = regexp.MustCompile(`/((?:19|20)\d{2})/(\d{2})/(\d{2})/`)
	guidRE            = regexp.MustCompile(`var\s+guid\s*=\s*"([a-fA-F0-9]+)"`)
	tagRE             = regexp.MustCompile(`<[^>]+>`)
)

type Client struct {
	http *http.Client
}

type VideoInfo struct {
	Title    string
	Image    string
	HLSURL   string
	HLSURLs  []string
	Duration float64
}

func New() *Client {
	return &Client{http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) Search(ctx context.Context, query string) ([]model.SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("请输入搜索关键词或 CCTV 剧集链接")
	}
	if u, err := url.Parse(query); err == nil && u.Hostname() == "tv.cctv.com" {
		series, err := c.ResolveLink(ctx, query)
		if err != nil {
			return nil, err
		}
		return []model.SearchResult{series.SearchResult}, nil
	}

	searchURL := "https://search.cctv.com/search.php?qtext=" + url.QueryEscape(query) + "&type=video"
	body, err := c.fetch(ctx, searchURL)
	if err != nil {
		return nil, fmt.Errorf("搜索 CCTV: %w", err)
	}
	results := parseSearchResults(string(body))
	if len(results) == 0 {
		return []model.SearchResult{}, nil
	}

	// Enrich the first results with year/category and the actual episode count.
	limit := len(results)
	if limit > 8 {
		limit = 8
	}
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i := 0; i < limit; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			series, resolveErr := c.ResolveSeries(ctx, results[i].SourceURL)
			if resolveErr == nil {
				results[i] = series.SearchResult
			}
		}()
	}
	wg.Wait()
	return results, nil
}

// ResolveLink accepts an episode video page as well as a series page.
// A direct single-episode link must only preview that episode, even if
// the playback page embeds suggestions for other episodes.
func (c *Client) ResolveLink(ctx context.Context, rawURL string) (model.Series, error) {
	normalized, err := normalizeCCTVURL(rawURL)
	if err != nil {
		return model.Series{}, err
	}
	body, err := c.fetch(ctx, normalized)
	if err != nil {
		return model.Series{}, fmt.Errorf("读取央视页面: %w", err)
	}
	if u, err := url.Parse(normalized); err == nil && episodePageRE.MatchString(u.Path) {
		if len(guidRE.FindSubmatch(body)) != 2 {
			return model.Series{}, errors.New("单集页面没有可用的视频标识，请检查 CCTV 链接")
		}
		title := firstMatch(metaTitleRE, string(body))
		if title == "" {
			return model.Series{}, errors.New("单集页面没有可识别的视频标题")
		}
		number := 1
		if match := episodeNumberRE.FindStringSubmatch(title); len(match) == 2 {
			number, _ = strconv.Atoi(match[1])
		}
		seriesTitle := title
		if match := seriesInTitleRE.FindStringSubmatch(title); len(match) == 2 {
			seriesTitle = strings.TrimSpace(match[1])
		}
		image := absoluteURL(firstMatch(metaImageRE, string(body)))
		published := ""
		if match := publishedRE.FindStringSubmatch(normalized); len(match) == 4 {
			published = strings.Join(match[1:], "-")
		}
		episode := model.Episode{Number: number, Title: title, Image: image, PageURL: normalized, PublishedAt: published}
		return model.Series{
			SearchResult: model.SearchResult{
				Title: seriesTitle, SourceURL: normalized, Poster: image,
				Description: firstMatch(metaDescriptionRE, string(body)),
				EpisodeCount: 1, IsSingle: true,
			},
			Episodes: []model.Episode{episode},
		}, nil
	}
	series := parseSeries(string(body), normalized)
	if series.Title == "" {
		return model.Series{}, errors.New("这个页面不是可识别的 CCTV 节目页面")
	}
	if len(series.Episodes) == 0 {
		return model.Series{}, errors.New("页面中没有找到可下载的选集列表；可以粘贴具体单集播放页")
	}
	return series, nil
}

func (c *Client) ResolveSeries(ctx context.Context, rawURL string) (model.Series, error) {
	series, err := c.ResolveLink(ctx, rawURL)
	if err != nil {
		return model.Series{}, err
	}
	if series.IsSingle {
		return model.Series{}, errors.New("自动追更需要剧集主页链接；单集请使用选集下载")
	}
	return series, nil
}

func (c *Client) ResolveVideo(ctx context.Context, pageURL string) (VideoInfo, error) {
	body, err := c.fetch(ctx, pageURL)
	if err != nil {
		return VideoInfo{}, fmt.Errorf("读取单集页面: %w", err)
	}
	match := guidRE.FindSubmatch(body)
	if len(match) != 2 {
		return VideoInfo{}, errors.New("单集页面中没有找到视频标识")
	}
	return c.ResolveVideoByGUID(ctx, string(match[1]))
}

// ResolveVideoByGUID uses CCTV's playback API for video entries returned
// directly by the official library API, without fetching a playback page.
func (c *Client) ResolveVideoByGUID(ctx context.Context, guid string) (VideoInfo, error) {
	if !libraryGUIDRE.MatchString(guid) {
		return VideoInfo{}, errors.New("视频 GUID 格式不正确")
	}
	infoURL := "https://vdn.apps.cntv.cn/api/getHttpVideoInfo.do?pid=" + guid
	data, err := c.fetch(ctx, infoURL)
	if err != nil {
		return VideoInfo{}, fmt.Errorf("读取播放信息: %w", err)
	}
	var payload struct {
		Ack      string `json:"ack"`
		Title    string `json:"title"`
		Image    string `json:"image"`
		HLSURL   string `json:"hls_url"`
		TipMsg   string `json:"tip_msg"`
		Manifest struct {
			HLSH5EURL  string `json:"hls_h5e_url"`
			HLSEncURL  string `json:"hls_enc_url"`
			HLSEnc2URL string `json:"hls_enc2_url"`
		} `json:"manifest"`
		Video struct {
			TotalLength string `json:"totalLength"`
		} `json:"video"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return VideoInfo{}, fmt.Errorf("解析播放信息: %w", err)
	}
	urls := uniqueURLs(payload.Manifest.HLSH5EURL, payload.Manifest.HLSEnc2URL, payload.Manifest.HLSEncURL, payload.HLSURL)
	if payload.Ack != "yes" || len(urls) == 0 {
		if payload.TipMsg == "" {
			payload.TipMsg = "CCTV 没有返回可下载的播放地址"
		}
		return VideoInfo{}, errors.New(payload.TipMsg)
	}
	duration, _ := strconv.ParseFloat(payload.Video.TotalLength, 64)
	return VideoInfo{Title: payload.Title, Image: payload.Image, HLSURL: urls[0], HLSURLs: urls, Duration: duration}, nil
}

func (c *Client) HTTPClient() *http.Client {
	return c.http
}

func (c *Client) fetch(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/json,application/xhtml+xml,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("Referer", "https://tv.cctv.com/")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 12<<20))
}

func parseSearchResults(source string) []model.SearchResult {
	cards := searchCardRE.FindAllStringSubmatch(source, -1)
	results := make([]model.SearchResult, 0, len(cards))
	seen := map[string]bool{}
	for _, card := range cards {
		link := searchLinkRE.FindStringSubmatch(card[1])
		imageMatch := searchImageRE.FindStringSubmatch(card[1])
		if len(link) != 3 || seen[link[1]] {
			continue
		}
		seen[link[1]] = true
		item := model.SearchResult{SourceURL: html.UnescapeString(link[1]), Title: cleanText(link[2])}
		if len(imageMatch) == 2 {
			item.Poster = absoluteURL(imageMatch[1])
		}
		if brief := searchBriefRE.FindStringSubmatch(card[1]); len(brief) == 2 {
			item.Description = cleanText(brief[1])
		}
		results = append(results, item)
	}
	return results
}

func parseSeries(source, sourceURL string) model.Series {
	series := model.Series{}
	series.SourceURL = sourceURL
	series.Title = firstMatch(metaTitleRE, source)
	series.Description = firstMatch(metaDescriptionRE, source)
	series.Poster = absoluteURL(firstMatch(seriesPosterRE, source))
	series.Year = cleanText(firstMatch(seriesYearRE, source))
	series.Category = cleanText(firstMatch(seriesCategoryRE, source))
	series.Region = cleanText(firstMatch(seriesRegionRE, source))

	matches := episodeRE.FindAllStringSubmatch(source, -1)
	for i, match := range matches {
		title := decodeJSString(match[1])
		number := i + 1
		if numberMatch := episodeNumberRE.FindStringSubmatch(title); len(numberMatch) == 2 {
			number, _ = strconv.Atoi(numberMatch[1])
		}
		pageURL := absoluteURL(decodeJSString(match[4]))
		published := ""
		if dateMatch := publishedRE.FindStringSubmatch(pageURL); len(dateMatch) == 4 {
			published = strings.Join(dateMatch[1:], "-")
		}
		series.Episodes = append(series.Episodes, model.Episode{
			Number: number, Title: title, Image: absoluteURL(decodeJSString(match[2])),
			Description: decodeJSString(match[3]), PageURL: pageURL, PublishedAt: published,
		})
	}
	sort.SliceStable(series.Episodes, func(i, j int) bool { return series.Episodes[i].Number < series.Episodes[j].Number })
	series.EpisodeCount = len(series.Episodes)
	return series
}

func normalizeCCTVURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", errors.New("CCTV 链接必须以 http:// 或 https:// 开头")
	}
	if u.Hostname() != "tv.cctv.com" || u.Port() != "" || u.User != nil {
		return "", errors.New("只支持 tv.cctv.com 的节目或单集链接")
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func firstMatch(re *regexp.Regexp, source string) string {
	match := re.FindStringSubmatch(source)
	if len(match) == 2 {
		return cleanText(match[1])
	}
	return ""
}

func cleanText(value string) string {
	value = tagRE.ReplaceAllString(value, "")
	value = html.UnescapeString(value)
	return strings.TrimSpace(strings.Join(strings.Fields(value), " "))
}

func decodeJSString(value string) string {
	value = strings.ReplaceAll(value, `\'`, `'`)
	value = strings.ReplaceAll(value, `\\`, `\`)
	value = strings.ReplaceAll(value, `\/`, `/`)
	return html.UnescapeString(strings.TrimSpace(value))
}

func absoluteURL(value string) string {
	value = html.UnescapeString(strings.TrimSpace(value))
	if strings.HasPrefix(value, "//") {
		return "https:" + value
	}
	return value
}

func uniqueURLs(values ...string) []string {
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		value = maximizeHLSURL(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func maximizeHLSURL(value string) string {
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" {
		return value
	}
	query := u.Query()
	if query.Has("maxbr") {
		query.Set("maxbr", "100000")
		u.RawQuery = query.Encode()
	}
	return u.String()
}
