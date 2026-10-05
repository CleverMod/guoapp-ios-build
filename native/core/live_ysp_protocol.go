package core

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const yspVersion = "3.2.7.26212"
const yspAppVersion = "V8.22.1035.3031"
const yspUA = "qqlive"
const yspJCEUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"

type yspWriter struct{ bytes.Buffer }

func (w *yspWriter) head(kind, tag int) {
	if tag < 15 {
		w.WriteByte(byte(tag<<4 | kind))
	} else {
		w.WriteByte(byte(240 | kind))
		w.WriteByte(byte(tag))
	}
}
func (w *yspWriter) number(value int64, tag int) {
	if value == 0 {
		w.head(12, tag)
		return
	}
	if value >= -128 && value <= 127 {
		w.head(0, tag)
		w.WriteByte(byte(value))
		return
	}
	if value >= -32768 && value <= 32767 {
		w.head(1, tag)
		binary.Write(w, binary.BigEndian, int16(value))
		return
	}
	if value >= math.MinInt32 && value <= math.MaxInt32 {
		w.head(2, tag)
		binary.Write(w, binary.BigEndian, int32(value))
		return
	}
	w.head(3, tag)
	binary.Write(w, binary.BigEndian, value)
}
func (w *yspWriter) text(value string, tag int) {
	if len(value) > 255 {
		w.head(7, tag)
		binary.Write(w, binary.BigEndian, int32(len(value)))
	} else {
		w.head(6, tag)
		w.WriteByte(byte(len(value)))
	}
	w.WriteString(value)
}
func (w *yspWriter) blob(value []byte, tag int) {
	w.head(13, tag)
	w.head(0, 0)
	w.number(int64(len(value)), 0)
	w.Write(value)
}
func (w *yspWriter) object(tag int, body func(*yspWriter)) { w.head(10, tag); body(w); w.head(11, 0) }
func (w *yspWriter) emptyList(tag int)                     { w.head(9, tag); w.number(0, 0) }

type yspReader struct {
	data []byte
	pos  int
}

func (r *yspReader) take(n int) ([]byte, error) {
	if n < 0 || n > len(r.data)-r.pos {
		return nil, io.ErrUnexpectedEOF
	}
	data := r.data[r.pos : r.pos+n]
	r.pos += n
	return data, nil
}
func (r *yspReader) head() (int, int, error) {
	b, err := r.take(1)
	if err != nil {
		return 0, 0, err
	}
	kind, tag := int(b[0]&15), int(b[0]>>4)
	if tag == 15 {
		b, err = r.take(1)
		if err != nil {
			return 0, 0, err
		}
		tag = int(b[0])
	}
	return kind, tag, nil
}
func (r *yspReader) field(depth int) (any, error) {
	kind, _, err := r.head()
	if err != nil {
		return nil, err
	}
	return r.value(kind, depth)
}
func (r *yspReader) count(depth int) (int, error) {
	v, err := r.field(depth)
	if err != nil {
		return 0, err
	}
	n, ok := v.(int64)
	if !ok || n < 0 || n > 4<<20 {
		return 0, errors.New("央视频协议长度无效")
	}
	return int(n), nil
}
func (r *yspReader) object(depth int) (map[int]any, error) {
	result := map[int]any{}
	for r.pos < len(r.data) {
		kind, tag, err := r.head()
		if err != nil {
			return nil, err
		}
		if kind == 11 {
			return result, nil
		}
		v, err := r.value(kind, depth+1)
		if err != nil {
			return nil, err
		}
		result[tag] = v
	}
	return result, nil
}
func (r *yspReader) value(kind, depth int) (any, error) {
	if depth > 24 {
		return nil, errors.New("央视频协议嵌套过深")
	}
	sizes := map[int]int{0: 1, 1: 2, 2: 4, 3: 8, 4: 4, 5: 8}
	if size, ok := sizes[kind]; ok {
		b, err := r.take(size)
		if err != nil {
			return nil, err
		}
		switch kind {
		case 0:
			return int64(int8(b[0])), nil
		case 1:
			return int64(int16(binary.BigEndian.Uint16(b))), nil
		case 2:
			return int64(int32(binary.BigEndian.Uint32(b))), nil
		case 3:
			return int64(binary.BigEndian.Uint64(b)), nil
		case 4:
			return math.Float32frombits(binary.BigEndian.Uint32(b)), nil
		case 5:
			return math.Float64frombits(binary.BigEndian.Uint64(b)), nil
		}
	}
	switch kind {
	case 6, 7:
		size := 1
		if kind == 7 {
			size = 4
		}
		b, err := r.take(size)
		if err != nil {
			return nil, err
		}
		n := int(b[0])
		if kind == 7 {
			n = int(binary.BigEndian.Uint32(b))
		}
		b, err = r.take(n)
		return string(b), err
	case 8, 9:
		n, err := r.count(depth)
		if err != nil {
			return nil, err
		}
		if n > len(r.data)-r.pos {
			return nil, io.ErrUnexpectedEOF
		}
		for i := 0; i < n; i++ {
			if _, err = r.field(depth + 1); err != nil {
				return nil, err
			}
			if kind == 8 {
				if _, err = r.field(depth + 1); err != nil {
					return nil, err
				}
			}
		}
		return nil, nil
	case 10:
		return r.object(depth)
	case 11:
		return nil, nil
	case 12:
		return int64(0), nil
	case 13:
		kind, _, err := r.head()
		if err != nil || kind != 0 {
			return nil, errors.New("央视频字节字段无效")
		}
		n, err := r.count(depth)
		if err != nil {
			return nil, err
		}
		return r.take(n)
	}
	return nil, errors.New("央视频协议类型无效")
}

func yspRandom(n int) ([]byte, error) { b := make([]byte, n); _, err := rand.Read(b); return b, err }

func yspPacket(body []byte, guid string, requestID int64) []byte {
	w := &yspWriter{}
	qua := &yspWriter{}
	qua.text(yspVersion, 0)
	qua.text("302070", 1)
	qua.number(1080, 2)
	qua.number(2400, 3)
	qua.number(3, 4)
	qua.text("12", 5)
	qua.number(1, 6)
	qua.number(1, 7)
	qua.number(420, 8)
	qua.text("10070", 9)
	for i := 10; i < 15; i++ {
		qua.text("", i)
	}
	qua.object(15, func(w *yspWriter) { w.number(0, 0); w.number(0, 1); w.text("", 2) })
	for i := 16; i < 19; i++ {
		qua.text("", i)
	}
	qua.object(19, func(w *yspWriter) {
		w.number(0, 0)
		w.head(4, 1)
		binary.Write(w, binary.BigEndian, float32(0))
		w.head(4, 2)
		binary.Write(w, binary.BigEndian, float32(0))
		w.head(5, 3)
		binary.Write(w, binary.BigEndian, float64(0))
	})
	qua.text(guid[:16], 20)
	qua.text("Pixel 6", 21)
	qua.number(1, 22)
	for i := 23; i < 27; i++ {
		qua.number(0, i)
	}
	qua.text("", 27)
	qua.text("", 28)
	qua.text(guid, 29)
	w.object(0, func(w *yspWriter) {
		w.number(requestID, 0)
		w.number(25312, 1)
		w.object(2, func(w *yspWriter) { w.Write(qua.Bytes()) })
		w.text("1200013", 3)
		w.text(guid, 4)
		w.emptyList(5)
		w.object(6, func(w *yspWriter) {})
		w.emptyList(7)
		w.number(0, 8)
		w.number(0, 9)
		w.number(0, 10)
	})
	w.blob(body, 1)
	inner := &bytes.Buffer{}
	inner.WriteByte(38)
	binary.Write(inner, binary.BigEndian, int32(w.Len()+17))
	inner.WriteByte(1)
	inner.Write(make([]byte, 10))
	inner.Write(w.Bytes())
	inner.WriteByte(40)
	compressed := &bytes.Buffer{}
	zw := gzip.NewWriter(compressed)
	zw.Write(inner.Bytes())
	zw.Close()
	out := &bytes.Buffer{}
	out.WriteByte(19)
	binary.Write(out, binary.BigEndian, int32(0))
	for _, n := range []uint16{2, 65281, 25312, 0} {
		binary.Write(out, binary.BigEndian, n)
	}
	binary.Write(out, binary.BigEndian, requestID)
	binary.Write(out, binary.BigEndian, int32(531))
	binary.Write(out, binary.BigEndian, int32(10012))
	binary.Write(out, binary.BigEndian, int64(0))
	out.WriteString(guid)
	out.WriteByte(1)
	binary.Write(out, binary.BigEndian, int32(302070))
	out.Write(make([]byte, 11))
	binary.Write(out, binary.BigEndian, int32(inner.Len()))
	out.Write(compressed.Bytes())
	out.WriteByte(3)
	packet := out.Bytes()
	binary.BigEndian.PutUint32(packet[1:5], uint32(len(packet)))
	return packet
}

func (live *yspLiveServer) request(ctx context.Context, method, address string, body []byte, headers map[string]string) ([]byte, string, error) {
	return yspRequestClient(ctx, live.client, method, address, body, headers)
}

func yspRequestClient(ctx context.Context, client *http.Client, method, address string, body []byte, headers map[string]string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, method, address, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", errors.New("央视频网络请求失败，请重试")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("央视频 HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil || len(b) > 4<<20 {
		return nil, "", errors.New("央视频响应读取失败或超过限制")
	}
	return b, resp.Request.URL.String(), nil
}

func (live *yspLiveServer) timeshift(ctx context.Context, ch yspChannel) (string, error) {
	now := time.Now().Unix()
	return live.timeshiftRange(ctx, ch, now-300, now)
}

func (live *yspLiveServer) timeshiftRange(ctx context.Context, ch yspChannel, start, end int64) (string, error) {
	w := &yspWriter{}
	w.text(ch.PID, 0)
	w.text(ch.SID, 1)
	w.number(start, 2)
	w.number(end, 3)
	w.text(ch.Definition, 4)
	b, _, err := live.request(ctx, "POST", "https://jacc.ysp.cctv.cn", yspPacket(w.Bytes(), live.guid, time.Now().UnixMilli()&0x7fffffff), map[string]string{"Content-Type": "application/octet-stream"})
	if err != nil {
		return "", err
	}
	if len(b) < 90 || b[0] != 19 {
		return "", errors.New("央视频主协议响应无效")
	}
	payload := b[89 : len(b)-1]
	if binary.BigEndian.Uint32(b[21:25])&2 != 0 {
		zr, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			return "", err
		}
		payload, err = io.ReadAll(io.LimitReader(zr, (4<<20)+1))
		zr.Close()
		if err != nil || len(payload) > 4<<20 {
			return "", errors.New("央视频主协议解压失败")
		}
	}
	if len(payload) < 17 || payload[0] != 38 || payload[len(payload)-1] != 40 {
		return "", errors.New("央视频主协议包无效")
	}
	r := &yspReader{data: payload[16 : len(payload)-1]}
	envelope, err := r.object(0)
	if err != nil {
		return "", err
	}
	raw, ok := envelope[1].([]byte)
	if !ok {
		return "", errors.New("央视频主协议缺少数据")
	}
	r = &yspReader{data: raw}
	fields, err := r.object(0)
	if err != nil {
		return "", err
	}
	if code, _ := fields[0].(int64); code != 0 {
		return "", fmt.Errorf("央视频主协议错误 %d", code)
	}
	address, _ := fields[2].(string)
	if !isProviderHTTPMediaURL(address) || strings.Contains(address, "liverecord.video.cloud.cctv.com") {
		return "", errors.New("央视频主协议返回无效线路")
	}
	return address, nil
}

func yspChecksum(b []byte) uint32 {
	var n uint32
	for _, v := range b {
		n = (n*131 + uint32(v)) & 0x7fffffff
	}
	return n
}
func yspTEABlock(b, key []byte) []byte {
	y, z := binary.BigEndian.Uint32(b), binary.BigEndian.Uint32(b[4:])
	var sum uint32
	k := []uint32{binary.BigEndian.Uint32(key), binary.BigEndian.Uint32(key[4:]), binary.BigEndian.Uint32(key[8:]), binary.BigEndian.Uint32(key[12:])}
	for i := 0; i < 16; i++ {
		sum += 0x9e3779b9
		y += ((z << 4) + k[0]) ^ (z + sum) ^ ((z >> 5) + k[1])
		z += ((y << 4) + k[2]) ^ (y + sum) ^ ((y >> 5) + k[3])
	}
	out := make([]byte, 8)
	binary.BigEndian.PutUint32(out, y)
	binary.BigEndian.PutUint32(out[4:], z)
	return out
}
func yspEncrypt(data, key []byte) ([]byte, error) {
	pad := (8 - (len(data)+10)%8) % 8
	noise, err := yspRandom(pad + 3)
	if err != nil {
		return nil, err
	}
	noise[0] = noise[0]&248 | byte(pad)
	plain := append(noise, data...)
	plain = append(plain, make([]byte, 7)...)
	out := []byte{}
	previousPlain, previousCipher := make([]byte, 8), make([]byte, 8)
	for off := 0; off < len(plain); off += 8 {
		mixed := make([]byte, 8)
		for i := 0; i < 8; i++ {
			mixed[i] = plain[off+i] ^ previousCipher[i]
		}
		cipher := yspTEABlock(mixed, key)
		for i := 0; i < 8; i++ {
			cipher[i] ^= previousPlain[i]
		}
		out = append(out, cipher...)
		previousPlain, previousCipher = mixed, cipher
	}
	return out, nil
}
func yspLP(b []byte) []byte {
	out := make([]byte, 2)
	binary.BigEndian.PutUint16(out, uint16(len(b)))
	return append(out, b...)
}
func yspU32(n uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, n); return b }
func yspKey(ch yspChannel) (url.Values, error) {
	ts := uint32(time.Now().Unix())
	random, err := yspRandom(36)
	if err != nil {
		return nil, err
	}
	guid := hex.EncodeToString(random[:16])
	uid := strings.ToUpper(hex.EncodeToString(random[16:20]))
	random[26] = random[26]&15 | 64
	random[28] = random[28]&63 | 128
	flow := strings.ToUpper(hex.EncodeToString(random[20:]))
	guardKey, _ := hex.DecodeString("110DBEC10C23E7D2E56A1CAD6914EF1B")
	teaKey, _ := hex.DecodeString("59b2f7cf725ef43c34fdd7c123411ed3")
	guard := yspU32(ts)
	for _, s := range []string{guid[len(guid)-5:], "", "", "-1"} {
		guard = append(guard, yspLP([]byte(s))...)
	}
	guard = yspLP(guard)
	enc, err := yspEncrypt(guard, guardKey)
	if err != nil {
		return nil, err
	}
	enc = append(enc, yspU32(yspChecksum(guard))...)
	gx := []byte{0xb3, 0xc9, 0x53, 0xa0, 0x69, 0x13, 0xad, 0x4d}
	for i := range enc {
		enc[i] ^= gx[i%8]
	}
	body, _ := hex.DecodeString("0000004200000004000004d2")
	for _, n := range []uint32{4330403, 0, ts} {
		body = append(body, yspU32(n)...)
	}
	for _, s := range []string{"dcgh", "_zj1A5Gh6QYcxWjIUGos2w==", yspAppVersion, ch.SID, guid} {
		body = append(body, yspLP([]byte(s))...)
	}
	body = append(body, yspU32(1)...)
	body = append(body, yspU32(1)...)
	for _, s := range []string{uid, "nil", "57eab0c4-2c58-44c6-8ae9-dd2757525dc5", "nil", "v0.1.000", "com.cctv.yangshipin.app.iphone", "4330403", "ex_json_bus", "ex_json_vs", strings.ToUpper(hex.EncodeToString(enc))} {
		body = append(body, yspLP([]byte(s))...)
	}
	packet := yspLP(body)
	binary.BigEndian.PutUint32(packet[18:22], yspChecksum(packet))
	enc, err = yspEncrypt(packet, teaKey)
	if err != nil {
		return nil, err
	}
	enc = append(enc, yspU32(yspChecksum(packet))...)
	xor := []byte{0x84, 0x2e, 0xed, 0x08, 0xf0, 0x66, 0xe6, 0xea, 0x48, 0xb4, 0xca, 0xa9, 0x91, 0xed, 0x6f, 0xf3}
	for i := range enc {
		enc[i] ^= xor[i%16]
	}
	key := strings.TrimRight(base64.StdEncoding.EncodeToString(enc), "=")
	key = strings.NewReplacer("+", "_", "/", "-").Replace(key)
	return url.Values{"cKey": {"--01" + key}, "guid": {guid}, "fntick": {fmt.Sprint(ts)}, "flowid": {flow + "_4330403"}}, nil
}
func (live *yspLiveServer) backupURLs(ctx context.Context, ch yspChannel) ([]string, error) {
	q, err := yspKey(ch)
	if err != nil {
		return nil, err
	}
	params := map[string]string{"atime": "120", "livepid": ch.PID, "cnlid": ch.SID, "appVer": yspAppVersion, "app_version": "300090", "caplv": "1", "cmd": "2", "defn": ch.Definition, "device": "iPhone", "encryptVer": "4.2", "getpreviewinfo": "0", "hevclv": "0", "lang": "zh-Hans_CN", "livequeue": "0", "logintype": "1", "nettype": "1", "newnettype": "1", "newplatform": "4330403", "platform": "4330403", "sdtfrom": "v3021", "spacode": "23", "spaudio": "1", "spdemuxer": "6", "spdrm": "2", "spdynamicrange": "1", "spflv": "1", "spflvaudio": "1", "sphdrfps": "60", "sphttps": "1", "spvcode": base64.StdEncoding.EncodeToString([]byte("H(30:1080,60:1080|30:1080,60:1080)")), "spvideo": "4", "stream": "1", "system": "1", "sysver": "ios18.2.1", "uhd_flag": "0", "playbacktime": "0"}
	for k, v := range params {
		q.Set(k, v)
	}
	b, _, err := live.request(ctx, "GET", "https://bkliveinfo.ysp.cctv.cn/?"+q.Encode(), nil, map[string]string{"User-Agent": yspUA, "Accept": "application/json"})
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if json.Unmarshal(b, &data) != nil {
		return nil, errors.New("央视频备用协议响应无效")
	}
	code, ok := data["iretcode"].(float64)
	if !ok || code != 0 {
		return nil, fmt.Errorf("央视频备用协议拒绝取流（%v）", data["iretcode"])
	}
	urls := []string{}
	seen := map[string]bool{}
	add := func(s string) {
		u, err := url.Parse(s)
		if err == nil && (u.Scheme == "https" || u.Scheme == "http") && (strings.HasSuffix(u.Hostname(), ".cctv.cn") || strings.HasSuffix(u.Hostname(), ".cctv.com")) && !seen[s] {
			seen[s] = true
			urls = append(urls, s)
		}
	}
	if s, ok := data["playurl"].(string); ok {
		add(s)
	}
	for _, key := range []string{"backurl_list", "backurlList", "backurl"} {
		switch v := data[key].(type) {
		case []any:
			for _, entry := range v {
				switch e := entry.(type) {
				case string:
					add(e)
				case map[string]any:
					for _, k := range []string{"url", "playurl"} {
						if s, ok := e[k].(string); ok {
							add(s)
						}
					}
				}
			}
		case string:
			for _, s := range strings.FieldsFunc(v, func(r rune) bool { return r == ';' || r == ',' }) {
				add(strings.TrimSpace(s))
			}
		}
	}
	sort.SliceStable(urls, func(i, j int) bool {
		return strings.Contains(urls[i], "bklive-") && !strings.Contains(urls[j], "bklive-")
	})
	if len(urls) == 0 {
		return nil, errors.New("央视频备用协议无可用线路")
	}
	return urls, nil
}
