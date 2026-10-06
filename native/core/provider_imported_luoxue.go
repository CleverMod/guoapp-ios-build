package core

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func (c *attachedClient) luoxueAPI(ctx context.Context, values url.Values) (map[string]any, error) {
	data, err := c.importedJSON(ctx, "/api/proxy.php", values)
	if err != nil {
		return nil, err
	}
	row := attachedObject(data)
	if row["need_captcha"] == true {
		return nil, errors.New("洛雪TV 请求频率受限，需要源站验证，请稍后重试")
	}
	if row["success"] != true {
		return nil, errors.New("洛雪TV 接口暂不可用")
	}
	return row, nil
}

func (c *attachedClient) luoxueConfig(ctx context.Context) (map[string]any, error) {
	if c.imported.config != nil && time.Since(c.imported.configAt) < 10*time.Minute {
		return c.imported.config, nil
	}
	data, err := c.luoxueAPI(ctx, attachedParams("action", "config"))
	if err == nil {
		c.imported.config = data
		c.imported.configAt = time.Now()
	}
	return data, err
}

func luoxueTypeMatches(category, typ string) bool {
	for _, entry := range luoxueClassTypes[category] {
		if entry == typ {
			return true
		}
	}
	return category == ""
}

func (c *attachedClient) luoxueCatalog(ctx context.Context, page int, category, query string) ([]Drama, bool, error) {
	key := firstNonEmpty(query, "%")
	if c.imported.searchPools == nil {
		c.imported.searchPools = map[string]*importedPool{}
	}
	pool := c.imported.searchPools[key]
	ttl := 30 * time.Minute
	if query != "" {
		ttl = 3 * time.Minute
	}
	if pool == nil || time.Since(pool.updated) > ttl {
		pool = &importedPool{updated: time.Now()}
		if len(c.imported.searchPools) > 20 {
			c.imported.searchPools = map[string]*importedPool{}
		}
		c.imported.searchPools[key] = pool
	}
	collect := func() []map[string]any {
		rows := []map[string]any{}
		for _, row := range pool.rows {
			if query != "" || luoxueTypeMatches(category, mapString(row, "type")) {
				rows = append(rows, row)
			}
		}
		return rows
	}
	matched := collect()
	for len(matched) <= page*20 && !pool.done && pool.page < 50 {
		data, err := c.luoxueAPI(ctx, attachedParams("action", "search", "wd", key, "page", strconv.Itoa(pool.page+1)))
		if err != nil {
			return nil, false, err
		}
		rows := attachedRows(data["results"])
		seen := map[string]bool{}
		for _, row := range pool.rows {
			seen[mapString(row, "source")+"@"+mapString(row, "id")] = true
		}
		added := 0
		for _, row := range rows {
			id := mapString(row, "source") + "@" + mapString(row, "id")
			if !seen[id] && mapString(row, "id") != "" && mapString(row, "source") != "" {
				pool.rows = append(pool.rows, row)
				seen[id] = true
				added++
			}
		}
		pool.page++
		pool.updated = time.Now()
		if added == 0 || len(rows) == 0 {
			pool.done = true
		}
		matched = collect()
	}
	start := (page - 1) * 20
	if start >= len(matched) {
		return []Drama{}, false, nil
	}
	end := start + 20
	if end > len(matched) {
		end = len(matched)
	}
	rows := []Drama{}
	for _, row := range matched[start:end] {
		drama := c.importedDrama(row, mapString(row, "source")+"@"+mapString(row, "id"))
		if drama.ID != "" {
			rows = append(rows, drama)
		}
	}
	return rows, end < len(matched) || !pool.done && pool.page < 50, nil
}

func (c *attachedClient) luoxueDetail(ctx context.Context, id string) (Drama, []Chapter, error) {
	source, remote, ok := strings.Cut(id, "@")
	if !ok || source == "" || remote == "" {
		return Drama{}, nil, errors.New("洛雪影片标识无效")
	}
	data, err := c.luoxueAPI(ctx, attachedParams("action", "detail", "source", source, "id", remote))
	if err != nil {
		return Drama{}, nil, err
	}
	details := attachedRows(data["details"])
	if len(details) == 0 {
		return Drama{}, nil, errors.New("洛雪未返回影片详情")
	}
	row := details[0]
	if actual := mapString(row, "id"); actual != "" && actual != remote {
		return Drama{}, nil, errors.New("洛雪返回的影片标识不符")
	}
	row["id"] = id
	lines := []importedLine{}
	for _, group := range attachedRows(row["episodes"]) {
		name := firstNonEmpty(mapString(group, "group"), "线路")
		line := importedLine{key: source + "~" + name, name: name}
		for _, ep := range attachedRows(group["episodes"]) {
			line.episodes = append(line.episodes, importedEpisode{name: mapString(ep, "name"), url: mapString(ep, "url")})
		}
		if len(line.episodes) > 0 {
			lines = append(lines, line)
		}
	}
	return c.importedDetailResult(id, row, lines)
}

func importedMediaLike(address string) bool {
	parsed, err := url.Parse(address)
	if err != nil || !isProviderHTTPMediaURL(address) || parsed.User != nil {
		return false
	}
	path := strings.ToLower(parsed.Path)
	return strings.HasSuffix(path, ".m3u") || maccmsDirectMediaURL(address) != ""
}

func (c *attachedClient) luoxueBFQ(ctx context.Context, address string) (string, error) {
	escaped := strings.ReplaceAll(url.QueryEscape(address), "+", "%20")
	player := c.access.Settings["bfqPlayer"]
	referer := c.access.Settings["bfqReferer"]
	if player == "" || referer == "" {
		return "", errors.New("洛雪内置解析参数缺失")
	}
	body, err := c.importedRaw(ctx, http.MethodGet, player+escaped, "", "", map[string]string{"Referer": referer + escaped})
	if err != nil {
		return "", err
	}
	match := regexp.MustCompile(`result\s*=\s*"([^"]+)"`).FindStringSubmatch(body)
	if len(match) < 2 || len(match[1]) < 33 {
		return "", errors.New("洛雪解析未返回媒体")
	}
	value := match[1]
	n := len(value)
	plain, err := importedCBCText(value[:n-32], value[n-32:n-16], value[n-16:])
	if err != nil {
		return "", err
	}
	data, err := importedDecode(plain)
	if err != nil {
		return "", err
	}
	video := attachedAt(data, "video_info", "video")
	target := mapString(attachedObject(video), "url")
	if rows := attachedRows(video); len(rows) > 0 {
		target = mapString(rows[0], "url")
	}
	if !importedMediaLike(target) {
		return "", errors.New("洛雪解析未返回正片媒体")
	}
	return target, nil
}

func (c *attachedClient) luoxuePlay(ctx context.Context, p importedPayload) (providerMedia, error) {
	address := p.URL
	referer := ""
	if parsed, err := url.Parse(address); err == nil {
		referer = parsed.Scheme + "://" + parsed.Host + "/"
	}
	if importedMediaLike(address) {
		media, err := importedURLMedia(address, c.agent(), referer, nil)
		if err == nil && strings.HasSuffix(strings.ToLower(address), ".m3u") {
			return c.opaqueHLS(ctx, media)
		}
		return media, err
	}
	if target, err := c.luoxueBFQ(ctx, address); err == nil {
		return importedURLMedia(target, c.agent(), referer, nil)
	}
	config, err := c.luoxueConfig(ctx)
	if err != nil {
		return providerMedia{}, err
	}
	source, _, _ := strings.Cut(p.Player, "~")
	api := mapString(attachedObject(attachedAt(config, "sources", source)), "player_api")
	api = firstNonEmpty(api, mapString(config, "player_api"))
	if api == "" {
		return providerMedia{}, errors.New("洛雪此线路未提供内置解析接口")
	}
	return c.importedParser(ctx, api, address, c.p.base+"/")
}

func (c *attachedClient) importedParser(ctx context.Context, api, raw, referer string) (providerMedia, error) {
	target := strings.ReplaceAll(url.QueryEscape(raw), "+", "%20")
	if strings.Contains(api, "%s") {
		target = strings.ReplaceAll(api, "%s", target)
	} else {
		target = api + target
	}
	for step := 0; step < 3; step++ {
		body, err := c.importedRaw(ctx, http.MethodGet, target, "", "", map[string]string{"Referer": referer})
		if err != nil {
			return providerMedia{}, err
		}
		data, err := importedDecode(body)
		if err == nil {
			row := attachedObject(data)
			address := mapString(row, "url")
			headers := map[string]string{}
			if values := attachedObject(row["headers"]); values != nil {
				for k, v := range values {
					headers[k] = nativeText(v)
				}
			}
			if text := mapString(row, "headers"); text != "" {
				_ = jsonUnmarshalHeaders(text, headers)
			}
			if importedMediaLike(address) {
				return importedURLMedia(address, c.agent(), referer, headers)
			}
			if mapString(row, "type") == "url" && isProviderHTTPMediaURL(address) {
				target = address
				continue
			}
		}
		if address := attachedMediaURL.FindString(body); importedMediaLike(address) {
			return importedURLMedia(address, c.agent(), referer, nil)
		}
		break
	}
	return providerMedia{}, errors.New("站源解析接口未返回可播放媒体")
}
