package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type attachedAccess struct {
	Headers    map[string]string `json:"headers,omitempty"`
	Query      map[string]string `json:"query,omitempty"`
	PrivateKey string            `json:"privateKey,omitempty"`
	SignKey    string            `json:"signKey,omitempty"`
	DeviceID   string            `json:"deviceId,omitempty"`
}

var bundledAttachedAccessBase64 string

type attachedRequestClientKey struct{}

type attachedClient struct {
	d      *Downloader
	p      attachedProvider
	mu     sync.Mutex
	jar    http.CookieJar
	access attachedAccess
	device string
	token  string
	userID string
	expiry time.Time
	sid    string
	skey   []byte
}

func attachedProviderByID(id string) (attachedProvider, bool) {
	for _, p := range attachedProviders {
		if p.id == id {
			return p, true
		}
	}
	return attachedProvider{}, false
}

func isAttachedSource(source string) bool {
	_, ok := attachedProviderByID(source)
	return ok
}

func attachedSearchSource(source string) bool {
	p, ok := attachedProviderByID(source)
	return ok && p.search
}

func (d *Downloader) loadAttachedAccess(directory string) {
	d.attachedAccess = map[string]attachedAccess{}
	if data, err := base64.StdEncoding.DecodeString(bundledAttachedAccessBase64); err == nil && len(data) <= 256<<10 {
		_ = json.Unmarshal(data, &d.attachedAccess)
	}
	if d.attachedAccess == nil {
		d.attachedAccess = map[string]attachedAccess{}
	}
	data, err := os.ReadFile(filepath.Join(directory, "source_access.json"))
	if err == nil && len(data) <= 256<<10 {
		var local map[string]attachedAccess
		if json.Unmarshal(data, &local) == nil {
			for source, access := range local {
				if isAttachedSource(source) {
					d.attachedAccess[source] = access
				}
			}
		}
	}
}

func (engine *nativeEngine) importAttachedAccess(source string, raw json.RawMessage) error {
	if !isAttachedSource(source) || len(raw) > 64<<10 {
		return errors.New("接口授权格式无效")
	}
	var access attachedAccess
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&access) != nil {
		return errors.New("接口授权格式无效")
	}
	for k, v := range access.Headers {
		if !validHTTPHeaderName(k) || strings.ContainsAny(v, "\r\n") {
			return errors.New("接口授权请求头无效")
		}
	}
	d := engine.downloader
	d.attachedMu.Lock()
	defer d.attachedMu.Unlock()
	next := map[string]attachedAccess{}
	for key, value := range d.attachedAccess {
		next[key] = value
	}
	next[source] = access
	data, _ := json.Marshal(next)
	if len(data) > 256<<10 {
		return errors.New("接口授权配置过大")
	}
	file := filepath.Join(engine.directory, "source_access.json")
	if err := nativeDownloadWrite(file, data); err != nil {
		return errors.New("保存接口授权失败")
	}
	d.attachedAccess = next
	delete(d.attachedClients, source)
	return nil
}

func validHTTPHeaderName(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", r)) {
			return false
		}
	}
	return true
}

func attachedSourceForHost(host string) string {
	for _, p := range attachedProviders {
		address, _ := url.Parse(p.base)
		if address != nil && strings.EqualFold(address.Hostname(), host) {
			return p.id
		}
	}
	return ""
}

func (p attachedProvider) category(id string) (attachedCategory, bool) {
	if id == "" && len(p.categories) > 0 {
		return p.categories[0], true
	}
	for _, cat := range p.categories {
		if cat.id == id {
			return cat, true
		}
	}
	return attachedCategory{}, false
}

func attachedNonce() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(value[:])
}

func (d *Downloader) attachedClient(source string) (*attachedClient, error) {
	p, ok := attachedProviderByID(source)
	if !ok {
		return nil, errors.New("请选择有效站源")
	}
	d.attachedMu.Lock()
	defer d.attachedMu.Unlock()
	if d.attachedClients == nil {
		d.attachedClients = map[string]*attachedClient{}
	}
	if c := d.attachedClients[source]; c != nil {
		return c, nil
	}
	jar, _ := cookiejar.New(nil)
	access := d.attachedAccess[source]
	if access.DeviceID != "" && (len(access.DeviceID) < 16 || len(access.DeviceID) > 128 || strings.ContainsAny(access.DeviceID, "\r\n\x00")) {
		return nil, errors.New("接口授权设备标识格式无效")
	}
	c := &attachedClient{d: d, p: p, jar: jar, device: firstNonEmpty(access.DeviceID, attachedNonce()), access: access}
	d.attachedClients[source] = c
	return c, nil
}

func (c *attachedClient) raw(ctx context.Context, method, target, contentType, body string, headers map[string]string) (string, int, error) {
	if !isProviderHTTPMediaURL(target) {
		target = c.p.base + target
	}
	req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
	if err != nil || req.URL.User != nil {
		return "", 0, errors.New("站源请求地址无效")
	}
	req.Header.Set("User-Agent", c.agent())
	req.Header.Set("Referer", c.p.base+"/")
	req.Header.Set("Accept", "application/json, text/html;q=0.9, */*;q=0.8")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	client := *c.d.client
	client.Jar = c.jar
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("站源重定向次数过多")
		}
		if len(via) > 0 && !strings.EqualFold(next.URL.Host, via[0].URL.Host) {
			for key := range next.Header {
				if key != "User-Agent" && key != "Accept" && key != "Referer" && key != "Content-Type" {
					next.Header.Del(key)
				}
			}
		}
		return nil
	}
	req = req.WithContext(context.WithValue(req.Context(), attachedRequestClientKey{}, &client))
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	response, err := c.d.doCatalogRequestWithTimeout(req, 20*time.Second)
	if err != nil {
		if ctx.Err() != nil {
			return "", 0, ctx.Err()
		}
		return "", 0, fmt.Errorf("%s接口网络请求失败，请检查网络或授权配置", c.p.name)
	}
	defer response.Body.Close()
	if response.Request != nil {
		c.jar.SetCookies(response.Request.URL, response.Cookies())
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, providerMaxBodyBytes+1))
	if err != nil {
		return "", response.StatusCode, err
	}
	if len(data) > providerMaxBodyBytes {
		return "", response.StatusCode, errors.New("站源响应过大")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", response.StatusCode, fmt.Errorf("%s接口 HTTP %d", c.p.name, response.StatusCode)
	}
	return string(data), response.StatusCode, nil
}

func (c *attachedClient) agent() string {
	if value := c.access.Headers["User-Agent"]; value != "" {
		return value
	}
	switch c.p.id {
	case "weiguan", "hema", "shanhai", "niuniudj", "xifan":
		return "okhttp/4.10.0"
	case "baidu":
		return "Dalvik/2.1.0 (Linux; U; Android 9) baiduboxapp/15.21.0.10"
	}
	return "Mozilla/5.0 (Linux; Android 13; Pixel 7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36"
}

func attachedDecode(body string) (any, error) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(strings.TrimPrefix(body, "\ufeff")))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil || value == nil {
		return nil, errors.New("站源未返回有效 JSON 数据")
	}
	if row, ok := value.(map[string]any); ok {
		if code, found := row["code"]; found {
			switch nativeText(code) {
			case "0", "200", "1", "ok", "0000":
			default:
				return nil, errors.New("站源接口暂不可用或需要有效授权")
			}
		}
	}
	return value, nil
}

func attachedObject(value any) map[string]any {
	row, _ := value.(map[string]any)
	return row
}

func attachedAt(value any, keys ...string) any {
	for _, key := range keys {
		value = attachedObject(value)[key]
	}
	return value
}

func attachedRows(value any) []map[string]any { return jsonVideoRows(value) }

func attachedInt(row map[string]any, keys ...string) int {
	value, _ := strconv.Atoi(mapString(row, keys...))
	return value
}

var attachedBlockedTitle = regexp.MustCompile(`偷拍|强奸|幼女|未成年|萝莉|乱伦|兽交`)

func (c *attachedClient) drama(row map[string]any, id string) Drama {
	if nested := attachedObject(row["theater"]); nested != nil {
		row = nested
	}
	if nested := attachedObject(row["drama"]); nested != nil {
		row = nested
	}
	if id == "" {
		id = mapString(row, "oneId", "one_id", "bookId", "theater_parent_id", "playlet_id", "playletId", "duanjuId", "duanjuID", "albumId", "collId", "play_id", "drama_id", "token", "id", "movieId", "albumid")
		if c.p.id == "xifan" && mapString(row, "source") != "" {
			id += "@" + mapString(row, "source")
		}
	}
	title := cleanText(mapString(row, "title", "bookName", "name", "playlet_title", "albumTitle", "movieName", "drama_name", "short_play_name"))
	if id == "" || title == "" || (c.p.id == "batvideo" || c.p.id == "md2048") && attachedBlockedTitle.MatchString(title) {
		return Drama{}
	}
	cover := mapString(row, "cover_n", "cover_url", "vertPoster", "horizonPoster", "coverWap", "coverImageUrl", "coverUrl", "playlet_poster", "image_link", "verticalImg", "poster_url", "cover", "img", "icon", "poster", "pic", "image")
	if cover != "" {
		cover = resolveProviderURL(c.p.base+"/", cover)
	}
	desc := cleanText(mapString(row, "introduction", "description", "introduce", "intro", "overview", "desc", "albumBrief"))
	count := attachedInt(row, "episodeCount", "episode_count", "episodes_num", "total_episode_num", "totalEpisodes", "total", "chapterNum", "total_num")
	return Drama{ID: providerDramaID(c.p.id, id), Source: c.p.id, SourceID: id, Title: title, Name: title,
		Cover: cover, CoverURL: cover, Desc: desc, Intro: desc, TotalEpisode: count, EpisodeCount: count,
		CategoryName: mapString(row, "classify", "type_name", "tags"), ChannelName: c.p.name,
		Remark: mapString(row, "remark", "updateStatus"), Heat: mapString(row, "hot_value", "heat"), Views: mapString(row, "viewCount")}
}

func attachedChapter(source, id, key string, order int, title, address, page string) Chapter {
	return Chapter{ID: providerChapterID(source, id, key), Source: source, Title: firstNonEmpty(title, fmt.Sprintf("第%d集", order)),
		CurrentEpisode: rawEpisode(order), VideoURL: address, PageURL: page}
}

func (d *Downloader) fetchAttachedCategories(ctx context.Context, source string) ([]nativeCategory, error) {
	p, ok := attachedProviderByID(source)
	if !ok {
		return nil, errors.New("请选择有效站源")
	}
	var values []nativeCategory
	for _, entry := range p.categories {
		values = append(values, nativeCategory{ID: entry.id, Name: entry.name})
	}
	return values, nil
}

func (d *Downloader) fetchAttachedCatalogPage(ctx context.Context, source string, page int, category, query string) ([]Drama, bool, error) {
	c, err := d.attachedClient(source)
	if err != nil {
		return nil, false, err
	}
	cat, ok := c.p.category(category)
	if !ok || page < 1 || page > 1000000 {
		return nil, false, errors.New("站源分类或页码无效")
	}
	if query != "" && !c.p.search {
		return nil, false, errors.New("此站源仅支持已加载目录内搜索")
	}
	if c.p.kind == "html" {
		return c.htmlCatalog(ctx, page, cat, query)
	}
	return c.apiCatalog(ctx, page, cat, query)
}

func (d *Downloader) fetchAttachedDetail(ctx context.Context, source, id string) (Drama, []Chapter, error) {
	c, err := d.attachedClient(source)
	if err != nil {
		return Drama{}, nil, err
	}
	if id == "" || len(id) > 512 || strings.ContainsAny(id, "\x00\r\n") {
		return Drama{}, nil, errors.New("站源剧集标识无效")
	}
	if c.p.kind == "html" {
		return c.htmlDetail(ctx, id)
	}
	return c.apiDetail(ctx, id)
}

func (d *Downloader) resolveAttachedMedia(ctx context.Context, task Task) (providerMedia, error) {
	source, id, ok := splitProviderDramaID(task.DramaID)
	if !ok || !isAttachedSource(source) || task.Chapter.Source != "" && canonicalProviderSource(task.Chapter.Source) != source {
		return providerMedia{}, errors.New("站源与播放章节不符")
	}
	_, chapters, err := d.fetchAttachedDetail(ctx, source, id)
	if err != nil {
		return providerMedia{}, err
	}
	c, _ := d.attachedClient(source)
	for _, chapter := range chapters {
		if chapter.ID != task.Chapter.ID {
			continue
		}
		key := strings.TrimPrefix(chapter.ID, providerDramaID(source, id)+":")
		media, err := c.play(ctx, id, key, chapter)
		if err != nil {
			return providerMedia{}, err
		}
		if media.Referer == "" && source != "damang" {
			media.Referer = c.p.base + "/"
		}
		if media.credentials == nil {
			media.credentials = &providerMediaCredentials{userAgent: c.agent(), referer: media.Referer}
		}
		return d.prepareWebProviderMedia(ctx, media, c.p.name)
	}
	return providerMedia{}, errors.New("此章节已调整，请刷新详情后重试")
}
