package core

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func catpawURLMedia(address string, headers map[string]string) (providerMedia, error) {
	media, err := importedURLMedia(address, catpawMobileAgent, "", headers)
	if err != nil {
		return providerMedia{}, err
	}
	media.credentials.headers = map[string]string{}
	for key, value := range headers {
		if value != "" && !strings.EqualFold(key, "User-Agent") && !strings.EqualFold(key, "Referer") && !strings.EqualFold(key, "Cookie") {
			media.credentials.headers[key] = value
		}
	}
	return media, nil
}

func (c *attachedClient) catpawPlayEpisode(ctx context.Context, id, line string, ep catpawEpisode) (providerMedia, error) {
	target := ep.URL
	headers := map[string]string{"User-Agent": catpawMobileAgent}
	for key, value := range ep.Headers {
		if value != "" {
			headers[key] = value
		}
	}
	var data any
	var err error
	s := c.catpaw
	switch s.script {
	case "八戒影视.py":
		payload := map[string]any{"id": id, "playerId": line, "source": 0, "typeId": "M16", "userId": c.cpValue("userID")}
		if ep.Parser == "virtual" {
			payload["episodeIndex"] = strconv.Itoa(ep.Index)
		} else {
			payload["episodeId"] = ep.ID
		}
		data, err = c.cpJSONPost(ctx, "/api/v1/app/play/movieDetails", payload)
		if err != nil {
			break
		}
		row := attachedObject(attachedAt(data, "data"))
		data, err = c.cpGet(ctx, "/api/v1/app/play/analysisMovieUrl", attachedParams("playerUrl", mapString(row, "url"), "playerId", mapString(row, "playerId")))
		target = mapString(attachedObject(data), "data")
	case "独播库.py":
		data, err = c.cpDuboku(ctx, target, nil)
		target = catpawDubokuDecode(mapString(attachedObject(data), "HId"))
		headers = map[string]string{"User-Agent": catpawDesktopAgent, "Origin": "https://w.duboku.io", "Referer": "https://w.duboku.io/", "priority": "u=1, i"}
	case "新浪资源.py":
		if maccmsDirectMediaURL(target) == "" {
			target, headers, err = c.cpParse(ctx, "", target)
		}
	case "壹影视.py":
		headers["User-Agent"] = firstNonEmpty(s.headers["User-Agent"], "Android/OkHttp")
		data, err = c.cpPost(ctx, "/vod-app/vod/playUrl", attachedParams("sourceCode", line, "urlEncode", target))
		if err == nil {
			row := attachedObject(attachedAt(data, "data"))
			if address := mapString(row, "url"); isProviderHTTPMediaURL(address) {
				for key, value := range catpawParserHeaders(row) {
					headers[key] = value
				}
				return c.cpYiMedia(ctx, address, headers)
			}
		}
	case "cycapp.py":
		headers["User-Agent"] = "libmpv"
		parse := ep.ParseType == "1"
		for _, prefix := range strings.Split(c.cpExt("jxPrefix"), ",") {
			if prefix != "" && strings.HasPrefix(target, prefix) {
				parse = true
			}
		}
		if parse {
			parsed, h, e := c.cpParse(ctx, "", target)
			if e == nil {
				target = parsed
				for k, v := range h {
					headers[k] = v
				}
			} else {
				err = e
			}
		}
	case "美剧侠.py":
		headers["User-Agent"] = "com.jubaotaige.jubaotaigeapp/2.3.2 (Linux;Android 12) ExoPlayerLib/2.14.2"
		if strings.Contains(target, "url=") && !strings.Contains(target, "nkvod.com") {
			parsed, h, e := c.cpParse(ctx, "", target)
			if e == nil {
				target = parsed
				for k, v := range h {
					headers[k] = v
				}
			}
		}
	case "RJAPP.py", "AppV2.py":
		if len(ep.Parses) > 0 {
			target, headers, err = c.cpParseMany(ctx, ep.Parses, target, headers)
		}
	case "XinJie.py":
		headers["User-Agent"] = firstNonEmpty(c.cpExt("playua"), "Dalvik/2.1.0 (Linux; U; Android 15; Xiaomi 15 Pro Build/AP2A.240905.003)")
		if c.cpValue("jiexi") != "" {
			data, err = c.cpGet(ctx, "/admin/jiexi.php", attachedParams("url", target, "source", line))
			if err == nil {
				target = firstNonEmpty(mapString(attachedObject(data), "url"), mapString(attachedObject(attachedAt(data, "data")), "url"))
				if agent := mapString(attachedObject(data), "UA"); agent != "" {
					headers["User-Agent"] = agent
				}
			}
		}
	case "Appfox.py":
		parsers := []string{}
		for _, parser := range catpawRows(s.config["jiexiDataList"]) {
			if mapString(parser, "playerCode") == line {
				parsers = append(parsers, mapString(parser, "url"))
			}
		}
		custom := attachedObject(s.ext["parse"])
		customParsers := []string{}
		for key, value := range custom {
			if strings.Contains(key, line) {
				customParsers = append(customParsers, catpawStrings(value)...)
			}
		}
		if c.cpExt("custom_first") == "1" {
			parsers = append(customParsers, parsers...)
		} else {
			parsers = append(parsers, customParsers...)
		}
		if len(parsers) > 0 && maccmsDirectMediaURL(target) == "" {
			target, headers, err = c.cpParseMany(ctx, parsers, target, headers)
		}
	case "FeiApp.py":
		data, err = c.cpGet(ctx, "/api.php", attachedParams("type", "jx", "vodurl", target, "vodid", id))
		if err == nil {
			target = firstNonEmpty(mapString(attachedObject(data), "url"), target)
		}
		headers["User-Agent"] = "Dalvik/2.1.0 (Linux; U; Android 14; Xiaomi 15 Build/SQ3A.220705.004)"
	case "ApptoV5无加密.py":
		for _, config := range catpawRows(attachedAt(s.config, "get_parsing", "lists")) {
			if mapString(config, "key") != line {
				continue
			}
			for _, parser := range catpawRows(config["config"]) {
				if mapString(parser, "type") != "json" {
					continue
				}
				data, err = c.cpPost(ctx, "/apptov5/v1/parsing/proxy", attachedParams("play_url", target, "label", mapString(parser, "label"), "key", line, "__platform", "android"))
				if err == nil {
					row := attachedObject(attachedAt(data, "data"))
					if address := mapString(row, "url"); isProviderHTTPMediaURL(address) {
						target = address
						headers["User-Agent"] = firstNonEmpty(mapString(row, "UA", "UserAgent"), headers["User-Agent"])
						break
					}
				}
			}
		}
	case "AppMuou.py":
		target, headers, err = c.cpMuouPlay(ctx, line, target, headers)
	case "skapp.py":
		direct := false
		for _, prefix := range strings.Split(mapString(s.config, "direct_link"), "|") {
			if prefix != "" && strings.Contains(target, prefix) {
				direct = true
			}
		}
		for _, prefix := range strings.Split(mapString(s.config, "direct_json_link"), "|") {
			if prefix != "" && strings.Contains(target, prefix) {
				direct = false
			}
		}
		if !direct {
			data, err = c.cpGet(ctx, "/sk-api/vod/skjson", attachedParams("url", target, "skjsonindex", "0"))
			if err == nil {
				target = firstNonEmpty(mapString(attachedObject(attachedAt(data, "data")), "url"), target)
			}
		}
	case "getapp3.4.6.py":
		headers["User-Agent"] = "Dalvik/2.1.0 (Linux; U; Android 14; 23113RK12C Build/SKQ1.231004.001)"
		switch {
		case ep.ParseType == "0":
		case ep.ParseType == "2":
			target, headers, err = c.cpParse(ctx, ep.Parser, target)
		case ep.PlayerParse == "2":
			target, headers, err = c.cpParse(ctx, ep.Parser, target)
		default:
			encrypted, e := catpawCBCEncode(target, c.cpValue("dataKey"), c.cpValue("dataIV"))
			if e != nil {
				return providerMedia{}, e
			}
			data, err = c.cpPost(ctx, c.cpValue("api")+".index/vodParse", attachedParams("parse_api", ep.Parser, "url", encrypted, c.cpConst("const5"), ep.PlayerParse, "token", ep.Token))
			if err == nil {
				decoded, e := catpawParseJSON(mapString(attachedObject(data), "json"))
				if e != nil {
					err = e
				} else {
					target = mapString(attachedObject(decoded), "url")
				}
			}
		}
	case "Hmys.py":
		return c.cpHmysPlay(ctx, id, ep)
	case "99APP2.py":
		target, headers, err = c.cp99Play(ctx, line, target)
	case "开端.py":
		headers["User-Agent"] = "ijkplayer/1.0.0 (Linux;Android 11) ExoPlayerLib/2.14.1"
	case "247看.py":
		parsers := []string{}
		for key, value := range attachedObject(s.ext["parse"]) {
			if strings.Contains(key, line) {
				parsers = append(parsers, catpawStrings(value)...)
			}
		}
		if len(parsers) > 0 {
			target, headers, err = c.cpParseMany(ctx, parsers, target, headers)
		}
	}
	if ctx.Err() != nil {
		return providerMedia{}, ctx.Err()
	}
	if err == nil && isProviderHTTPMediaURL(target) && (target != ep.URL ||
		s.script == "开端.py" || s.script == "cycapp.py" && ep.ParseType != "1" ||
		s.script == "getapp3.4.6.py" && ep.ParseType == "0") {
		return catpawURLMedia(target, headers)
	}
	if err != nil && maccmsDirectMediaURL(ep.URL) != "" {
		target, err = ep.URL, nil
	}
	if err != nil || maccmsDirectMediaURL(target) == "" {
		if target == "" {
			target = ep.URL
		}
		parsed, h, e := c.cpFallback(ctx, target)
		if e != nil {
			if err != nil {
				return providerMedia{}, err
			}
			return providerMedia{}, e
		}
		target = parsed
		for key, value := range h {
			headers[key] = value
		}
	}
	return catpawURLMedia(target, headers)
}

func (c *attachedClient) cpMuouPlay(ctx context.Context, line, target string, headers map[string]string) (string, map[string]string, error) {
	if c.catpaw.config["players"] == nil {
		data, err := c.cpGet(ctx, attachedPath(c.cpValue("appHost")+"/api.php", attachedParams("action", "playerinfo")), nil)
		if err == nil {
			c.catpaw.config["players"] = attachedAt(data, "data")
		}
	}
	config := attachedObject(c.catpaw.config["players"])
	for _, player := range catpawRows(config["playerinfo"]) {
		parser := mapString(player, "playerjiekou")
		if mapString(player, "playername") != line || !isProviderHTTPMediaURL(parser) {
			continue
		}
		address, h, err := c.cpParse(ctx, parser, target+"&playerkey="+url.QueryEscape(line))
		if err != nil {
			continue
		}
		for _, ua := range catpawRows(config["playerua"]) {
			if mapString(ua, "player") != line {
				continue
			}
			pattern, e := regexp.Compile(mapString(ua, "matching"))
			if e == nil && pattern.MatchString(address) {
				_ = jsonUnmarshalHeaders(mapString(ua, "playerua"), h)
			}
		}
		for key, value := range h {
			headers[key] = value
		}
		return address, headers, nil
	}
	if maccmsDirectMediaURL(target) != "" {
		return target, headers, nil
	}
	if parser := c.cpValue("jxAPI"); isProviderHTTPMediaURL(parser) {
		return c.cpParseMany(ctx, []string{parser}, target, headers)
	}
	return target, headers, errors.New("站源没有返回有效解析线路")
}

func (c *attachedClient) cp99Play(ctx context.Context, line, target string) (string, map[string]string, error) {
	player := attachedObject(attachedAt(c.catpaw.config, "player", line))
	headers := map[string]string{"User-Agent": "Lavf/58.12.100"}
	_ = jsonUnmarshalHeaders(mapString(player, "headers"), headers)
	if mapString(player, "type") == "0" {
		return target, headers, nil
	}
	allowed := strings.Split(mapString(player, "parseUrl"), ",")
	web := []string{}
	for _, parser := range catpawRows(c.catpaw.config["parser_api"]) {
		if len(allowed) > 0 && allowed[0] != "" {
			found := false
			for _, id := range allowed {
				found = found || id == mapString(parser, "id")
			}
			if !found {
				continue
			}
		}
		filter := mapString(parser, c.cpConst("const6"))
		if filter != "" {
			found := false
			for _, value := range strings.Split(filter, ",") {
				found = found || value == line
			}
			if !found {
				continue
			}
		}
		api := mapString(parser, "api_url")
		if mapString(parser, "api_type") == "webview" {
			web = append(web, api)
			continue
		}
		if mapString(parser, c.cpConst("const9")) == "1" {
			idValue, err := strconv.Atoi(mapString(parser, "id"))
			if err != nil {
				continue
			}
			data, err := c.cp99Call(ctx, "/app/vodParser", map[string]any{"id": idValue, "url": target})
			if err == nil {
				if address := mapString(attachedObject(data), "data"); isProviderHTTPMediaURL(address) {
					return address, headers, nil
				}
			}
		} else if api != "" {
			address, h, err := c.cpParse(ctx, api, target)
			if err == nil {
				for k, v := range h {
					headers[k] = v
				}
				return address, headers, nil
			}
		}
	}
	if len(web) > 0 {
		return c.cpParseMany(ctx, web, target, headers)
	}
	return target, headers, errors.New("99APP 未返回可用解析结果")
}

func (c *attachedClient) cpHmysPlay(ctx context.Context, id string, ep catpawEpisode) (providerMedia, error) {
	data, err := c.cpPost(ctx, "/api/vod/play_url", attachedParams("xz", "0", "vod_map_id", ep.ID, "vod_id", id, "collection", ep.Token))
	if err != nil {
		return providerMedia{}, err
	}
	row := attachedObject(attachedAt(data, "result"))
	if check := mapString(row, "check_url"); isProviderHTTPMediaURL(check) {
		return catpawURLMedia(check, map[string]string{"User-Agent": "Mozi"})
	}
	target := mapString(row, "vod_url")
	ck := mapString(row, "ck")
	if decoded, e := base64.StdEncoding.DecodeString(ck); e == nil {
		ck = string(decoded)
	}
	media, err := catpawURLMedia(target, map[string]string{"User-Agent": "Mozi"})
	if err != nil {
		return providerMedia{}, err
	}
	playDomain := c.cpValue("playDomain")
	encryptDomain := firstNonEmpty(c.cpExt("EncryptDomain"), c.cpConst("const1"))
	media.credentials.rewrite = func(address *url.URL) {
		if address.RawQuery == "" {
			address.RawQuery = ck
		} else if ck != "" && !strings.Contains(address.RawQuery, ck) {
			address.RawQuery += "&" + ck
		}
		params := address.Query()
		params.Del("wsSecret")
		params.Del("wsTime")
		clean := *address
		clean.RawQuery = ""
		clean.Fragment = ""
		t := strconv.FormatInt(time.Now().Unix(), 16)
		params.Set("wsSecret", attachedMD5(strings.ReplaceAll(clean.String(), playDomain, encryptDomain)+t))
		params.Set("wsTime", t)
		address.RawQuery = params.Encode()
	}
	return media, nil
}
