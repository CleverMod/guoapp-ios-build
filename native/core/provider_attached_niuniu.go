package core

import (
	"context"
	"crypto/aes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type niuniuHTTPTransport struct {
	base http.RoundTripper
	api  http.RoundTripper
	host string
}

func (transport *niuniuHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme == "https" && strings.EqualFold(request.URL.Hostname(), transport.host) {
		return transport.api.RoundTrip(request)
	}
	return transport.base.RoundTrip(request)
}

func (c *attachedClient) niuniuTransport() http.RoundTripper {
	serverName := c.access.Settings["apiTlsServerName"]
	if serverName != "ad.tianjinzhitongdaohe.com" {
		return c.d.client.Transport
	}
	origin, _ := url.Parse(c.p.base)
	if origin == nil || origin.Hostname() != "new.tianjinzhitongdaohe.com" {
		return c.d.client.Transport
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = c.d.proxyRouter.proxy
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}
	transport.ResponseHeaderTimeout = 20 * time.Second
	return &niuniuHTTPTransport{base: c.d.client.Transport, api: transport, host: origin.Hostname()}
}

func niuniuID(value string) any {
	if number, err := strconv.ParseUint(value, 10, 64); err == nil {
		return number
	}
	return value
}

func (c *attachedClient) niuniuCall(ctx context.Context, method, target string, payload any) (any, error) {
	for attempt := 0; attempt < 2; attempt++ {
		token, _, err := c.session(ctx)
		if err != nil {
			return nil, err
		}
		body := ""
		if payload != nil {
			body = attachedJSON(payload)
		}
		raw, _, err := c.raw(ctx, method, target, "application/json;charset=UTF-8", body,
			map[string]string{"token": token, "deviceid": c.device, "User-Agent": c.agent()})
		if err != nil {
			return nil, err
		}
		var envelope any
		envelope, err = attachedDecode(raw)
		if err == nil {
			return attachedData(envelope), nil
		}
		if !strings.Contains(raw, `"2006"`) && !strings.Contains(raw, `:2006`) && !strings.Contains(raw, `: 2006`) {
			return nil, err
		}
		c.mu.Lock()
		c.token, c.expiry = "", time.Time{}
		c.mu.Unlock()
	}
	return nil, errors.New("牛牛游客授权更新失败")
}

func niuniuECBDecode(raw, key string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return nil, errors.New("牛牛第三方响应编码无效")
	}
	block, err := aes.NewCipher([]byte(key))
	if err != nil || len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("牛牛第三方内置密钥或密文无效")
	}
	for index := 0; index < len(data); index += aes.BlockSize {
		block.Decrypt(data[index:index+aes.BlockSize], data[index:index+aes.BlockSize])
	}
	plain, err := pkcs7Unpad(data, aes.BlockSize)
	if err != nil {
		return nil, errors.New("牛牛第三方响应解密失败")
	}
	return plain, nil
}

func (c *attachedClient) niuniuCSJRequest(ctx context.Context, path, body, timestamp string, login bool, token string) (any, error) {
	settings := c.access.Settings
	prefix, agent := "biz_", settings["csj_ua"]
	if login {
		prefix, agent = "login_", c.agent()
	}
	key, nonce := settings[prefix+"aes_key"], settings[prefix+"nonce"]
	encrypted, err := attachedECB([]byte(body), []byte(key))
	if err != nil || settings["hmac_key"] == "" || nonce == "" {
		return nil, errors.New("牛牛第三方内置签名参数缺失")
	}
	mac := hmac.New(sha256.New, []byte(settings["hmac_key"]))
	_, _ = mac.Write([]byte(timestamp + nonce + body))
	headers := map[string]string{"X-Salt": settings[prefix+"salt"], "X-Nonce": nonce,
		"X-Timestamp": timestamp, "X-Signature": hex.EncodeToString(mac.Sum(nil)), "User-Agent": agent}
	if token != "" {
		headers["X-Access-Token"] = token
	}
	base := settings["csj_base"]
	if !isProviderHTTPMediaURL(base) {
		return nil, errors.New("牛牛第三方内置接口地址无效")
	}
	raw, _, err := c.raw(ctx, http.MethodPost, strings.TrimRight(base, "/")+path,
		"application/x-www-form-urlencoded", base64.StdEncoding.EncodeToString(encrypted), headers)
	if err != nil {
		return nil, err
	}
	plain, err := niuniuECBDecode(raw, key)
	if err != nil {
		return nil, err
	}
	envelope, err := attachedDecode(string(plain))
	if err != nil {
		return nil, err
	}
	if ret := mapString(attachedObject(envelope), "ret"); ret != "" && ret != "0" && ret != "200" {
		return nil, errors.New("牛牛第三方线路未返回有效授权")
	}
	return attachedData(envelope), nil
}

func (c *attachedClient) niuniuCSJToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.csjToken != "" && time.Now().Before(c.csjExpiry) {
		return c.csjToken, nil
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	body := strings.ReplaceAll(c.access.Settings["csjLoginBody"], "{timestamp}", timestamp)
	if body == "" {
		return "", errors.New("牛牛第三方内置登录参数缺失")
	}
	data, err := c.niuniuCSJRequest(ctx, "/csj_sp/api/v1/user/login?siteid="+url.QueryEscape(c.access.Settings["site_id"]), body, timestamp, true, "")
	if err != nil {
		return "", err
	}
	c.csjToken = mapString(attachedObject(data), "access_token")
	if c.csjToken == "" {
		return "", errors.New("牛牛第三方游客登录未返回授权")
	}
	c.csjExpiry = time.Now().Add(30 * time.Minute)
	return c.csjToken, nil
}

func (c *attachedClient) niuniuCSJDetail(ctx context.Context, id string, episode int) (any, error) {
	for attempt := 0; attempt < 2; attempt++ {
		token, err := c.niuniuCSJToken(ctx)
		if err != nil {
			return nil, err
		}
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		body := strings.NewReplacer("{timestamp}", timestamp, "{index}", strconv.Itoa(episode), "{shortplayId}", url.QueryEscape(id)).Replace(c.access.Settings["csjBody"])
		data, err := c.niuniuCSJRequest(ctx, "/csj_sp/api/v1/shortplay/detail?siteid="+url.QueryEscape(c.access.Settings["site_id"]), body, timestamp, false, token)
		if err == nil {
			return data, nil
		}
		if attempt > 0 {
			return nil, err
		}
		c.mu.Lock()
		c.csjToken, c.csjExpiry = "", time.Time{}
		c.mu.Unlock()
	}
	return nil, errors.New("牛牛第三方线路暂不可用")
}

func (c *attachedClient) niuniuCSJMedia(ctx context.Context, id string, episode int) (string, error) {
	data, err := c.niuniuCSJDetail(ctx, id, episode)
	if err != nil {
		return "", err
	}
	for _, row := range attachedRows(attachedAt(data, "list")) {
		encoded := nativeText(attachedAt(row, "video_model", "video_list", "video_1", "main_url"))
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err == nil && isProviderHTTPMediaURL(string(decoded)) {
			return string(decoded), nil
		}
	}
	return "", errors.New("牛牛第三方线路未返回本集播放权限或地址")
}
