package core

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed catpaw_sources.json
var catpawSourceManifest []byte

type catpawDefinition struct {
	ID, Name, Script, SHA256 string
	Helper, Existing         bool
}

var catpawDefinitions = func() []catpawDefinition {
	var values []catpawDefinition
	if json.Unmarshal(catpawSourceManifest, &values) != nil {
		panic("CatPaw 站源登记格式无效")
	}
	return values
}()

func init() {
	for _, entry := range catpawDefinitions {
		if !entry.Helper && !entry.Existing {
			attachedProviders = append(attachedProviders, attachedProvider{id: entry.ID, name: entry.Name, kind: "catpaw", search: true})
		}
	}
}

type catpawState struct {
	script    string
	base      string
	ext       map[string]any
	defaults  map[string]any
	constants map[string]string
	headers   map[string]string
	values    map[string]string
	config    map[string]any
	ready     time.Time
}

type catpawEpisode struct {
	Name, URL, ID, Token, Parser, ParseType, PlayerParse string
	Parses                                               []string
	Headers                                              map[string]string
	Index                                                int
}

const catpawMobileAgent = "Mozilla/5.0 (iPhone; CPU iPhone OS 13_2_3 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/13.0.3 Mobile/15E148 Safari/604.1"
const catpawDesktopAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36"

func (c *attachedClient) catpawLoad() error {
	if c.catpaw != nil {
		return nil
	}
	s := &catpawState{script: c.access.Settings["script"], ext: map[string]any{}, defaults: map[string]any{}, constants: map[string]string{}, headers: map[string]string{"Referer": ""}, values: map[string]string{}, config: map[string]any{}}
	defaults, defaultErr := catpawParseJSON(c.access.Settings["defaults"])
	s.defaults = attachedObject(defaults)
	if s.script == "" || json.Unmarshal([]byte(c.access.Settings["constants"]), &s.constants) != nil || defaultErr != nil || s.defaults == nil {
		return errors.New("CatPaw 内置协议配置缺失，请使用包含授权的新安装包")
	}
	for _, entry := range catpawDefinitions {
		if entry.ID == c.p.id && c.access.Settings["scriptSHA256"] != entry.SHA256 {
			return errors.New("CatPaw 内置授权与源协议版本不符")
		}
	}
	raw := c.access.Settings["extend"]
	if json.Unmarshal([]byte(raw), &s.ext) != nil || s.ext == nil {
		s.ext = map[string]any{}
		if strings.HasPrefix(raw, "http") {
			s.ext["host"] = raw
		}
	}
	s.base = firstNonEmpty(mapString(s.ext, "host", "api"), mapString(s.defaults, "host"))
	if strings.HasPrefix(s.base, "http") { s.base = strings.TrimRight(s.base, "/") }
	for key, value := range c.access.Headers {
		s.headers[key] = value
	}
	c.catpaw = s
	return nil
}

func (c *attachedClient) catpawInit(ctx context.Context) error {
	if err := c.catpawLoad(); err != nil {
		return err
	}
	if !c.catpaw.ready.IsZero() && time.Since(c.catpaw.ready) < 30*time.Minute {
		return nil
	}
	if err := c.catpawBootstrap(ctx); err != nil {
		c.catpaw.ready = time.Time{}
		return err
	}
	if !isProviderHTTPMediaURL(c.catpaw.base) {
		return errors.New("站源未返回有效接口地址")
	}
	c.catpaw.ready = time.Now()
	return nil
}

func (c *attachedClient) cpExt(keys ...string) string { return mapString(c.catpaw.ext, keys...) }
func (c *attachedClient) cpConst(key string) string   { return c.catpaw.constants[key] }
func (c *attachedClient) cpValue(key string) string   { return c.catpaw.values[key] }

func catpawBareHost(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Host != "" && (u.Path == "" || u.Path == "/") && u.RawQuery == ""
}

func catpawParseJSON(value string) (any, error) {
	var out any
	decoder := json.NewDecoder(strings.NewReader(strings.TrimPrefix(value, "\ufeff")))
	decoder.UseNumber()
	if decoder.Decode(&out) != nil || out == nil {
		return nil, errors.New("站源响应格式无效")
	}
	return out, nil
}

func catpawJSON(value any) string {
	var out strings.Builder
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(value) != nil { return "" }
	return strings.TrimSuffix(out.String(), "\n")
}

func catpawRows(value any) []map[string]any {
	if rows, ok := value.([]any); ok {
		out := make([]map[string]any, 0, len(rows))
		for _, item := range rows {
			if row := attachedObject(item); row != nil {
				out = append(out, row)
			}
		}
		return out
	}
	if row := attachedObject(value); row != nil {
		keys := make([]string, 0, len(row))
		for key := range row {
			keys = append(keys, key)
		}
		sort.SliceStable(keys, func(i, j int) bool {
			a, ae := strconv.Atoi(keys[i])
			b, be := strconv.Atoi(keys[j])
			if ae == nil && be == nil {
				return a < b
			}
			return keys[i] < keys[j]
		})
		out := []map[string]any{}
		for _, key := range keys {
			if item := attachedObject(row[key]); item != nil {
				out = append(out, item)
			}
		}
		return out
	}
	return nil
}

func catpawList(value any, keys ...string) []map[string]any {
	if row := attachedObject(value); row != nil {
		for _, key := range keys {
			if child, found := row[key]; found {
				return catpawRows(child)
			}
		}
	}
	return catpawRows(value)
}

func catpawStrings(value any) []string {
	switch v := value.(type) {
	case string:
		if v != "" {
			return []string{v}
		}
	case []any:
		out := []string{}
		for _, item := range v {
			if s := nativeText(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func (c *attachedClient) cpRaw(ctx context.Context, method, target, contentType, body string, extra map[string]string) (string, error) {
	if !isProviderHTTPMediaURL(target) {
		target = c.catpaw.base + target
	}
	headers := map[string]string{}
	targetURL, _ := url.Parse(target)
	baseURL, _ := url.Parse(c.catpaw.base)
	sameOrigin := baseURL != nil && targetURL != nil && providerMediaOrigin(baseURL) == providerMediaOrigin(targetURL)
	for key, value := range c.catpaw.headers {
		if !sameOrigin && !strings.EqualFold(key, "User-Agent") && !strings.EqualFold(key, "Accept") &&
			!strings.EqualFold(key, "Accept-Language") && !strings.EqualFold(key, "Cache-Control") && !strings.EqualFold(key, "Referer") {
			continue
		}
		headers[key] = value
	}
	for key, value := range extra {
		headers[key] = value
	}
	raw, status, err := c.raw(ctx, method, target, contentType, body, headers)
	if status == 401 || status == 403 {
		c.catpaw.ready = time.Time{}
	}
	for i := 0; i < 2 && strings.HasPrefix(strings.TrimSpace(raw), "\""); i++ {
		var text string
		if json.Unmarshal([]byte(raw), &text) != nil {
			break
		}
		raw = text
	}
	return raw, err
}

func (c *attachedClient) cpGet(ctx context.Context, path string, params url.Values) (any, error) {
	return c.cpRequest(ctx, http.MethodGet, path, params)
}
func (c *attachedClient) cpPost(ctx context.Context, path string, params url.Values) (any, error) {
	return c.cpRequest(ctx, http.MethodPost, path, params)
}
func (c *attachedClient) cpJSONPost(ctx context.Context, path string, payload any) (any, error) {
	body := ""
	if payload != nil {
		body = attachedJSON(payload)
	}
	raw, err := c.cpRaw(ctx, http.MethodPost, path, "application/json;charset=UTF-8", body, nil)
	if err != nil {
		return nil, err
	}
	return catpawParseJSON(raw)
}

func (c *attachedClient) cpDiscover(ctx context.Context, address string) (string, error) {
	raw, err := c.cpRaw(ctx, http.MethodGet, address, "", "", nil)
	if err != nil {
		return "", err
	}
	if value, e := catpawParseJSON(raw); e == nil {
		switch v := value.(type) {
		case string:
			raw = v
		case []any:
			if len(v) > 0 {
				raw = nativeText(v[0])
			}
		case map[string]any:
			raw = firstNonEmpty(mapString(v, "domain", "apiDomain", "host"), mapString(attachedObject(v["server"]), "url"))
		}
	}
	for _, line := range strings.Fields(strings.TrimSpace(raw)) {
		if isProviderHTTPMediaURL(line) {
			return strings.TrimRight(line, "/"), nil
		}
	}
	return "", errors.New("站源发布配置未返回有效接口")
}

func catpawVod(row map[string]any) map[string]any {
	return map[string]any{
		"vod_id":      mapString(row, "vod_id", "vodId", "id"),
		"vod_name":    mapString(row, "vod_name", "vodName", "name", "title", "Name"),
		"vod_pic":     mapString(row, "vod_pic", "vodPic", "pic", "cover", "img"),
		"vod_content": mapString(row, "vod_content", "vodContent", "vodBlurb", "vod_blurb", "content", "details", "intro", "introduce", "Description", "desc"),
		"vod_remarks": mapString(row, "vod_remarks", "vodRemarks", "vodRemark", "remarks", "remark", "vod_title", "Tag"),
		"type_name":   mapString(row, "type_name", "vod_class", "vodClass", "classify", "category", "class", "tags"),
		"vod_total":   attachedFirst(row, "vod_total", "episodeTotal", "serial", "total"),
	}
}

func (c *attachedClient) cpDramas(rows []map[string]any) []Drama {
	out := []Drama{}
	seen := map[string]bool{}
	for _, row := range rows {
		vod := catpawVod(row)
		pic := mapString(vod, "vod_pic")
		if strings.HasPrefix(pic, "mac://") {
			vod["vod_pic"] = "http://" + strings.TrimPrefix(pic, "mac://")
		} else if pic != "" {
			vod["vod_pic"] = resolveProviderURL(c.catpaw.base+"/", pic)
		}
		d := c.importedDrama(vod, "")
		if d.ID != "" && !seen[d.ID] {
			seen[d.ID] = true
			out = append(out, d)
		}
	}
	return out
}

func catpawMore(data any, page, count, limit int) bool {
	row := attachedObject(data)
	for _, key := range []string{"pagecount", "page_count", "total_pages", "totalPageCount", "totalpage"} {
		if n := attachedInt(row, key); n > 0 {
			return page < n
		}
	}
	if total := attachedInt(row, "total", "totalCount"); total > 0 {
		return page*limit < total
	}
	return count >= limit
}

func catpawLine(key, name string, episodes []catpawEpisode) importedLine {
	line := importedLine{key: key, name: firstNonEmpty(name, key)}
	for _, ep := range episodes {
		line.episodes = append(line.episodes, importedEpisode{name: ep.Name, url: ep.URL, series: attachedJSON(ep)})
	}
	return line
}

func catpawURLList(value string) []catpawEpisode {
	out := []catpawEpisode{}
	for i, item := range strings.Split(value, "#") {
		name, address, ok := strings.Cut(item, "$")
		if !ok {
			address, name = name, fmt.Sprintf("第%d集", i+1)
		}
		if address != "" {
			out = append(out, catpawEpisode{Name: name, URL: address})
		}
	}
	return out
}

func catpawMacLines(row map[string]any) []importedLine {
	lines := []importedLine{}
	froms := strings.Split(mapString(row, "vod_play_from", "play_from"), "$$$")
	for i, list := range strings.Split(mapString(row, "vod_play_url", "play_url"), "$$$") {
		key := strconv.Itoa(i + 1)
		if i < len(froms) && froms[i] != "" {
			key = froms[i]
		}
		lines = append(lines, catpawLine(key, key, catpawURLList(list)))
	}
	return lines
}

func catpawStructuredLines(value any) []importedLine {
	lines := []importedLine{}
	for i, row := range catpawRows(value) {
		player := attachedObject(row["player_info"])
		key := firstNonEmpty(mapString(row, "from", "code", "source_key", "sourceCode", "flag", "id"), mapString(player, "from"), strconv.Itoa(i+1))
		name := firstNonEmpty(mapString(row, "show", "sourceName", "title", "name"), mapString(player, "show"), key)
		parses := catpawStrings(row["parse_urls"])
		for _, field := range []string{"parse", "parse2"} {
			if v := mapString(player, field); isProviderHTTPMediaURL(v) {
				parses = append(parses, v)
			}
		}
		if v := mapString(row, "parse_api"); isProviderHTTPMediaURL(v) && attachedInt(row, "parse_secret") == 0 {
			parses = append(parses, v)
		}
		episodes := catpawURLList(mapString(row, "url", "urls"))
		for _, item := range catpawRows(attachedFirst(row, "urls", "players", "episode")) {
			episodes = append(episodes, catpawEpisode{Name: mapString(item, "name", "title"), URL: mapString(item, "url", "play_url"), Token: mapString(item, "token")})
		}
		for j := range episodes {
			episodes[j].Parses = parses
			episodes[j].Parser = mapString(player, "parse")
			episodes[j].ParseType = mapString(player, "parse_type")
			episodes[j].PlayerParse = mapString(player, "player_parse_type")
			episodes[j].Headers = map[string]string{"User-Agent": mapString(row, "ua"), "Referer": mapString(row, "referer")}
		}
		lines = append(lines, catpawLine(key, name, episodes))
	}
	return lines
}

func (c *attachedClient) cpDetailResult(id string, row map[string]any, lines []importedLine) (Drama, []Chapter, error) {
	vod := catpawVod(row)
	if remote := mapString(vod, "vod_id"); remote != "" && remote != id {
		return Drama{}, nil, errors.New("站源返回的影片标识不符")
	}
	vod["vod_id"] = id
	if pic := mapString(vod, "vod_pic"); pic != "" {
		vod["vod_pic"] = resolveProviderURL(c.catpaw.base+"/", pic)
	}
	drama := c.importedDrama(vod, id)
	chapters := []Chapter{}
	count := 0
	lineKeys := map[string]int{}
	for _, line := range lines {
		lineKeys[line.key]++
		identity := line.key
		if lineKeys[line.key] > 1 {
			identity += ":" + strconv.Itoa(lineKeys[line.key])
		}
		lineID := attachedSHA(identity)[:24]
		names := map[string]int{}
		size := 0
		for i, episode := range line.episodes {
			if episode.url == "" {
				continue
			}
			size++
			name := firstNonEmpty(cleanText(episode.name), fmt.Sprintf("第%d集", i+1))
			names[name]++
			identityName := name
			if names[name] > 1 {
				identityName += ":" + strconv.Itoa(names[name])
			}
			key := attachedSHA(identity + "\x00" + identityName)[:24]
			payload := attachedJSON(importedPayload{Player: line.key, URL: episode.url, Series: episode.series})
			chapter := attachedChapter(c.p.id, id, key, i+1, name, payload, "")
			chapter.LineID, chapter.LineName = lineID, truncate(cleanText(firstNonEmpty(line.name, line.key)), 128)
			chapters = append(chapters, chapter)
		}
		count = max(count, size)
	}
	if drama.ID == "" || len(chapters) == 0 {
		return Drama{}, nil, errors.New("站源未返回可用在线章节")
	}
	drama.TotalEpisode, drama.EpisodeCount = count, count
	return drama, chapters, nil
}

func (c *attachedClient) catpawCategories(ctx context.Context) ([]nativeCategory, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.catpawInit(ctx); err != nil {
		return nil, err
	}
	rows, err := c.catpawCategoryRows(ctx)
	if err != nil {
		return nil, err
	}
	out := []nativeCategory{}
	seen := map[string]bool{}
	for _, row := range rows {
		id := mapString(row, "type_id", "typeId", "list_id", "type_pid", "id", "cate", "1")
		name := mapString(row, "type_name", "typeName", "list_name", "name", "title", "2")
		if id != "" && name != "" && validNativeCategory(c.p.id, id) && !seen[id] {
			seen[id] = true
			out = append(out, nativeCategory{ID: id, Name: name})
		}
	}
	if len(out) == 0 {
		return nil, errors.New("站源未返回有效分类")
	}
	return out, nil
}

func (c *attachedClient) catpawCatalog(ctx context.Context, page int, category, query string) ([]Drama, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.catpawInit(ctx); err != nil {
		return nil, false, err
	}
	return c.catpawCatalogPage(ctx, page, category, query)
}
func (c *attachedClient) catpawDetail(ctx context.Context, id string) (Drama, []Chapter, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.catpawInit(ctx); err != nil {
		return Drama{}, nil, err
	}
	return c.catpawDetailPage(ctx, id)
}
func (c *attachedClient) catpawPlay(ctx context.Context, id string, chapter Chapter) (providerMedia, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.catpawInit(ctx); err != nil {
		return providerMedia{}, err
	}
	var payload importedPayload
	if json.Unmarshal([]byte(chapter.VideoURL), &payload) != nil {
		return providerMedia{}, errors.New("站源章节格式无效")
	}
	var ep catpawEpisode
	if json.Unmarshal([]byte(payload.Series), &ep) != nil {
		return providerMedia{}, errors.New("站源线路参数无效")
	}
	return c.catpawPlayEpisode(ctx, id, payload.Player, ep)
}

func catpawCBCDecode(value, key, iv string, hexadecimal bool) (string, error) {
	var data []byte
	var err error
	if hexadecimal {
		data, err = hex.DecodeString(value)
	} else {
		data, err = importedUnbase64(value)
	}
	if err != nil || len(iv) != 16 {
		return "", errors.New("站源加密数据无效")
	}
	plain, err := aesCBCDecrypt(data, []byte(key), []byte(iv))
	return string(plain), err
}
func catpawCBCEncode(value, key, iv string) (string, error) {
	if len(iv) != 16 {
		return "", errors.New("站源加密参数缺失")
	}
	data, err := aesCBCEncrypt([]byte(value), []byte(key), []byte(iv))
	return base64.StdEncoding.EncodeToString(data), err
}
