package cctv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"cctv-sub/internal/model"
)

var (
	cctvColumnPathRE = regexp.MustCompile(`(?i)^/lm/([a-z0-9_-]+)(?:/|$)`)
	cctvColumnVarRE = regexp.MustCompile(`(?i)\b(?:var\s+)?(?:topicID|lmtopId|column_id|columnId|topicid)\s*(?:=|:)\s*["']?(TOPC[0-9A-Za-z_-]{8,32})`)
	cctvColumnAnchorRE = regexp.MustCompile(`(?is)<a\b([^>]*)>(.*?)</a>`)
	cctvMetaTagRE = regexp.MustCompile(`(?is)<meta\b[^>]*>`)
	cctvTitleTagRE = regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title>`)
	cctvAttributeRE = regexp.MustCompile(`(?i)\b%s\s*=\s*["']([^"']*)["']`)
	cctvColumnDateRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	cctvColumnTitleSuffixRE = regexp.MustCompile(`(?i)\s*[_|｜]\s*(?:CCTV|央视网).*$`)
)

// resolveColumnPage recognizes CCTV's /lm/<slug>/ pages, which are column
// archives rather than ordinary series pages. It discovers the TOPC identifier
// from page data (or the sibling videoset page) and reads the official API.
func (c *Client) resolveColumnPage(ctx context.Context, pageURL string, body []byte, includeArchive bool) (model.Series, bool, error) {
	u, err := url.Parse(pageURL)
	if err != nil {
		return model.Series{}, false, nil
	}
	match := cctvColumnPathRE.FindStringSubmatch(u.Path)
	if len(match) != 2 || strings.EqualFold(match[1], "index") {
		return model.Series{}, false, nil
	}
	slug := match[1]
	htmlText := string(body)
	series := model.Series{SearchResult: model.SearchResult{
		Title: cctvColumnTitle(htmlText, slug),
		SourceURL: pageURL,
		Poster: cctvColumnMeta(htmlText, "og:image"),
		Description: cctvColumnMeta(htmlText, "og:description"),
		IsSingle: false,
	}}
	staticEpisodes := parseColumnPageAnchors(htmlText, pageURL)
	columnID := extractCCTVColumnID(htmlText)
	if columnID == "" {
		videosetURL := "https://tv.cctv.com/lm/" + slug + "/videoset"
		if videosetBody, fetchErr := c.fetch(ctx, videosetURL); fetchErr == nil {
			columnID = extractCCTVColumnID(string(videosetBody))
			if len(staticEpisodes) == 0 {
				staticEpisodes = parseColumnPageAnchors(string(videosetBody), pageURL)
			}
		}
	}

	var apiEpisodes []model.Episode
	var apiErr error
	if columnID != "" {
		apiEpisodes, apiErr = c.fetchCCTVColumnEpisodes(ctx, columnID, pageURL, includeArchive)
	}
	episodes := mergeColumnEpisodes(apiEpisodes, staticEpisodes, series.Poster)
	if len(episodes) == 0 {
		if columnID == "" {
			return model.Series{}, true, errors.New("已识别这是央视栏目页，但页面及 videoset 备用页都没有找到 TOPC 栏目标识，也没有提取到可播放的视频链接")
		}
		if apiErr != nil {
			return model.Series{}, true, fmt.Errorf("已识别央视栏目 %s（%s），但栏目视频接口读取失败：%w", series.Title, columnID, apiErr)
		}
		return model.Series{}, true, fmt.Errorf("已识别央视栏目 %s（%s），但栏目接口和页面链接均未返回可用视频", series.Title, columnID)
	}
	for i := range episodes {
		episodes[i].Number = i + 1
		if episodes[i].Image == "" {
			episodes[i].Image = series.Poster
		}
	}
	series.Episodes = episodes
	series.EpisodeCount = len(episodes)
	series.IsSingle = false
	return series, true, nil
}

func extractCCTVColumnID(source string) string {
	if match := cctvColumnVarRE.FindStringSubmatch(source); len(match) == 2 && libraryColumnRE.MatchString(match[1]) {
		return match[1]
	}
	return libraryColumnRE.FindString(source)
}

func cctvColumnMeta(source, wanted string) string {
	for _, tag := range cctvMetaTagRE.FindAllString(source, -1) {
		property := strings.ToLower(strings.TrimSpace(cctvColumnAttribute(tag, "property")))
		name := strings.ToLower(strings.TrimSpace(cctvColumnAttribute(tag, "name")))
		if property == strings.ToLower(wanted) || name == strings.ToLower(wanted) {
			return cleanText(cctvColumnAttribute(tag, "content"))
		}
	}
	return ""
}

func cctvColumnAttribute(tag, name string) string {
	re := regexp.MustCompile(fmt.Sprintf(cctvAttributeRE.String(), regexp.QuoteMeta(name)))
	match := re.FindStringSubmatch(tag)
	if len(match) == 2 {
		return match[1]
	}
	return ""
}

func cctvColumnTitle(source, slug string) string {
	title := cctvColumnMeta(source, "og:title")
	if title == "" {
		if match := cctvTitleTagRE.FindStringSubmatch(source); len(match) == 2 {
			title = cleanText(match[1])
		}
	}
	title = cctvColumnTitleSuffixRE.ReplaceAllString(title, "")
	title = strings.TrimSpace(strings.Trim(title, "_|｜- "))
	if title == "" {
		return slug
	}
	return title
}

func parseColumnPageAnchors(source, baseURL string) []model.Episode {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil
	}
	var episodes []model.Episode
	for _, match := range cctvColumnAnchorRE.FindAllStringSubmatch(source, -1) {
		if len(match) != 3 {
			continue
		}
		href := cctvColumnAttribute(match[1], "href")
		if href == "" {
			continue
		}
		link, err := url.Parse(strings.TrimSpace(href))
		if err != nil {
			continue
		}
		link = base.ResolveReference(link)
		if !episodePageRE.MatchString(link.Path) {
			continue
		}
		pageURL := safeCCTVLink(link.String())
		title := cleanText(match[2])
		if pageURL == "" || title == "" {
			continue
		}
		published := ""
		if dateMatch := publishedRE.FindStringSubmatch(pageURL); len(dateMatch) == 4 {
			published = strings.Join(dateMatch[1:], "-")
		}
		episodes = append(episodes, model.Episode{Title: title, PageURL: pageURL, PublishedAt: published})
	}
	return episodes
}

func (c *Client) fetchCCTVColumnEpisodes(ctx context.Context, columnID, pageURL string, includeArchive bool) ([]model.Episode, error) {
	if !libraryColumnRE.MatchString(columnID) {
		return nil, errors.New("栏目 ID 不是有效的 TOPC 标识")
	}
	now := time.Now()
	var collected []model.Episode
	// Manual URL browsing scans up to ten years of monthly archives. Automatic
	// subscription checks stop at the first non-empty window, avoiding repeated
	// historical API traffic while still finding older programmes with no recent
	// releases.
	for offset, windowSize := 0, 24; offset < 120; {
		episodes, errorsSeen := c.fetchCCTVColumnWindow(ctx, columnID, pageURL, now, offset, windowSize)
		if len(episodes) > 0 {
			if !includeArchive {
				return episodes, nil
			}
			collected = append(collected, episodes...)
		}
		if ctx.Err() != nil {
			if len(collected) > 0 {
				return mergeColumnEpisodes(collected, nil, ""), nil
			}
			return nil, ctx.Err()
		}
		if len(errorsSeen) > 0 {
			if len(collected) > 0 {
				return mergeColumnEpisodes(collected, nil, ""), nil
			}
			return nil, errorsSeen[0]
		}
		offset += windowSize
		windowSize = 12
		if offset+windowSize > 120 {
			windowSize = 120 - offset
		}
	}
	return mergeColumnEpisodes(collected, nil, ""), nil
}

func (c *Client) fetchCCTVColumnWindow(ctx context.Context, columnID, pageURL string, now time.Time, offset, count int) ([]model.Episode, []error) {
	type monthResult struct {
		episodes []model.Episode
		err error
	}
	results := make([]monthResult, count)
	semaphore := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		i := i
		month := now.AddDate(0, -(offset+i), 0).Format("200601")
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				results[i].err = ctx.Err()
				return
			}
			defer func() { <-semaphore }()
			results[i].episodes, results[i].err = c.fetchCCTVColumnMonth(ctx, columnID, pageURL, month)
		}()
	}
	wg.Wait()
	var episodes []model.Episode
	var errorsSeen []error
	for _, result := range results {
		if result.err != nil {
			errorsSeen = append(errorsSeen, result.err)
		}
		episodes = append(episodes, result.episodes...)
	}
	return episodes, errorsSeen
}

func (c *Client) fetchCCTVColumnMonth(ctx context.Context, columnID, pageURL, month string) ([]model.Episode, error) {
	const pageSize = 100
	var episodes []model.Episode
	maxPages := 1
	for page := 1; page <= maxPages; page++ {
		query := url.Values{
			"id": {columnID}, "n": {fmt.Sprint(pageSize)}, "p": {fmt.Sprint(page)},
			"d": {month}, "mode": {"0"}, "serviceId": {"tvcctv"}, "sort": {"desc"},
		}
		body, err := c.fetch(ctx, "https://api.cntv.cn/NewVideo/getVideoListByColumn?"+query.Encode())
		if err != nil {
			return episodes, err
		}
		var payload libraryVideoList
		if err := json.Unmarshal(body, &payload); err != nil {
			return episodes, fmt.Errorf("解析栏目视频列表（%s，第 %d 页）：%w", month, page, err)
		}
		if payload.Data == nil {
			return episodes, fmt.Errorf("栏目视频接口没有返回 data（%s）", month)
		}
		if len(payload.Data.List) == 0 {
			break
		}
		if page == 1 {
			total := libraryTotal(payload.Data.Total)
			if total <= 0 {
				total = len(payload.Data.List)
			}
			maxPages = (total + pageSize - 1) / pageSize
			if maxPages < 1 {
				maxPages = 1
			}
			if maxPages > 5 {
				maxPages = 5
			}
		}
		for _, item := range payload.Data.List {
			if !libraryGUIDRE.MatchString(item.GUID) || strings.TrimSpace(item.Title) == "" {
				continue
			}
			videoURL := resolveColumnVideoURL(pageURL, item.URL)
			if videoURL == "" {
				videoURL = pageURL + "?cctv_guid=" + item.GUID
			}
			published := strings.TrimSpace(item.Time)
			if len(published) >= 10 && cctvColumnDateRE.MatchString(published[:10]) {
				published = published[:10]
			} else {
				published = ""
				if dateMatch := publishedRE.FindStringSubmatch(videoURL); len(dateMatch) == 4 {
					published = strings.Join(dateMatch[1:], "-")
				}
			}
			episodes = append(episodes, model.Episode{
				Title: cleanText(item.Title), PageURL: videoURL, VideoGUID: item.GUID,
				Image: absoluteURL(item.Image), Description: cleanText(item.Brief), PublishedAt: published,
			})
		}
		if len(payload.Data.List) < pageSize {
			break
		}
	}
	return episodes, nil
}

func resolveColumnVideoURL(baseURL, raw string) string {
	base, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	link, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || strings.TrimSpace(raw) == "" {
		return ""
	}
	link = base.ResolveReference(link)
	return safeCCTVLink(link.String())
}

func mergeColumnEpisodes(primary, fallback []model.Episode, poster string) []model.Episode {
	seenGUIDs := make(map[string]struct{}, len(primary)+len(fallback))
	seenURLs := make(map[string]struct{}, len(primary)+len(fallback))
	merged := make([]model.Episode, 0, len(primary)+len(fallback))
	for _, episode := range append(append([]model.Episode(nil), primary...), fallback...) {
		guid := strings.ToLower(strings.TrimSpace(episode.VideoGUID))
		pageURL := strings.ToLower(strings.TrimSpace(episode.PageURL))
		if guid == "" && pageURL == "" {
			continue
		}
		_, guidSeen := seenGUIDs[guid]
		_, urlSeen := seenURLs[pageURL]
		if (guid != "" && guidSeen) || (pageURL != "" && urlSeen) {
			continue
		}
		if guid != "" {
			seenGUIDs[guid] = struct{}{}
		}
		if pageURL != "" {
			seenURLs[pageURL] = struct{}{}
		}
		if episode.Image == "" {
			episode.Image = poster
		}
		merged = append(merged, episode)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].PublishedAt == merged[j].PublishedAt {
			return merged[i].Title < merged[j].Title
		}
		return merged[i].PublishedAt > merged[j].PublishedAt
	})
	return merged
}
