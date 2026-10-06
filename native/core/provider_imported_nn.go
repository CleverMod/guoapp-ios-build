package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (c *attachedClient) nnHosts(ctx context.Context) []string {
	if len(c.imported.hosts) > 0 {
		return c.imported.hosts
	}
	var discovery, hosts []string
	_ = json.Unmarshal([]byte(c.access.Settings["discoveryURLs"]), &discovery)
	for _, address := range discovery {
		body, err := c.importedRaw(ctx, http.MethodGet, address, "", "", map[string]string{"Referer": ""})
		if err != nil {
			continue
		}
		ciphertext, err := importedUnbase64(body)
		if err != nil {
			continue
		}
		plain, err := importedECBDecrypt(ciphertext, []byte(c.access.Settings["discoveryKey"]))
		if err != nil {
			continue
		}
		for _, line := range strings.Fields(string(plain)) {
			if isProviderHTTPMediaURL(line) {
				hosts = append(hosts, strings.TrimRight(line, "/"))
			}
		}
		if len(hosts) > 0 {
			break
		}
	}
	var fallback []string
	_ = json.Unmarshal([]byte(c.access.Settings["hosts"]), &fallback)
	hosts = append(hosts, fallback...)
	if len(hosts) == 0 {
		hosts = []string{c.p.base}
	}
	c.imported.hosts = hosts
	return hosts
}

func (c *attachedClient) nnAPI(ctx context.Context, path string, values url.Values) (any, error) {
	target := attachedPath(path, values)
	hosts := c.nnHosts(ctx)
	if c.imported.host != "" {
		hosts = append([]string{c.imported.host}, hosts...)
	}
	headers := map[string]string{"p": "android", "pkg": "com.qingbian.jz", "t": "", "d": c.device[:16], "v": "1.6.3", "y": "0", "product": "Pixel", "os": "13", "Referer": ""}
	var last error
	seen := map[string]bool{}
	for _, host := range hosts {
		if seen[host] {
			continue
		}
		seen[host] = true
		body, err := c.importedRaw(ctx, http.MethodGet, host+target, "", "", headers)
		if err != nil {
			last = err
			continue
		}
		data, err := importedDecode(body)
		if err != nil {
			key := []byte(target)
			if len(key) < 16 {
				key = append(key, []byte(strings.Repeat("0", 16-len(key)))...)
			}
			key = key[:16]
			if cipher, e := importedUnbase64(body); e == nil {
				if plain, e := importedECBDecrypt(cipher, key); e == nil {
					data, err = importedDecode(string(plain))
				}
			}
		}
		if err == nil && attachedObject(data)["data"] != nil {
			c.imported.host = host
			return attachedAt(data, "data"), nil
		}
		last = errors.New("牛牛视频接口未返回可解密数据")
	}
	if last == nil {
		last = errors.New("牛牛视频接口不可用")
	}
	return nil, last
}

func (c *attachedClient) nnConfig(ctx context.Context) (map[string]any, error) {
	if c.imported.config != nil && time.Since(c.imported.configAt) < 10*time.Minute {
		return c.imported.config, nil
	}
	data, err := c.nnAPI(ctx, "/config", nil)
	if err != nil {
		return nil, err
	}
	row := attachedObject(data)
	if row == nil {
		return nil, errors.New("牛牛视频配置无效")
	}
	c.imported.config = row
	c.imported.configAt = time.Now()
	return row, nil
}

func (c *attachedClient) nnDetail(ctx context.Context, id string) (Drama, []Chapter, error) {
	data, err := c.nnAPI(ctx, "/detail", attachedParams("vod_id", id))
	if err != nil {
		return Drama{}, nil, err
	}
	row := attachedObject(data)
	config, _ := c.nnConfig(ctx)
	aliases := map[string]string{}
	_ = json.Unmarshal([]byte(c.access.Settings["playerAliases"]), &aliases)
	for _, parser := range attachedRows(config["parser"]) {
		aliases[mapString(parser, "player_id")] = mapString(parser, "player_name")
	}
	groups := attachedRows(row["sources"])
	sort.SliceStable(groups, func(i, j int) bool { return attachedInt(groups[i], "prio") < attachedInt(groups[j], "prio") })
	lines := []importedLine{}
	for _, group := range groups {
		key := mapString(group, "player_id")
		line := importedLine{key: key, name: firstNonEmpty(aliases[key], key)}
		for _, ep := range attachedRows(group["episodes"]) {
			line.episodes = append(line.episodes, importedEpisode{name: mapString(ep, "name"), url: mapString(ep, "url")})
		}
		if key != "" && len(line.episodes) > 0 {
			lines = append(lines, line)
		}
	}
	return c.importedDetailResult(id, row, lines)
}

func (c *attachedClient) nnXCConfig(ctx context.Context) (map[string]any, error) {
	conf := map[string]any{}
	if json.Unmarshal([]byte(c.access.Settings["xcConfig"]), &conf) != nil {
		return nil, errors.New("牛牛视频内置播放授权参数缺失")
	}
	if cfg, err := c.nnConfig(ctx); err == nil {
		src := attachedObject(cfg["src7"])
		for _, key := range []string{"tokenUrl", "listUrl", "detailUrl", "key", "iv", "salt"} {
			if value := mapString(src, key); value != "" {
				conf[key] = value
			}
		}
		headers := map[string]any{}
		for _, header := range attachedRows(src["headers"]) {
			if key := mapString(header, "key"); key != "" {
				headers[key] = mapString(header, "value")
			}
		}
		if len(headers) > 0 {
			conf["headers"] = headers
		}
	}
	return conf, nil
}

func (c *attachedClient) nnXCPost(ctx context.Context, target string, form url.Values, conf map[string]any, token string) (any, error) {
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	device := c.device[:16]
	headers := map[string]string{}
	for key, value := range attachedObject(conf["headers"]) {
		headers[key] = nativeText(value)
	}
	for key, value := range map[string]string{"log-header": "I am the log request header.", "cur_time": ts, "device_id": device, "mob_mfr": "google", "mobmodel": "Pixel", "sys_platform": "2", "sign": strings.ToUpper(attachedMD5(mapString(conf, "salt") + device + ts)), "token": token, "Referer": ""} {
		headers[key] = value
	}
	body, err := c.importedRaw(ctx, http.MethodPost, target, "application/x-www-form-urlencoded", form.Encode(), headers)
	if err != nil {
		return nil, err
	}
	data, err := importedDecode(body)
	if err != nil {
		plain, e := importedCBCText(body, mapString(conf, "key"), mapString(conf, "iv"))
		if e != nil {
			return nil, errors.New("牛牛视频播放授权响应解密失败")
		}
		data, err = importedDecode(plain)
	}
	return data, err
}

func (c *attachedClient) nnXCPlay(ctx context.Context, raw string) (providerMedia, error) {
	parts := strings.Split(raw, "@")
	if len(parts) < 2 {
		return providerMedia{}, errors.New("牛牛视频章节标识无效")
	}
	conf, err := c.nnXCConfig(ctx)
	if err != nil {
		return providerMedia{}, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		if c.token == "" || attempt > 0 || !time.Now().Before(c.expiry) {
			data, e := c.nnXCPost(ctx, mapString(conf, "tokenUrl"), attachedParams("invited_by", "", "is_install", "1"), conf, "")
			if e != nil {
				return providerMedia{}, e
			}
			c.token = mapString(attachedObject(attachedAt(data, "result", "user_info")), "token")
			c.expiry = time.Now().Add(30 * time.Minute)
		}
		if c.token == "" {
			return providerMedia{}, errors.New("牛牛视频游客播放授权未返回")
		}
		data, e := c.nnXCPost(ctx, mapString(conf, "listUrl"), attachedParams("vod_id", parts[0]), conf, c.token)
		if e != nil {
			return providerMedia{}, e
		}
		var episode map[string]any
		for _, ep := range attachedRows(attachedAt(data, "result", "vod_collection")) {
			if mapString(ep, "collection") == parts[1] {
				episode = ep
				break
			}
		}
		if episode == nil {
			return providerMedia{}, errors.New("牛牛视频未返回所选章节")
		}
		values := attachedParams("collection_id", mapString(episode, "id"), "sig", "", "nc_token", "", "code", "", "phone", "", "vod_id", parts[0], "session_id", "", "vod_token", mapString(episode, "vod_token"), "cur_time", firstNonEmpty(mapString(episode, "cur_time"), strconv.FormatInt(time.Now().Unix(), 10)))
		data, e = c.nnXCPost(ctx, mapString(conf, "detailUrl"), values, conf, c.token)
		if e != nil {
			continue
		}
		address := mapString(attachedObject(attachedAt(data, "result")), "vod_url")
		if isProviderHTTPMediaURL(address) {
			return importedURLMedia(address, c.agent(), "", nil)
		}
	}
	return providerMedia{}, errors.New("牛牛视频此章节需要有效源站授权")
}

func (c *attachedClient) nnPlay(ctx context.Context, p importedPayload) (providerMedia, error) {
	if importedMediaLike(p.URL) {
		return importedURLMedia(p.URL, c.agent(), "", nil)
	}
	if (p.Player == "xiaocao" || p.Player == "xm3u8") && strings.Contains(p.URL, "@") {
		if media, err := c.nnXCPlay(ctx, p.URL); err == nil {
			return media, nil
		}
	}
	config, err := c.nnConfig(ctx)
	if err != nil {
		return providerMedia{}, err
	}
	parser := map[string]any{}
	for _, row := range attachedRows(config["parser"]) {
		if mapString(row, "player_id") == p.Player {
			parser = row
			break
		}
	}
	for _, rule := range strings.Split(mapString(parser, "no_parse_rule"), ",") {
		if rule != "" && strings.Contains(strings.ToLower(p.URL), strings.ToLower(strings.TrimSpace(rule))) && isProviderHTTPMediaURL(p.URL) {
			media, e := importedURLMedia(p.URL, c.agent(), "", nil)
			if e == nil {
				return c.opaqueHLS(ctx, media)
			}
		}
	}
	templates := []string{}
	backend := map[string]string{"madou": "src4", "paopao": "src11"}[p.Player]
	if backend != "" {
		templates = append(templates, mapString(attachedObject(config[backend]), "yUrl"))
		fallback := map[string]string{}
		_ = json.Unmarshal([]byte(c.access.Settings["backends"]), &fallback)
		templates = append(templates, fallback[backend])
	}
	templates = append(templates, mapString(parser, "url"))
	if strings.Contains(p.URL, "@") {
		templates = append(templates, firstNonEmpty(mapString(attachedObject(config["src10"]), "yUrl"), c.access.Settings["zhenxiangURL"]), firstNonEmpty(mapString(attachedObject(config["src8"]), "yUrl"), c.access.Settings["sjURL"]))
	}
	for _, api := range templates {
		if api == "" {
			continue
		}
		if media, e := c.importedParser(ctx, api, p.URL, ""); e == nil {
			return media, nil
		}
	}
	return providerMedia{}, errors.New("牛牛视频此线路未返回可播放媒体或需要外部播放器解析")
}
