package core

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

func (c *attachedClient) catpawDetailPage(ctx context.Context, id string) (Drama, []Chapter, error) {
	var data any
	var row map[string]any
	var lines []importedLine
	var err error
	s := c.catpaw
	switch s.script {
	case "八戒影视.py":
		data, err = c.cpJSONPost(ctx, "/api/v1/app/play/movieDetails", map[string]any{"id": id, "source": 0, "typeId": "M17", "userId": c.cpValue("userID")})
		if err != nil {
			break
		}
		play := attachedObject(attachedAt(data, "data"))
		current := mapString(play, "playerId")
		players := catpawRows(play["moviePlayerList"])
		for _, player := range players {
			key := mapString(player, "id")
			episodes := []catpawEpisode{}
			if key == current {
				for i, item := range catpawRows(play["episodeList"]) {
					episodeID := mapString(item, "id")
					episodes = append(episodes, catpawEpisode{Name: mapString(item, "episode"), URL: "bajie:" + episodeID, ID: episodeID, Index: i})
				}
			} else {
				count := attachedInt(player, "episodeTotal")
				if count > 0 && count <= 2000 {
					for i := 0; i < count; i++ {
						episodes = append(episodes, catpawEpisode{Name: "第" + strconv.Itoa(i+1) + "集", URL: "bajie_virtual:" + strconv.Itoa(i), Parser: "virtual", Index: i})
					}
				}
			}
			lines = append(lines, catpawLine(key, mapString(player, "moviePlayerName"), episodes))
		}
		data, err = c.cpJSONPost(ctx, "/api/v1/app/play/movieDesc", map[string]any{"id": id, "typeId": "M17"})
		row = attachedObject(attachedAt(data, "data"))
	case "独播库.py":
		data, err = c.cpDuboku(ctx, id, nil)
		if err != nil {
			break
		}
		row = attachedObject(data)
		episodes := []catpawEpisode{}
		for _, item := range catpawRows(row["Playlist"]) {
			episodes = append(episodes, catpawEpisode{Name: mapString(item, "EpisodeName"), URL: catpawDubokuDecode(mapString(item, "VId"))})
		}
		row["vod_name"] = row["Name"]
		row["vod_pic"] = catpawDubokuDecode(mapString(row, "TnId"))
		lines = []importedLine{catpawLine("duboku", "独播库", episodes)}
	case "新浪资源.py":
		return c.cpXinlangDetail(ctx, id)
	case "壹影视.py":
		data, err = c.cpPost(ctx, "/vod-app/vod/info", attachedParams("tid", "", "vodId", id))
		row = attachedObject(attachedAt(data, "data"))
		sources := catpawRows(row["vodSources"])
		sort.SliceStable(sources, func(i, j int) bool { return attachedInt(sources[i], "sort") < attachedInt(sources[j], "sort") })
		for _, source := range sources {
			episodes := []catpawEpisode{}
			for _, item := range catpawRows(attachedAt(source, "vodPlayList", "urls")) {
				episodes = append(episodes, catpawEpisode{Name: mapString(item, "name"), URL: mapString(item, "url")})
			}
			lines = append(lines, catpawLine(mapString(source, "sourceCode"), mapString(source, "sourceName"), episodes))
		}
	case "cycapp.py":
		data, err = c.cpCycRequest(ctx, "/v2/video/info/"+url.PathEscape(id), nil)
		if err != nil {
			break
		}
		detail := attachedObject(attachedFirst(attachedObject(data), "data", "3"))
		row = catpawCycVod(detail, true)
		for _, source := range catpawObjects(attachedFirst(detail, "vod_play_from", "24", "20")) {
			key := mapString(source, "code", "1")
			name := firstNonEmpty(mapString(source, "name", "2"), key)
			var list any
			list, err = c.cpCycRequest(ctx, "/video/play_url", attachedParams("id", id, "from", key, "index", "-1"))
			if err != nil {
				break
			}
			episodes := []catpawEpisode{}
			for _, item := range catpawObjects(attachedFirst(attachedObject(list), "data", "3")) {
				episodes = append(episodes, catpawEpisode{Name: mapString(item, "name", "1"), URL: mapString(item, "url", "play_url", "2"), ParseType: mapString(item, "parse", "3")})
			}
			lines = append(lines, catpawLine(key, name, episodes))
		}
	case "美剧侠.py":
		data, err = c.cpPost(ctx, "", attachedParams("service", "App.Vod.Video", "id", id))
		for _, item := range catpawRows(attachedAt(data, "data")) {
			if mapString(item, "type") == "player" {
				row = attachedObject(item["player_vod"])
				break
			}
		}
		for i, line := range catpawRows(row["vod_play"]) {
			episodes := []catpawEpisode{}
			players := catpawRows(line["players"])
			for j := len(players) - 1; j >= 0; j-- {
				episodes = append(episodes, catpawEpisode{Name: mapString(players[j], "title"), URL: mapString(players[j], "url")})
			}
			lines = append(lines, catpawLine(strconv.Itoa(i+1), mapString(line, "title"), episodes))
		}
	case "RJAPP.py":
		data, err = c.cpPost(ctx, "/v3/home/vod_details", attachedParams("vod_id", id))
		row = attachedObject(attachedAt(data, "data"))
		lines = catpawStructuredLines(row["vod_play_list"])
	case "XinJie.py":
		data, err = c.cpGet(ctx, "/admin/duanju.php", attachedParams("vod_id", id))
		row = attachedObject(attachedAt(data, "data"))
		lines = catpawStructuredLines(row["play_sources"])
		c.catpaw.values["jiexi"] = mapString(row, "jiexi")
	case "Appfox.py":
		if c.cpValue("ver") == "3" {
			data, err = c.cpPost(ctx, "/api.php/appfoxs/vod", attachedParams("ac", "detail", "ids", id))
		} else {
			data, err = c.cpGet(ctx, "/api.php/Appfox/vod", attachedParams("ac", "detail", "ids", id))
		}
		rows := catpawRows(attachedAt(data, "list"))
		if len(rows) > 0 {
			row = rows[0]
		}
		lines = catpawMacLines(row)
		configPath := "/api.php/Appfox/config"
		if c.cpValue("ver") == "3" {
			configPath = "/api.php/appfoxs/config"
		}
		if err == nil {
			config, e := c.cpGet(ctx, configPath, nil)
			if e == nil {
				conf := attachedObject(attachedAt(config, "data"))
				s.config["playerList"], s.config["jiexiDataList"] = conf["playerList"], conf["jiexiDataList"]
				for i := range lines {
					for _, player := range catpawRows(conf["playerList"]) {
						if mapString(player, "playerCode") == lines[i].key {
							lines[i].name = firstNonEmpty(mapString(player, "playerName"), lines[i].key)
						}
					}
				}
			}
		}
	case "FeiApp.py":
		data, err = c.cpGet(ctx, "/api.php", attachedParams("type", "getVodinfo", "id", id))
		row = attachedObject(data)
		lines = catpawStructuredLines(attachedAt(data, "vod_player", "list"))
	case "ApptoV5无加密.py":
		data, err = c.cpGet(ctx, "/apptov5/v1/vod/getVod", attachedParams("id", id))
		row = attachedObject(attachedAt(data, "data"))
		lines = catpawStructuredLines(row["vod_play_list"])
	case "AppMuou.py":
		data, err = c.cpGet(ctx, "/api.php/v1.vod/detail", attachedParams("vod_id", id))
		row = attachedObject(attachedAt(data, "data"))
		lines = catpawStructuredLines(row["vod_play_list"])
	case "skapp.py":
		data, err = c.cpGet(ctx, "/sk-api/vod/one", attachedParams("vodId", id))
		row = attachedObject(attachedAt(data, "data"))
		lines = catpawMacLines(row)
	case "AppV2.py":
		api := c.cpValue("api")
		if strings.HasSuffix(api, "v1.vod") || strings.HasSuffix(api, "v1.xvod") {
			data, err = c.cpGet(ctx, api+c.cpV2Path(attachedPath("/detail", attachedParams("vod_id", id, "rel_limit", "10"))), nil)
		} else {
			data, err = c.cpGet(ctx, api+"/video_detail", attachedParams("id", id))
		}
		row = attachedObject(attachedAt(data, "data"))
		if info := attachedObject(row["vod_info"]); info != nil {
			row = info
		}
		lines = catpawStructuredLines(attachedFirst(row, "vod_url_with_player", "vod_play_list"))
		if len(lines) == 0 {
			lines = catpawMacLines(row)
		}
	case "getapp3.4.6.py":
		if c.cpExt("username") != "" && c.cpExt("password") != "" && s.headers["app-user-token"] == "" {
			login, e := c.cpPost(ctx, c.cpValue("api")+".index/appLogin", attachedParams("password", c.cpExt("password"), "code", "", "device_id", c.cpValue("device"), "user_name", c.cpExt("username"), "invite_code", "", "is_emulator", "0"))
			if e != nil {
				return Drama{}, nil, e
			}
			s.headers["app-user-token"] = mapString(attachedObject(attachedAt(login, "user")), "auth_token")
		}
		endpoints := []string{"vodDetail"}
		if strings.HasSuffix(c.cpValue("api"), "qijiappapi") {
			endpoints = []string{"vodDetail2", "vodDetail3"}
		}
		for _, endpoint := range endpoints {
			data, err = c.cpPost(ctx, c.cpValue("api")+".index/"+endpoint, attachedParams("vod_id", id))
			if err == nil {
				break
			}
		}
		row = attachedObject(attachedAt(data, "vod"))
		lines = catpawStructuredLines(attachedAt(data, "vod_play_list"))
	case "Hmys.py":
		data, err = c.cpPost(ctx, "/api/vod/info", attachedParams("vod_id", id))
		row = attachedObject(attachedAt(data, "result"))
		episodes := []catpawEpisode{}
		for _, item := range catpawRows(row["map_list"]) {
			episodes = append(episodes, catpawEpisode{Name: mapString(item, "title"), URL: "hmys:" + mapString(item, "id"), ID: mapString(item, "id"), Token: mapString(item, "collection")})
		}
		lines = []importedLine{catpawLine("hmys", "九霄视频", episodes)}
	case "99APP2.py":
		data, err = c.cp99Call(ctx, "/vod/detail", map[string]any{"id": id, "eps": "1", "v": "2.0.0", "pl": 1})
		row = attachedObject(attachedAt(data, "data"))
		original := catpawMacLines(row)
		players := attachedObject(s.config["player"])
		sort.SliceStable(original, func(i, j int) bool {
			return attachedInt(attachedObject(players[original[i].key]), "sort") > attachedInt(attachedObject(players[original[j].key]), "sort")
		})
		for _, line := range original {
			if player := attachedObject(players[line.key]); player != nil {
				line.name = firstNonEmpty(mapString(player, "name"), line.name)
			}
			lines = append(lines, line)
		}
	case "开端.py":
		path := "/user/movie/cms/v1/play"
		params := attachedParams("id", id)
		if isProviderHTTPMediaURL(id) {
			path = id
			params = nil
		}
		data, err = c.cpGet(ctx, path, params)
		row = attachedObject(attachedAt(data, "data"))
		if row == nil {
			row = attachedObject(data)
		}
		episodes := attachedFirst(row, "episodes", "episodeList")
		nested := catpawRows(episodes)
		for i, line := range nested {
			if line["episode"] != nil {
				lines = append(lines, catpawStructuredLines([]any{map[string]any{"id": strconv.Itoa(i + 1), "title": mapString(line, "title", "name"), "episode": line["episode"]}})...)
			}
		}
		if len(lines) == 0 && len(nested) > 0 {
			lines = catpawStructuredLines([]any{map[string]any{"id": "1", "title": "1线", "urls": episodes}})
		}
		if len(lines) == 0 {
			lines = catpawStructuredLines(attachedAt(data, "datas"))
		}
	case "247看.py":
		data, err = c.cpGet(ctx, "/api/videos/"+url.PathEscape(id), nil)
		row = attachedObject(attachedAt(data, "data"))
		lines = catpawMacLines(row)
	default:
		err = errors.New("CatPaw 详情协议未登记")
	}
	if err != nil {
		return Drama{}, nil, err
	}
	if row == nil {
		return Drama{}, nil, errors.New("站源未返回影片详情")
	}
	return c.cpDetailResult(id, row, lines)
}
