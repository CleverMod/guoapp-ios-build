package core

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func peachHash(value string) uint32 {
	h := uint32(0x811c9dc5)
	for _, ch := range value {
		h ^= uint32(ch)
		h *= 0x01000193
	}
	return h
}
func peachProof(ctx context.Context, challenge string, bits int) (string, error) {
	if bits < 0 || bits > 24 || len(challenge) > 512 {
		return "", errors.New("蜜桃握手挑战超出支持范围")
	}
	want := strings.Repeat("0", bits>>2)
	for number := 0; number < 20000000; number++ {
		if number%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}
		nonce := strconv.FormatInt(int64(number), 16)
		hash := peachHash(challenge + ":" + nonce)
		for i := 0; i < 4; i++ {
			hash = peachHash(fmt.Sprintf("%08x", hash) + challenge)
		}
		if strings.HasPrefix(fmt.Sprintf("%08x", hash), want) {
			return challenge + "." + nonce, nil
		}
	}
	return "", errors.New("蜜桃握手挑战未完成，请稍后重试")
}

func (c *attachedClient) peachHandshake(ctx context.Context) (string, []byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sid != "" && len(c.skey) > 0 && time.Now().Add(time.Minute).Before(c.expiry) {
		return c.sid, c.skey, nil
	}
	target := "/api/handshake?dev=" + url.QueryEscape(c.device)
	raw, _, err := c.raw(ctx, http.MethodGet, target, "", "", nil)
	if err != nil {
		return "", nil, err
	}
	value, err := attachedDecode(raw)
	if err != nil {
		return "", nil, err
	}
	data := attachedObject(value)
	if challenge := mapString(data, "need_chal"); challenge != "" {
		bits := attachedInt(data, "bits")
		if bits == 0 {
			bits = 16
		}
		proof, e := peachProof(ctx, challenge, bits)
		if e != nil {
			return "", nil, e
		}
		raw, _, err = c.raw(ctx, http.MethodGet, target+"&c="+url.QueryEscape(proof), "", "", nil)
		if err != nil {
			return "", nil, err
		}
		value, err = attachedDecode(raw)
		if err != nil {
			return "", nil, err
		}
		data = attachedObject(value)
	}
	key, err := hex.DecodeString(mapString(data, "skey"))
	sid := mapString(data, "sid")
	if err != nil || len(key) == 0 || sid == "" {
		return "", nil, errors.New("蜜桃接口握手不可用")
	}
	expiry, _ := strconv.ParseInt(mapString(data, "exp"), 10, 64)
	if expiry <= time.Now().Unix() {
		return "", nil, errors.New("蜜桃接口握手已过期")
	}
	c.sid = sid
	c.skey = key
	c.expiry = time.Unix(expiry, 0)
	return sid, key, nil
}

func (c *attachedClient) peachCall(ctx context.Context, method, target string, payload any) (any, error) {
	body, content := "", ""
	if payload != nil {
		body = attachedJSON(payload)
		content = "application/json"
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, errors.New("蜜桃接口地址无效")
	}
	for attempt := 0; attempt < 2; attempt++ {
		sid, key, e := c.peachHandshake(ctx)
		if e != nil {
			return nil, e
		}
		ts, nonce := strconv.FormatInt(time.Now().Unix(), 10), attachedNonce()
		canonical := strings.Join([]string{method, parsed.Path, parsed.RawQuery, attachedSHA(body), ts, nonce, sid}, "\n")
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(canonical))
		headers := map[string]string{"X-Hp-Sid": sid, "X-Hp-Ts": ts, "X-Hp-Nonce": nonce, "X-Hp-Sign": hex.EncodeToString(mac.Sum(nil))}
		raw, status, e := c.raw(ctx, method, target, content, body, headers)
		if status == 401 && attempt == 0 {
			c.mu.Lock()
			c.sid = ""
			c.skey = nil
			c.expiry = time.Time{}
			c.mu.Unlock()
			continue
		}
		if e != nil {
			return nil, e
		}
		return attachedDecode(raw)
	}
	return nil, errors.New("蜜桃接口授权未通过，请刷新重试")
}

func peachSource(module, category string) string {
	for _, key := range []string{category, module} {
		switch key {
		case "video":
			return "missav"
		case "shortv":
			return "dongman"
		case "chiguo", "pengran", "guochan":
			return key
		case "tuijian", "duanju", "manju", "huanlian", "mogai", "rank":
			return "huangguo"
		}
	}
	return "huangdou"
}
func (c *attachedClient) peachDrama(row map[string]any, module, source string) Drama {
	realID := mapString(row, "id", "code")
	if realID == "" {
		return Drama{}
	}
	drama := c.drama(row, module+"@@"+realID+"@@"+source)
	cover := mapString(row, "cover_n", "cover")
	if cover != "" {
		mod := module
		if module == "duanju" {
			mod = source
		}
		if strings.HasPrefix(cover, "/") {
			cover = resolveProviderURL(c.p.base+"/", cover)
		} else {
			cover = c.p.base + "/cover/" + url.PathEscape(mod) + "?u=" + url.QueryEscape(cover)
		}
		drama.Cover = cover
		drama.CoverURL = cover
	}
	return drama
}
func (c *attachedClient) peachCatalog(ctx context.Context, page int, cat attachedCategory, query string) ([]Drama, bool, error) {
	parts := strings.SplitN(cat.value, "@@", 2)
	module, category := parts[0], ""
	if len(parts) == 2 {
		category = parts[1]
	}
	modules := []string{module}
	if query != "" {
		modules = []string{"video", "duanju", "shortv", "guochan", "caibian"}
	}
	out := []Drama{}
	more := false
	seen := map[string]bool{}
	for _, mod := range modules {
		v := attachedParams("key", mod, "page", strconv.Itoa(page))
		path := "/api/module"
		source := peachSource(mod, category)
		if query != "" {
			path = "/api/search"
			v.Set("kw", query)
		} else {
			v.Set("src", source)
			if category != "" {
				v.Set("cat", category)
			}
		}
		data, err := c.call(ctx, http.MethodGet, attachedPath(path, v), nil)
		if err != nil {
			return nil, false, err
		}
		rows := attachedRows(attachedAt(data, "list"))
		for _, row := range rows {
			src := source
			if query != "" {
				src = firstNonEmpty(mapString(row, "src"), mod)
			}
			drama := c.peachDrama(row, mod, src)
			if drama.ID != "" && !seen[drama.ID] {
				seen[drama.ID] = true
				out = append(out, drama)
			}
		}
		if flag, ok := attachedObject(data)["has_more"].(bool); ok {
			more = more || flag
		} else if query != "" {
			more = more || len(rows) >= 10
		}
	}
	return out, more, nil
}
func peachID(id string) (string, string, string, error) {
	parts := strings.Split(id, "@@")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return "", "", "", errors.New("蜜桃剧集标识无效")
	}
	return parts[0], parts[1], parts[2], nil
}
func (c *attachedClient) peachDetail(ctx context.Context, id string) (Drama, []Chapter, error) {
	module, realID, source, err := peachID(id)
	if err != nil {
		return Drama{}, nil, err
	}
	data, err := c.get(ctx, "/api/detail/"+url.PathEscape(module)+"/"+url.PathEscape(realID), "src", source)
	if err != nil {
		return Drama{}, nil, err
	}
	detail := attachedObject(attachedAt(data, "detail"))
	if detail == nil {
		return Drama{}, nil, errors.New("蜜桃未返回剧集详情")
	}
	detail["id"] = realID
	drama := c.peachDrama(detail, module, source)
	out := []Chapter{}
	seen := map[string]bool{}
	for index, row := range attachedRows(detail["episodes"]) {
		key := mapString(row, "ep")
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		order, _ := strconv.Atoi(key)
		if order < 1 {
			order = index + 1
		}
		title := firstNonEmpty(mapString(row, "name"), fmt.Sprintf("第%d集", order))
		if !valueEmpty(row["lock"]) && nativeText(row["lock"]) != "false" {
			title += " [预告10s]"
		}
		out = append(out, attachedChapter(c.p.id, id, key, order, title, "", ""))
	}
	if len(out) == 0 {
		out = append(out, attachedChapter(c.p.id, id, "1", 1, "正片", "", ""))
	}
	drama.TotalEpisode = len(out)
	drama.EpisodeCount = len(out)
	return drama, out, nil
}
func (c *attachedClient) peachPlay(ctx context.Context, id, key string) (providerMedia, error) {
	module, realID, source, err := peachID(id)
	if err != nil {
		return providerMedia{}, err
	}
	data, err := c.get(ctx, "/api/play/"+url.PathEscape(module)+"/"+url.PathEscape(realID)+"/"+url.PathEscape(key), "src", source)
	if err != nil {
		return providerMedia{}, err
	}
	play := attachedObject(attachedAt(data, "play"))
	address := firstNonEmpty(mapString(play, "src"), mapString(play, "src_proxy"))
	address = resolveProviderURL(c.p.base+"/", address)
	if !isProviderHTTPMediaURL(address) {
		return providerMedia{}, errors.New("蜜桃未返回可用播放地址")
	}
	referer := c.p.base + "/"
	if strings.Contains(address, "hembed.com") || strings.Contains(address, "hanime") {
		referer = "https://hanime1.me/"
	}
	media := providerMedia{URL: address, Referer: referer}
	parsed, _ := url.Parse(address)
	if !strings.HasSuffix(strings.ToLower(parsed.Path), ".m3u8") && !strings.HasSuffix(strings.ToLower(parsed.Path), ".mp4") && !strings.HasSuffix(strings.ToLower(parsed.Path), ".mpd") {
		return c.opaqueHLS(ctx, media)
	}
	return media, nil
}
