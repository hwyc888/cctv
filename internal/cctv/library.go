package cctv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"cctv-sub/internal/model"
)

const libraryProgramPageSize = 24
const libraryEpisodePageSize = 100

type LibraryProgram struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	Poster      string `json:"poster"`
	Description string `json:"description"`
	SourceURL   string `json:"sourceUrl,omitempty"`
}

type LibraryPrograms struct {
	Programs []LibraryProgram `json:"programs"`
	Page     int              `json:"page"`
	HasMore  bool             `json:"hasMore"`
}

type LibraryEpisodes struct {
	Title    string          `json:"title"`
	Kind     string          `json:"kind"`
	ID       string          `json:"id"`
	Page     int             `json:"page"`
	HasMore  bool            `json:"hasMore"`
	Episodes []model.Episode `json:"episodes"`
}

var libraryIDRE = regexp.MustCompile("^[A-Za-z0-9_-]{8,80}$")
var libraryGUIDRE = regexp.MustCompile("^[a-fA-F0-9]{32}$")
var libraryColumnRE = regexp.MustCompile("^TOPC[0-9]{8,32}$")

var libraryKinds = map[string]struct {
	channelID string
	label     string
}{
	"animation":   {"CHAL1460955899450127", "动画片"},
	"drama":       {"CHAL1460955853485115", "电视剧"},
	"documentary": {"CHAL1460955924871139", "纪录片"},
	"special":     {"CHAL1460955953877151", "特别节目"},
	"columns":     {"", "栏目大全"},
}

// Channel IDs used by CCTV's columnSearch endpoint. Never forward unverified
// client-provided identifiers to the upstream API.
var LibraryChannels = []struct {
	ID   string
	Name string
}{
	{"", "全部频道"},
	{"EPGC1386744804340101", "CCTV-1 综合"},
	{"EPGC1386744804340102", "CCTV-2 财经"},
	{"EPGC1386744804340103", "CCTV-3 综艺"},
	{"EPGC1386744804340104", "CCTV-4 中文国际"},
	{"EPGC1386744804340107", "CCTV-5 体育"},
	{"EPGC1386744804340108", "CCTV-6 电影"},
	{"EPGC1386744804340109", "CCTV-7 国防军事"},
	{"EPGC1386744804340110", "CCTV-8 电视剧"},
	{"EPGC1386744804340112", "CCTV-9 纪录"},
	{"EPGC1386744804340113", "CCTV-10 科教"},
	{"EPGC1386744804340114", "CCTV-11 戏曲"},
	{"EPGC1386744804340115", "CCTV-12 社会与法"},
	{"EPGC1386744804340116", "CCTV-13 新闻"},
	{"EPGC1386744804340117", "CCTV-14 少儿"},
	{"EPGC1386744804340118", "CCTV-15 音乐"},
	{"EPGC1634630207058998", "CCTV-16 奥林匹克"},
	{"EPGC1563932742616872", "CCTV-17 农业农村"},
	{"EPGC1468294755566101", "CCTV-5+ 体育赛事"},
}

func validLibraryPage(page int) bool { return page >= 1 && page <= 200 }

func (c *Client) BrowseLibrary(ctx context.Context, kind, channel string, page int) (LibraryPrograms, error) {
	category, ok := libraryKinds[kind]
	if !ok || !validLibraryPage(page) {
		return LibraryPrograms{}, errors.New("无效的央视片库分类或页码")
	}
	result := LibraryPrograms{Programs: []LibraryProgram{}, Page: page}
	query := url.Values{"p": {strconv.Itoa(page)}, "n": {strconv.Itoa(libraryProgramPageSize)}, "serviceId": {"tvcctv"}, "t": {"json"}}
	endpoint := ""
	if kind == "columns" {
		allowed := false
		for _, entry := range LibraryChannels {
			if entry.ID == channel {
				allowed = true
				break
			}
		}
		if !allowed {
			return LibraryPrograms{}, errors.New("请选择有效的 CCTV 频道")
		}
		endpoint = "https://api.cntv.cn/lanmu/columnSearch"
		query.Set("fl", "")
		query.Set("fc", "")
		query.Set("cid", channel)
		query.Set("cb", "ko")
	} else {
		if channel != "" {
			return LibraryPrograms{}, errors.New("此片库分类不支持频道过滤")
		}
		endpoint = "https://api.cntv.cn/list/getVideoAlbumList"
		query.Set("channelid", category.channelID)
		query.Set("fc", category.label)
		query.Set("area", "")
		query.Set("sc", "")
		query.Set("channel", "")
		query.Set("year", "")
		query.Set("letter", "")
		query.Set("topv", "1")
		if kind == "special" {
			query.Set("bigday", "")
		}
	}
	body, err := c.fetch(ctx, endpoint+"?"+query.Encode())
	if err != nil {
		return result, fmt.Errorf("读取央视节目目录: %w", err)
	}
	if kind == "columns" {
		doc, err := unwrapCCTVJSONP(body)
		if err != nil {
			return result, err
		}
		var payload struct {
			Response struct {
				Docs []struct {
					Name        string `json:"column_name"`
					Website     string `json:"column_website"`
					Logo        string `json:"column_logo"`
					Description string `json:"column_brief"`
					LastVIDE    struct {
						GUID string `json:"videoSharedCode"`
					} `json:"lastVIDE"`
				} `json:"docs"`
			} `json:"response"`
		}
		if err := json.Unmarshal(doc, &payload); err != nil {
			return result, fmt.Errorf("央视栏目目录响应异常: %w", err)
		}
		for _, item := range payload.Response.Docs {
			if !libraryGUIDRE.MatchString(item.LastVIDE.GUID) || strings.TrimSpace(item.Name) == "" {
				continue
			}
			result.Programs = append(result.Programs, LibraryProgram{
				Kind: kind, ID: item.LastVIDE.GUID, Title: item.Name,
				Poster: absoluteURL(item.Logo), Description: item.Description,
				SourceURL: safeCCTVLink(item.Website),
			})
		}
		result.HasMore = len(payload.Response.Docs) >= libraryProgramPageSize
		return result, nil
	}
	var payload struct {
		Data struct {
			List []struct {
				ID          string `json:"id"`
				Title       string `json:"title"`
				Image       string `json:"image"`
				URL         string `json:"url"`
				Brief       string `json:"brief"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return result, fmt.Errorf("央视片库响应异常: %w", err)
	}
	for _, item := range payload.Data.List {
		if !libraryIDRE.MatchString(item.ID) || strings.TrimSpace(item.Title) == "" {
			continue
		}
		result.Programs = append(result.Programs, LibraryProgram{
			ID: item.ID, Kind: kind, Title: item.Title,
			Poster: absoluteURL(item.Image), Description: item.Brief,
			SourceURL: safeCCTVLink(item.URL),
		})
	}
	result.HasMore = len(payload.Data.List) >= libraryProgramPageSize
	return result, nil
}

func unwrapCCTVJSONP(body []byte) ([]byte, error) {
	value := strings.TrimSpace(string(body))
	if strings.HasPrefix(value, "ko(") {
		value = strings.TrimSpace(strings.TrimSuffix(value, ";"))
		if !strings.HasSuffix(value, ")") {
			return nil, errors.New("央视栏目接口返回了不完整的 JSONP")
		}
		value = value[3 : len(value)-1]
	}
	if !json.Valid([]byte(value)) {
		return nil, errors.New("央视栏目接口未返回有效 JSON")
	}
	return []byte(value), nil
}

func safeCCTVLink(value string) string {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.Hostname() != "tv.cctv.com" || (u.Scheme != "https" && u.Scheme != "http") || u.Port() != "" {
		return ""
	}
	return u.String()
}

func (c *Client) BrowseLibraryEpisodes(ctx context.Context, kind, id string, page int) (LibraryEpisodes, error) {
	if _, ok := libraryKinds[kind]; !ok || !validLibraryPage(page) || !libraryIDRE.MatchString(id) {
		return LibraryEpisodes{}, errors.New("无效的央视节目或页码")
	}
	result := LibraryEpisodes{Kind: kind, ID: id, Page: page, Episodes: []model.Episode{}}
	query := url.Values{
		"id": {id}, "p": {strconv.Itoa(page)}, "n": {strconv.Itoa(libraryEpisodePageSize)},
		"mode": {"0"}, "serviceId": {"tvcctv"},
	}
	endpoint := "https://api.cntv.cn/NewVideo/getVideoListByAlbumIdNew"
	if kind == "columns" {
		if !libraryGUIDRE.MatchString(id) {
			return result, errors.New("栏目需要有效的视频 GUID")
		}
		infoURL := "https://api.cntv.cn/video/videoinfoByGuid?guid=" + id + "&serviceId=tvcctv"
		infoBody, err := c.fetch(ctx, infoURL)
		if err != nil {
			return result, fmt.Errorf("读取央视栏目信息: %w", err)
		}
		var info struct {
			CTID string `json:"ctid"`
			Data struct {
				CTID string `json:"ctid"`
			} `json:"data"`
		}
		if err := json.Unmarshal(infoBody, &info); err != nil {
			return result, fmt.Errorf("解析栏目标识失败: %w", err)
		}
		ctid := info.CTID
		if ctid == "" {
			ctid = info.Data.CTID
		}
		if !libraryColumnRE.MatchString(ctid) {
			return result, errors.New("栏目没有可用的视频列表标识")
		}
		query.Set("id", ctid)
		query.Set("sort", "desc")
		query.Set("d", "")
		endpoint = "https://api.cntv.cn/NewVideo/getVideoListByColumn"
	} else {
		query.Set("sort", "asc")
		query.Set("pub", "1")
	}
	body, err := c.fetch(ctx, endpoint+"?"+query.Encode())
	if err != nil {
		return result, fmt.Errorf("读取央视节目选集: %w", err)
	}
	var payload struct {
		Data struct {
			Total json.RawMessage `json:"total"`
			List []struct {
				GUID  string `json:"guid"`
				Title string `json:"title"`
				Time  string `json:"time"`
				Image string `json:"image"`
				URL   string `json:"url"`
				Brief string `json:"brief"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return result, fmt.Errorf("解析央视选集列表失败: %w", err)
	}
	for index, item := range payload.Data.List {
		if !libraryGUIDRE.MatchString(item.GUID) || strings.TrimSpace(item.Title) == "" {
			continue
		}
		published := strings.TrimSpace(item.Time)
		if len(published) >= 10 {
			published = published[:10]
		}
		result.Episodes = append(result.Episodes, model.Episode{
			Number: (page-1)*libraryEpisodePageSize + index + 1,
			Title: item.Title, VideoGUID: item.GUID,
			PageURL: safeCCTVLink(item.URL), Image: absoluteURL(item.Image),
			Description: item.Brief, PublishedAt: published,
		})
	}
	result.HasMore = len(payload.Data.List) >= libraryEpisodePageSize
	return result, nil
}
