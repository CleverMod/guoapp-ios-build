package core

import (
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

func attachedJSON(value any) string { data, _ := json.Marshal(value); return string(data) }
func attachedPath(path string, values url.Values) string {
	if len(values) == 0 {
		return path
	}
	return path + "?" + values.Encode()
}
func attachedParams(values ...string) url.Values {
	out := url.Values{}
	for i := 0; i+1 < len(values); i += 2 {
		out.Set(values[i], values[i+1])
	}
	return out
}
func attachedMD5(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}
func attachedSHA(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func attachedData(value any) any {
	row := attachedObject(value)
	if row["data"] != nil {
		return row["data"]
	}
	if row["result"] != nil {
		return row["result"]
	}
	if row["body"] != nil {
		return row["body"]
	}
	return value
}
func attachedSorted(values url.Values) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		b.WriteString(key + "=" + values.Get(key))
	}
	return b.String()
}
func attachedECB(data, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	pad := block.BlockSize() - len(data)%block.BlockSize()
	for i := 0; i < pad; i++ {
		data = append(data, byte(pad))
	}
	out := make([]byte, len(data))
	for i := 0; i < len(data); i += block.BlockSize() {
		block.Encrypt(out[i:i+block.BlockSize()], data[i:i+block.BlockSize()])
	}
	return out, nil
}

func (c *attachedClient) session(ctx context.Context) (string, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expiry) {
		return c.token, c.userID, nil
	}
	for key, value := range c.access.Headers {
		if (strings.EqualFold(key, "Authorization") && (c.p.id == "xingya" || c.p.id == "qixing" || c.p.id == "shanhai")) || strings.EqualFold(key, "token") && c.p.id == "niuniudj" {
			if value != "" {
				return value, c.access.Query["userId"], nil
			}
		}
	}
	var target, body, content string
	headers := map[string]string{}
	switch c.p.id {
	case "xingya", "qixing", "shanhai":
		body = c.access.Settings["loginBody"]
		if body == "" {
			return "", "", fmt.Errorf("%s内置登录参数缺失，需要更新安装包", c.p.name)
		}
		if c.p.id == "xingya" {
			target = "https://u.shytkjgs.com/user/v1/account/login"
			content = "application/x-www-form-urlencoded"
		} else {
			target = "https://u.shytkjgs.com/user/v3/account/login"
			if c.p.id == "shanhai" {
				target = "https://u.app.gxshxy.com/user/v3/account/login"
			}
			var payload map[string]any
			if json.Unmarshal([]byte(body), &payload) != nil {
				return "", "", errors.New("内置登录参数格式无效")
			}
			payload["timestamp"] = time.Now().UnixMilli()
			encrypted, err := attachedECB([]byte(attachedJSON(payload)), []byte(c.access.Settings["loginKey"]))
			if err != nil {
				return "", "", err
			}
			body = base64.StdEncoding.EncodeToString(encrypted)
			content = "application/json; charset=utf-8"
		}
	case "niuniudj":
		target = "/api/v1/app/user/visitorInfo"
		headers = map[string]string{"deviceid": c.device, "token": "", "client": "app", "devicetype": "Android"}

	default:
		return "", "", nil
	}
	for key, value := range c.access.Headers {
		headers[key] = value
	}
	method := http.MethodPost
	if c.p.id == "niuniudj" {
		method = http.MethodGet
	}
	raw, _, err := c.raw(ctx, method, target, content, body, headers)
	if err != nil {
		return "", "", err
	}
	envelope, err := attachedDecode(raw)
	if err != nil {
		return "", "", err
	}
	data := attachedObject(attachedData(envelope))
	token := mapString(data, "token", "access_token")
	if token == "" {
		return "", "", fmt.Errorf("%s游客会话不可用，请刷新或更新安装包内的授权", c.p.name)
	}
	c.token = token
	c.userID = firstNonEmpty(c.access.Query["userId"], mapString(data, "userId", "user_id", "id"))
	c.expiry = time.Now().Add(30 * time.Minute)
	return c.token, c.userID, nil
}

func (c *attachedClient) call(ctx context.Context, method, target string, payload any) (any, error) {
	if c.p.id == "honeypeach" {
		return c.peachCall(ctx, method, target, payload)
	}
	if c.p.id == "niuniudj" {
		return c.niuniuCall(ctx, method, target, payload)
	}
	headers := map[string]string{}
	for key, value := range c.access.Headers {
		headers[key] = value
	}
	content, body := "", ""
	if payload != nil {
		content = "application/json;charset=utf-8"
		body = attachedJSON(payload)
	}
	token, _, err := c.session(ctx)
	if err != nil {
		return nil, err
	}
	switch c.p.id {
	case "xingya", "qixing", "shanhai":
		headers["authorization"] = firstNonEmpty(token, headers["authorization"])
		headers["User-Agent"] = c.agent()
	case "niuniudj":
		headers["token"] = token
		headers["deviceid"] = c.device
		headers["client"] = "app"
		headers["devicetype"] = "Android"
	case "hema":
		if headers["datas"] == "" && headers["DATAS"] == "" {
			return nil, errors.New("河马内置授权请求头缺失，需要更新安装包")
		}
		key, _ := hex.DecodeString("647a6b6a67667978677368796c677a6d")
		iv, _ := hex.DecodeString("6170697570646f776e65646372797074")
		enc, err := aesCBCEncrypt([]byte(body), key, iv)
		if err != nil {
			return nil, err
		}
		body = strings.ToUpper(hex.EncodeToString(enc))
		content = "text/plain"

	case "qimao":
		parsed, _ := url.Parse(target)
		v := parsed.Query()
		v.Set("sign", attachedMD5(attachedSorted(v)+"d3dGiJc651gSQ8w1"))
		parsed.RawQuery = v.Encode()
		target = parsed.String()
		device := attachedJSON(map[string]string{"static_score": "0.8", "uuid": c.device, "device-id": c.device, "mac": "", "sourceuid": c.device[:16], "refresh-type": "0", "model": "Pixel 7", "wlb-imei": "", "client-id": c.device[:16], "brand": "Google", "oaid": "", "oaid-no-cache": "", "sys-ver": "13", "trusted-id": "", "phone-level": "H", "imei": "", "wlb-uid": c.device[:16], "session-id": strconv.FormatInt(time.Now().UnixMilli(), 10)})
		from := "+/0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
		to := "PXMUlErYWbdJ9saI0oy_HGitgNA8Fk3hfRqC4pmBOuc6Kx5T-2zSZ1VvjQ7DwnLe"
		encoded := base64.StdEncoding.EncodeToString([]byte(device))
		var q strings.Builder
		for _, ch := range encoded {
			index := strings.IndexRune(from, ch)
			if index >= 0 {
				q.WriteByte(to[index])
			} else {
				q.WriteRune(ch)
			}
		}
		qm := q.String()
		headers["qm-params"] = qm
		headers["net-env"] = "5"
		headers["reg"] = ""
		headers["channel"] = "unknown"
		headers["is-white"] = ""
		headers["platform"] = "android"
		headers["application-id"] = "com.duoduo.read"
		headers["authorization"] = ""
		headers["app-version"] = "10001"
		headers["User-Agent"] = "webviewversion/0"
		headers["sign"] = attachedMD5("AUTHORIZATION=app-version=10001application-id=com.duoduo.readchannel=unknownis-white=net-env=5platform=androidqm-params=" + qm + "reg=d3dGiJc651gSQ8w1")

	case "yimi":
		if c.access.PrivateKey == "" || c.access.Settings["commonQuery"] == "" || c.access.Settings["userQuery"] == "" {
			return nil, errors.New("薏米内置签名私钥或接口参数缺失，需要更新安装包")
		}
		parsed, _ := url.Parse(target)
		v := parsed.Query()
		common, user := c.access.Settings["commonQuery"], c.access.Settings["userQuery"]
		sec := c.access.Settings["listSec"]
		if strings.Contains(parsed.Path, "episode_list") {
			sec = c.access.Settings["detailSec"]
			parsed.RawQuery = "end_id=" + v.Get("end_id") + "&" + common + "&pc=10&play_id=" + url.QueryEscape(v.Get("play_id")) + "&start_id=" + v.Get("start_id") + "&" + user
		} else {
			parsed.RawQuery = "key=" + url.QueryEscape(v.Get("key")) + "&" + common + "&page=" + v.Get("page") + "&pc=10&" + user
		}
		target = parsed.String()
		block, _ := pem.Decode([]byte(c.access.PrivateKey))
		if block == nil {
			return nil, errors.New("薏米签名私钥格式无效")
		}
		var private *rsa.PrivateKey
		private, _ = x509.ParsePKCS1PrivateKey(block.Bytes)
		if private == nil {
			key, _ := x509.ParsePKCS8PrivateKey(block.Bytes)
			private, _ = key.(*rsa.PrivateKey)
		}
		if private == nil {
			return nil, errors.New("薏米签名私钥格式无效")
		}
		ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
		sum := sha256.Sum256([]byte("&" + parsed.RawQuery + "&" + parsed.Path + "&" + ts + "&" + sec))
		sig, err := rsa.SignPKCS1v15(rand.Reader, private, crypto.SHA256, sum[:])
		if err != nil {
			return nil, errors.New("薏米签名失败")
		}
		headers["x-appid"] = "zy9351ae"
		headers["x-sig-timestamp"] = ts
		headers["x-sig-alg"] = "RSA-SHA256"
		headers["x-sig-sign"] = base64.StdEncoding.EncodeToString(sig)
		headers["x-sig-ver"] = "v1.1"
		headers["x-sig-sec"] = sec
	}
	raw, _, err := c.raw(ctx, method, target, content, body, headers)
	if err != nil {
		return nil, err
	}
	envelope, err := attachedDecode(raw)
	if err != nil {
		return nil, err
	}
	data := attachedData(envelope)
	switch c.p.id {
	case "hema":
		cipherText, err := hex.DecodeString(nativeText(data))
		if err != nil {
			return nil, errors.New("河马响应编码无效")
		}
		key, _ := hex.DecodeString("647a6b6a67667978677368796c677a6d")
		iv, _ := hex.DecodeString("6170697570646f776e65646372797074")
		plain, err := aesCBCDecrypt(cipherText, key, iv)
		if err != nil {
			return nil, errors.New("河马响应解密失败")
		}
		data, err = attachedDecode(string(plain))
		if err != nil {
			return nil, err
		}
	case "shanhai":
		row := attachedObject(data)
		if row["nonce"] != nil && row["data"] != nil {
			encrypted, err := hex.DecodeString(mapString(row, "data"))
			if err != nil {
				return nil, errors.New("山海响应编码无效")
			}
			nonce, err := hex.DecodeString(mapString(row, "nonce"))
			if err != nil {
				return nil, errors.New("山海响应随机数无效")
			}
			block, err := aes.NewCipher([]byte(c.access.Settings["gcmKey"]))
			if err != nil {
				return nil, errors.New("山海内置解密密钥无效")
			}
			gcm, err := cipher.NewGCM(block)
			if err != nil {
				return nil, errors.New("山海解密协议不可用")
			}
			if len(nonce) != gcm.NonceSize() {
				return nil, errors.New("山海响应随机数长度无效")
			}
			plain, err := gcm.Open(nil, nonce, encrypted, nil)
			if err != nil {
				return nil, errors.New("山海响应解密失败")
			}
			data, err = attachedDecode(string(plain))
			if err != nil {
				return nil, err
			}
		}
	}
	return data, nil
}

func (c *attachedClient) get(ctx context.Context, path string, values ...string) (any, error) {
	return c.call(ctx, http.MethodGet, attachedPath(path, attachedParams(values...)), nil)
}
func (c *attachedClient) post(ctx context.Context, path string, value any) (any, error) {
	return c.call(ctx, http.MethodPost, path, value)
}
func (c *attachedClient) form(ctx context.Context, target string, values url.Values) (any, error) {
	headers := map[string]string{}
	for key, value := range c.access.Headers {
		headers[key] = value
	}
	raw, _, err := c.raw(ctx, http.MethodPost, target, "application/x-www-form-urlencoded", values.Encode(), headers)
	if err != nil {
		return nil, err
	}
	v, err := attachedDecode(raw)
	return attachedData(v), err
}

func (c *attachedClient) apiCatalog(ctx context.Context, page int, cat attachedCategory, query string) ([]Drama, bool, error) {
	var data any
	var err error
	var rows []map[string]any
	limit := 24
	more := true
	pg := strconv.Itoa(page)
	switch c.p.id {
	case "honeypeach":
		return c.peachCatalog(ctx, page, cat, query)
	case "weiguan":
		limit = 30
		data, err = c.post(ctx, "/drama/home/search?version_code=1500&os_type=1", map[string]any{"audience": "全部受众", "page": page, "pageSize": limit, "searchWord": query, "subject": "全部主题"})
		rows = attachedRows(data)
	case "hema":
		if query != "" {
			limit = 15
			data, err = c.post(ctx, "/free-video-portal/portal/1803", map[string]any{"keyword": query, "page": page, "size": limit})
			rows = attachedRows(attachedAt(data, "searchVos"))
		} else {
			data, err = c.post(ctx, "/free-video-portal/portal/1125", map[string]any{"recSwitch": true, "storePageId": 10002, "channelGroupId": "10", "channelId": cat.value, "channelName": cat.name, "lastColumnStyle": 3, "fromColumnId": "1", "pageFlag": pg, "page": page, "theaterSubscriptSwitch": true})
			columns := attachedRows(attachedAt(data, "columnData"))
			if len(columns) > 0 {
				rows = attachedRows(columns[0]["videoData"])
			}
		}
	case "shanhai", "xingya", "qixing":
		if query != "" {
			if page > 1 {
				return nil, false, nil
			}
			data, err = c.post(ctx, "/v3/search", map[string]string{"text": query})
			rows = attachedRows(attachedAt(data, "theater", "search_data"))
			more = false
		} else {
			path := "/cloud/v2/theater/home_page"
			values := attachedParams("theater_class_id", cat.value, "type", "1", "class2_ids", "0", "page_num", pg, "page_size", "24")
			if c.p.id == "shanhai" {
				path = "/shanhai-theater/v2/theater_parent/cloud/v2/theater/home_page"
				values.Set("theater_class_id", "1")
				values.Set("class2_ids", cat.value)
			}
			if c.p.id == "qixing" {
				path = "/v1/theater/home_page"
			}
			data, err = c.call(ctx, http.MethodGet, attachedPath(path, values), nil)
			rows = attachedRows(attachedFirst(attachedObject(data), "list", "items"))
		}
	case "haokan":
		limit = 12
		data, err = c.form(ctx, "/haokan/ui-feed/playletTagsFeed?log=vhk&tn=1020970b&ctn=1008350n&blur=1", attachedParams("tag_id", cat.value, "pn", pg, "rn", "12"))
		rows = attachedRows(attachedAt(data, "list"))

	case "xifan":
		limit = 30
		parts := strings.SplitN(cat.value, "@", 2)
		if query != "" {
			data, err = c.get(ctx, "/xifan/search/getSearchList", "reqType", "search", "offset", strconv.Itoa((page-1)*30), "keyword", query, "requestId", attachedNonce(), "appId", "drama")
			for _, row := range attachedRows(attachedAt(data, "elements")) {
				if value := attachedObject(row["duanjuVo"]); value != nil {
					rows = append(rows, value)
				}
			}
		} else {
			name := cat.name
			if len(parts) == 2 {
				name = parts[1]
			}
			data, err = c.get(ctx, "/xifan/drama/portalPage", "reqType", "aggregationPage", "offset", strconv.Itoa((page-1)*30), "categoryId", parts[0], "categoryNames", name, "pageID", "page_theater", "appId", "drama")
			for _, element := range attachedRows(attachedAt(data, "elements")) {
				for _, row := range attachedRows(element["contents"]) {
					if value := attachedObject(row["duanjuVo"]); value != nil {
						rows = append(rows, value)
					}
				}
			}
		}
	case "qimao":
		if query != "" {
			data, err = c.get(ctx, "/api/v1/playlet/search", "extend", "", "page", pg, "wd", query, "read_preference", "0", "track_id", attachedNonce())
		} else {
			v := attachedParams("tag_id", cat.value, "playlet_privacy", "1", "operation", "1")
			if page > 1 {
				v.Set("next_id", pg)
			}
			data, err = c.call(ctx, http.MethodGet, attachedPath("/api/v1/playlet/index", v), nil)
		}
		rows = attachedRows(attachedAt(data, "list"))

	case "yimi":
		limit = 10
		data, err = c.get(ctx, "/bookstore/local/visual/channel/list", "key", cat.value, "page", pg, "pc", "10")
		list := attachedRows(attachedAt(data, "list"))
		if len(list) > 0 {
			rows = attachedRows(list[0]["short_plays"])
		}

	case "niuniudj":
		limit = 24
		path := "/api/v1/app/screen/screenMovie"
		condition := map[string]string{"classify": cat.value, "typeId": "S1"}
		if query != "" {
			path = "/api/v1/app/search/searchMovie"
			condition = map[string]string{"value": query, "typeId": "S1"}
		}
		data, err = c.post(ctx, path, map[string]any{"condition": condition, "pageNum": page, "pageSize": limit})
		rows = attachedRows(attachedAt(data, "records"))

	case "kuangbiao":
		return c.rushCatalog(ctx, page, cat)
	case "yizk":
		v := attachedParams("page", pg, "pageSize", "24")
		path := "/api/v1/films"
		if query != "" {
			v.Set("keyword", query)
		} else if cat.value != "recommend" && cat.value != "all" {
			path = "/api/v1/categories/" + url.PathEscape(cat.value) + "/films"
		}
		data, err = c.call(ctx, http.MethodGet, attachedPath(path, v), nil)
		rows = attachedRows(attachedAt(data, "list"))
	default:
		return nil, false, errors.New("站源目录协议不可用")
	}
	if err != nil {
		return nil, false, err
	}
	out := []Drama{}
	seen := map[string]bool{}
	for _, row := range rows {
		drama := c.drama(row, "")
		if drama.ID != "" && !seen[drama.ID] {
			seen[drama.ID] = true
			out = append(out, drama)
		}
	}
	if flag, ok := attachedObject(data)["has_more"].(bool); ok {
		more = flag
	} else {
		more = more && len(rows) > 0
	}
	return out, more, nil
}

func attachedBestURL(value any) string {
	if address, ok := value.(string); ok {
		if isProviderHTTPMediaURL(address) {
			return address
		}
		decoded, err := attachedDecode(address)
		if err == nil {
			return attachedBestURL(decoded)
		}
		return ""
	}
	if row := attachedObject(value); row != nil {
		for _, key := range []string{"m3u8720p", "mp4SwitchUrl", "super", "high", "normal", "play_url", "playUrl", "playURL", "son_video_url", "shortPlayUrl", "video_url", "videoUrl", "videoURL", "manifest_url", "url", "src", "content"} {
			if address := attachedBestURL(row[key]); address != "" {
				return address
			}
		}
	}
	if list, ok := value.([]any); ok {
		best, quality := "", -1
		for _, raw := range list {
			row := attachedObject(raw)
			if q := attachedInt(row, "video_quality"); row["video_quality"] != nil && q > quality {
				if address := attachedBestURL(row); address != "" {
					best, quality = address, q
				}
			}
		}
		if best != "" {
			return best
		}
		for _, raw := range list {
			row := attachedObject(raw)
			if strings.EqualFold(mapString(row, "clarity"), "1080p") || mapString(row, "clarity") == "super" {
				if address := attachedBestURL(raw); address != "" {
					return address
				}
			}
		}
		for _, raw := range list {
			if address := attachedBestURL(raw); address != "" {
				return address
			}
		}
	}
	return ""
}

func (c *attachedClient) apiDetail(ctx context.Context, id string) (Drama, []Chapter, error) {
	var data, episodes any
	var err error
	escaped := url.PathEscape(id)
	switch c.p.id {
	case "honeypeach":
		return c.peachDetail(ctx, id)
	case "weiguan":
		data, err = c.get(ctx, "/drama/home/shortVideoDetail", "version_code", "1000", "os_type", "1", "oneId", id, "page", "1", "pageSize", "1000")
		episodes = data
	case "hema":
		data, err = c.post(ctx, "/free-video-portal/portal/1131", map[string]string{"bookId": id})
		if err == nil {
			data = attachedAt(data, "videoInfo")
			var chapters any
			chapters, err = c.post(ctx, "/free-video-portal/portal/1132", map[string]any{"bookId": id, "chapterMin": 1, "chapterMax": 10000})
			episodes = attachedAt(chapters, "chapterList")
		}
	case "xingya", "qixing", "shanhai":
		path := "/v2/theater_parent/detail"
		if c.p.id == "shanhai" {
			path = "/shanhai-theater/v2/theater_parent/detail"
		}
		data, err = c.get(ctx, path, "theater_parent_id", id)
		episodes = attachedAt(data, "theaters")
	case "haokan":
		data, err = c.form(ctx, "https://sv.baidu.com/haokan/ui-video/playlet/rec/detail?log=vhk&tn=1020970b&ctn=1008350n&blur=1", attachedParams("playlet_id", id, "vid", "undefined"))
		values := attachedAt(data, "vid_list")
		if text, ok := values.(string); ok {
			var parsed any
			if json.Unmarshal([]byte(text), &parsed) == nil {
				values = parsed
			} else {
				parts := strings.Split(text, ",")
				list := []any{}
				for _, part := range parts {
					list = append(list, strings.TrimSpace(part))
				}
				values = list
			}
		}
		if list, ok := values.([]any); ok {
			rows := []any{}
			for _, item := range list {
				if attachedObject(item) != nil {
					rows = append(rows, item)
				} else {
					rows = append(rows, map[string]any{"vid": item})
				}
			}
			episodes = rows
		}
	case "xifan":
		parts := strings.SplitN(id, "@", 2)
		source := ""
		if len(parts) == 2 {
			source = parts[1]
		}
		data, err = c.get(ctx, "/xifan/drama/getDuanjuInfo", "duanjuId", parts[0], "source", source, "appId", "drama")
		episodes = attachedAt(data, "episodeList")
	case "qimao":
		data, err = c.get(ctx, "https://api-read.qmplaylet.com/player/api/v1/playlet/info", "playlet_id", id)
		episodes = attachedAt(data, "play_list")

	case "yimi":
		all := []any{}
		for start := 1; start <= 2000; start += 30 {
			var page any
			page, err = c.get(ctx, "/video/client/short_play/episode_list", "end_id", strconv.Itoa(start+29), "pc", "10", "play_id", id, "start_id", strconv.Itoa(start))
			if err != nil {
				break
			}
			if start == 1 {
				data = page
			}
			list, _ := attachedAt(page, "episode_list").([]any)
			all = append(all, list...)
			if len(list) < 30 || len(all) >= attachedInt(attachedObject(page), "target_count") && attachedInt(attachedObject(page), "target_count") > 0 {
				break
			}
		}
		episodes = all

	case "niuniudj":
		_, user, e := c.session(ctx)
		if e != nil {
			err = e
			break
		}
		data, err = c.post(ctx, "/api/v1/app/play/movieDesc", map[string]any{"id": niuniuID(id), "typeId": "S1"})
		if err != nil {
			break
		}
		var detail any
		detail, err = c.post(ctx, "/api/v1/app/play/movieDetails", map[string]any{"id": niuniuID(id), "source": 0, "typeId": "S1", "userId": user})
		episodes = attachedAt(detail, "episodeList")
		if third := mapString(attachedObject(detail), "thirdPlayId"); len(attachedRows(episodes)) == 0 && third != "" && third != "0" && err == nil {
			var csj any
			csj, err = c.niuniuCSJDetail(ctx, third, 1)
			list := []any{}
			for _, row := range attachedRows(attachedAt(csj, "episode_right_list")) {
				if number := attachedInt(row, "index"); number > 0 && number <= 2000 {
					list = append(list, map[string]any{"id": "t:" + third + ":" + strconv.Itoa(number), "number": number})
				}
			}
			episodes = list
		}

	case "kuangbiao":
		data, err = c.rushCall(ctx, "episode.watch", map[string]any{"dramaId": id, "episodeNumber": 1})
		episodes = attachedAt(data, "episodes")
		data = attachedAt(data, "drama")
	case "yizk":
		data, err = c.get(ctx, "/api/v1/films/"+escaped)
		count := attachedInt(attachedObject(data), "episode_count")
		if count < 1 || count > 2000 {
			err = errors.New("一直看未返回有效集数")
			break
		}
		list := []any{}
		for i := 1; i <= count; i++ {
			list = append(list, map[string]any{"id": strconv.Itoa(i), "number": i})
		}
		episodes = list
	default:
		err = errors.New("站源详情协议不可用")
	}
	if err != nil {
		return Drama{}, nil, err
	}
	info := attachedObject(data)

	if info == nil && len(attachedRows(data)) > 0 {
		info = attachedRows(data)[0]
	}
	if !c.detailIdentity(info, id) {
		return Drama{}, nil, errors.New("站源返回详情与请求剧集不符")
	}
	drama := c.drama(info, id)
	if drama.ID == "" {
		drama = Drama{ID: providerDramaID(c.p.id, id), Source: c.p.id, SourceID: id}
	}
	out := []Chapter{}
	seen := map[string]bool{}
	for index, row := range attachedRows(episodes) {
		order := attachedInt(row, "playOrder", "chapterIndex", "episode_number", "number", "num", "serialno", "order", "sort", "index")
		if order < 1 {
			order = index + 1
		}
		key := mapString(row, "episode_token", "token", "chapterId", "vid", "id", "episode_id", "key")
		if key == "" {
			key = strconv.Itoa(order)
		}
		address := attachedBestURL(row)
		title := mapString(row, "son_title", "chapterName", "name", "title")
		switch c.p.id {
		case "weiguan":
			address = attachedBestURL(attachedFirst(row, "playSetting", "videoClarityList", "playUrl"))

		case "niuniudj":
			if !strings.HasPrefix(key, "t:") {
				key = "f" + key
			}
		case "yimi":
			if parsed, e := url.Parse(address); e == nil && strings.Contains(parsed.Hostname(), "zhangyuecdn") {
				parsed.Scheme = "https"
				parsed.Host = "mother-t.d.ireader.com"
				address = parsed.String()
			}
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, attachedChapter(c.p.id, id, key, order, title, address, ""))
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, _ := strconv.Atoi(out[i].EpisodeString(i))
		b, _ := strconv.Atoi(out[j].EpisodeString(j))
		return a < b
	})
	drama.TotalEpisode = len(out)
	drama.EpisodeCount = len(out)
	if len(out) == 0 {
		return Drama{}, nil, fmt.Errorf("%s未返回有效章节，请刷新详情或检查接口授权", c.p.name)
	}
	return drama, out, nil
}

func (c *attachedClient) detailIdentity(row map[string]any, id string) bool {
	var returned string
	switch c.p.id {
	case "hema":
		returned = mapString(row, "bookId")
	case "xingya", "qixing", "shanhai", "niuniudj", "kuangbiao":
		returned = mapString(row, "id")
	case "xifan":
		returned = mapString(row, "duanjuId", "duanjuID")
		id, _, _ = strings.Cut(id, "@")
	case "qimao", "haokan":
		returned = mapString(row, "playlet_id")

	case "yizk":
		returned = mapString(row, "token")
	}
	return returned == "" || returned == id
}

func (c *attachedClient) play(ctx context.Context, id, key string, chapter Chapter) (providerMedia, error) {
	if c.p.kind == "imported" {
		return c.importedPlay(ctx, id, chapter)
	}
	if c.p.kind == "html" {
		return c.htmlPlay(ctx, id, key, chapter)
	}
	address := chapter.VideoURL
	var data any
	var err error
	switch c.p.id {
	case "honeypeach":
		return c.peachPlay(ctx, id, key)
	case "hema":
		data, err = c.post(ctx, "/free-video-portal/portal/1133", map[string]any{"bookId": id, "chapterId": key, "autoPayFlag": false, "confirmPay": 0})
		if err == nil {
			if pay := mapString(attachedObject(data), "chaptersPayType"); pay != "免费" {
				return providerMedia{}, errors.New("此章节需要站源授权或未返回免费播放权限")
			}
			rows := attachedRows(attachedAt(data, "chapterInfo"))
			if len(rows) > 0 {
				if pay := mapString(rows[0], "chaptersPayType"); pay != "" && pay != "免费" && pay != "0" {
					return providerMedia{}, errors.New("此章节需要站源付费授权")
				}
				address = attachedBestURL(rows[0]["content"])
			}
		}
	case "haokan":
		data, err = c.form(ctx, "https://sv.baidu.com/appui/api?cmd=video/relate&log=vhk&tn=1020970b&ctn=1008350n&blur=1", attachedParams("method", "post", "vid", key))
		address = attachedBestURL(attachedAt(data, "video/relate", "data", "cur_video", "clarityUrl"))

	case "niuniudj":
		if strings.HasPrefix(key, "t:") {
			parts := strings.SplitN(strings.TrimPrefix(key, "t:"), ":", 2)
			if len(parts) != 2 {
				return providerMedia{}, errors.New("牛牛第三方章节参数无效")
			}
			number, e := strconv.Atoi(parts[1])
			if e != nil || number < 1 {
				return providerMedia{}, errors.New("牛牛第三方集数无效")
			}
			address, err = c.niuniuCSJMedia(ctx, parts[0], number)
			break
		}
		_, user, e := c.session(ctx)
		if e != nil {
			return providerMedia{}, e
		}
		data, err = c.post(ctx, "/api/v1/app/play/movieDetails", map[string]any{"id": niuniuID(strings.TrimPrefix(key, "f")), "episodeId": niuniuID(id), "source": 0, "typeId": "S1", "userId": user})
		address = attachedBestURL(data)

	case "yizk":
		sequence, _ := strconv.Atoi(chapter.EpisodeString(0))
		data, err = c.post(ctx, "/api/v1/films/"+url.PathEscape(id)+"/play", map[string]any{"episode": sequence, "source": "direct"})
		address = attachedBestURL(data)

	case "kuangbiao":
		data, err = c.rushCall(ctx, "episode.watch", map[string]any{"dramaId": id, "episodeNumber": attachedEpisodeNumber(chapter)})
		episode := attachedObject(attachedAt(data, "episode"))
		if mapString(episode, "id") != key {
			return providerMedia{}, errors.New("狂飙章节已调整，请刷新详情")
		}
		address = attachedBestURL(episode)
		if address == "" {
			address = "https://raw.shorttv.online/uploads/direct/" + url.PathEscape(key) + "/video.mp4"
		}

	}
	if err != nil {
		return providerMedia{}, err
	}
	if !isProviderHTTPMediaURL(address) {
		return providerMedia{}, fmt.Errorf("%s未返回可用播放地址，可能需要站源授权", c.p.name)
	}
	media := providerMedia{URL: address}
	if c.p.id == "niuniudj" {
		agent := c.agent()
		if strings.HasPrefix(key, "t:") {
			agent = c.access.Settings["csj_ua"]
		}
		media.credentials = &providerMediaCredentials{userAgent: agent}
	}

	return media, nil
}

func attachedEpisodeNumber(chapter Chapter) int {
	value, _ := strconv.Atoi(chapter.EpisodeString(0))
	return value
}
func (c *attachedClient) rushCall(ctx context.Context, method string, payload any) (any, error) {
	v, err := c.get(ctx, "/api/trpc/"+method, "input", attachedJSON(map[string]any{"json": payload}))
	if err != nil {
		return nil, err
	}
	return attachedAt(v, "data", "json"), nil
}
func (c *attachedClient) rushCatalog(ctx context.Context, page int, cat attachedCategory) ([]Drama, bool, error) {
	if page > 200 {
		return nil, false, errors.New("狂飙分页范围无效")
	}
	cursor := ""
	var data any
	for i := 1; i <= page; i++ {
		values := map[string]any{"limit": 12}
		switch cat.value {
		case "normal_short":
			values["contentKind"] = "SHORT_DRAMA"
		case "adult_short":
			values["tagSlug"] = "adult"
		default:
			values["categorySlug"] = cat.value
		}
		if cursor != "" {
			values["cursor"] = cursor
		}
		var err error
		data, err = c.rushCall(ctx, "feed.browse", values)
		if err != nil {
			return nil, false, err
		}
		cursor = mapString(attachedObject(data), "nextCursor")
		if i < page && cursor == "" {
			return nil, false, nil
		}
	}
	out := []Drama{}
	for _, row := range attachedRows(attachedAt(data, "items")) {
		if drama := c.drama(row, ""); drama.ID != "" {
			out = append(out, drama)
		}
	}
	return out, cursor != "", nil
}

func (c *attachedClient) opaqueHLS(ctx context.Context, media providerMedia) (providerMedia, error) {
	media.credentials = &providerMediaCredentials{userAgent: c.agent(), referer: media.Referer}
	body, finalURL, err := c.d.fetchMediaPlaylist(providerMediaContext(ctx, media.credentials), media.URL, media.Referer)
	if err != nil {
		return providerMedia{}, err
	}
	media.Playlist = body
	media.URL = finalURL
	if duration := m3u8Duration(body); duration > 0 {
		media.Duration = duration
	}
	return media, nil
}
