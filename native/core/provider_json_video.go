package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	xiaopingguoBaseURL = "http://asp.xpgtv.com"
	xifuBaseURL        = "https://minidrama-api.contentchina.com"
	hongdouBaseURL     = "https://api.dramaplay.shop"
)

var xiaopingguoMediaID = regexp.MustCompile(`^[A-Za-z0-9_+/=-]{1,512}$`)

func isJSONVideoSource(source string) bool {
	switch canonicalProviderSource(source) {
	case sourceXiaopingguo, sourceXifu, sourceHongdou:
		return true
	}
	return false
}

func jsonVideoReferer(source string) string {
	if source == sourceHongdou {
		return "https://hdou.tv/"
	}
	if source == sourceXifu {
		return "https://minidrama.contentchina.com/"
	}
	return xiaopingguoBaseURL + "/"
}

func jsonVideoUserAgent(source string) string {
	if source == sourceXiaopingguo {
		return "okhttp/3.12.11"
	}
	return "Mozilla/5.0"
}

func decodeJSONVideoResponse(body string) (map[string]any, error) {
	var response map[string]any
	decoder := json.NewDecoder(strings.NewReader(strings.TrimPrefix(body, "\ufeff")))
	decoder.UseNumber()
	if err := decoder.Decode(&response); err != nil || response == nil {
		return nil, errors.New("站源未返回有效 JSON 数据")
	}
	if code, exists := response["code"]; exists && nativeText(code) != "200" {
		return nil, errors.New("站源接口暂不可用或尚未授权，请稍后重试")
	}
	return response, nil
}

func (d *Downloader) jsonVideoRequest(ctx context.Context, source, path string, parameters url.Values) (map[string]any, error) {
	if !isJSONVideoSource(source) {
		return nil, errors.New("请选择有效站源")
	}
	ctx = context.WithValue(ctx, providerTextUserAgentKey{}, jsonVideoUserAgent(source))
	address := d.providerBaseURL(source) + path
	if len(parameters) > 0 {
		address += "?" + parameters.Encode()
	}
	body, err := d.fetchProviderText(ctx, address, jsonVideoReferer(source))
	if err != nil {
		return nil, fmt.Errorf("站源接口请求失败：%w", err)
	}
	return decodeJSONVideoResponse(body)
}

func jsonVideoRows(value any) []map[string]any {
	var rows []map[string]any
	values, _ := value.([]any)
	for _, value := range values {
		if row, ok := value.(map[string]any); ok {
			rows = append(rows, row)
		}
	}
	return rows
}

func (d *Downloader) fetchJSONVideoCategories(ctx context.Context, source string) ([]nativeCategory, error) {
	if source == sourceHongdou {
		return []nativeCategory{
			{ID: "334", Name: "都市"}, {ID: "155", Name: "古装"}, {ID: "338", Name: "爱情"},
			{ID: "362", Name: "总裁"}, {ID: "285", Name: "甜宠"}, {ID: "332", Name: "逆袭"},
			{ID: "341", Name: "重生"}, {ID: "306", Name: "穿越"}, {ID: "397", Name: "玄幻"}, {ID: "220", Name: "悬疑"},
		}, nil
	}
	path, parameters := "/api.php/v2.vod/androidtypes", url.Values{}
	if source == sourceXifu {
		path, parameters = "/web/v1/home/categoryList", url.Values{"isLeft": {"1"}}
	}
	response, err := d.jsonVideoRequest(ctx, source, path, parameters)
	if err != nil {
		return nil, err
	}
	rows := jsonVideoRows(response["data"])
	if source == sourceXifu {
		rows = jsonVideoRows(nestedMap(response, "data")["categories"])
	}
	var categories []nativeCategory
	seen := map[string]bool{}
	for _, row := range rows {
		id := mapString(row, "type_id", "id")
		name := truncate(cleanText(mapString(row, "type_name", "name")), 64)
		if webProviderNumericID.MatchString(id) && name != "" && !seen[id] {
			seen[id] = true
			categories = append(categories, nativeCategory{ID: id, Name: name})
		}
	}
	if len(categories) == 0 {
		return nil, errors.New("站源暂未返回内容分类")
	}
	return categories, nil
}

func (d *Downloader) jsonVideoDrama(source string, row map[string]any) Drama {
	id, title := mapString(row, "id", "albumId"), truncate(cleanText(mapString(row, "name", "title")), 512)
	if !webProviderNumericID.MatchString(id) || title == "" {
		return Drama{}
	}
	coverBase := d.providerBaseURL(source) + "/"
	if source == sourceHongdou {
		coverBase = "https://static.hdou.tv/"
	}
	cover := providerCoverAddress(firstNonEmpty(mapString(row, "coverUrl", "img", "pic")), coverBase)
	intro := truncate(cleanText(mapString(row, "introduction", "story", "content", "info")), 8192)
	total, _ := webProviderInteger(row["total"], 10000)
	if source == sourceHongdou {
		total, _ = webProviderInteger(row["sum"], 10000)
	}
	category := truncate(cleanText(mapString(row, "className", "text")), 128)
	var tags []string
	if source == sourceXifu {
		for _, item := range jsonVideoRows(row["categoryData"]) {
			if name := truncate(cleanText(mapString(item, "name")), 64); name != "" {
				tags = append(tags, name)
			}
		}
		category = strings.Join(tags, " · ")
	}
	return Drama{
		ID: providerDramaID(source, id), Source: source, SourceID: id, Title: title, Name: title,
		Desc: intro, Intro: intro, Cover: cover, CoverURL: cover, TotalEpisode: total,
		CategoryName: category, Remark: truncate(cleanText(mapString(row, "updateInfo")), 128),
		Score: mapString(row, "score"), Views: mapString(row, "views"),
		OnlineDate: mapString(row, "updateTime", "updatetime"), Tags: tags,
	}
}

func (d *Downloader) fetchJSONVideoCatalogPage(ctx context.Context, source string, page int, category, query string) ([]Drama, bool, error) {
	if !isJSONVideoSource(source) || page < 1 || page > 100000 || !validNativeCategory(source, category) {
		return nil, false, errors.New("站源目录参数无效")
	}
	var path string
	parameters := url.Values{}
	query = strings.TrimSpace(query)
	switch source {
	case sourceXiaopingguo:
		parameters.Set("page", strconv.Itoa(page))
		path = "/api.php/v2.vod/androidfilter10086"
		parameters.Set("type", firstNonEmpty(category, "0"))
		if query != "" {
			path = "/api.php/v2.vod/androidsearch10086"
			parameters.Del("type")
			parameters.Set("wd", query)
		}
	case sourceXifu:
		if query != "" {
			return nil, false, errors.New("喜福仅支持已加载目录内搜索")
		}
		path = "/web/v1/drama/list"
		parameters.Set("currentPage", strconv.Itoa(page))
		parameters.Set("pageSize", "24")
		if category != "" {
			parameters.Set("filterCategories[]", category)
		}
	case sourceHongdou:
		path = "/api/video/lists"
		parameters = url.Values{"limit": {"24"}, "offset": {strconv.Itoa((page - 1) * 24)}, "lx": {"1"}}
		if query != "" {
			parameters.Set("key", query)
		} else if category != "" {
			parameters.Set("type", category)
		}
	}
	response, err := d.jsonVideoRequest(ctx, source, path, parameters)
	if err != nil {
		return nil, false, err
	}
	rows, more := jsonVideoRows(response["data"]), false
	switch source {
	case sourceXiaopingguo:
		more = len(rows) >= 20
	case sourceXifu:
		data := nestedMap(response, "data")
		rows = jsonVideoRows(data["data"])
		pagination := nestedMap(data, "pagination")
		actual, ok := webProviderInteger(pagination["currentPage"], 100000)
		if ok && actual > 0 && actual != page {
			return nil, false, errors.New("喜福返回的页码与请求不符")
		}
		pages, ok := webProviderInteger(pagination["totalPages"], 100000)
		more = ok && page < pages || !ok && len(rows) >= 24
	case sourceHongdou:
		rows = jsonVideoRows(response["rows"])
		total, ok := webProviderInteger(response["total"], 10000000)
		more = ok && page*24 < total || !ok && len(rows) >= 24
	}
	items := make([]Drama, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		drama := d.jsonVideoDrama(source, row)
		if drama.ID != "" && !seen[drama.ID] {
			seen[drama.ID] = true
			items = append(items, drama)
		}
	}
	return items, more && len(rows) > 0, nil
}

func jsonVideoChapter(source, id, key, title, address string, sequence int) Chapter {
	return Chapter{ID: providerChapterID(source, id, key), Source: source,
		Title:    firstNonEmpty(truncate(cleanText(title), 128), fmt.Sprintf("第%d集", sequence)),
		VideoURL: address, CurrentEpisode: json.RawMessage(strconv.Itoa(sequence)), Referer: jsonVideoReferer(source)}
}

func (d *Downloader) fetchJSONVideoDetail(ctx context.Context, source, id string, known nativeDrama) (Drama, []Chapter, error) {
	if !isJSONVideoSource(source) || !webProviderNumericID.MatchString(id) {
		return Drama{}, nil, errors.New("站源影片 ID 无效")
	}
	if source == sourceXifu {
		return d.fetchXifuDetail(ctx, id, known)
	}
	path, parameters := "/api.php/v3.vod/androiddetail2", url.Values{"vod_id": {id}}
	if source == sourceHongdou {
		path, parameters = "/api/video/info", url.Values{"id": {id}, "mid": {"0"}}
	}
	response, err := d.jsonVideoRequest(ctx, source, path, parameters)
	if err != nil {
		return Drama{}, nil, err
	}
	row := response
	if source == sourceXiaopingguo {
		row = nestedMap(response, "data")
	}
	if mapString(row, "id") != id {
		return Drama{}, nil, errors.New("详情未返回所请求的影片，请刷新目录")
	}
	drama := d.jsonVideoDrama(source, row)
	var chapters []Chapter
	if source == sourceXiaopingguo {
		for index, entry := range jsonVideoRows(row["urls"]) {
			raw := strings.TrimSpace(mapString(entry, "url"))
			address := maccmsDirectMediaURL(raw)
			if address == "" && xiaopingguoMediaID.MatchString(raw) {
				address = "http://c.xpgtv.net/m3u8/" + url.PathEscape(raw) + ".m3u8"
			}
			if address != "" {
				chapters = append(chapters, jsonVideoChapter(source, id, strconv.Itoa(index), mapString(entry, "key", "name"), address, index+1))
			}
		}
	} else {
		rows := jsonVideoRows(row["video"])
		sort.SliceStable(rows, func(i, j int) bool {
			a, _ := webProviderInteger(rows[i]["weigh"], 100000)
			b, _ := webProviderInteger(rows[j]["weigh"], 100000)
			return a < b
		})
		seen := map[string]bool{}
		for index, entry := range rows {
			key := mapString(entry, "id")
			address := maccmsDirectMediaURL(mapString(entry, "src", "videourl"))
			if !webProviderNumericID.MatchString(key) || address == "" || seen[key] {
				continue
			}
			seen[key] = true
			sequence, _ := webProviderInteger(entry["weigh"], 10000)
			if sequence < 1 {
				sequence = index + 1
			}
			chapters = append(chapters, jsonVideoChapter(source, id, key, mapString(entry, "name", "fjname"), address, sequence))
		}
		if len(chapters) == 0 {
			if address := maccmsDirectMediaURL(mapString(row, "videourl")); address != "" {
				chapters = append(chapters, jsonVideoChapter(source, id, "current", mapString(row, "ji"), address, 1))
			}
		}
	}
	if drama.ID == "" || len(chapters) == 0 {
		return Drama{}, nil, errors.New("该影片暂无可直接播放的分集")
	}
	drama.TotalEpisode = len(chapters)
	return drama, chapters, nil
}

func (d *Downloader) fetchXifuDetail(ctx context.Context, id string, known nativeDrama) (Drama, []Chapter, error) {
	drama := Drama{}
	if known.ID == providerDramaID(sourceXifu, id) && known.Episodes > 0 && known.Episodes <= 10000 {
		drama = Drama{ID: known.ID, Source: sourceXifu, SourceID: id, Title: known.Title, Name: known.Title,
			Desc: known.Description, Cover: known.Cover, TotalEpisode: known.Episodes, CategoryName: known.Category, Tags: known.Tags}
	} else {
		for page := 1; page <= 50; page++ {
			items, more, err := d.fetchJSONVideoCatalogPage(ctx, sourceXifu, page, "", "")
			if err != nil {
				return Drama{}, nil, err
			}
			for _, item := range items {
				if item.SourceID == id {
					drama = item
					break
				}
			}
			if drama.ID != "" || !more {
				break
			}
		}
	}
	total, valid := webProviderInteger(drama.TotalEpisode, 10000)
	if drama.ID == "" || !valid || total < 1 {
		return Drama{}, nil, errors.New("喜福未返回该剧的分集数量，请刷新目录")
	}
	chapters := make([]Chapter, 0, total)
	for sequence := 1; sequence <= total; sequence++ {
		chapters = append(chapters, jsonVideoChapter(sourceXifu, id, strconv.Itoa(sequence), "", "", sequence))
	}
	return drama, chapters, nil
}

func (d *Downloader) resolveJSONVideoMedia(ctx context.Context, task Task) (providerMedia, error) {
	source, id, valid := splitProviderDramaID(task.DramaID)
	prefix := providerDramaID(source, id) + ":"
	if !valid || !isJSONVideoSource(source) || task.Chapter.Source != "" && canonicalProviderSource(task.Chapter.Source) != source || !strings.HasPrefix(task.Chapter.ID, prefix) {
		return providerMedia{}, errors.New("播放章节与影片不符，请刷新详情")
	}
	ctx = context.WithValue(ctx, providerTextNoCacheKey{}, true)
	if source == sourceXifu {
		key := strings.TrimPrefix(task.Chapter.ID, prefix)
		sequence, err := strconv.Atoi(key)
		if err != nil || sequence < 1 || sequence > 10000 || strconv.Itoa(sequence) != key {
			return providerMedia{}, errors.New("喜福播放集数无效")
		}
		return d.resolveXifuMedia(ctx, id, sequence)
	}
	_, chapters, err := d.fetchJSONVideoDetail(ctx, source, id, nativeDrama{})
	if err != nil {
		return providerMedia{}, err
	}
	for _, chapter := range chapters {
		if chapter.ID == task.Chapter.ID && (task.Chapter.Title == "" || task.Chapter.Title == chapter.Title) {
			credentials := &providerMediaCredentials{referer: chapter.Referer, userAgent: jsonVideoUserAgent(source)}
			return d.prepareWebProviderMedia(ctx, providerMedia{URL: chapter.VideoURL, Referer: chapter.Referer, credentials: credentials}, "站源")
		}
	}
	return providerMedia{}, errors.New("原播放分集已变化，请刷新详情")
}
