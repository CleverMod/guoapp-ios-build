package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"strconv"
	"strings"
)

const (
	liangziBaseURL = "https://cj.lziapi.com"
	jciyuanBaseURL = "https://www.jciyuan.com"
)

type maccmsProvider struct {
	name     string
	apiPath  string
	pageSize int
}

type maccmsResponse struct {
	Code      any              `json:"code"`
	Message   string           `json:"msg"`
	Page      any              `json:"page"`
	PageCount any              `json:"pagecount"`
	Limit     any              `json:"limit"`
	List      []map[string]any `json:"list"`
	Classes   []map[string]any `json:"class"`
}

type maccmsEpisode struct {
	index int
	title string
	url   string
}

type maccmsLine struct {
	index    int
	episodes []maccmsEpisode
}

func maccmsProviderForSource(source string) (maccmsProvider, bool) {
	switch canonicalProviderSource(source) {
	case sourceLiangzi:
		return maccmsProvider{name: "量子", apiPath: "/api.php/provide/vod", pageSize: 20}, true
	case sourceJciyuan:
		return maccmsProvider{name: "囧次元", apiPath: "/api.php/provide/vod/", pageSize: 21}, true
	default:
		return maccmsProvider{}, false
	}
}

func isMaccmsSource(source string) bool {
	_, valid := maccmsProviderForSource(source)
	return valid
}

func decodeMaccmsResponse(body string) (maccmsResponse, error) {
	var response maccmsResponse
	decoder := json.NewDecoder(strings.NewReader(strings.TrimPrefix(body, "\ufeff")))
	decoder.UseNumber()
	if err := decoder.Decode(&response); err != nil {
		return response, errors.New("采集接口未返回有效 JSON 数据")
	}
	if nativeText(response.Code) != "1" {
		return response, fmt.Errorf("采集接口未返回成功结果：%s", firstNonEmpty(truncate(cleanText(response.Message), 128), "请稍后重试"))
	}
	return response, nil
}

func (d *Downloader) maccmsRequest(ctx context.Context, source string, parameters url.Values) (maccmsResponse, error) {
	provider, valid := maccmsProviderForSource(source)
	if !valid {
		return maccmsResponse{}, errors.New("请选择有效采集源")
	}
	base := d.providerBaseURL(source)
	address, err := url.Parse(base + provider.apiPath)
	if err != nil || address.User != nil || !isProviderHTTPMediaURL(address.String()) {
		return maccmsResponse{}, fmt.Errorf("%s采集地址无效", provider.name)
	}
	address.RawQuery, address.Fragment = parameters.Encode(), ""
	body, err := d.fetchProviderText(ctx, address.String(), base+"/")
	if err != nil {
		return maccmsResponse{}, fmt.Errorf("%s请求失败：%w", provider.name, err)
	}
	response, err := decodeMaccmsResponse(body)
	if err != nil {
		return response, fmt.Errorf("%s：%w", provider.name, err)
	}
	return response, nil
}

func (d *Downloader) fetchMaccmsCategories(ctx context.Context, source string) ([]nativeCategory, error) {
	response, err := d.maccmsRequest(ctx, source, url.Values{"ac": {"list"}, "pg": {"1"}})
	if err != nil {
		return nil, err
	}
	var categories []nativeCategory
	seen := map[string]bool{}
	for _, row := range response.Classes {
		id := mapString(row, "type_id")
		name := truncate(cleanText(mapString(row, "type_name")), 64)
		if !webProviderNumericID.MatchString(id) || name == "" || seen[id] {
			continue
		}
		seen[id] = true
		categories = append(categories, nativeCategory{ID: id, Name: name})
	}
	if len(categories) == 0 {
		return nil, errors.New("采集源暂未返回内容分类，请稍后刷新")
	}
	return categories, nil
}

func maccmsDirectMediaURL(raw string) string {
	raw = strings.TrimSpace(html.UnescapeString(strings.ReplaceAll(raw, "\\/", "/")))
	address, err := url.Parse(raw)
	if err != nil || address.User != nil || !isProviderHTTPMediaURL(raw) {
		return ""
	}
	path := strings.ToLower(address.Path)
	for _, extension := range []string{".m3u8", ".mp4", ".flv"} {
		if strings.HasSuffix(path, extension) {
			return address.String()
		}
	}
	return ""
}

func maccmsBestLine(row map[string]any) maccmsLine {
	best := maccmsLine{index: -1}
	for lineIndex, segment := range strings.Split(mapString(row, "vod_play_url"), "$$$") {
		line := maccmsLine{index: lineIndex}
		for episodeIndex, entry := range strings.Split(segment, "#") {
			title, raw, named := strings.Cut(entry, "$")
			if !named {
				title, raw = "", entry
			}
			address := maccmsDirectMediaURL(raw)
			if address == "" {
				continue
			}
			title = truncate(cleanText(title), 128)
			if title == "" {
				title = fmt.Sprintf("第%d集", episodeIndex+1)
			}
			line.episodes = append(line.episodes, maccmsEpisode{index: episodeIndex, title: title, url: address})
		}
		if len(line.episodes) > len(best.episodes) {
			best = line
		}
	}
	return best
}

func (d *Downloader) maccmsDrama(source string, row map[string]any) Drama {
	source = canonicalProviderSource(source)
	provider, valid := maccmsProviderForSource(source)
	id := mapString(row, "vod_id")
	title := truncate(cleanText(mapString(row, "vod_name")), 512)
	if !valid || !webProviderNumericID.MatchString(id) || title == "" {
		return Drama{}
	}
	intro := truncate(cleanText(mapString(row, "vod_content", "vod_blurb")), 8192)
	cover := providerCoverAddress(row["vod_pic"], d.providerBaseURL(source)+"/")
	var tags []string
	seen := map[string]bool{}
	for _, value := range strings.FieldsFunc(mapString(row, "vod_class"), func(r rune) bool { return r == ',' || r == '，' || r == '/' }) {
		value = truncate(cleanText(value), 64)
		if value != "" && !seen[value] {
			seen[value] = true
			tags = append(tags, value)
		}
	}
	return Drama{
		ID: providerDramaID(source, id), Source: source, SourceID: id, Title: title, Name: title,
		Desc: intro, Intro: intro, Cover: cover, CoverURL: cover, ChannelName: provider.name,
		CategoryName: truncate(cleanText(mapString(row, "type_name")), 64),
		Remark:       truncate(cleanText(mapString(row, "vod_remarks")), 128),
		TotalEpisode: len(maccmsBestLine(row).episodes), Score: mapString(row, "vod_score"),
		OnlineDate: mapString(row, "vod_pubdate", "vod_time"), Tags: tags,
	}
}

func (d *Downloader) fetchMaccmsCatalogPage(ctx context.Context, source string, page int, category, query string) ([]Drama, bool, error) {
	provider, valid := maccmsProviderForSource(source)
	if !valid || page < 1 || page > 100000 || !validNativeCategory(canonicalProviderSource(source), category) {
		return nil, false, errors.New("采集源目录参数无效")
	}
	parameters := url.Values{"ac": {"detail"}, "pg": {strconv.Itoa(page)}}
	if query = strings.TrimSpace(query); query != "" {
		parameters.Set("wd", query)
	} else if category != "" {
		parameters.Set("t", category)
	}
	response, err := d.maccmsRequest(ctx, source, parameters)
	if err != nil {
		return nil, false, err
	}
	if actual, ok := webProviderInteger(response.Page, 100000); ok && actual > 0 && actual != page {
		return nil, false, errors.New("采集接口返回的页码与请求不符")
	}
	items := make([]Drama, 0, len(response.List))
	seen := map[string]bool{}
	for _, row := range response.List {
		drama := d.maccmsDrama(source, row)
		if drama.ID != "" && !seen[drama.ID] {
			seen[drama.ID] = true
			items = append(items, drama)
		}
	}
	more := false
	if len(response.List) > 0 {
		if count, ok := webProviderInteger(response.PageCount, 100000); ok && count > 0 {
			more = page < count
		} else {
			limit := provider.pageSize
			if count, ok := webProviderInteger(response.Limit, 10000); ok && count > 0 {
				limit = count
			}
			more = len(response.List) >= limit
		}
	}
	return items, more, nil
}

func (d *Downloader) fetchMaccmsRecord(ctx context.Context, source, id string) (map[string]any, error) {
	if !webProviderNumericID.MatchString(id) {
		return nil, errors.New("采集源影片 ID 无效")
	}
	response, err := d.maccmsRequest(ctx, source, url.Values{"ac": {"detail"}, "ids": {id}})
	if err != nil {
		return nil, err
	}
	for _, row := range response.List {
		if mapString(row, "vod_id") == id {
			return row, nil
		}
	}
	return nil, errors.New("采集详情未返回所请求的影片，请刷新目录")
}

func (d *Downloader) fetchMaccmsDetail(ctx context.Context, source, id string) (Drama, []Chapter, error) {
	source = canonicalProviderSource(source)
	row, err := d.fetchMaccmsRecord(ctx, source, id)
	if err != nil {
		return Drama{}, nil, err
	}
	drama := d.maccmsDrama(source, row)
	if drama.ID == "" {
		return Drama{}, nil, errors.New("采集详情缺少有效影片信息")
	}
	line := maccmsBestLine(row)
	if len(line.episodes) == 0 {
		return Drama{}, nil, errors.New("该影片暂无可直接播放的分集，解析页线路暂不支持")
	}
	chapters := make([]Chapter, 0, len(line.episodes))
	for _, episode := range line.episodes {
		key := strconv.Itoa(line.index) + "-" + strconv.Itoa(episode.index)
		chapters = append(chapters, Chapter{
			ID: providerChapterID(source, id, key), Source: source, Title: episode.title,
			VideoURL: episode.url, CurrentEpisode: json.RawMessage(strconv.Itoa(episode.index + 1)),
			Referer: d.providerBaseURL(source) + "/",
		})
	}
	return drama, chapters, nil
}

func (d *Downloader) resolveMaccmsMedia(ctx context.Context, task Task) (providerMedia, error) {
	source, id, valid := splitProviderDramaID(task.DramaID)
	if !valid || !isMaccmsSource(source) || task.Chapter.Source != "" && canonicalProviderSource(task.Chapter.Source) != source || !strings.HasPrefix(task.Chapter.ID, providerDramaID(source, id)+":") {
		return providerMedia{}, errors.New("采集源播放章节与影片不符，请刷新详情")
	}
	ctx = context.WithValue(ctx, providerTextNoCacheKey{}, true)
	_, chapters, err := d.fetchMaccmsDetail(ctx, source, id)
	if err != nil {
		return providerMedia{}, err
	}
	for _, chapter := range chapters {
		if chapter.ID == task.Chapter.ID && (task.Chapter.Title == "" || chapter.Title == task.Chapter.Title) {
			return d.prepareWebProviderMedia(ctx, providerMedia{URL: chapter.VideoURL, Referer: chapter.Referer}, "采集源")
		}
	}
	return providerMedia{}, errors.New("采集源原播放线路或分集已变化，请刷新详情")
}
