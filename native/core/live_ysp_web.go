package core

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const yspWebUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36"

type yspWebResolver struct {
	mu      sync.Mutex
	control chan struct{}
	client  *http.Client
	access  attachedAccess
	signer  *yspWebSigner
	entries map[string]yspDeviceEntry
}

func newYSPWebResolver(client *http.Client, access attachedAccess) *yspWebResolver {
	return &yspWebResolver{client: client, access: access, control: make(chan struct{}, 1), entries: map[string]yspDeviceEntry{}}
}

func (web *yspWebResolver) resolve(ctx context.Context, channel yspChannel) (yspDeviceEntry, error) {
	web.mu.Lock()
	entry := web.entries[channel.ID]
	web.mu.Unlock()
	if time.Now().Before(entry.expires) {
		return entry, nil
	}
	select {
	case web.control <- struct{}{}:
		defer func() { <-web.control }()
	case <-ctx.Done():
		return yspDeviceEntry{}, ctx.Err()
	}
	web.mu.Lock()
	entry = web.entries[channel.ID]
	web.mu.Unlock()
	if time.Now().Before(entry.expires) {
		return entry, nil
	}
	for _, name := range []string{"appID", "videoAppID", "videoSecret", "authSalt", "liveSalt", "cKeyKey", "cKeyIV", "cKeyMarker", "version", "cookie"} {
		if web.access.Settings[name] == "" {
			return yspDeviceEntry{}, errors.New("央视频 Web 备用线路缺少包内签名配置")
		}
	}
	if web.signer == nil {
		signer, err := newYSPWebSigner(ctx)
		if err != nil {
			return yspDeviceEntry{}, err
		}
		web.signer = signer
	}
	entry, err := web.resolveChannel(ctx, channel)
	if err == nil {
		web.mu.Lock()
		web.entries[channel.ID] = entry
		for id, cached := range web.entries {
			if time.Now().After(cached.expires) {
				delete(web.entries, id)
			}
		}
		web.mu.Unlock()
	}
	return entry, err
}

func (web *yspWebResolver) reject(channel yspChannel, entry yspDeviceEntry) {
	web.mu.Lock()
	defer web.mu.Unlock()
	if web.entries[channel.ID].address == entry.address {
		delete(web.entries, channel.ID)
	}
}

func yspWebRandom(length int) (string, error) {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	result := make([]byte, 0, length)
	for len(result) < length {
		random, err := yspRandom(length - len(result))
		if err != nil {
			return "", err
		}
		for _, value := range random {
			if value < 252 {
				result = append(result, alphabet[int(value)%len(alphabet)])
			}
		}
	}
	return string(result), nil
}

func yspWebValue(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case json.Number:
		return value.String()
	case int:
		return strconv.Itoa(value)
	}
	return ""
}

func yspWebPairs(body map[string]any, folded bool) string {
	keys := make([]string, 0, len(body))
	for key := range body {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if folded {
			first, second := strings.ToLower(keys[i]), strings.ToLower(keys[j])
			if first != second {
				return first < second
			}
		}
		return keys[i] < keys[j]
	})
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, key+"="+yspWebValue(body[key]))
	}
	return strings.Join(values, "&")
}

func yspWebMD5(value string) string {
	digest := md5.Sum([]byte(value))
	return hex.EncodeToString(digest[:])
}

func yspWebCKey(channel yspChannel, timestamp int64, guid string, settings map[string]string) (string, error) {
	key, err := hex.DecodeString(settings["cKeyKey"])
	if err != nil || len(key) != 16 {
		return "", errors.New("央视频 Web 密钥配置无效")
	}
	iv, err := hex.DecodeString(settings["cKeyIV"])
	if err != nil || len(iv) != aes.BlockSize {
		return "", errors.New("央视频 Web 密钥配置无效")
	}
	source := strings.Join([]string{"", channel.SID, strconv.FormatInt(timestamp, 10), settings["cKeyMarker"], settings["version"], guid, "5910204", "https://www.yangshipin.c", "mozilla/5.0 (macintosh; ", "", "Mozilla", "Netscape", "MacIntel", ""}, "|")
	plain := []byte("|" + strconv.FormatInt(yspJavaHash(source), 10) + source)
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	plain = append(plain, bytes.Repeat([]byte{byte(padding)}, padding)...)
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(plain, plain)
	return "--01" + strings.ToUpper(hex.EncodeToString(plain)), nil
}

func (web *yspWebResolver) request(ctx context.Context, method, address string, body []byte, headers map[string]string) (map[string]any, error) {
	data, _, err := yspRequestClient(ctx, web.client, method, address, body, headers)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var reply map[string]any
	if decoder.Decode(&reply) != nil {
		return nil, errors.New("央视频 Web 协议响应无效")
	}
	result, ok := reply["data"].(map[string]any)
	if !ok {
		return nil, errors.New("央视频 Web 协议未返回播放数据")
	}
	return result, nil
}

func (web *yspWebResolver) resolveChannel(ctx context.Context, channel yspChannel) (yspDeviceEntry, error) {
	settings := web.access.Settings
	now := time.Now()
	random, err := yspWebRandom(33)
	if err != nil {
		return yspDeviceEntry{}, err
	}
	guid := strconv.FormatInt(now.UnixMilli(), 36) + "_" + random[:13]
	cookie := "guid=" + guid + "; " + settings["cookie"]
	headers := map[string]string{"Origin": "https://www.yangshipin.cn", "Referer": "https://www.yangshipin.cn/", "User-Agent": yspWebUA, "Cookie": cookie, "yspappid": settings["appID"], "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8"}
	authBody := map[string]any{"pid": channel.PID, "guid": guid, "appid": "ysp_pc", "rand_str": random[13:23]}
	authBody["signature"] = yspWebMD5(yspWebPairs(authBody, false) + settings["authSalt"])
	authForm := url.Values{}
	for key, value := range authBody {
		authForm.Set(key, yspWebValue(value))
	}
	auth, err := web.request(ctx, "POST", "https://player-api.yangshipin.cn/v1/player/auth", []byte(authForm.Encode()), headers)
	if err != nil {
		return yspDeviceEntry{}, err
	}
	token, authTime := yspWebValue(auth["token"]), yspWebValue(auth["ts"])
	if token == "" || authTime == "" {
		return yspDeviceEntry{}, errors.New("央视频 Web 播放授权失败")
	}
	milliseconds := strconv.FormatInt(now.UnixMilli(), 10)
	state := map[string]string{"cctvh5openapi.state.guid": guid, "cctvh5openapi.state.yspappid": settings["appID"], "cctvh5openapi.state.version": "v1", "window.location.host": "www.yangshipin.cn", "window.location.protocol": "https:", "cctvh5openapi.state.ts": milliseconds}
	rnd, err := web.signer.key(ctx, state, "get_token_rnd")
	if err != nil {
		return yspDeviceEntry{}, errors.New("央视频 Web 随机签名失败")
	}
	query := url.Values{"yspappid": {settings["appID"]}, "guid": {guid}, "vappid": {settings["videoAppID"]}, "vsecret": {settings["videoSecret"]}, "raw": {"1"}, "version": {"v1"}, "ts": {milliseconds}, "rnd": {rnd}}
	openToken, err := web.request(ctx, "GET", "https://h5access.yangshipin.cn/web/open/token?"+query.Encode(), nil, map[string]string{"Referer": headers["Referer"], "User-Agent": yspWebUA})
	if err != nil {
		return yspDeviceEntry{}, err
	}
	if yspWebValue(openToken["token"]) == "" {
		return yspDeviceEntry{}, errors.New("央视频 Web 签名授权失败")
	}
	ckey, err := yspWebCKey(channel, now.Unix(), guid, settings)
	if err != nil {
		return yspDeviceEntry{}, err
	}
	body := map[string]any{"adjust": 1, "appVer": settings["version"], "app_version": settings["version"], "cKey": ckey, "channel": "ysp_tx", "cmd": "2", "cnlid": channel.SID, "defn": channel.Definition, "devid": "devid", "dtype": "1", "encryptVer": "8.1", "guid": guid, "livepid": channel.PID, "otype": "ojson", "platform": "5910204", "sphttps": "1", "stream": "2"}
	bodyHash := yspWebMD5(yspWebPairs(body, true))
	requestRandom, err := yspWebRandom(10)
	if err != nil {
		return yspDeviceEntry{}, err
	}
	requestID := "999999" + requestRandom + milliseconds
	sdkInput := bodyHash + "-" + guid + "-1-" + requestID
	state["cctvh5openapi.state.token"], state["cctvh5openapi.state.input"] = yspWebValue(openToken["token"]), sdkInput
	state["cctvh5openapi.state.ts"] = firstNonEmpty(yspWebValue(openToken["ts"]), milliseconds)
	signature, err := web.signer.key(ctx, state, "get_signature")
	if err != nil {
		return yspDeviceEntry{}, errors.New("央视频 Web 播放签名失败")
	}
	ticket, err := web.signer.playerTicket(ctx, channel.PID, authTime, channel.SID, guid, settings["appID"], settings["version"])
	if err != nil {
		return yspDeviceEntry{}, errors.New("央视频 Web 播放凭据失败")
	}
	body["rand_str"] = random[23:33]
	body["signature"] = yspWebMD5(yspWebPairs(body, false) + settings["liveSalt"])
	encoded, err := json.Marshal(body)
	if err != nil {
		return yspDeviceEntry{}, err
	}
	headers["Content-Type"], headers["yspsdkinput"], headers["yspsdksign"] = "application/json;charset=UTF-8", bodyHash, signature+"-"+sdkInput
	headers["seqId"], headers["request-id"], headers["yspPlayerToken"], headers["yspticket"] = "1", requestID, token, ticket
	result, err := web.request(ctx, "POST", "https://player-api.yangshipin.cn/v1/player/get_live_info", encoded, headers)
	if err != nil {
		return yspDeviceEntry{}, err
	}
	address := yspWebValue(result["playurl"]) + yspWebValue(result["extended_param"])
	if !isProviderHTTPMediaURL(address) {
		return yspDeviceEntry{}, errors.New("央视频 Web 协议无可用线路")
	}
	return yspDeviceEntry{address: address, headers: map[string]string{"User-Agent": yspWebUA, "Referer": "https://www.yangshipin.cn/"}, expires: time.Now().Add(30 * time.Second)}, nil
}

func (live *yspLiveServer) refreshWeb(ctx context.Context, channel yspChannel, state *yspLiveState) error {
	entry, err := live.web.resolve(ctx, channel)
	if err != nil {
		return err
	}
	info := yspStreamInfo{Route: "web"}
	text, err := live.playlistWithHeaders(context.WithValue(ctx, yspManifestInfoKey{}, &info), entry.address, 0, live.client, entry.headers)
	if err == nil && strings.Contains(text, "#EXT-X-ENDLIST") {
		err = errors.New("央视频 Web 线路未提供直播清单")
	}
	if err == nil {
		err = yspMergePlaylist(state, text, state.mode != "web")
	}
	if err != nil {
		if ctx.Err() == nil {
			live.web.reject(channel, entry)
		}
		return err
	}
	if state.media == nil {
		state.media = map[string]yspLiveResource{}
	}
	yspVisitPlaylistURLs(text, func(address string) {
		if isProviderHTTPMediaURL(address) {
			state.media[yspLiveResourceID(address)] = yspLiveResource{address: address, headers: entry.headers, expires: time.Now().Add(2 * time.Minute)}
		}
	})
	yspPruneLiveResources(state)
	state.mode, state.errorText, state.failures, state.refreshed = "web", "", 0, time.Now()
	state.info = info
	return nil
}
