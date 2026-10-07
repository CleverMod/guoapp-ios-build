package core

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

func catpawXXHash(data []byte, seed uint64) uint64 {
	const p1 uint64 = 11400714785074694791
	const p2 uint64 = 14029467366897019727
	const p3 uint64 = 1609587929392839161
	const p4 uint64 = 9650029242287828579
	const p5 uint64 = 2870177450012600261
	round := func(a, v uint64) uint64 { return bits.RotateLeft64(a+v*p2, 31) * p1 }
	merge := func(a, v uint64) uint64 { return (a^round(0, v))*p1 + p4 }
	var hash uint64
	position := 0
	if len(data) >= 32 {
		a, b, c, d := seed+p1, seed+p2, seed, seed-p1
		a += p2
		for position <= len(data)-32 {
			a = round(a, binary.LittleEndian.Uint64(data[position:]))
			position += 8
			b = round(b, binary.LittleEndian.Uint64(data[position:]))
			position += 8
			c = round(c, binary.LittleEndian.Uint64(data[position:]))
			position += 8
			d = round(d, binary.LittleEndian.Uint64(data[position:]))
			position += 8
		}
		hash = bits.RotateLeft64(a, 1) + bits.RotateLeft64(b, 7) + bits.RotateLeft64(c, 12) + bits.RotateLeft64(d, 18)
		hash = merge(hash, a)
		hash = merge(hash, b)
		hash = merge(hash, c)
		hash = merge(hash, d)
	} else {
		hash = seed + p5
	}
	hash += uint64(len(data))
	for position+8 <= len(data) {
		hash ^= round(0, binary.LittleEndian.Uint64(data[position:]))
		hash = bits.RotateLeft64(hash, 27)*p1 + p4
		position += 8
	}
	if position+4 <= len(data) {
		hash ^= uint64(binary.LittleEndian.Uint32(data[position:])) * p1
		hash = bits.RotateLeft64(hash, 23)*p2 + p3
		position += 4
	}
	for position < len(data) {
		hash ^= uint64(data[position]) * p5
		hash = bits.RotateLeft64(hash, 11) * p1
		position++
	}
	hash ^= hash >> 33
	hash *= p2
	hash ^= hash >> 29
	hash *= p3
	hash ^= hash >> 32
	return hash
}

func catpawCycSignature(path, material string, params url.Values, timestamp int64) (url.Values, string) {
	values := url.Values{}
	for key, value := range params {
		if len(value) > 0 && value[0] != "" {
			values[key] = append([]string{}, value...)
		}
	}
	order := []string{}
	switch {
	case path == "/v2/video/query":
		order = []string{"tid", "page", "limit", "order"}
	case path == "/v2/video/search":
		order = []string{"text", "pg", "type_id", "limit"}
	case path == "/video/play_url":
		order = []string{"id", "from", "index"}
	}
	seen := map[string]bool{}
	extra := []string{}
	for _, key := range order {
		seen[key] = true
	}
	for key := range values {
		if !seen[key] {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	order = append(order, extra...)
	t := strconv.FormatInt(timestamp, 10)
	values.Set("timestamp", t)
	order = append(order, "timestamp")
	fields := []string{}
	for _, key := range order {
		if value, ok := values[key]; ok {
			fields = append(fields, catpawJSON(key)+":"+catpawJSON(value))
		}
	}
	signed := "{\"method\":\"GET\",\"timestamp\":" + t + ",\"path\":" + catpawJSON(path) + ",\"parameters\":{" + strings.Join(fields, ",") + "},\"body\":\"\"}"
	h1 := catpawXXHash([]byte(material), 0)
	h2 := catpawXXHash([]byte(signed), uint64(timestamp))
	input := fmt.Sprintf("%s|%016x|%016x", t, h2, h1)
	hash := catpawXXHash([]byte(input), uint64(timestamp)^uint64(11400714785074694791))
	return values, fmt.Sprintf("v3:%s:%016x", t, hash)
}

func catpawPB(data []byte, depth int) (map[string]any, error) {
	if depth > 20 || len(data) > providerMaxBodyBytes {
		return nil, errors.New("次元城 Protobuf 嵌套过深")
	}
	fields, err := xpgFields(data)
	if err != nil || len(fields) == 0 {
		return nil, errors.New("次元城 Protobuf 格式无效")
	}
	out := map[string]any{}
	for _, field := range fields {
		var value any
		if field.wire == 0 {
			value = strconv.FormatUint(field.integer, 10)
		} else if field.wire == 2 {
			printable := utf8.Valid(field.bytes)
			if printable {
				for _, r := range string(field.bytes) {
					if !unicode.IsPrint(r) && !unicode.IsSpace(r) {
						printable = false
						break
					}
				}
			}
			if printable {
				value = string(field.bytes)
			} else if child, e := catpawPB(field.bytes, depth+1); e == nil {
				value = child
			} else {
				value = hex.EncodeToString(field.bytes)
			}
		} else {
			value = hex.EncodeToString(field.bytes)
		}
		key := strconv.Itoa(field.number)
		if old, found := out[key]; found {
			if list, ok := old.([]any); ok {
				out[key] = append(list, value)
			} else {
				out[key] = []any{old, value}
			}
		} else {
			out[key] = value
		}
	}
	return out, nil
}

func catpawObjects(value any) []map[string]any {
	if row := attachedObject(value); row != nil {
		return []map[string]any{row}
	}
	return catpawRows(value)
}

func (c *attachedClient) cpCycInit(ctx context.Context) error {
	s := c.catpaw
	material := c.cpExt("pkg") + "|" + c.cpExt("ver") + "|" + c.cpExt("md5")
	if c.cpExt("doh") == "" || c.cpExt("pkg") == "" || len(c.cpExt("md5")) != 32 {
		return errors.New("次元城内置设备签名配置缺失")
	}
	s.values["material"] = material
	s.headers["User-Agent"] = "Dalvik/2.1.0 (Linux; U; Android 17; Pixel 10 Build/TQ3A.260701.001); cycdm-android/" + c.cpExt("ver")
	s.headers["Accept"] = "application/json,application/protobuf"
	raw, err := c.cpRaw(ctx, http.MethodGet, attachedPath("https://doh.pub/dns-query", attachedParams("name", c.cpExt("doh"), "type", "txt")), "", "", map[string]string{"User-Agent": "okhttp/5.1.0", "Accept": "application/dns-json"})
	if err != nil {
		return err
	}
	data, err := catpawParseJSON(raw)
	if err != nil {
		return err
	}
	for _, answer := range catpawRows(attachedAt(data, "Answer")) {
		if mapString(answer, "type") != "16" {
			continue
		}
		domains := strings.Fields(strings.ReplaceAll(strings.Trim(mapString(answer, "data"), "\"'"), ",", " "))
		for _, domain := range domains {
			if !isProviderHTTPMediaURL(domain) {
				continue
			}
			params, auth := catpawCycSignature("", material, nil, time.Now().UnixMilli())
			_, err = c.cpRaw(ctx, http.MethodGet, attachedPath(domain, params), "", "", map[string]string{"x-cyc-auth": auth})
			if err == nil {
				s.base = strings.TrimRight(domain, "/")
				return nil
			}
		}
	}
	return errors.New("次元城发布记录未返回可连接线路")
}

func (c *attachedClient) cpCycRequest(ctx context.Context, path string, params url.Values) (any, error) {
	params, auth := catpawCycSignature(path, c.cpValue("material"), params, time.Now().UnixMilli())
	raw, err := c.cpRaw(ctx, http.MethodGet, attachedPath(path, params), "", "", map[string]string{"x-cyc-auth": auth})
	if err != nil {
		return nil, err
	}
	if json.Valid([]byte(raw)) {
		return catpawParseJSON(raw)
	}
	return catpawPB([]byte(raw), 0)
}

func catpawCycVod(row map[string]any, detail bool) map[string]any {
	if detail {
		return map[string]any{"vod_name": mapString(row, "vod_name", "3", "2"), "vod_pic": mapString(row, "vod_pic", "7", "4"), "vod_content": mapString(row, "vod_content", "23", "15"), "vod_remarks": mapString(row, "vod_remarks", "12", "5")}
	}
	return map[string]any{"vod_id": mapString(row, "vod_id", "1"), "vod_name": mapString(row, "name", "2"), "vod_pic": mapString(row, "pic", "3", "4"), "vod_remarks": mapString(row, "remarks", "8", "5")}
}
