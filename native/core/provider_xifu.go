package core

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var xifuVODRegion = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
var xifuVideoID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func xifuPercentEncode(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}

func xifuVODSignature(parameters url.Values, secret string) string {
	canonical := strings.ReplaceAll(parameters.Encode(), "+", "%20")
	signer := hmac.New(sha1.New, []byte(secret+"&"))
	_, _ = signer.Write([]byte("GET&%2F&" + xifuPercentEncode(canonical)))
	return base64.StdEncoding.EncodeToString(signer.Sum(nil))
}

func xifuVODRequestURL(auth map[string]any, now time.Time, nonce string) (string, error) {
	data := nestedMap(auth, "data")
	videoID, encoded := mapString(data, "vid"), mapString(data, "playAuth")
	if !xifuVideoID.MatchString(videoID) || len(encoded) == 0 || len(encoded) > 65536 {
		return "", errors.New("喜福未返回有效播放授权")
	}
	body, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		body, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil {
		return "", errors.New("喜福播放授权格式无效")
	}
	var credentials map[string]any
	if err := json.Unmarshal(body, &credentials); err != nil {
		return "", errors.New("喜福播放授权格式无效")
	}
	region := firstNonEmpty(mapString(credentials, "Region"), "cn-shanghai")
	secret := mapString(credentials, "AccessKeySecret")
	key, token := mapString(credentials, "AccessKeyId"), mapString(credentials, "SecurityToken")
	if !xifuVODRegion.MatchString(region) || secret == "" || key == "" || token == "" || len(nonce) == 0 {
		return "", errors.New("喜福播放授权不完整")
	}
	parameters := url.Values{
		"Action": {"GetPlayInfo"}, "Version": {"2017-03-21"}, "Format": {"JSON"},
		"AccessKeyId": {key}, "SecurityToken": {token}, "VideoId": {videoID},
		"AuthInfo": {mapString(credentials, "AuthInfo")}, "Timestamp": {now.UTC().Format("2006-01-02T15:04:05Z")},
		"SignatureMethod": {"HMAC-SHA1"}, "SignatureVersion": {"1.0"}, "SignatureNonce": {nonce},
	}
	if config, exists := credentials["PlayConfig"]; exists && config != nil {
		if text, ok := config.(string); ok {
			parameters.Set("PlayConfig", text)
		} else if body, err := json.Marshal(config); err == nil {
			parameters.Set("PlayConfig", string(body))
		}
	}
	parameters.Set("Signature", xifuVODSignature(parameters, secret))
	return "https://vod." + region + ".aliyuncs.com/?" + strings.ReplaceAll(parameters.Encode(), "+", "%20"), nil
}

func xifuVODMedia(response map[string]any) (providerMedia, error) {
	best := providerMedia{}
	for _, row := range jsonVideoRows(nestedMap(response, "PlayInfoList")["PlayInfo"]) {
		if mapString(row, "EncryptType") == "AliyunVoDEncryption" {
			continue
		}
		address := maccmsDirectMediaURL(mapString(row, "PlayURL"))
		if address == "" {
			continue
		}
		quality, _ := webProviderInteger(row["Height"], 10000)
		if best.URL == "" || quality > best.Quality {
			best = providerMedia{URL: address, Quality: quality, Referer: jsonVideoReferer(sourceXifu)}
		}
	}
	if best.URL == "" {
		return providerMedia{}, errors.New("喜福暂未返回可播放地址或当前视频需专用播放器")
	}
	best.credentials = &providerMediaCredentials{referer: best.Referer, userAgent: jsonVideoUserAgent(sourceXifu)}
	return best, nil
}

func (d *Downloader) resolveXifuMedia(ctx context.Context, id string, sequence int) (providerMedia, error) {
	auth, err := d.jsonVideoRequest(ctx, sourceXifu, "/web/v1/drama/play_auth", url.Values{"albumId": {id}, "seq": {strconv.Itoa(sequence)}})
	if err != nil {
		return providerMedia{}, err
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return providerMedia{}, errors.New("喜福播放授权初始化失败")
	}
	address, err := xifuVODRequestURL(auth, time.Now(), hex.EncodeToString(nonce))
	if err != nil {
		return providerMedia{}, err
	}
	ctx = context.WithValue(ctx, providerTextUserAgentKey{}, jsonVideoUserAgent(sourceXifu))
	body, err := d.fetchProviderText(ctx, address, jsonVideoReferer(sourceXifu))
	if err != nil {
		return providerMedia{}, errors.New("喜福临时播放授权请求失败，请重新解析")
	}
	response, err := decodeJSONVideoResponse(body)
	if err != nil {
		return providerMedia{}, errors.New("喜福播放服务暂未返回有效结果")
	}
	media, err := xifuVODMedia(response)
	if err != nil {
		return providerMedia{}, err
	}
	return d.prepareWebProviderMedia(ctx, media, "喜福")
}
