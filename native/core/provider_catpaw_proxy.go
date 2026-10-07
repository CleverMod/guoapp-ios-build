package core

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (d *Downloader) catpawHelper(id string) (*attachedClient, error) {
	d.attachedMu.Lock()
	defer d.attachedMu.Unlock()
	if d.attachedClients == nil {
		d.attachedClients = map[string]*attachedClient{}
	}
	if cached := d.attachedClients[id]; cached != nil {
		return cached, nil
	}
	valid := false
	for _, entry := range catpawDefinitions {
		valid = valid || entry.ID == id && entry.Helper
	}
	if !valid {
		return nil, errors.New("未知 CatPaw 辅助协议")
	}
	c := &attachedClient{d: d, p: attachedProvider{id: id, name: "内置播放解析", kind: "catpaw"}, access: d.attachedAccess[id], device: attachedNonce()}
	if err := c.catpawLoad(); err != nil {
		return nil, err
	}
	d.attachedClients[id] = c
	return c, nil
}

func catpawProxySupports(params url.Values, target string) bool {
	from, prefix, include := params.Get("from"), params.Get("prefix"), params.Get("include")
	if from == "" && prefix == "" && include == "" {
		return true
	}
	domains := map[string]string{"qq": "qq.com", "qiyi": "iqiyi.com", "youku": "youku.com", "mgtv": "mgtv.com", "bili": "bilibili.com"}
	if from == "all" {
		for _, domain := range domains {
			if strings.Contains(target, domain) {
				return true
			}
		}
	}
	for _, key := range strings.Split(from, ",") {
		if domain := domains[key]; domain != "" && strings.Contains(target, domain) {
			return true
		}
	}
	for _, value := range strings.Split(prefix, ",") {
		if value != "" && strings.HasPrefix(target, value) {
			return true
		}
	}
	for _, value := range strings.Split(include, ",") {
		if value != "" && strings.Contains(target, value) {
			return true
		}
	}
	return false
}

func (c *attachedClient) cpProxy(ctx context.Context, params url.Values) (string, map[string]string, error) {
	kind := params.Get("type")
	id := "catpaw_proxy_" + kind
	if kind == "ryccjx" {
		id = "catpaw_proxy_ryjx"
	}
	helper, err := c.d.catpawHelper(id)
	if err != nil {
		return "", nil, err
	}
	helper.mu.Lock()
	defer helper.mu.Unlock()
	data, err := helper.cpProxyData(ctx, params)
	if err != nil {
		return "", nil, err
	}
	address := catpawResponseURL(data)
	if !isProviderHTTPMediaURL(address) {
		return "", nil, errors.New("内置辅助解析未返回有效地址")
	}
	headers := catpawParserHeaders(data)
	if strings.Contains(params.Get("v"), "bilibili.com") {
		if headers["Referer"] == "" {
			headers["Referer"] = "https://www.bilibili.com/"
		}
		if headers["User-Agent"] == "" {
			headers["User-Agent"] = catpawDesktopAgent
		}
	}
	if strings.Contains(params.Get("v"), "mgtv.com") && headers["User-Agent"] == "" {
		headers["User-Agent"] = "MGDS/Android/2.0.5"
	}
	return address, headers, nil
}

func (c *attachedClient) cpProxyData(ctx context.Context, params url.Values) (any, error) {
	target, api := params.Get("v"), params.Get("api")
	kind := params.Get("type")
	if kind == "drama" {
		return c.cpDramaParams(ctx, params)
	}
	if target == "" || !catpawProxySupports(params, target) {
		return nil, errors.New("此辅助解析不支持当前地址")
	}
	switch kind {
	case "artjx":
		t := strconv.FormatInt(time.Now().Unix(), 10)
		origin := ""
		if parsed, e := url.Parse(api); e == nil {
			origin = parsed.Host
		}
		raw, err := c.cpRaw(ctx, http.MethodPost, api, "application/x-www-form-urlencoded",
			attachedParams("url", target, "time", t, "key", attachedMD5(firstNonEmpty(params.Get("md5Salt"), "123456789")+target+t)).Encode(),
			map[string]string{"User-Agent": catpawDesktopAgent, "Origin": origin, "X-Requested-With": "XMLHttpRequest"})
		if err != nil {
			return nil, err
		}
		value, err := catpawParseJSON(raw)
		if err != nil {
			return nil, err
		}
		row := attachedObject(value)
		plain, err := catpawCBCDecode(mapString(row, "url"), firstNonEmpty(params.Get("key"), c.cpConst("const1")), firstNonEmpty(params.Get("iv"), c.cpConst("const2")), false)
		if err != nil {
			return nil, errors.New("辅助解析响应解密失败")
		}
		row["url"] = plain
		delete(row, "user-agent")
		return row, nil
	case "ryjx":
		retry, _ := strconv.Atoi(params.Get("retry"))
		retry = max(0, min(2, retry))
		for i := 0; i <= retry; i++ {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			raw, err := c.cpRaw(ctx, http.MethodGet, api+target, "", "", nil)
			if err != nil {
				continue
			}
			data, err := catpawParseJSON(raw)
			if err != nil {
				continue
			}
			row := attachedObject(data)
			address, err := c.cpRYDecrypt(mapString(row, "url"))
			if err != nil || !isProviderHTTPMediaURL(address) || address == target || strings.Contains(address, "act=ZhiBaiQq") {
				continue
			}
			row["url"] = address
			return row, nil
		}
	case "ryccjx":
		if !strings.Contains(api, "act=jxjm") || len(params.Get("key")) < 16 || params.Get("appkey") == "" {
			return nil, errors.New("辅助解析签名配置缺失")
		}
		t := strconv.FormatInt(time.Now().Unix(), 10)
		base, line, ok := strings.Cut(api, "id=")
		if !ok {
			return nil, errors.New("辅助解析缺少线路标识")
		}
		payload := attachedParams("url", target, "id", line, "t", t, "sign", attachedMD5("url="+target+"&id="+line+"&t="+t+"&"+params.Get("appkey")))
		raw, err := c.cpRaw(ctx, http.MethodPost, base+"url="+target+"&app=10000", "application/x-www-form-urlencoded", payload.Encode(), nil)
		if err != nil {
			return nil, err
		}
		key := params.Get("key")
		plain, err := catpawCBCDecode(raw, key[:16], key[len(key)-16:], false)
		if err != nil {
			return nil, err
		}
		return catpawParseJSON(plain)
	case "fyjx":
		if parser := c.cpValue("jxAPI"); parser != "" {
			raw, err := c.cpRaw(ctx, http.MethodGet, parser+target, "", "", nil)
			if err == nil {
				return catpawParseJSON(raw)
			}
			c.catpaw.values["jxAPI"] = ""
		}
		discovery := "http://fy4k.fc8001.top/api.php?act=ini&app=10000&pay="
		t := strconv.FormatInt(time.Now().Unix(), 10)
		raw, err := c.cpRaw(ctx, http.MethodPost, discovery, "application/x-www-form-urlencoded", attachedParams("t", t, "sign", attachedMD5("pay&t="+t+"&"+c.cpConst("const1"))).Encode(), nil)
		if err != nil {
			return nil, err
		}
		data, err := catpawParseJSON(raw)
		if err != nil {
			raw, err = jumiRC4(raw, "fy4k.fc8001.top", true)
			if err != nil {
				return nil, err
			}
			data, err = catpawParseJSON(raw)
		}
		if err != nil {
			return nil, err
		}
		for _, parser := range catpawRows(attachedAt(data, "msg", "analysis")) {
			if mapString(parser, "encry") != "n" {
				continue
			}
			api = mapString(parser, "url")
			raw, err = c.cpRaw(ctx, http.MethodGet, api+target, "", "", nil)
			if err != nil {
				continue
			}
			response, e := catpawParseJSON(raw)
			if e == nil && isProviderHTTPMediaURL(catpawResponseURL(response)) {
				c.catpaw.values["jxAPI"] = api
				return response, nil
			}
		}
	}
	return nil, errors.New("内置辅助解析失败")
}

func (c *attachedClient) cpRYDecrypt(value string) (string, error) {
	original := value
	if strings.Contains(value,"baidu.con/") && strings.Contains(value,":") {
		clean := strings.ReplaceAll(strings.TrimPrefix(strings.TrimPrefix(value,"https://baidu.con/"),"http://baidu.con/"),"lvDou+","")
		if pieces := strings.SplitN(clean,":",4); len(pieces)==4 {
			if plain,e := catpawCBCDecode(pieces[3],pieces[1],pieces[2],false); e==nil && isProviderHTTPMediaURL(plain) { return plain,nil }
		}
	}
	if strings.HasPrefix(value, "https://vod-parses.baidu.com/") {
		value = strings.Trim(strings.TrimPrefix(value, "https://vod-parses.baidu.com/"), "\"")
		aesPart, rsaPart, ok := strings.Cut(value, "81238")
		if !ok {
			return "", errors.New("辅助解析密文分隔符缺失")
		}
		modulus := mapString(c.catpaw.defaults, "n")
		n, valid := new(big.Int).SetString(modulus, 10)
		if !valid {
			return "", errors.New("辅助解析 RSA 配置缺失")
		}
		keyText, err := catpawRSAPublicDecode(rsaPart, &rsa.PublicKey{N: n, E: 65537})
		if err != nil {
			return "", err
		}
		key, iv, ok := strings.Cut(keyText, "|")
		if !ok {
			return "", errors.New("辅助解析媒体密钥缺失")
		}
		return catpawCBCDecode(aesPart, key, iv, false)
	}
	if strings.Contains(value, "lvDou+") || strings.HasPrefix(value, "lvdou+") {
		value = strings.TrimPrefix(strings.TrimPrefix(value,"https://baidu.con/"),"http://baidu.con/")
		value = strings.TrimPrefix(strings.TrimPrefix(strings.ReplaceAll(value, "\"", ""), "lvDou+"), "lvdou+")
		if len(value) < 33 {
			return "", errors.New("辅助解析密文长度不足")
		}
		key := sha256.Sum256([]byte(value[:32] + c.cpConst("const1")))
		iv := sha256.Sum256([]byte(value[:32] + c.cpConst("ryIVSalt")))
		plain, err := catpawCBCDecode(value[32:], string(key[:]), string(iv[:16]), false)
		if err != nil {
			return "", err
		}
		if data, e := catpawParseJSON(plain); e == nil {
			row := attachedObject(data)
			fields := attachedObject(row["_f"])
			if field := mapString(fields, "data"); field != "" {
				if raw, e := importedUnbase64(mapString(row, field)); e == nil {
					plain = string(raw)
				}
			}
		}
		return plain, nil
	}
	if strings.HasPrefix(value, "https://6max.con/") {
		value = strings.TrimPrefix(value, "https://6max.con/")
		if len(value) < 17 {
			return "", errors.New("辅助解析密文长度不足")
		}
		key := catpawReverse(value[:16])
		return catpawCBCDecode(value[16:], key, key, false)
	}
	if strings.Contains(value, "baidu.con/") {
		clean := strings.TrimPrefix(strings.TrimPrefix(value, "https://baidu.con/"), "http://baidu.con/")
		if pieces := strings.SplitN(clean, ":", 4); len(pieces) == 4 {
			if plain, e := catpawCBCDecode(pieces[3], pieces[1], pieces[2], false); e == nil {
				return plain, nil
			}
		}
		for i := 0; i < 5 && strings.Contains(value, "baidu.con/"); i++ {
			clean = strings.TrimPrefix(strings.TrimPrefix(value, "https://baidu.con/"), "http://baidu.con/")
			if len(clean) < 17 {
				break
			}
			plain, err := catpawCBCDecode(clean[16:], clean[:16], clean[:16], false)
			if err != nil {
				break
			}
			value = plain
		}
		if value != original && isProviderHTTPMediaURL(value) {
			return value, nil
		}
	}
	for _, input := range []string{value, original} {
		clean := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(input, "https://baidu.con/"), "http://baidu.con/"), "https://vod.baidu.com/")
		if strings.HasPrefix(clean, "2423") && len(clean) > 6 {
			clean = clean[6:]
		}
		raw, err := importedUnbase64(clean)
		if err != nil {
			continue
		}
		t := strconv.FormatInt(time.Now().Unix(), 10)[:8]
		xored := append([]byte{}, raw...)
		for i := range xored {
			xored[i] ^= t[i%len(t)]
		}
		if plain, e := importedUnbase64(string(xored)); e == nil && isProviderHTTPMediaURL(string(plain)) {
			return string(plain), nil
		}
		first, second, ok := strings.Cut(string(raw), "|")
		key := c.cpConst("yd_xor_key")
		if ok && len(first) > 0 && key != "" {
			mask := []byte(first)
			for i := range mask {
				mask[i] ^= key[i%len(key)]
			}
			out := []byte(second)
			for i := range out {
				out[i] ^= byte((int(mask[(i+5)%len(mask)]) + int(mask[i%len(mask)])) % 256)
			}
			if isProviderHTTPMediaURL(string(out)) {
				return string(out), nil
			}
		}
	}
	if isProviderHTTPMediaURL(original) && !strings.Contains(original, "baidu.con") && !strings.Contains(original, "vod.baidu.com") {
		return original, nil
	}
	return "", errors.New("辅助解析媒体地址解密失败")
}

func (c *attachedClient) cpDramaParams(ctx context.Context, params url.Values) (any, error) {
	if params.Get("from") == "host" {
		return c.cpDiscover(ctx, params.Get("url"))
	}
	verName, ver := params.Get("verName"), params.Get("ver")
	if ver == "" {
		ver = strings.ReplaceAll(verName, ".", "")
	}
	if verName == "" || ver == "" || params.Get("AppName") == "" || params.Get("pkg") == "" || params.Get("publicKey") == "" {
		return nil, errors.New("设备协议配置缺失")
	}
	t := strconv.FormatInt(time.Now().UnixMilli(), 10)
	random := ""
	alphabet := c.cpConst("const7")
	for attempts:=0;len(random)<15 && attempts<256;attempts++ {
		char := catpawRandomString(1, alphabet)
		if char != "" && !strings.Contains(random, char) {
			random += char
		}
	}
	if len(random)!=15 { return nil,errors.New("无法生成设备签名随机数") }
	random += "="
	key, err := catpawRSAPublic(strings.ReplaceAll(params.Get("publicKey"), " ", "+"))
	if err != nil {
		return nil, err
	}
	sig, err := rsa.EncryptPKCS1v15(rand.Reader, key, []byte(t+random+ver))
	if err != nil {
		return nil, err
	}
	sign, err := attachedECB([]byte(t+random), []byte(c.cpConst("const2")))
	if err != nil {
		return nil, err
	}
	signText := base64.StdEncoding.EncodeToString(sign)
	uuid := strings.ToUpper(attachedMD5(c.cpConst("const1") + "02:00:00:00:00:00" + "23113RKC6C" + "Xiaomi"))
	device := map[string]any{"country": "CN", "vName": verName, "cpuId": "", "young": 0, "facturer": "Xiaomi", "pkg": params.Get("pkg"), "uuid": uuid, "resolution": "900x1600", "mac": url.QueryEscape("02:00:00:00:00:00"), "sig": base64.StdEncoding.EncodeToString(sig), "abid": "6249", "model": "23113RKC6C", "plat": "android", "udid": uuid, "dpi": "240", "net": "1", "lang": "zh", "random_str": random, "brand": "Redmi", "timestamp": json.Number(t), "density": "3.25", "appName": url.QueryEscape(params.Get("AppName")), "cpu": "arm64-v8a", "chid": "10000", "carrier": url.QueryEscape("移动"), "sig2": signText[:8], "sig3": signText[8:], "_vOsCode": "32", "vOs": "12", "vApp": ver, "device": "0", "androidID": c.cpConst("const1")}
	encrypted, err := catpawCBCEncode(attachedJSON(device), c.cpConst("const3"), c.cpConst("const4"))
	if err != nil {
		return nil, err
	}
	bytes, err := importedUnbase64(encrypted)
	return hex.EncodeToString(bytes), err
}
