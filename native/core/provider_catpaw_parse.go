package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"html"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func catpawRandomString(length int, alphabet string) string {
	if alphabet == "" {
		alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	}
	data := make([]byte, length)
	if _, err := rand.Read(data); err != nil {
		return ""
	}
	for i := range data {
		data[i] = alphabet[int(data[i])%len(alphabet)]
	}
	return string(data)
}

func catpawDubokuDecode(value string) string {
	value = strings.Trim(value, "'\"")
	var out strings.Builder
	for i := 0; i < len(value); i += 10 {
		out.WriteString(catpawReverse(value[i:min(i+10, len(value))]))
	}
	plain, err := importedUnbase64(strings.ReplaceAll(out.String(), ".", "="))
	if err != nil {
		return ""
	}
	return string(plain)
}

func (c *attachedClient) cpDuboku(ctx context.Context, path string, params url.Values) (any, error) {
	if params == nil {
		params = url.Values{}
	}
	n, err := rand.Int(rand.Reader, big.NewInt(800000001))
	if err != nil {
		return nil, err
	}
	first := strconv.FormatInt(n.Int64()+100000000, 10) + strconv.FormatInt(900000000-n.Int64(), 10)
	second := strconv.FormatInt(time.Now().Unix(), 10)
	var interleaved strings.Builder
	for i := 0; i < min(len(first), len(second)); i++ {
		interleaved.WriteByte(first[i])
		interleaved.WriteByte(second[i])
	}
	if len(first) > len(second) {
		interleaved.WriteString(first[len(second):])
	} else {
		interleaved.WriteString(second[len(first):])
	}
	params.Set("ssid", strings.ReplaceAll(base64.StdEncoding.EncodeToString([]byte(interleaved.String())), "=", "."))
	params.Set("sign", catpawRandomString(60, c.cpConst("const1")))
	params.Set("token", catpawRandomString(38, c.cpConst("const1")))
	raw, err := c.cpRaw(ctx, http.MethodGet, attachedPath(path, params), "", "", nil)
	if err != nil {
		return nil, err
	}
	return catpawParseJSON(raw)
}

func catpawXMLVods(body string) ([]map[string]any, int, error) {
	decoder := xml.NewDecoder(strings.NewReader(body))
	rows := []map[string]any{}
	pagecount := 0
	var current map[string]any
	var field string
	inDD := false
	lineFlag := ""
	lineFields := []string{}
	lineURLs := []string{}
	var text strings.Builder
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, errors.New("新浪 XML 响应格式无效")
		}
		switch v := token.(type) {
		case xml.StartElement:
			switch v.Name.Local {
			case "list":
				for _, a := range v.Attr {
					if a.Name.Local == "pagecount" {
						pagecount, _ = strconv.Atoi(a.Value)
					}
				}
			case "video":
				current = map[string]any{}
				lineFields = nil
				lineURLs = nil
			case "dd":
				inDD = true
				lineFlag = ""
				text.Reset()
				for _, a := range v.Attr {
					if a.Name.Local == "flag" {
						lineFlag = a.Value
					}
				}
			default:
				if current != nil && v.Name.Local != "dl" {
					field = v.Name.Local
					text.Reset()
				}
			}
		case xml.CharData:
			if current != nil {
				text.Write(v)
			}
		case xml.EndElement:
			if current == nil {
				continue
			}
			switch v.Name.Local {
			case "video":
				current["vod_play_from"] = strings.Join(lineFields, "$$$")
				current["vod_play_url"] = strings.Join(lineURLs, "$$$")
				rows = append(rows, current)
				current = nil
			case "dd":
				lineFields = append(lineFields, firstNonEmpty(lineFlag, "播放线路"))
				lineURLs = append(lineURLs, strings.TrimSpace(text.String()))
				inDD = false
				text.Reset()
			default:
				if !inDD && v.Name.Local == field {
					key := map[string]string{"id": "vod_id", "name": "vod_name", "pic": "vod_pic", "note": "vod_remarks", "des": "vod_content", "type": "type_name"}[field]
					if key == "" {
						key = field
					}
					current[key] = strings.TrimSpace(text.String())
					field = ""
					text.Reset()
				}
			}
		}
	}
	return rows, pagecount, nil
}

func (c *attachedClient) cpXinlangCatalog(ctx context.Context, page int, category, query string) ([]Drama, bool, error) {
	params := attachedParams("ac", "list", "pg", strconv.Itoa(page))
	if category != "" {
		params.Set("t", category)
	}
	if query != "" {
		params.Set("wd", query)
	}
	if category == "" && query == "" {
		params.Set("h", "24")
	}
	raw, err := c.cpRaw(ctx, http.MethodGet, attachedPath(c.catpaw.base, params), "", "", nil)
	if err != nil {
		return nil, false, err
	}
	rows, count, err := catpawXMLVods(raw)
	if err != nil {
		return nil, false, err
	}
	ids := []string{}
	for _, row := range rows {
		ids = append(ids, mapString(row, "vod_id"))
	}
	if len(ids) > 0 {
		raw, err = c.cpRaw(ctx, http.MethodGet, attachedPath(c.catpaw.base, attachedParams("ac", "detail", "ids", strings.Join(ids, ","))), "", "", nil)
		if err != nil {
			return nil, false, err
		}
		rows, _, err = catpawXMLVods(raw)
	}
	return c.cpDramas(rows), page < count, err
}

func (c *attachedClient) cpXinlangDetail(ctx context.Context, id string) (Drama, []Chapter, error) {
	raw, err := c.cpRaw(ctx, http.MethodGet, attachedPath(c.catpaw.base, attachedParams("ac", "detail", "ids", id)), "", "", nil)
	if err != nil {
		return Drama{}, nil, err
	}
	rows, _, err := catpawXMLVods(raw)
	if err != nil {
		return Drama{}, nil, err
	}
	for _, row := range rows {
		if mapString(row, "vod_id") == id {
			return c.cpDetailResult(id, row, catpawMacLines(row))
		}
	}
	return Drama{}, nil, errors.New("新浪未返回所选影片")
}

var catpawHTMLMedia = regexp.MustCompile("(?i)[\"']((?:https?:)?//[^\"'\\s<>]+\\.(?:m3u8|mp4|flv|mkv)(?:[^\"'\\s<>]*))[\"']")

func catpawResponseURL(data any) string {
	if address, ok := data.(string); ok {
		return address
	}
	row := attachedObject(data)
	return firstNonEmpty(mapString(row, "url", "play_url", "playUrl", "videoUrl", "src"), mapString(attachedObject(row["data"]), "url", "play_url", "playUrl"), nativeStringValue(row["data"]))
}

func nativeStringValue(value any) string {
	text, _ := value.(string)
	return text
}

func catpawParserHeaders(data any) map[string]string {
	row := attachedObject(data)
	headers := map[string]string{}
	for _, key := range []string{"User-Agent", "Referer", "Cookie", "Origin", "Authorization"} {
		if value := mapString(row, key); value != "" {
			headers[key] = value
		}
	}
	if agent := mapString(row, "ua", "UA", "user-agent"); agent != "" {
		headers["User-Agent"] = agent
	}
	if raw := row["header"]; raw != nil {
		if nested := attachedObject(raw); nested != nil {
			for k, v := range nested {
				if text, ok := v.(string); ok {
					headers[k] = text
				}
			}
		} else {
			_ = jsonUnmarshalHeaders(nativeText(raw), headers)
		}
	}
	return headers
}

func (c *attachedClient) cpParse(ctx context.Context, parser, target string) (string, map[string]string, error) {
	parser = strings.TrimPrefix(parser, "parse:")
	if strings.HasPrefix(parser, "proxy://") {
		params, err := url.ParseQuery(strings.TrimPrefix(parser, "proxy://"))
		if err != nil {
			return "", nil, errors.New("内置辅助解析参数无效")
		}
		params.Set("v", target)
		return c.cpProxy(ctx, params)
	}
	full := parser + target
	if parser == "" {
		full = target
	}
	if parsed, err := url.Parse(full); err == nil && (parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "localhost") && parsed.Query().Get("do") == "py" {
		params := parsed.Query()
		params.Set("v", target)
		return c.cpProxy(ctx, params)
	}
	raw, err := c.cpRaw(ctx, http.MethodGet, full, "", "", nil)
	if err != nil {
		return "", nil, err
	}
	var data any
	var decodeErr error
	if json.Valid([]byte(raw)) {
		data, decodeErr = catpawParseJSON(raw)
	} else {
		data, decodeErr = c.cpDecode(raw)
	}
	if decodeErr == nil {
		address := catpawResponseURL(data)
		if isProviderHTTPMediaURL(address) {
			return address, catpawParserHeaders(data), nil
		}
	}
	if strings.HasPrefix(strings.TrimSpace(raw), "#EXTM3U") {
		return full, map[string]string{}, nil
	}
	clean := strings.ReplaceAll(raw, "\\/", "/")
	if match := catpawHTMLMedia.FindStringSubmatch(clean); len(match) > 1 {
		return resolveProviderURL(full, html.UnescapeString(match[1])), map[string]string{"Referer": full}, nil
	}
	return "", nil, errors.New("解析线路未返回媒体地址")
}

func (c *attachedClient) cpParseMany(ctx context.Context, parsers []string, target string, headers map[string]string) (string, map[string]string, error) {
	for _, parser := range parsers {
		if parser == "" {
			continue
		}
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		address, extra, err := c.cpParse(ctx, parser, target)
		if err == nil && isProviderHTTPMediaURL(address) {
			for k, v := range extra {
				headers[k] = v
			}
			return address, headers, nil
		}
	}
	return target, headers, errors.New("本线路的解析接口均未返回媒体地址")
}

func (c *attachedClient) cpFallback(ctx context.Context, target string) (string, map[string]string, error) {
	if maccmsDirectMediaURL(target) != "" {
		return target, map[string]string{}, nil
	}
	var parsers []map[string]any
	decoder := json.NewDecoder(strings.NewReader(c.d.attachedAccess["catpaw_playback"].Settings["parses"]))
	decoder.UseNumber()
	if decoder.Decode(&parsers) != nil {
		return "", nil, errors.New("内置播放解析线路缺失")
	}
	for _, parser := range parsers {
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		api := mapString(parser, "url")
		address, headers, err := c.cpParse(ctx, api, target)
		if err == nil && isProviderHTTPMediaURL(address) {
			return address, headers, nil
		}
	}
	return "", nil, errors.New("内置解析线路未返回可播放地址；网页脚本线路可能需要站源更新")
}
