package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const xifuBaseURL = "https://minidrama-api.contentchina.com"

func isJSONVideoSource(source string) bool {
	return canonicalProviderSource(source) == sourceXifu
}

func jsonVideoReferer(source string) string {
	return "https://minidrama.contentchina.com/"
}

func jsonVideoUserAgent(source string) string {
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
	response, err := d.jsonVideoRequest(ctx, source, "/web/v1/home/categoryList", url.Values{"isLeft": {"1"}})
	if err != nil {
		return nil, err
	}
	var categories []nativeCategory
	seen := map[string]bool{}
	for _, row := range jsonVideoRows(nestedMap(response, "data")["categories"]) {
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
	cover := providerCoverAddress(firstNonEmpty(mapString(row, "coverUrl", "img", "pic")), coverBase)
	intro := truncate(cleanText(mapString(row, "introduction", "story", "content", "info")), 8192)
	total, _ := webProviderInteger(row["total"], 10000)
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
	if strings.TrimSpace(query) != "" {
		return nil, false, errors.New("喜福仅支持已加载目录内搜索")
	}
	parameters := url.Values{"currentPage": {strconv.Itoa(page)}, "pageSize": {"24"}}
	if category != "" {
		parameters.Set("filterCategories[]", category)
	}
	response, err := d.jsonVideoRequest(ctx, source, "/web/v1/drama/list", parameters)
	if err != nil {
		return nil, false, err
	}
	data := nestedMap(response, "data")
	rows := jsonVideoRows(data["data"])
	pagination := nestedMap(data, "pagination")
	actual, ok := webProviderInteger(pagination["currentPage"], 100000)
	if ok && actual > 0 && actual != page {
		return nil, false, errors.New("喜福返回的页码与请求不符")
	}
	pages, ok := webProviderInteger(pagination["totalPages"], 100000)
	more := ok && page < pages || !ok && len(rows) >= 24
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
	return d.fetchXifuDetail(ctx, id, known)
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
	key := strings.TrimPrefix(task.Chapter.ID, prefix)
	sequence, err := strconv.Atoi(key)
	if err != nil || sequence < 1 || sequence > 10000 || strconv.Itoa(sequence) != key {
		return providerMedia{}, errors.New("喜福播放集数无效")
	}
	ctx = context.WithValue(ctx, providerTextNoCacheKey{}, true)
	return d.resolveXifuMedia(ctx, id, sequence)
}
