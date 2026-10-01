package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

const (
	yeguoWorkerBaseURL = "https://www.yeguodj.com"
	yeguoWorkerAgent   = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"
)

var yeguoWorkerCategories = []nativeCategory{
	{ID: "explore", Name: "发现"}, {ID: "rank", Name: "排行榜"},
	{ID: "dushi", Name: "都市"}, {ID: "xiandai", Name: "现代"},
	{ID: "xiaoyuan", Name: "校园"}, {ID: "gudai", Name: "古代"},
	{ID: "xiangcun", Name: "乡村"}, {ID: "zhichang", Name: "职场"},
	{ID: "chongsheng", Name: "重生"}, {ID: "chuanyue", Name: "穿越"},
	{ID: "xitong", Name: "系统"}, {ID: "nixi", Name: "逆袭"},
	{ID: "mogai", Name: "魔改"},
}

var yeguoWorkerBackgrounds = map[string]int{
	"dushi": 40, "xiandai": 39, "xiaoyuan": 47,
	"gudai": 41, "xiangcun": 42, "zhichang": 44,
}

var yeguoWorkerSettings = map[string]int{
	"chongsheng": 26, "chuanyue": 27, "xitong": 28, "nixi": 53, "mogai": 56,
}

func validYeguoWorkerCategory(category string) bool {
	if category == "" {
		return true
	}
	for _, entry := range yeguoWorkerCategories {
		if category == entry.ID {
			return true
		}
	}
	return false
}

func decodeYeguoWorkerResponse(body []byte) (any, error) {
	envelope, err := decodeYeguoJSONObject(body)
	if err != nil {
		return nil, errors.New("野果专线接口返回格式无效")
	}
	if mapString(envelope, "status") == "-1" {
		return nil, errors.New("野果专线当前内容需要站源授权")
	}
	data, found := envelope["data"]
	if !found || data == nil {
		data = envelope
	}
	if encrypted, ok := data.(string); ok {
		ciphertext, err := base64.StdEncoding.DecodeString(encrypted)
		if err != nil || len(ciphertext) == 0 {
			return nil, errors.New("野果专线接口数据解码失败")
		}
		plain, err := aesCBCDecrypt(ciphertext, []byte("2acf7e91e9864673"), []byte("1c29882d3ddfcfd6"))
		if err != nil {
			return nil, errors.New("野果专线接口数据解密失败")
		}
		decoder := json.NewDecoder(bytes.NewReader(plain))
		decoder.UseNumber()
		if decoder.Decode(&data) != nil || decoder.Decode(new(any)) != io.EOF {
			return nil, errors.New("野果专线解密数据格式无效")
		}
	}
	if wrapper, ok := data.(map[string]any); ok && wrapper["data"] != nil {
		switch nested := wrapper["data"].(type) {
		case map[string]any, []any:
			data = nested
		}
	}
	return data, nil
}

func (d *Downloader) callYeguoWorker(ctx context.Context, route string, values map[string]any) (any, error) {
	body, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	site := d.providerBaseURL(sourceYeguoWorker)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, site+"/api.php"+route, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("野果专线接口地址无效")
	}
	request.Header.Set("User-Agent", yeguoWorkerAgent)
	request.Header.Set("Referer", site+"/")
	request.Header.Set("Origin", site)
	request.Header.Set("Accept", "application/json, text/plain, */*")
	request.Header.Set("Content-Type", "application/json;charset=UTF-8")
	response, err := d.doCatalogRequestWithTimeout(request, providerTimeout)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err = io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil || len(body) > 8<<20 {
		return nil, errors.New("野果专线接口数据过大或读取失败")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || catalogResponseBlockReason(response, body) != "" {
		return nil, d.catalogResponseError(request, response, body)
	}
	return decodeYeguoWorkerResponse(body)
}

func yeguoWorkerDrama(row map[string]any, site string) (Drama, error) {
	copy := make(map[string]any, len(row)+4)
	for key, value := range row {
		copy[key] = value
	}
	copy["title"] = firstPresent(row, "title", "video_title", "name")
	copy["cover"] = firstPresent(row, "cover", "cover_img", "pic")
	copy["description"] = firstPresent(row, "description", "intro")
	drama, err := yeguoDramaFromMap(copy, site)
	if err != nil {
		return Drama{}, errors.New("野果专线剧集资料不完整")
	}
	drama.ID, drama.Source, drama.ChannelName = providerDramaID(sourceYeguoWorker, drama.SourceID), sourceYeguoWorker, "野果专线"
	drama.Remark = truncate(mapString(row, "update_status", "serialize_status_text"), 256)
	return drama, nil
}

func (d *Downloader) fetchYeguoWorkerCatalogPage(ctx context.Context, page int, category, query string) ([]Drama, bool, error) {
	if page < 1 || page > 1000000 || len(query) > 1024 || !validYeguoWorkerCategory(category) {
		return nil, false, errors.New("野果专线目录查询参数无效")
	}
	route, limit := "/api/theater/exploreList", 24
	values := map[string]any{"page": page, "limit": limit}
	if query != "" {
		route, limit = "/api/search/result", 14
		values = map[string]any{"keyword": query, "page": page}
	} else if category == "rank" {
		route = "/api/theater/videoRank"
	} else if background, found := yeguoWorkerBackgrounds[category]; found {
		values["background"] = background
	} else if setting, found := yeguoWorkerSettings[category]; found {
		values["setting"] = setting
	}
	payload, err := d.callYeguoWorker(ctx, route, values)
	if err != nil {
		return nil, false, err
	}
	rows, valid := payload.([]any)
	data, _ := payload.(map[string]any)
	if !valid {
		rows, valid = data["list"].([]any)
		if !valid {
			rows, valid = data["top_list"].([]any)
		}
	}
	if !valid || len(rows) > 500 {
		return nil, false, errors.New("野果专线目录格式无效")
	}
	if actual, valid := webProviderInteger(firstPresent(data, "page", "current_page"), 1000000); valid && actual != page {
		return nil, false, errors.New("野果专线返回的分页与请求不符")
	}
	if size, valid := webProviderInteger(firstPresent(data, "limit", "page_size", "per_page"), 500); valid && size > 0 {
		limit = size
	}
	more := len(rows) >= limit
	if total, valid := webProviderInteger(firstPresent(data, "total", "total_count"), 100000000); valid && total >= len(rows) {
		more = page < (total+limit-1)/limit
	}
	if data["has_more"] != nil {
		var valid bool
		more, valid = yeguoFlag(data["has_more"])
		if !valid {
			return nil, false, errors.New("野果专线分页信息无效")
		}
	}
	if more && len(rows) == 0 {
		return nil, false, errors.New("野果专线未返回应有的目录页")
	}
	items := make([]Drama, 0, len(rows))
	seen := map[string]bool{}
	for _, value := range rows {
		row, _ := value.(map[string]any)
		drama, err := yeguoWorkerDrama(row, d.providerBaseURL(sourceYeguoWorker))
		if err != nil {
			return nil, false, err
		}
		if !seen[drama.ID] {
			seen[drama.ID] = true
			items = append(items, drama)
		}
	}
	return items, more, nil
}

func (d *Downloader) fetchYeguoWorkerDetail(ctx context.Context, sourceID string) (Drama, []Chapter, error) {
	if !webProviderNumericID.MatchString(sourceID) {
		return Drama{}, nil, errors.New("野果专线剧集 ID 无效")
	}
	payload, err := d.callYeguoWorker(ctx, "/api/playlet/detail", map[string]any{"video_id": json.Number(sourceID)})
	if err != nil {
		return Drama{}, nil, err
	}
	row, valid := payload.(map[string]any)
	if !valid {
		return Drama{}, nil, errors.New("野果专线详情格式无效")
	}
	if id := mapString(row, "video_id", "id"); id != "" && id != sourceID {
		return Drama{}, nil, errors.New("野果专线详情与请求剧集不符")
	}
	row["video_id"] = sourceID
	if firstPresent(row, "title", "video_title", "name") == nil {
		row["title"] = sourceID
	}
	site := d.providerBaseURL(sourceYeguoWorker)
	drama, err := yeguoWorkerDrama(row, site)
	if err != nil {
		return Drama{}, nil, err
	}
	episodes, _ := row["episodes"].([]any)
	if len(episodes) > 10000 {
		return Drama{}, nil, errors.New("野果专线分集数量过多")
	}
	if len(episodes) == 0 {
		count, valid := webProviderInteger(firstPresent(row, "episode_count", "total_serial"), 10000)
		if !valid || count < 1 {
			return Drama{}, nil, errors.New("野果专线暂未返回分集")
		}
		for number := 1; number <= count; number++ {
			episodes = append(episodes, map[string]any{"sort": number})
		}
	}
	chapters := make([]Chapter, 0, len(episodes))
	seenNumbers, seenIDs := map[int]bool{}, map[string]bool{}
	for index, value := range episodes {
		episode, valid := value.(map[string]any)
		if !valid {
			return Drama{}, nil, errors.New("野果专线分集资料无效")
		}
		if advertisement, _ := yeguoFlag(episode["is_adv"]); advertisement {
			continue
		}
		number := index + 1
		if raw := firstPresent(episode, "sort", "episode", "index"); raw != nil {
			number, valid = webProviderInteger(raw, 100000)
			if !valid || number < 1 {
				return Drama{}, nil, errors.New("野果专线分集编号无效")
			}
		}
		id := mapString(episode, "id", "episode_id", "video_episode_id", "nid")
		if seenNumbers[number] || id != "" && (!webProviderNumericID.MatchString(id) || seenIDs[id]) {
			return Drama{}, nil, errors.New("野果专线分集编号无效或重复")
		}
		seenNumbers[number], seenIDs[id] = true, true
		key := strconv.Itoa(number) + ":" + id
		address := mapString(episode, "video_url", "url", "play_url")
		if address != "" {
			address = resolveProviderURL(site+"/", address)
			if !isProviderHTTPMediaURL(address) {
				address = ""
			}
		}
		chapters = append(chapters, Chapter{ID: providerChapterID(sourceYeguoWorker, sourceID, key), Source: sourceYeguoWorker,
			Title:          truncate(firstNonEmpty(mapString(episode, "title"), fmt.Sprintf("第 %d 集", number)), 256),
			CurrentEpisode: rawEpisode(number), VideoURL: address,
			PageURL: yeguoEpisodePage(site, sourceID, number), Referer: site + "/"})
	}
	sort.SliceStable(chapters, func(i, j int) bool {
		left, _ := strconv.Atoi(chapters[i].EpisodeString(i + 1))
		right, _ := strconv.Atoi(chapters[j].EpisodeString(j + 1))
		return left < right
	})
	return drama, chapters, nil
}

func (d *Downloader) resolveYeguoWorkerMedia(ctx context.Context, task Task) (providerMedia, error) {
	source, sourceID, valid := splitProviderDramaID(task.DramaID)
	prefix := providerChapterID(sourceYeguoWorker, sourceID, "")
	if !valid || source != sourceYeguoWorker || !webProviderNumericID.MatchString(sourceID) || !strings.HasPrefix(task.Chapter.ID, prefix) {
		return providerMedia{}, errors.New("野果专线播放分集信息无效，请刷新详情")
	}
	numberText, id, valid := strings.Cut(strings.TrimPrefix(task.Chapter.ID, prefix), ":")
	number, err := strconv.Atoi(numberText)
	if !valid || err != nil || number < 1 || number > 100000 || id != "" && !webProviderNumericID.MatchString(id) {
		return providerMedia{}, errors.New("野果专线播放分集编号无效")
	}
	address := task.Chapter.VideoURL
	if !isProviderHTTPMediaURL(address) {
		values := map[string]any{"video_id": json.Number(sourceID), "vid": json.Number(sourceID),
			"ep": number, "sort": number, "episode": number, "index": number, "page": number}
		if id != "" {
			for _, field := range []string{"episode_id", "epid", "eid", "nid", "id", "video_episode_id"} {
				values[field] = json.Number(id)
			}
		}
		payload, err := d.callYeguoWorker(ctx, "/api/playlet/play", values)
		if err != nil {
			return providerMedia{}, err
		}
		row, valid := payload.(map[string]any)
		if !valid {
			return providerMedia{}, errors.New("野果专线播放信息格式无效")
		}
		if advertisement, _ := yeguoFlag(row["is_adv"]); advertisement {
			return providerMedia{}, errors.New("野果专线未返回该集正片，请重试")
		}
		if returned := mapString(row, "video_id", "playlet_id"); returned != "" && returned != sourceID {
			return providerMedia{}, errors.New("野果专线播放信息与请求剧集不符")
		}
		if episodes, valid := row["episodeAll"].([]any); valid {
			if len(episodes) > 10000 {
				return providerMedia{}, errors.New("野果专线播放分集数量过多")
			}
			for _, value := range episodes {
				episode, _ := value.(map[string]any)
				if advertisement, _ := yeguoFlag(episode["is_adv"]); advertisement {
					continue
				}
				epNumber := mapString(episode, "sort", "index", "episode")
				epID := mapString(episode, "id", "episode_id", "video_episode_id", "nid")
				if epNumber != "" && epNumber != numberText || id != "" && epID != "" && epID != id || epNumber == "" && (id == "" || epID != id) {
					continue
				}
				address = mapString(episode, "video_url", "url", "play_url")
				break
			}
		} else {
			if returned := mapString(row, "episode_id", "id"); id != "" && returned != "" && returned != id {
				return providerMedia{}, errors.New("野果专线返回了其他分集")
			}
			if returned := mapString(row, "episode_sort", "sort", "episode", "index"); returned != "" && returned != numberText {
				return providerMedia{}, errors.New("野果专线返回了其他分集")
			}
			address = mapString(row, "video_url", "url", "play_url")
		}
	}
	if address == "" {
		return providerMedia{}, errors.New("野果专线未提供该集播放地址，请重试或确认站源权限")
	}
	site := d.providerBaseURL(sourceYeguoWorker)
	media := providerMedia{URL: resolveProviderURL(site+"/", address), Referer: site + "/",
		credentials: &providerMediaCredentials{referer: site + "/", userAgent: yeguoWorkerAgent}}
	return d.prepareWebProviderMedia(ctx, media, "野果专线")
}
