package core

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type xpgField struct {
	number  int
	bytes   []byte
	integer uint64
	wire    int
}

func xpgVarint(value uint64) []byte { return binary.AppendUvarint(nil, value) }
func xpgBytes(number int, value []byte) []byte {
	out := xpgVarint(uint64(number<<3 | 2))
	out = append(out, xpgVarint(uint64(len(value)))...)
	return append(out, value...)
}
func xpgFields(data []byte) ([]xpgField, error) {
	out := []xpgField{}
	for len(data) > 0 {
		tag, n := binary.Uvarint(data)
		if n <= 0 || tag>>3 == 0 {
			return nil, errors.New("小苹果 Protobuf 字段无效")
		}
		data = data[n:]
		field := xpgField{number: int(tag >> 3), wire: int(tag & 7)}
		switch field.wire {
		case 0:
			field.integer, n = binary.Uvarint(data)
			if n <= 0 {
				return nil, errors.New("小苹果 Protobuf 整数无效")
			}
			data = data[n:]
		case 2:
			length, n := binary.Uvarint(data)
			if n <= 0 || length > uint64(len(data)-n) {
				return nil, errors.New("小苹果 Protobuf 长度无效")
			}
			field.bytes = data[n : n+int(length)]
			data = data[n+int(length):]
		case 1, 5:
			length := 8
			if field.wire == 5 {
				length = 4
			}
			if len(data) < length {
				return nil, errors.New("小苹果 Protobuf 响应截断")
			}
			field.bytes = data[:length]
			data = data[length:]
		default:
			return nil, errors.New("小苹果 Protobuf 类型无效")
		}
		out = append(out, field)
	}
	return out, nil
}
func xpgFlat(data []byte) map[int]string {
	out := map[int]string{}
	fields, _ := xpgFields(data)
	for _, f := range fields {
		if f.wire == 0 {
			out[f.number] = strconv.FormatUint(f.integer, 10)
		}
		if f.wire == 2 {
			out[f.number] = string(f.bytes)
		}
	}
	return out
}

func (c *attachedClient) xpgHeaders(ts string) (map[string]string, error) {
	s := c.access.Settings
	keyBytes, err := importedUnbase64(s["PUB1"])
	if err != nil {
		return nil, errors.New("小苹果内置 RSA 参数缺失")
	}
	value, err := x509.ParsePKIXPublicKey(keyBytes)
	public, ok := value.(*rsa.PublicKey)
	if err != nil || !ok {
		return nil, errors.New("小苹果内置 RSA 参数无效")
	}
	random := base64.StdEncoding.EncodeToString([]byte(attachedNonce()[:12]))
	signature, err := rsa.EncryptPKCS1v15(rand.Reader, public, []byte(ts+random+"1003"))
	if err != nil {
		return nil, err
	}
	sig2, err := attachedECB([]byte(ts+random), []byte(s["DATAIV"]))
	if err != nil {
		return nil, errors.New("小苹果内置 AES 参数缺失")
	}
	b := base64.StdEncoding.EncodeToString(sig2)
	device := strings.ToUpper(c.device)
	now, _ := strconv.ParseInt(ts, 10, 64)
	params := map[string]any{
		"country": "CN", "vName": "1.0.0.3", "cpuId": "", "young": 0, "facturer": "OnePlus", "pkg": "com.juechufsh.android.xpg1",
		"uuid": device, "resolution": "1080x2256", "mac": "02%3A00%3A00%3A00%3A00%3A00",
		"sig": base64.StdEncoding.EncodeToString(signature), "abid": "2557", "model": "PJX110", "plat": "android", "udid": device,
		"dpi": "480", "net": "1", "lang": "zh", "random_str": random, "brand": "OnePlus", "timestamp": now, "density": "3.0",
		"appName": "%E5%B0%8F%E8%8B%B9%E6%9E%9C%E5%BD%B1%E8%A7%86", "cpu": "arm64-v8a", "chid": "10000",
		"carrier": "%E8%81%94%E9%80%9A", "sig2": b[:8], "sig3": b[8:], "v": 1, "tenantId": "xpg",
		"_vOsCode": "36", "vOs": "16", "vApp": "1003", "device": 0, "androidID": strings.ToLower(device[:16]),
	}
	native := []byte(s["NATIVE"])
	if len(native) != 16 {
		return nil, errors.New("小苹果内置客户端参数缺失")
	}
	encrypted, err := aesCBCEncrypt([]byte(attachedJSON(params)), native, native)
	if err != nil {
		return nil, err
	}
	return map[string]string{"publicParams": attachedJSON(map[string]string{"paramsData": hex.EncodeToString(encrypted)}), "Accept": "application/x-protobuf", "Cache-Control": "no-cache", "Referer": ""}, nil
}

func (c *attachedClient) xpgRequest(ctx context.Context, path string, values url.Values) ([]byte, error) {
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	headers, err := c.xpgHeaders(ts)
	if err != nil {
		return nil, err
	}
	method, body := http.MethodGet, ""
	if values != nil {
		method = http.MethodPost
		keys := make([]string, 0, len(values))
		for k := range values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := []string{}
		for _, k := range keys {
			if values.Get(k) != "" {
				parts = append(parts, k+"="+values.Get(k))
			}
		}
		padded, err := attachedECB([]byte(strings.Join(parts, "&")+ts), []byte(c.access.Settings["DATAKEY"]))
		if err != nil {
			return nil, errors.New("小苹果内置请求参数缺失")
		}
		f8 := attachedNonce()[:8]
		encoded := f8 + base64.StdEncoding.EncodeToString(padded)
		wire := append(xpgBytes(1, []byte(encoded[:20])), xpgBytes(2, []byte(encoded[20:]))...)
		wire = append(wire, xpgBytes(3, []byte(attachedNonce()[:20]))...)
		now, _ := strconv.ParseUint(ts, 10, 64)
		wire = append(wire, xpgVarint(4<<3)...)
		wire = append(wire, xpgVarint(now)...)
		wire = append(wire, xpgBytes(5, []byte(f8))...)
		body = string(wire)
	}
	raw, err := c.importedRaw(ctx, method, path, "application/x-protobuf", body, headers)
	if err != nil {
		return nil, err
	}
	fields, err := xpgFields([]byte(raw))
	if err != nil {
		return nil, err
	}
	for _, f := range fields {
		if f.number == 3 && f.wire == 2 {
			return f.bytes, nil
		}
	}
	return nil, errors.New("小苹果接口未返回有效数据或客户端授权已失效")
}

var xpgURLPattern = regexp.MustCompile(`https?://[^\x00-\x20"']+`)

func (c *attachedClient) xpgCatalog(ctx context.Context, page int, category, query string) ([]Drama, bool, error) {
	path, values := "/api/proto/v5/drama/category", attachedParams("pagesize", "21", "typeId1", category, "page", strconv.Itoa(page), "vodOrderBy", "最新")
	tag := query == "" && (category == "117" || category == "115")
	if query == "" && category == "117" && page > 1 {
		return []Drama{}, false, nil
	}
	if query == "" && category == "115" && page > 1 {
		areas := []string{"美国", "韩国", "日本", "英国"}
		values = attachedParams("pagesize", "21", "typeId1", "2", "page", strconv.Itoa((page-2)/len(areas)+1), "vodArea", areas[(page-2)%len(areas)], "vodOrderBy", "最新")
		tag = false
	}
	if query != "" {
		path = "/api/proto/v5/drama/search"
		values = attachedParams("searchKeys", query, "page", strconv.Itoa(page), "pagesize", "21")
	}
	if tag {
		path = attachedPath("/api/proto/v4/tag/list/detail", attachedParams("pagesize", "21", "id", category, "page", strconv.Itoa(page)))
		values = nil
	}
	data, err := c.xpgRequest(ctx, path, values)
	if err != nil {
		return nil, false, err
	}
	fields, err := xpgFields(data)
	if err != nil {
		return nil, false, err
	}
	cards := [][]byte{}
	for _, f := range fields {
		if !tag && f.number == 1 && f.wire == 2 {
			cards = append(cards, f.bytes)
		}
		if tag && f.number == 31 && f.wire == 2 {
			group, _ := xpgFields(f.bytes)
			for _, item := range group {
				if item.number == 5 && item.wire == 2 {
					cards = append(cards, item.bytes)
				}
			}
		}
	}
	rows := []Drama{}
	seen := map[string]bool{}
	for _, card := range cards {
		m := xpgFlat(card)
		pics := xpgURLPattern.FindAllString(m[2], -1)
		pic := ""
		if len(pics) > 0 {
			pic = pics[0]
		}
		drama := c.importedDrama(map[string]any{"name": m[5], "pic": pic, "remarks": firstNonEmpty(m[13], m[26])}, m[3])
		if drama.ID != "" && !seen[drama.ID] {
			rows = append(rows, drama)
			seen[drama.ID] = true
		}
	}
	return rows, len(rows) > 0 && category != "117", nil
}

func (c *attachedClient) xpgDetail(ctx context.Context, id string) (Drama, []Chapter, error) {
	data, err := c.xpgRequest(ctx, "/api/proto/v5/drama/getDetail", attachedParams("id", id))
	if err != nil {
		return Drama{}, nil, err
	}
	fields, err := xpgFields(data)
	if err != nil {
		return Drama{}, nil, err
	}
	head := xpgFlat(data)
	pic := xpgURLPattern.FindString(head[2])
	lines := []importedLine{}
	index := map[string]int{}
	for _, f := range fields {
		if f.number != 29 || f.wire != 2 {
			continue
		}
		ep := xpgFlat(f.bytes)
		player := firstNonEmpty(ep[9], "Ksvideo")
		if strings.Contains(strings.ToLower(player), "youku") || ep[4] == "" {
			continue
		}
		line := player
		if ep[10] != "" {
			line += "·" + ep[10]
		}
		slot, found := index[line]
		if !found {
			slot = len(lines)
			index[line] = slot
			lines = append(lines, importedLine{key: player + "~" + ep[10], name: line})
		}
		lines[slot].episodes = append(lines[slot].episodes, importedEpisode{name: ep[3], url: ep[4]})
	}
	return c.importedDetailResult(id, map[string]any{"name": head[9], "pic": pic, "content": head[6], "remarks": head[26]}, lines)
}

func (c *attachedClient) xpgPlay(ctx context.Context, p importedPayload) (providerMedia, error) {
	player, _, _ := strings.Cut(p.Player, "~")
	if player == "RRSP" || strings.Contains(p.URL, "yichengwlkj.com") {
		return c.xpgRRPlay(ctx, p.URL)
	}
	data, err := c.xpgRequest(ctx, "/api/proto/v5/videoUsableUrl", attachedParams("vodPlayFrom", strings.ReplaceAll(url.QueryEscape(player), "+", "%20"), "playUrl", strings.ReplaceAll(url.QueryEscape(p.URL), "+", "%20")))
	address := ""
	if err == nil {
		address = xpgURLPattern.FindString(string(data))
	}
	if address == "" && maccmsDirectMediaURL(p.URL) != "" {
		address = p.URL
	}
	if address == "" {
		return providerMedia{}, errors.New("小苹果未返回正片媒体地址或客户端授权已失效")
	}
	media, err := importedURLMedia(address, c.agent(), "", nil)
	if err != nil {
		return media, err
	}
	if maccmsDirectMediaURL(address) == "" {
		return c.opaqueHLS(ctx, media)
	}
	return media, nil
}

func (c *attachedClient) xpgRRCall(ctx context.Context, path string, values url.Values) (any, error) {
	s := c.access.Settings
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	device := attachedNonce()
	query := values.Encode()
	signature := "GET\naliId:" + device + "\nct:web_applet\ncv:1.0.0\nt:" + ts + "\n" + path + "?" + query
	mac := hmac.New(sha256.New, []byte(s["RR_SS"]))
	mac.Write([]byte(signature))
	headers := map[string]string{"x-ca-sign": base64.StdEncoding.EncodeToString(mac.Sum(nil)), "t": ts, "aliId": device, "umid": device, "deviceId": device, "clientVersion": "1.0.0", "cv": "1.0.0", "clientType": "web_applet", "ct": "web_applet", "uet": "9", "User-Agent": s["RR_UA"], "Referer": s["RR_REF"], "Origin": strings.TrimRight(s["RR_REF"], "/")}
	body, err := c.importedRaw(ctx, http.MethodGet, "https://"+s["RR_API"]+path+"?"+query, "", "", headers)
	if err != nil {
		return nil, err
	}
	if decoded, err := importedUnbase64(body); err == nil {
		if plain, err := importedECBDecrypt(decoded, []byte(s["RR_DK"])); err == nil {
			body = string(plain)
		}
	}
	return importedDecode(body)
}

func (c *attachedClient) xpgRRPlay(ctx context.Context, address string) (providerMedia, error) {
	match := regexp.MustCompile(`/drama/([0-9]+)`).FindStringSubmatch(address)
	if len(match) < 2 {
		return providerMedia{}, errors.New("人人线路影片标识无效")
	}
	parsed, _ := url.Parse(address)
	sequence := firstNonEmpty(parsed.Query().Get("episodeNo"), "1")
	data, err := c.xpgRRCall(ctx, "/m-station/drama/page", attachedParams("hsdrOpen", "0", "isAgeLimit", "0", "dramaId", match[1], "pageNum", "1", "pageSize", "200"))
	if err != nil {
		return providerMedia{}, err
	}
	sid := ""
	for _, row := range attachedRows(attachedAt(data, "data", "episodeList")) {
		if mapString(row, "episodeNo") == sequence {
			sid = mapString(row, "sid")
			break
		}
	}
	if sid == "" {
		return providerMedia{}, errors.New("人人线路未返回所选章节")
	}
	for _, quality := range []string{"HD", "SD"} {
		data, err = c.xpgRRCall(ctx, "/m-station/drama/play", attachedParams("hsdrOpen", "0", "dramaId", match[1], "episodeSid", sid, "quality", quality, "hevcOpen", "0", "tria4k", "0"))
		if err != nil {
			continue
		}
		row := attachedObject(attachedAt(data, "data"))
		sign := mapString(row, "newSign")
		if len(sign) < 20 {
			continue
		}
		ciphertext := mapString(attachedObject(row["m3u8"]), "url")
		plain, err := importedCBCText(ciphertext, sign[4:20], c.access.Settings["RR_IV"])
		if err == nil && isProviderHTTPMediaURL(plain) {
			return importedURLMedia(plain, c.access.Settings["RR_UA"], c.access.Settings["RR_REF"], nil)
		}
	}
	return providerMedia{}, errors.New("人人线路暂未返回有效播放授权")
}
