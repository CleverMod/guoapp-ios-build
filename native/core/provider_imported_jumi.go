package core

import (
	"context"
	"crypto/rc4"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

func jumiRC4(value, key string, decrypt bool) (string, error) {
	cipher, err := rc4.NewCipher([]byte(key))
	if err != nil {
		return "", errors.New("剧迷会话密钥缺失")
	}
	data := []byte(value)
	if decrypt {
		data, err = hex.DecodeString(value)
		if err != nil {
			return "", errors.New("剧迷加密响应格式无效")
		}
	}
	out := make([]byte, len(data))
	cipher.XORKeyStream(out, data)
	if decrypt {
		return string(out), nil
	}
	return hex.EncodeToString(out), nil
}

func jumiNotice(value string) (string, error) {
	if len(value) < 17 {
		return "", errors.New("剧迷发布配置格式无效")
	}
	outer, err := importedUnbase64(value[16:])
	if err != nil {
		return "", err
	}
	plain, err := importedUnbase64(string(outer))
	return string(plain), err
}

func (c *attachedClient) jumiInit(ctx context.Context) error {
	if len(c.imported.session) > 0 && time.Since(c.imported.configAt) < 30*time.Minute {
		return nil
	}
	settings := c.access.Settings
	seed, suffix := settings["numberSeed"], settings["numberSuffix"]
	if seed == "" || suffix == "" {
		return errors.New("剧迷内置配置密钥缺失")
	}
	data, err := c.importedJSON(ctx, settings["discoveryURL"], nil)
	if err != nil {
		return err
	}
	notice := attachedObject(attachedAt(data, "msg", "notice"))
	main, err := jumiNotice(mapString(notice, "content"))
	if err != nil || !isProviderHTTPMediaURL(main) {
		return errors.New("剧迷未返回有效配置入口")
	}
	authorization, err := jumiNotice(mapString(notice, "type"))
	if err != nil {
		return errors.New("剧迷配置授权无效")
	}
	numberKey := attachedMD5(seed + suffix)
	key, err := jumiRC4(attachedMD5(seed), numberKey, false)
	if err != nil {
		return err
	}
	body, err := c.importedRaw(ctx, http.MethodPost, attachedPath(main, attachedParams("app", settings["appID"])), "application/x-www-form-urlencoded", attachedParams("t", strconv.FormatInt(time.Now().Unix(), 10), "key", key).Encode(), map[string]string{"Authorization": authorization})
	if err != nil {
		return err
	}
	value, err := importedDecode(body)
	if err != nil {
		return err
	}
	row := attachedObject(attachedAt(value, "data"))
	mainKey, err := jumiRC4(mapString(row, "Maink"), numberKey, true)
	if err != nil {
		return err
	}
	state := map[string]string{"authorization": authorization}
	for field, target := range map[string]string{"pg": "pgKey", "yry": "yryKey", "MT": "keyTime"} {
		state[target], err = jumiRC4(mapString(row, field), mainKey, true)
		if err != nil {
			return err
		}
	}
	pg, yry := mapString(row, "pgUrl"), mapString(row, "yryUrl")
	if len(pg) < 33 || len(yry) < 17 {
		return errors.New("剧迷配置地址格式无效")
	}
	aesKey := base64.StdEncoding.EncodeToString([]byte(state["keyTime"] + pg[:14]))
	iv := base64.StdEncoding.EncodeToString([]byte(state["keyTime"] + yry[:2]))
	if len(iv) < 16 {
		iv += strings.Repeat(" ", 16-len(iv))
	}
	for _, entry := range []struct {
		key, value string
		skip       int
	}{{"pgURL", pg, 32}, {"yryURL", yry, 16}} {
		ciphertext, e := importedUnbase64(entry.value[entry.skip:])
		if e != nil {
			return errors.New("剧迷入口解码失败")
		}
		state[entry.key], err = importedCBCText(string(ciphertext), aesKey, iv)
		if err != nil || !isProviderHTTPMediaURL(state[entry.key]) {
			return errors.New("剧迷入口解密失败")
		}
		state[entry.key] = strings.TrimRight(state[entry.key], "/")
	}
	for field, target := range map[string]string{"HOST": "basePath", "newClient": "client"} {
		state[target], err = jumiRC4(mapString(row, field), state["pgKey"], true)
		if err != nil {
			return err
		}
	}
	state["loginMode"] = firstNonEmpty(mapString(row, "Login"), "4")
	c.imported.session = state
	c.imported.configAt = time.Now()
	c.token = ""
	return nil
}

func (c *attachedClient) jumiDecode(body string) (any, error) {
	if data, err := importedDecode(body); err == nil {
		return data, nil
	}
	plain, err := jumiRC4(strings.TrimSpace(body), c.imported.session["pgKey"], true)
	if err != nil {
		return nil, err
	}
	return importedDecode(plain)
}

func (c *attachedClient) jumiRequest(ctx context.Context, path string, values url.Values) (any, error) {
	state := c.imported.session
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	key, err := jumiRC4(ts, ts, false)
	if err != nil {
		return nil, err
	}
	if values == nil {
		values = url.Values{}
	}
	values.Set("key", key)
	values.Set("tt", ts)
	target := state["pgURL"] + "/api.php/" + state["basePath"] + "/" + path
	address, err := url.Parse(target)
	if err != nil {
		return nil, errors.New("剧迷影片请求路径无效")
	}
	query := address.Query()
	for key, value := range values {
		query[key] = value
	}
	address.RawQuery = query.Encode()
	body, err := c.importedRaw(ctx, http.MethodGet, address.String(), "", "", map[string]string{"Authorization": state["authorization"]})
	if err != nil {
		return nil, err
	}
	return c.jumiDecode(body)
}

func (c *attachedClient) jumiSigned(ctx context.Context, target, plain string) (any, error) {
	state := c.imported.session
	encrypted, err := jumiRC4(plain, state["yryKey"], false)
	if err != nil {
		return nil, err
	}
	form := attachedParams("data", encrypted, "sign", attachedMD5(plain+"&"+state["pgKey"]))
	body, err := c.importedRaw(ctx, http.MethodPost, target, "application/x-www-form-urlencoded", form.Encode(), map[string]string{"Authorization": state["authorization"]})
	if err != nil {
		return nil, err
	}
	return importedDecode(body)
}

func (c *attachedClient) jumiLogin(ctx context.Context) error {
	if c.token != "" && time.Now().Before(c.expiry) {
		return nil
	}
	s := c.imported.session
	user, pwd, mark := c.access.Query["username"], c.access.Query["password"], c.access.Query["markcode"]
	if user == "" || pwd == "" || mark == "" {
		user = c.device[:16]
		if s["loginMode"] == "3" {
			user = strings.ToUpper(c.device[:12])
		}
		if s["loginMode"] != "2" && s["loginMode"] != "3" {
			digits := ""
			for _, b := range []byte(c.device[:9]) {
				digits += strconv.Itoa(int(b) % 10)
			}
			user = digits
		}
		pwd = user
		mark = user
		host, err := c.importedRaw(ctx, http.MethodGet, s["yryURL"]+"/ip.json", "", "", nil)
		if err != nil {
			return err
		}
		host = strings.TrimSpace(host)
		if strings.ContainsAny(host, "/\r\n?#@") || host == "" {
			return errors.New("剧迷游客注册入口无效")
		}
		plain := "user=" + user + "&password=" + pwd + "&markcode=" + mark + "&t=" + strconv.FormatInt(time.Now().Unix(), 10) + "&name=xiaomi&phone=xiaomi"
		_, err = c.jumiSigned(ctx, "http://"+host+"/api.php?"+attachedParams("app", c.access.Settings["appID"], "act", "user_reg").Encode(), plain)
		if err != nil {
			return err
		}
	}
	plain := "account=" + user + "&password=" + pwd + "&markcode=" + mark + "&t=" + strconv.FormatInt(time.Now().Unix(), 10)
	data, err := c.jumiSigned(ctx, s["yryURL"]+"/api.php?"+attachedParams("app", c.access.Settings["appID"], "act", "user_logon").Encode(), plain)
	if err != nil {
		return err
	}
	msg := attachedObject(data)["msg"]
	row := attachedObject(msg)
	if row == nil {
		body := nativeText(msg)
		if plain, e := jumiRC4(body, s["yryKey"], true); e == nil {
			body = plain
		}
		decoded, e := importedDecode(body)
		if e != nil {
			return errors.New("剧迷游客登录授权无效")
		}
		row = attachedObject(decoded)
	}
	c.token = mapString(row, "token")
	s["user"] = user
	s["password"] = pwd
	c.expiry = time.Now().Add(30 * time.Minute)
	if c.token == "" {
		return errors.New("剧迷未返回有效游客授权")
	}
	return nil
}

func (c *attachedClient) jumiCatalog(ctx context.Context, page int, category, query string) ([]Drama, bool, error) {
	if err := c.jumiInit(ctx); err != nil {
		return nil, false, err
	}
	if category == "" || category == "movie" || category == "tv" {
		data, err := c.jumiRequest(ctx, "Category", nil)
		if err != nil {
			return nil, false, err
		}
		for _, row := range attachedRows(data) {
			if mapString(row, "type_status") == "1" {
				category = mapString(row, "type_en")
				break
			}
		}
	}
	path, values := "vod/", attachedParams("ac", "list", "class", category, "page", strconv.Itoa(page))
	if query != "" {
		path = "So/"
		values = attachedParams("ac", "list", "zm", query, "page", strconv.Itoa(page))
	}
	data, err := c.jumiRequest(ctx, path, values)
	if err != nil {
		return nil, false, err
	}
	rows := c.importedDramas(attachedAt(data, "data"))
	return rows, attachedInt(attachedObject(data), "totalpage") > page || query != "" && len(rows) > 0, nil
}

func (c *attachedClient) jumiDetail(ctx context.Context, id string) (Drama, []Chapter, error) {
	if err := c.jumiInit(ctx); err != nil {
		return Drama{}, nil, err
	}
	if err := c.jumiLogin(ctx); err != nil {
		return Drama{}, nil, err
	}
	s := c.imported.session
	data, err := c.jumiSigned(ctx, s["yryURL"]+"/api.php?"+attachedParams("app", c.access.Settings["appID"], "act", "motion").Encode(), "token="+c.token+"&t="+strconv.FormatInt(time.Now().Unix(), 10))
	if err != nil {
		return Drama{}, nil, err
	}
	motion := attachedObject(attachedAt(data, "msg"))
	if mapString(motion, "Clientmode") == "0" && mapString(motion, "Try") != "1" {
		return Drama{}, nil, errors.New("剧迷此影片需要有效源站授权")
	}
	data, err = c.jumiRequest(ctx, "vod/"+id, nil)
	if err != nil {
		return Drama{}, nil, err
	}
	row := attachedObject(data)
	expected := id
	if query, err := url.ParseQuery(strings.TrimPrefix(id, "?")); err == nil && query.Get("ids") != "" {
		expected = query.Get("ids")
	}
	if remote := mapString(row, "id", "vod_id"); remote != "" && remote != expected {
		return Drama{}, nil, errors.New("剧迷返回的影片标识不符")
	}
	row["id"] = id
	video := attachedObject(row["videolist"])
	players := []map[string]any{}
	text := mapString(row, "player")
	if plain, e := jumiRC4(text, s["pgKey"], true); e == nil {
		text = plain
	}
	var decoded any
	if json.Unmarshal([]byte(text), &decoded) == nil {
		players = attachedRows(decoded)
	}
	keys := []string{}
	for key := range video {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := []importedLine{}
	for _, key := range keys {
		name := key
		for _, p := range players {
			if mapString(p, "from") == key {
				name = firstNonEmpty(mapString(p, "show"), key)
				break
			}
		}
		line := importedLine{key: key, name: name}
		for _, ep := range attachedRows(video[key]) {
			line.episodes = append(line.episodes, importedEpisode{name: mapString(ep, "title"), url: mapString(ep, "url"), series: mapString(row, "title") + "-" + mapString(ep, "title")})
		}
		if len(line.episodes) > 0 {
			lines = append(lines, line)
		}
	}
	return c.importedDetailResult(id, row, lines)
}

func (c *attachedClient) jumiPlay(ctx context.Context, p importedPayload) (providerMedia, error) {
	if err := c.jumiInit(ctx); err != nil {
		return providerMedia{}, err
	}
	if err := c.jumiLogin(ctx); err != nil {
		return providerMedia{}, err
	}
	s := c.imported.session
	client := s["client"]
	if !isProviderHTTPMediaURL(client) {
		return providerMedia{}, errors.New("剧迷未提供内置解析入口")
	}
	basePath := "&account=" + s["user"] + "&password=" + s["password"] + "&series=" + url.QueryEscape(p.Series) + "&edition=1.0"
	key, err := jumiRC4(basePath, s["yryKey"], false)
	if err != nil {
		return providerMedia{}, err
	}
	for i := 1; i <= 3; i++ {
		target := client
		if i > 1 {
			target += strconv.Itoa(i)
		}
		body, e := c.importedRaw(ctx, http.MethodPost, target+"/?url="+strings.ReplaceAll(url.QueryEscape(p.URL), "+", "%20"), "application/x-www-form-urlencoded", attachedParams("app", c.access.Settings["appID"], "key", key, "", "").Encode(), map[string]string{"Authorization": s["authorization"]})
		if e != nil {
			continue
		}
		data, e := c.jumiDecode(body)
		if e != nil {
			continue
		}
		address := mapString(attachedObject(data), "url")
		if importedMediaLike(address) {
			return importedURLMedia(address, "Windows", "", nil)
		}
		if maximum := attachedInt(attachedObject(data), "maxClient"); maximum > 0 && i >= maximum {
			break
		}
	}
	return providerMedia{}, errors.New("剧迷未返回所选章节的可播放媒体")
}
