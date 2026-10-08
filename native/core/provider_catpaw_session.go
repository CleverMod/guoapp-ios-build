package core

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/cipher"
	"crypto/des"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

func catpawSHA1(value string) string {
	sum := sha1.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

func catpawSortedParams(params url.Values) string {
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := []string{}
	for _, key := range keys {
		values = append(values, key+"="+params.Get(key))
	}
	return strings.Join(values, "&")
}

func catpawReverse(value string) string {
	data := []byte(value)
	for i, j := 0, len(data)-1; i < j; i, j = i+1, j-1 {
		data[i], data[j] = data[j], data[i]
	}
	return string(data)
}

func catpawRSAPublic(value string) (*rsa.PublicKey, error) {
	if block, _ := pem.Decode([]byte(value)); block != nil {
		if key, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
			if public, ok := key.(*rsa.PublicKey); ok {
				return public, nil
			}
		}
		if key, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
			return key, nil
		}
	}
	data, err := importedUnbase64(value)
	if err == nil {
		if key, e := x509.ParsePKIXPublicKey(data); e == nil {
			if public, ok := key.(*rsa.PublicKey); ok {
				return public, nil
			}
		}
	}
	return nil, errors.New("站源内置 RSA 参数无效")
}

func catpawRSAPublicDecode(value string, key *rsa.PublicKey) (string, error) {
	data, err := importedUnbase64(value)
	if err != nil || key == nil || len(data) > key.Size() {
		return "", errors.New("站源 RSA 响应无效")
	}
	input := new(big.Int).SetBytes(data)
	if input.Cmp(key.N) >= 0 {
		return "", errors.New("站源 RSA 响应超出范围")
	}
	out := new(big.Int).Exp(input, big.NewInt(int64(key.E)), key.N).FillBytes(make([]byte, key.Size()))
	if len(out) < 11 || out[0] != 0 || (out[1] != 1 && out[1] != 2) {
		return "", errors.New("站源 RSA 填充无效")
	}
	for i := 2; i < len(out); i++ {
		if out[i] == 0 && i >= 10 {
			return string(out[i+1:]), nil
		}
	}
	return "", errors.New("站源 RSA 响应缺少分隔符")
}

func (c *attachedClient) cpYiToken(ctx context.Context) error {
	data, err := c.cpRaw(ctx, http.MethodPost, "/vod-app/index/getGenerateKey", "application/x-www-form-urlencoded",
		attachedParams("appID", c.cpValue("appID"), "timestamp", strconv.FormatInt(time.Now().Unix(), 10)).Encode(),
		map[string]string{"APP-ID": c.cpValue("appID"), "X-Auth-Flow": "1"})
	if err != nil {
		return err
	}
	value, err := catpawParseJSON(data)
	if err != nil {
		return err
	}
	key, err := catpawRSAPublic(c.cpConst("const3"))
	if err != nil {
		return err
	}
	c.catpaw.values["token"], err = catpawRSAPublicDecode(mapString(attachedObject(value), "data"), key)
	if err == nil && c.cpValue("token") == "" {
		err = errors.New("壹影视未返回有效签名令牌")
	}
	return err
}

func (c *attachedClient) catpawBootstrap(ctx context.Context) error {
	s := c.catpaw
	var err error
	switch s.script {
	case "八戒影视.py":
		var raw string
		raw, err = c.cpRaw(ctx, http.MethodGet, "http://osstexll.oss-rg-china-mainland.aliyuncs.com/domainPath.json", "", "", nil)
		if err != nil {
			break
		}
		var data any
		data, err = catpawParseJSON(raw)
		domains := catpawStrings(attachedAt(data, "url"))
		if err == nil && len(domains) > 0 {
			s.base = strings.TrimRight(domains[0], "/")
		} else {
			return errors.New("八戒未返回可用线路")
		}
		s.headers["deviceId"] = c.device
		var login any
		login, err = c.cpGet(ctx, "/api/v1/app/user/visitorInfo", nil)
		if err == nil {
			s.values["userID"] = mapString(attachedObject(attachedAt(login, "data")), "id")
			s.headers["token"] = mapString(attachedObject(attachedAt(login, "data")), "token")
			if s.headers["token"] == "" {
				return errors.New("八戒访客授权未返回有效令牌")
			}
		}
	case "壹影视.py":
		if s.values["appID"] == "" {
			s.values["appID"] = c.device[:16]
		}
		err = c.cpYiToken(ctx)
	case "cycapp.py":
		err = c.cpCycInit(ctx)
	case "XinJie.py":
		if !strings.HasPrefix(s.base, "http") {
			s.base, err = catpawCBCDecode(s.base, firstNonEmpty(c.cpExt("hostkey"), c.cpConst("const1")), firstNonEmpty(c.cpExt("hostiv"), c.cpConst("const2")), false)
		}
		if err == nil && !catpawBareHost(s.base) {
			s.base, err = c.cpDiscover(ctx, s.base)
		}
		s.values["dataKey"] = c.cpExt("key")
		if len(s.values["dataKey"]) < 32 {
			s.values["dataKey"] += strings.Repeat("0", 32-len(s.values["dataKey"]))
		}
		s.values["dataIV"] = c.cpExt("iv")
	case "skapp.py":
		if !catpawBareHost(s.base) {
			s.base, err = c.cpDiscover(ctx, s.base)
		}
		if err != nil {
			break
		}
		key, iv := c.cpExt("key"), c.cpExt("iv")
		s.values["dataKey"], s.values["dataIV"] = key, iv
		check := base64.StdEncoding.EncodeToString([]byte(s.base + "##5483##" + strconv.FormatInt(time.Now().UnixMilli(), 10) + "##ckzmbc"))
		check = base64.StdEncoding.EncodeToString([]byte(check))
		var encrypted string
		encrypted, err = catpawCBCEncode(check, firstNonEmpty(c.cpExt("ckkey"), c.cpConst("const1")), firstNonEmpty(c.cpExt("ckiv"), c.cpConst("const2")))
		if err != nil {
			break
		}
		var binary []byte
		binary, err = importedUnbase64(encrypted)
		if err != nil {
			break
		}
		var raw string
		raw, err = c.cpRaw(ctx, http.MethodPost, "/get_config", "application/json; charset=utf-8", attachedJSON(map[string]string{
			"sign": attachedMD5(key + iv), "ck": base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(binary))),
		}), nil)
		if err != nil {
			break
		}
		if strings.HasPrefix(raw, "FROMSKZZJM") {
			raw, err = catpawCBCDecode(strings.TrimPrefix(raw, "FROMSKZZJM"), key, iv, true)
		}
		if err != nil {
			break
		}
		if raw == "" {
			return errors.New("站源未返回有效会话")
		}
		s.headers["authorization"] = "Bearer " + strings.Trim(raw, "\"")
		var config any
		config, err = c.cpGet(ctx, "/app/config", nil)
		s.config = attachedObject(config)
	case "AppV2.py":
		api := firstNonEmpty(c.cpExt("api"), c.cpExt("host"))
		if domain, suffix, found := strings.Cut(api, "$"); found {
			domain, err = c.cpDiscover(ctx, domain)
			api = domain + suffix
		}
		s.values["api"] = strings.TrimRight(api, "/")
		if u, e := url.Parse(api); e == nil {
			s.base = providerMediaOrigin(u)
		}
		if ua := c.cpExt("ua"); ua != "" {
			s.headers["User-Agent"] = ua
		}
	case "AppMuou.py":
		err = c.cpMuouInit(ctx)
	case "Appfox.py":
		if !catpawBareHost(s.base) {
			s.base, err = c.cpDiscover(ctx, s.base)
		}
		if err != nil {
			break
		}
		s.values["appKey"], s.values["appSign"] = c.cpExt("key"), c.cpExt("sign")
		s.values["ver"] = firstNonEmpty(c.cpExt("ver"), "2")
		if c.cpValue("appKey") != "" && c.cpValue("appSign") != "" {
			s.values["ver"] = "3"
		}
		if s.values["ver"] == "3" && s.values["appKey"] == "" {
			var config any
			config, err = c.cpGet(ctx, "/api.php/Appfox/config", nil)
			if err == nil {
				s.config = attachedObject(attachedAt(config, "data"))
				s.values["appKey"], s.values["appSign"] = mapString(s.config, "app_key"), mapString(s.config, "app_sign")
			}
		}
	case "ApptoV5无加密.py":
		if !catpawBareHost(s.base) {
			s.base, err = c.cpDiscover(ctx, s.base)
		}
		if err != nil {
			break
		}
		s.headers[c.cpConst("const1")] = uuidLike()
		var data any
		data, err = c.cpGet(ctx, "/apptov5/v1/config/get", attachedParams("p", "android", "__platform", "android"))
		s.config = attachedObject(attachedAt(data, "data"))
	case "getapp3.4.6.py":
		err = c.cpGetappInit(ctx)
	case "Hmys.py":
		s.headers["appid"], s.headers["Channel"], s.headers["Version-Code"] = c.cpExt("app_id"), c.cpExt("UMENG_CHANNEL"), c.cpExt("versionCode")
		s.headers["Device-Id"] = firstNonEmpty(c.cpExt("deviceid"), c.device[:16])
		s.headers["Cur-Time"] = strconv.FormatInt(time.Now().UnixMilli(), 10)
		var login any
		login, err = c.cpPost(ctx, "/api/user/init", attachedParams("password", "", "account", ""))
		if err == nil {
			result := attachedObject(attachedAt(login, "result"))
			s.headers["token"] = mapString(attachedObject(result["user_info"]), "token")
			conf := attachedObject(result["sys_conf"])
			s.values["playDomain"] = mapString(conf, "play_domain")
			if main := mapString(conf, "host_main"); isProviderHTTPMediaURL(main) {
				s.base = strings.TrimRight(main, "/")
			}
			if s.headers["token"] == "" {
				return errors.New("九霄访客登录未返回有效令牌")
			}
		}
	case "99APP2.py":
		err = c.cp99Init(ctx)
	case "开端.py":
		s.base = firstNonEmpty(s.base, "https://api.kaiduan.fun")
		s.headers["referer"] = s.base + "/"
		s.headers["Cookie"] = s.base + "/"
	case "247看.py":
		s.base = firstNonEmpty(s.base, "https://app.247kan.com")
		if key := c.cpExt("xapikey"); key != "" {
			s.headers["x-api-key"] = key
		}
	case "新浪资源.py":
		s.base = "https://api.xinlangapi.com/xinlangapi.php/provide/vod/from/xlyun/at/xml/"
	}
	return err
}

func (c *attachedClient) cpMuouInit(ctx context.Context) error {
	s := c.catpaw
	if !catpawBareHost(s.base) {
		base, err := c.cpDiscover(ctx, s.base)
		if err != nil {
			return err
		}
		s.base = base
	}
	name, api := firstNonEmpty(c.cpExt("name"), "muou"), firstNonEmpty(c.cpExt("api"), "/app_info.php")
	t := strconv.FormatInt(time.Now().Unix(), 10)
	s.headers["app-version"], s.headers["app-time"] = firstNonEmpty(c.cpExt("version"), "4.2.0"), t
	inner := catpawSHA1(t + name)
	salt := ""
	if strings.HasSuffix(api, "app_info.php") {
		salt = "muouapp"
	}
	outer := catpawSHA1(t + inner + salt)
	raw, err := c.cpRaw(ctx, http.MethodPost, api, "application/x-www-form-urlencoded", attachedParams("t", t, "n", inner, "m", outer).Encode(), nil)
	if err != nil {
		return err
	}
	value, err := catpawParseJSON(raw)
	if err != nil {
		return err
	}
	row := attachedObject(value)
	ciphertext := mapString(row, "data")
	a := mapString(row, "a")
	start, end := attachedInt(row, "s"), attachedInt(row, "e")
	if a != "" && start > 0 && end > 0 {
		if start+end > len(ciphertext) {
			return errors.New("站源配置密文长度无效")
		}
		ciphertext = ciphertext[start : len(ciphertext)-end]
	} else {
		a = mapString(row, "time")
	}
	plain, err := catpawCBCDecode(ciphertext, attachedMD5(a)[:16], attachedMD5(outer)[:16], false)
	if err != nil {
		return err
	}
	config, err := catpawParseJSON(plain)
	if err != nil {
		return err
	}
	conf := attachedObject(config)
	if key, iv := mapString(conf, "key"), mapString(conf, "iv"); key != "" && iv != "" {
		s.values["dataKey"], s.values["dataIV"] = attachedMD5(key)[:16], attachedMD5(iv)[:16]
	} else {
		return errors.New("站源未返回内容密钥")
	}
	s.values["appHost"], s.values["jxAPI"] = s.base, mapString(conf, "HBrjjg")
	s.base = strings.TrimRight(mapString(conf, "HBqq"), "/")
	return nil
}

func (c *attachedClient) cpGetappInit(ctx context.Context) error {
	s := c.catpaw
	s.values["dataKey"] = c.cpExt("key", "datakey")
	s.values["dataIV"] = firstNonEmpty(c.cpExt("iv", "dataiv"), s.values["dataKey"])
	s.values["getType"] = firstNonEmpty(c.cpExt("api"), "get")
	prefix := "/api.php/getappapi"
	if s.values["getType"] == "qiji" || s.values["getType"] == "2" {
		prefix = "/api.php/qijiappapi"
	}
	if !catpawBareHost(s.base) {
		base, err := c.cpDiscover(ctx, s.base)
		if err != nil {
			return err
		}
		s.base = base
	}
	s.values["api"] = s.base + prefix
	s.values["device"] = firstNonEmpty(c.cpExt("devideid"), c.device)
	if token := c.cpExt("token"); token != "" {
		s.headers["app-user-token"] = token
	}
	if ua := c.cpExt("ua"); ua != "" {
		s.headers["User-Agent"] = ua
	}
	value, err := c.cpGet(ctx, s.values["api"]+".index/initV119", nil)
	if err != nil {
		return err
	}
	s.config = attachedObject(value)
	return nil
}

func (c *attachedClient) cpDecode(raw string) (any, error) {
	s := c.catpaw
	var err error
	switch s.script {
	case "skapp.py":
		if strings.HasPrefix(raw, "FROMSKZZJM") {
			raw, err = catpawCBCDecode(strings.TrimPrefix(raw, "FROMSKZZJM"), c.cpValue("dataKey"), c.cpValue("dataIV"), true)
		}
	case "AppMuou.py":
		if !json.Valid([]byte(raw)) {
			raw, err = catpawCBCDecode(raw, c.cpValue("dataKey"), c.cpValue("dataIV"), false)
		}
	case "Appfox.py":
		if !json.Valid([]byte(raw)) {
			key := attachedMD5(c.cpValue("appKey"))[:16]
			raw, err = catpawCBCDecode(raw, key, catpawReverse(key), false)
		}
	case "开端.py":
		if strings.HasPrefix(raw, "AkEdSJx") {
			data, e := importedUnbase64(strings.TrimPrefix(raw, "AkEdSJx"))
			if e != nil || len(data) < 48 {
				return nil, errors.New("开端密文无效")
			}
			var plain []byte
			plain, err = aesCBCDecrypt(data[32:], data[:16], data[16:32])
			raw = string(plain)
		}
	case "99APP2.py":
		return c.cp99Decode(raw)
	}
	if err != nil {
		return nil, errors.New("站源响应解密失败")
	}
	value, err := catpawParseJSON(raw)
	if err != nil {
		return nil, err
	}
	row := attachedObject(value)
	switch s.script {
	case "XinJie.py":
		if mapString(row, "encrypted") == "1" {
			plain, e := catpawCBCDecode(mapString(row, "data"), c.cpValue("dataKey"), c.cpValue("dataIV"), false)
			if e != nil {
				return nil, errors.New("欣欣响应解密失败")
			}
			return catpawParseJSON(plain)
		}
	case "getapp3.4.6.py":
		if ciphertext := mapString(row, "data"); ciphertext != "" {
			plain, e := catpawCBCDecode(ciphertext, c.cpValue("dataKey"), c.cpValue("dataIV"), false)
			if e != nil {
				return nil, errors.New("咕咕响应解密失败")
			}
			return catpawParseJSON(plain)
		}
		return nil, errors.New("咕咕接口未返回内容，可能需要有效授权")
	case "美剧侠.py":
		if ciphertext, ok := row["data"].(string); ok && ciphertext != "" {
			plain, e := catpawCBCDecode(ciphertext, c.cpConst("const1"), c.cpConst("const2"), true)
			if e != nil {
				return nil, errors.New("美剧侠响应解密失败")
			}
			decoded, e := catpawParseJSON(plain)
			row["data"] = decoded
			return value, e
		}
	case "Hmys.py":
		data, e := importedUnbase64(mapString(row, "data"))
		block, be := des.NewTripleDESCipher([]byte(c.cpConst("des3_key_bytes")))
		iv := []byte(c.cpConst("des3_iv_bytes"))
		if e != nil || be != nil || len(iv) != 8 || len(data) == 0 || len(data)%8 != 0 {
			return nil, errors.New("九霄响应密文无效")
		}
		plain := make([]byte, len(data))
		cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, data)
		plain, e = pkcs7Unpad(plain, 8)
		if e != nil {
			return nil, errors.New("九霄响应解密失败")
		}
		return catpawParseJSON(string(plain))
	}
	return value, nil
}

func (c *attachedClient) cpRequest(ctx context.Context, method, path string, params url.Values) (any, error) {
	if params == nil {
		params = url.Values{}
	}
	if c.catpaw.script == "壹影视.py" {
		return c.cpYiRequest(ctx, method, path, params)
	}
	headers := map[string]string{}
	body, contentType := "", ""
	s := c.catpaw
	switch s.script {
	case "RJAPP.py":
		t := strconv.FormatInt(time.Now().Unix(), 10)
		params.Set("timestamp", t)
		params.Set("sign", attachedMD5(c.cpConst("const2")+t))
	case "美剧侠.py":
		t := time.Now().UnixMilli()
		digest := attachedMD5(strconv.FormatInt(8*t-12, 10))
		service := params.Get("service")
		version := firstNonEmpty(mapString(s.defaults, "versionCode"), "1030")
		params.Set("versionCode", version)
		params.Set("time", strconv.FormatInt(t, 10))
		params.Set("md5", digest)
		params.Set("sign", attachedMD5("Api_FeiFeiCms"+service+version+strconv.FormatInt(t, 10)+digest))
	case "Hmys.py":
		headers["timestamp"] = strconv.FormatInt(time.Now().UnixMilli(), 10)
	case "AppMuou.py":
		headers["app-time"] = strconv.FormatInt(time.Now().Unix(), 10)
	case "getapp3.4.6.py":
		t := strconv.FormatInt(time.Now().Unix(), 10)
		headers["User-Agent"] = firstNonEmpty(c.cpExt("ua"), "okhttp/3.14.9")
		headers[c.cpConst("const7")] = c.cpExt("version")
		headers["app-ui-mode"] = "light"
		headers[c.cpConst("const8")] = c.cpValue("device")
		headers[c.cpConst("const9")] = t
		encrypted, err := catpawCBCEncode(t, c.cpValue("dataKey"), c.cpValue("dataIV"))
		if err != nil {
			return nil, err
		}
		headers[c.cpConst("const10")] = encrypted
		if c.cpValue("getType") == "flutter" {
			headers["User-Agent"] = firstNonEmpty(c.cpExt("ua"), "Dart/3.5 (dart:io)")
			headers["app-os"] = "android"
		}
		if c.cpValue("getType") == "qiji" || c.cpValue("getType") == "2" {
			headers["User-Agent"] = firstNonEmpty(c.cpExt("ua"), "okhttp/3.10.0")
		}
	}
	if method == http.MethodGet {
		path = attachedPath(path, params)
	} else {
		body, contentType = params.Encode(), "application/x-www-form-urlencoded"
	}
	if s.script == "Appfox.py" && c.cpValue("ver") == "3" && strings.Contains(strings.ToLower(path), "appfox") {
		if method == http.MethodPost {
			value := map[string]string{}
			for key := range params {
				value[key] = params.Get(key)
			}
			body, contentType = attachedJSON(value), "application/json; charset=utf-8"
		}
		t := strconv.FormatInt(time.Now().UnixMilli(), 10)
		nonce := catpawRandomDigits(6)
		headers["x-security-auth"] = t + "|" + nonce + "|" + attachedMD5(c.cpValue("appSign")+c.cpValue("appKey")+t+nonce+body)
	}
	raw, err := c.cpRaw(ctx, method, path, contentType, body, headers)
	if err != nil {
		return nil, err
	}
	return c.cpDecode(raw)
}

func catpawRandomDigits(length int) string {
	data := make([]byte, length)
	if _, err := rand.Read(data); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)[:length]
	}
	for i := range data {
		data[i] = '0' + data[i]%10
	}
	if length > 0 && data[0] == '0' {
		data[0] = '1'
	}
	return string(data)
}

func (c *attachedClient) cp99Decode(raw string) (any, error) {
	data, err := importedUnbase64(raw)
	key := []byte(strings.ReplaceAll(c.cpValue("uuid"), "-", ""))
	if err != nil || len(data) < 32 {
		return nil, errors.New("99APP 响应密文无效")
	}
	plain, err := aesCBCDecrypt(data[16:], key, data[:16])
	if err != nil {
		return nil, errors.New("99APP 响应解密失败")
	}
	reader, err := zlib.NewReader(bytes.NewReader(plain))
	if err != nil {
		return nil, errors.New("99APP 响应压缩格式无效")
	}
	defer reader.Close()
	plain, err = io.ReadAll(io.LimitReader(reader, providerMaxBodyBytes+1))
	if err != nil || len(plain) > providerMaxBodyBytes {
		return nil, errors.New("99APP 响应解压长度无效")
	}
	return catpawParseJSON(string(plain))
}

func (c *attachedClient) cp99Call(ctx context.Context, path string, payload map[string]any) (any, error) {
	var iv [16]byte
	var nonce [16]byte
	if _, err := rand.Read(iv[:]); err != nil {
		return nil, err
	}
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	t := strconv.FormatInt(time.Now().UnixMilli(), 10)
	n := base64.StdEncoding.EncodeToString(nonce[:])
	payload["timestamp"], payload["nonce"], payload["token"] = t, n, c.cpValue("token")
	data, err := aesCBCEncrypt([]byte(attachedJSON(payload)), []byte(strings.ReplaceAll(c.cpValue("uuid"), "-", "")), iv[:])
	if err != nil {
		return nil, err
	}
	body := base64.StdEncoding.EncodeToString(append(iv[:], data...))
	headers := map[string]string{"uuid": c.cpValue("uuid"), "timestamp": t, "nonce": n, "appkey": c.cpExt("appkey"), "version": c.cpExt("version"),
		"sign": attachedSHA(body + ":" + t + ":" + c.cpValue("uuid") + ":" + n + ":" + c.cpExt("appkey"))}
	raw, err := c.cpRaw(ctx, http.MethodPost, path, "application/json", body, headers)
	if err != nil {
		return nil, err
	}
	return c.cp99Decode(raw)
}

func (c *attachedClient) cp99Init(ctx context.Context) error {
	s := c.catpaw
	s.base = strings.TrimRight(c.cpExt("host"), "/") + "/api"
	if s.values["uuid"] == "" {
		s.values["uuid"] = uuidLike()
	}
	s.values["token"] = c.cpExt("token")
	data, err := c.cp99Call(ctx, "/app/systemInit", map[string]any{"v": c.cpExt("versionName"), "n": c.cpExt("name"), "s": c.cpExt("buildSignature"), "pl": "1", "apiVersion": "v2"})
	if err != nil {
		return err
	}
	s.config = attachedObject(data)
	if s.config["player"] == nil || s.config["categorys"] == nil {
		return errors.New("99APP 初始化未返回线路与分类")
	}
	if s.values["token"] != "" {
		return nil
	}
	if s.values["installTime"] == "" {
		s.values["installTime"] = strconv.FormatInt(time.Now().UnixMilli(), 10)
	}
	install, _ := strconv.ParseInt(s.values["installTime"], 10, 64)
	device := map[string]any{}
	if fragment, e := catpawParseJSON(strings.Split(c.cpConst("const4"), ",\"app\":")[0] + "}"); e == nil {
		device = attachedObject(attachedAt(fragment, "device"))
	}
	if len(device) == 0 {
		return errors.New("99APP 内置设备模板无效")
	}
	login, err := c.cp99Call(ctx, firstNonEmpty(c.cpExt("LoginPath"), "/app/userInfo"), map[string]any{
		"device": device,
		"app":    map[string]any{"version": c.cpExt("versionName"), "name": c.cpExt("name"), "package": c.cpExt("package"), "buildNumber": c.cpExt("buildNumber"), "buildSignature": c.cpExt("buildSignature"), "install": install, "update": install},
		"did":    c.device, "apiVersion": "v2", "channel": "",
	})
	if err != nil {
		return err
	}
	s.values["token"] = mapString(attachedObject(attachedAt(login, "userInfo")), "user_token")
	if s.values["token"] == "" {
		return errors.New("99APP 访客登录未返回有效令牌")
	}
	return nil
}

func (c *attachedClient) cpV2Path(path string) string {
	if c.cpExt("apisignkey") == "" {
		return path
	}
	now := time.Now()
	apiKey := attachedMD5(fmt.Sprintf("%d:%02d:%d:%02d:%s", now.Year(), now.Hour(), now.Year(), now.Minute(), c.cpExt("apisignkey")))
	u, err := url.Parse(path)
	if err != nil {
		return path
	}
	values := u.Query()
	t := strconv.FormatInt(now.Unix(), 10)
	if path == "/types" {
		values.Set("timestamp", strconv.FormatFloat(float64(now.UnixNano())/1e9, 'f', -1, 64))
	} else {
		values.Set("apikey", apiKey)
		values.Set("keytime", t)
		values.Set("timestamp", t)
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	raw := []string{}
	for _, key := range keys {
		raw = append(raw, key+"="+values.Get(key))
	}
	values.Set("datasign", attachedMD5(strings.Join(raw, "&")+firstNonEmpty(c.cpExt("datasignkey"), c.cpConst("const1"))))
	u.RawQuery = values.Encode()
	return u.String()
}
