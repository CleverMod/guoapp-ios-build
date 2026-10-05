package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
)

const xiaopingguoNoticeSignature = "c65d9287686e1153a6e5fb1ffec5968951fe187c087200eec65bff11dc373966"

func xiaopingguoPlaylistSignature(body, address string) string {
	if !strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(body, "\ufeff")), "#EXTM3U") {
		return ""
	}
	base, err := url.Parse(address)
	if err != nil || !isProviderHTTPMediaURL(address) {
		return ""
	}
	var paths []string
	ended := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "#EXT-X-ENDLIST" {
			ended = true
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		ref, err := url.Parse(line)
		if err != nil {
			return ""
		}
		resource := base.ResolveReference(ref)
		if !isProviderHTTPMediaURL(resource.String()) {
			return ""
		}
		paths = append(paths, resource.Path)
	}
	if !ended || len(paths) == 0 {
		return ""
	}
	digest := sha256.Sum256([]byte(strings.Join(paths, "\n")))
	return hex.EncodeToString(digest[:])
}

func (d *Downloader) checkXiaopingguoMedia(ctx context.Context, media providerMedia) (providerMedia, error) {
	if media.Playlist == "" {
		return media, nil
	}
	ctx = providerMediaContext(ctx, media.credentials)
	body, address := media.Playlist, media.URL
	for depth := 0; depth < 6; depth++ {
		lines, _, master := nativeHLSSelect(strings.Split(body, "\n"), 480)
		if !master {
			if xiaopingguoPlaylistSignature(body, address) == xiaopingguoNoticeSignature {
				return providerMedia{}, errors.New("小苹果返回了指定播放器提示片，未取得正片地址，请切换其他站源")
			}
			return media, nil
		}
		base, err := url.Parse(address)
		if err != nil {
			return providerMedia{}, errors.New("小苹果播放列表地址无效")
		}
		next := ""
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			ref, err := url.Parse(line)
			if err == nil {
				next = base.ResolveReference(ref).String()
			}
			break
		}
		body, address, err = d.fetchMediaPlaylist(ctx, next, media.Referer)
		if err != nil {
			return providerMedia{}, err
		}
	}
	return providerMedia{}, errors.New("小苹果播放列表嵌套过多")
}

func hongdouPlaybackParameters(id, key string) url.Values {
	return url.Values{"page": {"1"}, "uid": {"0"}, "vid": {id}, "mid": {key}}
}

func (d *Downloader) resolveHongdouHLSMedia(ctx context.Context, id, key string) (providerMedia, error) {
	if !webProviderNumericID.MatchString(id) || !webProviderNumericID.MatchString(key) {
		return providerMedia{}, errors.New("红豆播放分集无效，请刷新详情")
	}
	response, err := d.jsonVideoRequest(ctx, sourceHongdou, "/api/video/videoinfo", hongdouPlaybackParameters(id, key))
	if err != nil {
		return providerMedia{}, err
	}
	for _, row := range jsonVideoRows(response["data"]) {
		if mapString(row, "vid") != id || mapString(row, "mid") != key {
			continue
		}
		if mapString(row, "pays") == "1" {
			return providerMedia{}, errors.New("红豆该分集需要源站播放授权，请切换其他站源")
		}
		address := maccmsDirectMediaURL(mapString(row, "src", "videourl"))
		if address == "" {
			break
		}
		referer := jsonVideoReferer(sourceHongdou)
		credentials := &providerMediaCredentials{referer: referer, userAgent: jsonVideoUserAgent(sourceHongdou)}
		return d.prepareWebProviderMedia(ctx, providerMedia{URL: address, Referer: referer, credentials: credentials}, "红豆")
	}
	return providerMedia{}, errors.New("红豆播放接口暂未提供该分集的可用地址，请稍后重试或切换其他站源")
}
