package core

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

func catpawFixedCategories(names ...string) []map[string]any {
	rows := []map[string]any{}
	for i := 0; i+1 < len(names); i += 2 {
		rows = append(rows, map[string]any{"type_id": names[i], "type_name": names[i+1]})
	}
	return rows
}

func (c *attachedClient) catpawCategoryRows(ctx context.Context) ([]map[string]any, error) {
	var data any
	var err error
	s := c.catpaw
	switch s.script {
	case "八戒影视.py":
		data, err = c.cpJSONPost(ctx, "/api/v1/app/screen/screenType", nil)
		data = attachedAt(data, "data")
	case "独播库.py":
		return catpawFixedCategories("2", "连续剧", "3", "综艺", "1", "电影", "4", "动漫"), nil
	case "新浪资源.py":
		return catpawFixedCategories("3", "动漫", "17", "动漫电影", "4", "综艺", "5", "纪录片", "6", "动作片", "7", "爱情片", "8", "科幻片", "9", "战争片", "10", "剧情片", "11", "恐怖片", "12", "喜剧片", "13", "大陆剧", "14", "港澳剧", "15", "台湾剧", "16", "欧美剧", "18", "韩剧", "20", "日剧", "21", "泰剧", "23", "体育"), nil
	case "壹影视.py":
		data, err = c.cpGet(ctx, "/vod-app/type/list", nil)
		data = attachedAt(data, "data")
	case "cycapp.py":
		data, err = c.cpCycRequest(ctx, "/index/nav", nil)
		return catpawObjects(attachedFirst(attachedObject(data), "3", "data")), err
	case "美剧侠.py":
		data, err = c.cpPost(ctx, "", attachedParams("service", "App.Vod.Main_type"))
		data = attachedAt(data, "data")
	case "RJAPP.py":
		data, err = c.cpPost(ctx, "/v3/type/top_type", nil)
		data = attachedAt(data, "data", "list")
	case "XinJie.py":
		data, err = c.cpGet(ctx, "/admin/duanjuc.php", attachedParams("page", "1", "limit", "30"))
		data = attachedAt(data, "data")
	case "Appfox.py":
		data, err = c.cpGet(ctx, "/api.php/Appfox/init", nil)
		data = attachedAt(data, "data", "type_list")
	case "FeiApp.py":
		data, err = c.cpGet(ctx, "/api.php", attachedParams("type", "getsort"))
		data = attachedAt(data, "list")
	case "ApptoV5无加密.py":
		data = s.config["get_home_cate"]
	case "AppMuou.py":
		data, err = c.cpGet(ctx, "/api.php/v1.vod/types", nil)
		data = attachedAt(data, "data", "typelist")
	case "skapp.py":
		data, err = c.cpGet(ctx, "/sk-api/type/list", nil)
		data = attachedAt(data, "data")
	case "AppV2.py":
		api := c.cpValue("api")
		if strings.HasSuffix(api, "v1.vod") || strings.HasSuffix(api, "v1.xvod") {
			data, err = c.cpGet(ctx, api+c.cpV2Path("/types"), nil)
			data = attachedAt(data, "data")
		} else {
			data, err = c.cpGet(ctx, api+"/nav", attachedParams("token", ""))
		}
		return catpawList(data, "list", "typelist", "data"), err
	case "getapp3.4.6.py":
		data = s.config["type_list"]
	case "Hmys.py":
		data, err = c.cpPost(ctx, "/api/block/category_type", nil)
		data = attachedAt(data, "result")
	case "99APP2.py":
		data = attachedAt(s.config, "categorys", "data")
	case "开端.py":
		return catpawFixedCategories("电影", "电影", "连续剧", "连续剧", "动漫", "动漫", "短剧", "短剧", "综艺", "综艺", "纪录片", "纪录片"), nil
	case "247看.py":
		return catpawFixedCategories("1", "电影", "2", "连续剧", "3", "综艺", "4", "动漫", "5", "短剧", "6", "纪录片"), nil
	default:
		return nil, errors.New("CatPaw 分类协议未登记")
	}
	rows := catpawRows(data)
	filtered := rows[:0]
	for _, row := range rows {
		name := mapString(row, "type_name", "name", "title")
		if s.script == "getapp3.4.6.py" && (name == "全部" || name == "QQ" || name == "juo.one" || strings.Contains(name, "企鹅群")) {
			continue
		}
		if s.script == "RJAPP.py" && (name == "泰剧" || name == "日剧" || name == "美剧" || name == "台剧") {
			continue
		}
		if s.script == "XinJie.py" && mapString(row, "type_id") == "0" {
			continue
		}
		filtered = append(filtered, row)
	}
	return filtered, err
}

func catpawFlatten(rows []map[string]any, keys ...string) []map[string]any {
	out := []map[string]any{}
	for _, row := range rows {
		for _, key := range keys {
			out = append(out, catpawRows(row[key])...)
		}
	}
	return out
}

func (c *attachedClient) catpawCatalogPage(ctx context.Context, page int, category, query string) ([]Drama, bool, error) {
	s := c.catpaw
	pg := strconv.Itoa(page)
	home := category == "" && query == ""
	var data any
	var rows []map[string]any
	var err error
	limit := 20
	switch s.script {
	case "八戒影视.py":
		limit = 40
		path := "/api/v1/app/screen/screenMovie"
		condition := map[string]any{"classify": "", "region": "", "sreecnTypeEnum": "NEWEST", "typeId": category, "year": ""}
		if home {
			var groups any
			groups, err = c.cpJSONPost(ctx, "/api/v1/app/recommend/recommendList", nil)
			if err == nil {
				for _, group := range catpawRows(attachedAt(groups, "data")) {
					var block any
					block, err = c.cpJSONPost(ctx, "/api/v1/app/recommend/recommendSubList", map[string]any{"condition": group["id"], "pageNum": page, "pageSize": 6})
					if err != nil {
						break
					}
					rows = append(rows, catpawRows(attachedAt(block, "data", "records"))...)
				}
			}
			if err != nil {
				return nil, false, err
			}
			return c.cpDramas(rows), false, nil
		}
		if query != "" {
			path = "/api/v1/app/search/searchMovie"
			condition = map[string]any{"value": query}
		}
		data, err = c.cpJSONPost(ctx, path, map[string]any{"condition": condition, "pageNum": page, "pageSize": limit})
		data = attachedAt(data, "data")
		rows = catpawRows(attachedAt(data, "records"))
	case "独播库.py":
		path := "/vodshow/" + category + "--------" + pg + "---"
		if page == 1 {
			path = "/vodshow/" + category + "-----------"
		}
		if home {
			path = "/home"
		}
		params := url.Values{}
		if query != "" {
			path = "/vodsearch"
			params.Set("wd", query)
		}
		data, err = c.cpDuboku(ctx, path, params)
		if home {
			rows = catpawFlatten(catpawRows(data), "VodList")
		} else if query != "" {
			rows = catpawRows(data)
		} else {
			rows = catpawRows(attachedAt(data, "VodList"))
		}
		for _, row := range rows {
			row["vod_id"] = catpawDubokuDecode(mapString(row, "DId", "DuId"))
			row["vod_pic"] = catpawDubokuDecode(mapString(row, "TnId"))
			row["vod_name"] = row["Name"]
		}
		limit = 20
		if home || query != "" {
			return c.cpDramas(rows), false, err
		}
	case "新浪资源.py":
		return c.cpXinlangCatalog(ctx, page, category, query)
	case "壹影视.py":
		if home {
			data, err = c.cpGet(ctx, "/vod-app/rank/hotHits", nil)
			rows = catpawFlatten(catpawRows(attachedAt(data, "data")), "vodBeans")
			return c.cpDramas(rows), false, err
		}
		path := "/vod-app/vod/list"
		params := attachedParams("tid", category, "page", pg, "limit", "12", "by", "time")
		if query != "" {
			path = "/vod-app/vod/segSearch"
			params = attachedParams("key", query, "limit", "20", "page", pg)
		} else {
			limit = 12
		}
		data, err = c.cpPost(ctx, path, params)
		data = attachedAt(data, "data")
		rows = catpawRows(attachedAt(data, "data"))
		for _, row := range rows {
			row["vod_pic"] = row["vodPic"]
			row["vod_content"] = row["vodBlurb"]
		}
	case "cycapp.py":
		path := "/v2/video/query"
		params := attachedParams("tid", category, "page", pg, "limit", "20", "order", "time")
		if home {
			path = "/index/video"
			params = nil
		}
		if query != "" {
			path = "/v2/video/search"
			params = attachedParams("text", query, "pg", pg, "type_id", "0", "limit", "20")
		}
		data, err = c.cpCycRequest(ctx, path, params)
		raw := attachedFirst(attachedObject(data), "data", "3")
		if home {
			for _, group := range catpawObjects(raw) {
				rows = append(rows, catpawObjects(group["5"])...)
			}
		} else if query != "" {
			rows = catpawObjects(attachedFirst(attachedObject(data), "data", "4", "3"))
		} else {
			rows = catpawObjects(attachedFirst(attachedObject(raw), "list", "2"))
			data = raw
		}
		normalized := []map[string]any{}
		for _, row := range rows {
			normalized = append(normalized, catpawCycVod(row, false))
		}
		rows = normalized
		if home {
			return c.cpDramas(rows), false, err
		}
	case "美剧侠.py":
		limit = 24
		params := attachedParams("service", "App.Vod.Videos", "list_id", category, "type", "全部", "year", "全部", "area", "全部", "language", "全部", "order", "time", "page", pg, "perpage", "24")
		if home {
			params = attachedParams("service", "App.Vod.HomeVideos")
		}
		if query != "" {
			params = attachedParams("service", "App.Vod.Search", "search", query, "page", pg, "perpage", "24")
		}
		data, err = c.cpPost(ctx, "", params)
		rows = catpawFlatten(catpawRows(attachedAt(data, "data")), "videos")
		if home {
			return c.cpDramas(rows), false, err
		}
	case "RJAPP.py":
		path := "/v3/home/type_search"
		params := attachedParams("type_id", category, "page", pg)
		if home {
			path = "/v3/type/tj_vod"
			params = nil
		}
		if query != "" {
			path = "/v3/home/search"
			params = attachedParams("keyword", query)
		}
		data, err = c.cpPost(ctx, path, params)
		data = attachedAt(data, "data")
		if home {
			row := attachedObject(data)
			rows = append(catpawRows(row["cai"]), catpawRows(row["loop"])...)
			rows = append(rows, catpawFlatten(catpawRows(row["type_vod"]), "vod")...)
			return c.cpDramas(rows), false, err
		}
		rows = catpawRows(attachedAt(data, "list"))
		if query != "" {
			return c.cpDramas(rows), false, err
		}
	case "XinJie.py":
		path := "/admin/duanjusy.php"
		params := attachedParams("limit", "20", "page", pg, "type_id", category)
		if home {
			path = "/admin/duanjuc.php"
			params = attachedParams("page", "1", "limit", "30")
		}
		if query != "" {
			params = attachedParams("suggest", query, "limit", "20", "page", pg)
		}
		data, err = c.cpGet(ctx, path, params)
		rows = catpawRows(attachedAt(data, "data"))
		if home {
			rows = catpawFlatten(rows, "videos")
			return c.cpDramas(rows), false, err
		}
		if pagination := attachedAt(data, "pagination"); pagination != nil {
			data = pagination
		}
	case "Appfox.py":
		return c.cpFoxCatalog(ctx, page, category, query)
	case "FeiApp.py":
		params := attachedParams("type", "getvod", "type_id", category, "page", pg, "tag", "全部", "year", "全部")
		if home {
			params = attachedParams("type", "getHome")
		}
		if query != "" {
			params = attachedParams("type", "getsearch", "text", query)
		}
		data, err = c.cpGet(ctx, "/api.php", params)
		if home {
			rows = catpawFlatten(catpawRows(data), "list")
			return c.cpDramas(rows), false, err
		}
		rows = catpawRows(attachedAt(data, "list"))
		if query != "" {
			return c.cpDramas(rows), false, err
		}
	case "ApptoV5无加密.py":
		limit = 21
		path := "/apptov5/v1/vod/lists"
		params := attachedParams("area", "", "lang", "", "year", "", "order", "time", "type_id", category, "type_name", "", "page", pg, "pageSize", "21", "__platform", "android")
		if home {
			path = "/apptov5/v1/home/data"
			params = attachedParams("id", "1", "mold", "1", "__platform", "android")
		}
		if query != "" {
			path = "/apptov5/v1/search/lists"
			params = attachedParams("wd", query, "page", pg, "type", "", "__platform", "android")
		}
		data, err = c.cpGet(ctx, path, params)
		data = attachedAt(data, "data")
		if home {
			rows = catpawFlatten(catpawRows(attachedAt(data, "sections")), "items")
			return c.cpDramas(rows), false, err
		}
		rows = catpawRows(attachedAt(data, "data"))
	case "AppMuou.py":
		limit = 18
		path := "/api.php/v1.vod"
		params := attachedParams("type", category, "class", "", "area", "", "year", "", "by", "time", "page", pg, "limit", "18")
		if home {
			path += "/HomeIndex"
			params = attachedParams("page", "", "limit", "6")
		}
		if query != "" {
			params = attachedParams("wd", query, "limit", "18", "page", pg)
		}
		data, err = c.cpGet(ctx, path, params)
		data = attachedAt(data, "data")
		if home {
			rows = catpawFlatten(catpawRows(data), "vod_list")
			return c.cpDramas(rows), false, err
		}
		rows = catpawRows(attachedAt(data, "list"))
	case "skapp.py":
		limit = 18
		path := "/sk-api/vod/list"
		params := attachedParams("typeId", category, "page", pg, "limit", "18", "type", "updateTime", "area", "", "lang", "", "year", "", "mtype", "", "extendtype", "")
		if home {
			params = attachedParams("page", "1", "limit", "12", "type", "randomlikeindex", "area", "", "lang", "", "year", "", "mtype", "")
			limit = 12
		}
		if query != "" {
			path = "/sk-api/search/pages"
			params = attachedParams("keyword", query, "page", pg, "limit", "10", "typeId", "-1")
			limit = 10
		}
		data, err = c.cpGet(ctx, path, params)
		data = attachedAt(data, "data")
		rows = catpawList(data, "list")
		if home {
			return c.cpDramas(rows), false, err
		}
	case "AppV2.py":
		api := c.cpValue("api")
		v1 := strings.HasSuffix(api, "v1.vod") || strings.HasSuffix(api, "v1.xvod")
		path := ""
		if v1 {
			limit = 9
			path = attachedPath("", attachedParams("type", category, "class", "", "lang", "", "area", "", "year", "", "by", "", "page", pg, "limit", "9"))
			if home {
				path = "/vodPhbAll"
			}
			if query != "" {
				path = attachedPath("", attachedParams("page", pg, "limit", "10", "wd", query))
				limit = 10
			}
			data, err = c.cpGet(ctx, api+c.cpV2Path(path), nil)
			data = attachedAt(data, "data")
		} else {
			limit = 18
			path = "/video"
			params := attachedParams("tid", category, "class", "", "area", "", "lang", "", "year", "", "limit", "18", "pg", pg)
			if home {
				path = "/index_video"
				params = attachedParams("token", "")
			}
			if query != "" {
				path = "/search"
				params = attachedParams("text", query, "pg", pg)
			}
			data, err = c.cpGet(ctx, api+path, params)
		}
		if home {
			rows = catpawFlatten(catpawList(data, "list", "data"), "vod_list", "vlist")
			return c.cpDramas(rows), false, err
		}
		rows = catpawList(data, "list", "data")
		c.catpaw.config["searchRows"] = rows
	case "getapp3.4.6.py":
		if home {
			rows = catpawFlatten(catpawRows(s.config["type_list"]), "recommend_list")
			return c.cpDramas(rows), false, nil
		}
		params := attachedParams("area", "全部", "year", "全部", "type_id", category, "page", pg, "sort", "最新", "lang", "全部", "class", "全部")
		path := c.cpValue("api") + ".index/typeFilterVodList"
		if query != "" {
			if value := mapString(attachedObject(s.config["config"]), c.cpConst("const2")); value == "1" || value == "true" {
				return nil, false, errors.New("该源搜索要求验证码；当前迁移不读取或处理源站图片")
			}
			path = c.cpValue("api") + ".index/searchList"
			params = attachedParams("keywords", query, "type_id", "0", "page", pg)
		}
		data, err = c.cpPost(ctx, path, params)
		rows = catpawRows(attachedFirst(attachedObject(data), "recommend_list", "search_list"))
		limit = 18
	case "Hmys.py":
		limit = 12
		if home {
			groups,e := c.cpPost(ctx,"/api/nav/list",nil)
			if e != nil { return nil,false,e }
			recommended := "253"
			for _,group := range catpawRows(attachedAt(groups,"result")) {
				if mapString(group,"nav_name") == "推荐" { recommended = mapString(group,"nav_id"); break }
			}
			data,err = c.cpPost(ctx,"/api/nav/index",attachedParams("nav_id",recommended))
			blocks := catpawFlatten(catpawRows(attachedAt(data,"result")),"block_list")
			rows = catpawFlatten(blocks,"vod_list")
			for _,row := range rows { row["vod_name"] = row["title"] }
			return c.cpDramas(rows),false,err
		}
		path := "/api/block/category"
		params := attachedParams("area", "全部", "cate", "全部", "type_pid", category, "year", "全部", "length", "12", "page", pg, "order", "最热")
		if query != "" {
			path = "/api/search/result"
			params = attachedParams("type_pid", "0", "kw", query, "pn", pg)
		}
		data, err = c.cpPost(ctx, path, params)
		data = attachedAt(data, "result")
		rows = catpawRows(data)
		for _, row := range rows {
			row["vod_name"] = row["title"]
		}
	case "99APP2.py":
		limit = 21
		payload := map[string]any{"kw": query, "page": pg, "limit": limit, "orderBy": "time", "isCategory": 1, "pid": firstNonEmpty(category, "1")}
		if query != "" {
			payload = map[string]any{"kw": query, "page": page, "limit": limit, "orderBy": "vod_hits_month", "sort": "desc"}
		}
		data, err = c.cp99Call(ctx, "/vod/search", payload)
		rows = catpawRows(attachedAt(data, "data"))
	case "开端.py":
		params := attachedParams("count", "20", "names", firstNonEmpty(category, "电影,连续剧,动漫,短剧,综艺,纪录片"), "page", pg)
		path := "/user/movie/cms/v1/category"
		if query != "" {
			path = "/user/movie/cms/v1/search"
			params = attachedParams("name", query, "page", pg, "count", "10")
			limit = 10
		}
		data, err = c.cpGet(ctx, path, params)
		rows = catpawList(data, "datas", "data")
	case "247看.py":
		limit = 21
		path := "/api/categories/" + firstNonEmpty(category, "1") + "/videos"
		params := attachedParams("page", pg, "limit", "21", "sort", "year", "area", "", "language", "", "year", "", "actor", "", "director", "", "letter", "", "tag", "", "genre", "", "trending", "false", "top250", "false", "highscore", "false")
		if home {
			path = "/api/home"
			params = attachedParams("featured_category_ids", "14")
		}
		if query != "" {
			path = "/api/videos/search"
			params = attachedParams("q", query, "page", pg, "limit", "20")
			limit = 20
		}
		data, err = c.cpGet(ctx, path, params)
		data = attachedAt(data, "data")
		if home {
			for _, value := range attachedObject(data) {
				rows = append(rows, catpawRows(value)...)
			}
			return c.cpDramas(rows), false, err
		}
		rows = catpawRows(attachedAt(data, "videos"))
	default:
		return nil, false, errors.New("CatPaw 目录协议未登记")
	}
	if err != nil {
		return nil, false, err
	}
	dramas := c.cpDramas(rows)
	return dramas, catpawMore(data, page, len(rows), limit), nil
}

func (c *attachedClient) cpFoxCatalog(ctx context.Context, page int, category, query string) ([]Drama, bool, error) {
	var data any
	var err error
	home := category == "" && query == ""
	if query != "" {
		if c.cpValue("ver") == "3" {
			data, err = c.cpPost(ctx, "/api.php/appfoxs/vod", attachedParams("ac", "detail", "wd", query, "pg", strconv.Itoa(page)))
		} else {
			data, err = c.cpGet(ctx, "/api.php/Appfox/vod", attachedParams("ac", "detail", "wd", query, "pg", strconv.Itoa(page)))
		}
		c.catpaw.config["detailRows"] = attachedAt(data, "list")
		rows := catpawRows(attachedAt(data, "list"))
		return c.cpDramas(rows), catpawMore(data, page, len(rows), 20), err
	}
	if home {
		nav, e := c.cpGet(ctx, "/api.php/appfox/nav", nil)
		if e != nil {
			return nil, false, e
		}
		groups := catpawRows(attachedAt(nav, "data"))
		if len(groups) == 0 {
			return nil, false, errors.New("站源未返回推荐导航")
		}
		data, err = c.cpGet(ctx, "/api.php/Appfox/nav_video", attachedParams("id", mapString(groups[0], "navigationId")))
		rows := catpawFlatten(catpawRows(attachedAt(data, "data")), "banner")
		for _, group := range catpawRows(attachedAt(data, "data")) {
			rows = append(rows, catpawFlatten(catpawRows(group["categories"]), "videos")...)
		}
		return c.cpDramas(rows), false, err
	}
	data, err = c.cpGet(ctx, "/api.php/Appfox/vodList", attachedParams("type_id", category, "class", "全部", "area", "全部", "lang", "全部", "year", "全部", "sort", "最新", "page", strconv.Itoa(page)))
	rows := catpawRows(attachedAt(data, "data", "recommend_list"))
	return c.cpDramas(rows), catpawMore(data, page, len(rows), 20), err
}
