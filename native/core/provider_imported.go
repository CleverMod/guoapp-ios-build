package core

import (
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type importedSourceState struct {
	hosts       []string
	host        string
	config      map[string]any
	configAt    time.Time
	session     map[string]string
	searchPools map[string]*importedPool
}

type importedPool struct {
	rows    []map[string]any
	page    int
	done    bool
	updated time.Time
}

func jsonUnmarshalHeaders(text string, headers map[string]string) error {
	if json.Unmarshal([]byte(text), &headers) == nil {
		return nil
	}
	text = strings.ReplaceAll(text, "\\r\\n", "\n")
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok && key != "" {
			headers[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return nil
}

type importedEpisode struct{ name, url, series string }
type importedLine struct {
	key, name string
	episodes  []importedEpisode
}
type importedPayload struct {
	Player string `json:"player"`
	URL    string `json:"url"`
	Series string `json:"series,omitempty"`
}

func importedUnbase64(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	return base64.StdEncoding.DecodeString(value + strings.Repeat("=", (4-len(value)%4)%4))
}

func importedDecode(body string) (any, error) {
	body = strings.TrimPrefix(body, "\ufeff")
	if !json.Valid([]byte(body)) {
		return nil, errors.New("站源未返回完整 JSON 数据")
	}
	value, err := attachedDecode(body)
	if err != nil {
		return nil, err
	}
	switch value.(type) {
	case map[string]any, []any:
		return value, nil
	default:
		return nil, errors.New("站源 JSON 数据结构无效")
	}
}

func importedECBDecrypt(data, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil || len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("站源加密响应无效")
	}
	plain := make([]byte, len(data))
	for i := 0; i < len(data); i += aes.BlockSize {
		block.Decrypt(plain[i:i+aes.BlockSize], data[i:i+aes.BlockSize])
	}
	return pkcs7Unpad(plain, aes.BlockSize)
}

func importedCBCText(value, key, iv string) (string, error) {
	if len(iv) != aes.BlockSize {
		return "", errors.New("站源内置解密参数缺失")
	}
	data, err := importedUnbase64(value)
	if err != nil || len(data) == 0 {
		return "", errors.New("站源加密内容无效")
	}
	plain, err := aesCBCDecrypt(data, []byte(key), []byte(iv))
	return string(plain), err
}

func (c *attachedClient) importedRaw(ctx context.Context, method, target, contentType, body string, extra map[string]string) (string, error) {
	headers := map[string]string{}
	for k, v := range c.access.Headers {
		headers[k] = v
	}
	for k, v := range extra {
		headers[k] = v
	}
	value, _, err := c.raw(ctx, method, target, contentType, body, headers)
	for depth := 0; depth < 2 && strings.HasPrefix(strings.TrimSpace(value), "\""); depth++ {
		var plain string
		if json.Unmarshal([]byte(value), &plain) != nil {
			break
		}
		value = plain
	}
	return value, err
}

func (c *attachedClient) importedJSON(ctx context.Context, target string, values url.Values) (any, error) {
	body, err := c.importedRaw(ctx, http.MethodGet, attachedPath(target, values), "", "", nil)
	if err != nil {
		return nil, err
	}
	return importedDecode(body)
}

func (c *attachedClient) importedDrama(row map[string]any, id string) Drama {
	if id == "" {
		id = mapString(row, "vod_id", "id", "tjurl", "nextlink")
	}
	return c.drama(map[string]any{
		"title":       mapString(row, "vod_name", "name", "title", "tjinfo"),
		"cover":       mapString(row, "vod_pic", "pic", "img_url", "tjpicurl", "cover"),
		"description": mapString(row, "vod_content", "intro", "description", "content"),
		"remark":      mapString(row, "vod_remarks", "remarks", "state", "trunk"),
		"type_name":   mapString(row, "type_name", "type"), "total": row["vod_total"],
	}, id)
}

func (c *attachedClient) importedDramas(rows any) []Drama {
	out := []Drama{}
	seen := map[string]bool{}
	for _, row := range attachedRows(rows) {
		drama := c.importedDrama(row, "")
		if drama.ID != "" && !seen[drama.ID] {
			seen[drama.ID] = true
			out = append(out, drama)
		}
	}
	return out
}

func (c *attachedClient) importedChapters(id string, lines []importedLine) ([]Chapter, int) {
	chapters := []Chapter{}
	seen := map[string]bool{}
	count := 0
	for _, line := range lines {
		if len(line.episodes) > count {
			count = len(line.episodes)
		}
		for i, episode := range line.episodes {
			if episode.url == "" {
				continue
			}
			name := firstNonEmpty(cleanText(episode.name), fmt.Sprintf("第%d集", i+1))
			key := attachedSHA(line.key + "\x00" + name)[:24]
			if seen[key] {
				continue
			}
			seen[key] = true
			title := name
			if len(lines) > 1 {
				title = firstNonEmpty(line.name, line.key) + " · " + name
			}
			payload := attachedJSON(importedPayload{Player: line.key, URL: episode.url, Series: episode.series})
			chapter := attachedChapter(c.p.id, id, key, i+1, title, payload, "")
			chapter.LineID = attachedSHA(line.key)[:24]
			chapter.LineName = truncate(cleanText(firstNonEmpty(line.name, line.key)), 128)
			chapters = append(chapters, chapter)
		}
	}
	return chapters, count
}

func (c *attachedClient) importedDetailResult(id string, row map[string]any, lines []importedLine) (Drama, []Chapter, error) {
	if remote := mapString(row, "vod_id", "id"); remote != "" && remote != id {
		return Drama{}, nil, errors.New("站源返回的影片标识不符，请刷新详情")
	}
	drama := c.importedDrama(row, id)
	chapters, count := c.importedChapters(id, lines)
	if drama.ID == "" || len(chapters) == 0 {
		return Drama{}, nil, fmt.Errorf("%s未返回可用在线章节；网盘或磁力资源需要对应客户端", c.p.name)
	}
	drama.EpisodeCount, drama.TotalEpisode = count, count
	return drama, chapters, nil
}

func importedURLMedia(address, agent, referer string, headers map[string]string) (providerMedia, error) {
	if !isProviderHTTPMediaURL(address) {
		return providerMedia{}, errors.New("站源未返回有效媒体地址")
	}
	for key, value := range headers {
		if strings.EqualFold(key, "User-Agent") {
			agent = value
		}
		if strings.EqualFold(key, "Referer") {
			referer = value
		}
	}
	parsed, _ := url.Parse(address)
	credentials := &providerMediaCredentials{userAgent: agent, referer: referer, origin: providerMediaOrigin(parsed)}
	for key, value := range headers {
		if strings.EqualFold(key, "Cookie") {
			credentials.cookie = value
		}
	}
	return providerMedia{URL: address, Referer: referer, credentials: credentials}, nil
}

func (c *attachedClient) importedCategories(ctx context.Context) ([]nativeCategory, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var data any
	var err error
	switch c.p.id {
	case "nnvideo":
		data, err = c.nnAPI(ctx, "/types", nil)
	case "jumi":
		if err = c.jumiInit(ctx); err == nil {
			data, err = c.jumiRequest(ctx, "Category", nil)
		}
	default:
		out := []nativeCategory{}
		for _, cat := range c.p.categories {
			out = append(out, nativeCategory{ID: cat.id, Name: cat.name})
		}
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	out := []nativeCategory{}
	for _, row := range attachedRows(data) {
		if c.p.id == "jumi" && mapString(row, "type_status") != "1" {
			continue
		}
		id := mapString(row, "type_id", "type_en")
		if c.p.id == "jumi" {
			id = mapString(row, "type_en")
		}
		name := mapString(row, "type_name")
		if id != "" && name != "" && validNativeCategory(c.p.id, id) {
			out = append(out, nativeCategory{ID: id, Name: name})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s未返回有效分类", c.p.name)
	}
	return out, nil
}

func (c *attachedClient) importedCatalog(ctx context.Context, page int, cat attachedCategory, query string) ([]Drama, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.p.id {
	case "xiaopingguo":
		return c.xpgCatalog(ctx, page, cat.value, query)
	case "luoxue":
		return c.luoxueCatalog(ctx, page, cat.value, query)
	case "xiaobao":
		return c.importedWebCatalog(ctx, page, cat.value, query)
	case "nnvideo":
		values := attachedParams("type_id", cat.value, "page", strconv.Itoa(page))
		if query != "" {
			values = attachedParams("wd", query, "page", strconv.Itoa(page))
		}
		data, err := c.nnAPI(ctx, "/list", values)
		if err != nil {
			return nil, false, err
		}
		rows := c.importedDramas(data)
		return rows, len(rows) >= 12, nil
	case "jumi":
		return c.jumiCatalog(ctx, page, cat.value, query)
	}
	return nil, false, errors.New("站源目录协议未登记")
}

func (c *attachedClient) importedDetail(ctx context.Context, id string) (Drama, []Chapter, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.p.id {
	case "xiaopingguo":
		return c.xpgDetail(ctx, id)
	case "luoxue":
		return c.luoxueDetail(ctx, id)
	case "xiaobao":
		return c.importedWebDetail(ctx, id)
	case "nnvideo":
		return c.nnDetail(ctx, id)
	case "jumi":
		return c.jumiDetail(ctx, id)
	}
	return Drama{}, nil, errors.New("站源详情协议未登记")
}

func (c *attachedClient) importedPlay(ctx context.Context, id string, chapter Chapter) (providerMedia, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var payload importedPayload
	if json.Unmarshal([]byte(chapter.VideoURL), &payload) != nil || payload.URL == "" {
		return providerMedia{}, errors.New("站源章节数据无效")
	}
	switch c.p.id {
	case "xiaopingguo":
		return c.xpgPlay(ctx, payload)
	case "luoxue":
		return c.luoxuePlay(ctx, payload)
	case "xiaobao":
		return c.importedWebPlay(ctx, payload)
	case "nnvideo":
		return c.nnPlay(ctx, payload)
	case "jumi":
		return c.jumiPlay(ctx, payload)
	}
	return providerMedia{}, errors.New("站源播放协议未登记")
}
