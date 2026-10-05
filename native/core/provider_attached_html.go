package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	stdhtml "html"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

var attachedBase64Outer = regexp.MustCompile(`\w+\('([A-Za-z0-9+/=]{1000,})'`)
var attachedBase64Inner = regexp.MustCompile(`document\.write\(d\('([^']+)'\)\);?`)
var attachedHiddenSpan = regexp.MustCompile(`(?is)<span\b[^>]*style=["'][^"']*display\s*:\s*none[^"']*["'][^>]*>.*?</span>`)
var attachedHLSCall = regexp.MustCompile(`(?is)playFilteredHLS\(\s*['"]video['"]\s*,\s*['"]([^'"]+)['"]`)
var attachedMediaURL = regexp.MustCompile(`https?://[^\s"'<>\\]+\.(?:m3u8|mp4)(?:\?[^\s"'<>\\]*)?`)
var attachedSlug = regexp.MustCompile(`^[A-Za-z0-9_\-]+$`)

func attachedUnpackHTML(body string) string {
	if match := attachedBase64Outer.FindStringSubmatch(body); len(match) > 1 {
		letters := []byte(match[1])
		for i, j := 0, len(letters)-1; i < j; i, j = i+1, j-1 {
			letters[i], letters[j] = letters[j], letters[i]
		}
		if plain, err := base64.StdEncoding.DecodeString(string(letters)); err == nil {
			text := string(plain)
			if index := strings.Index(strings.ToUpper(text), "<!DOCTYPE"); index >= 0 {
				text = text[index:]
			}
			body = text
		}
	}
	body = attachedBase64Inner.ReplaceAllStringFunc(body, func(value string) string {
		match := attachedBase64Inner.FindStringSubmatch(value)
		plain, err := base64.StdEncoding.DecodeString(match[1])
		if err != nil {
			return ""
		}
		return string(plain)
	})
	return attachedHiddenSpan.ReplaceAllString(body, "")
}
func (c *attachedClient) htmlFetch(ctx context.Context, target string) (string, error) {
	raw, _, err := c.raw(ctx, http.MethodGet, target, "", "", c.access.Headers)
	if err != nil {
		return "", err
	}
	if c.p.id == "batvideo" {
		raw = attachedUnpackHTML(raw)
	}
	if c.p.id == "duanjuone" && strings.Contains(raw, "cnaccess.duanju.one") && strings.Contains(raw, "_token") {
		doc, e := html.Parse(strings.NewReader(raw))
		if e != nil {
			return "", e
		}
		token := ""
		for _, node := range providerHTMLNodes(doc, func(n *html.Node) bool { return n.Data == "input" && providerHTMLAttr(n, "name") == "_token" }) {
			token = providerHTMLAttr(node, "value")
		}
		if token == "" {
			return "", errors.New("短剧one访问提示页缺少确认令牌")
		}
		_, _, e = c.raw(ctx, http.MethodPost, "https://cnaccess.duanju.one/accept", "application/x-www-form-urlencoded", attachedParams("_token", token).Encode(), map[string]string{"Referer": "https://cnaccess.duanju.one/", "Origin": "https://cnaccess.duanju.one"})
		if e != nil {
			return "", e
		}
		raw, _, err = c.raw(ctx, http.MethodGet, target, "", "", c.access.Headers)
	}
	return raw, err
}
func attachedEscapePath(value string) string {
	parts := strings.Split(value, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}
func (c *attachedClient) htmlCatalogPath(page int, cat attachedCategory, query string) (string, bool) {
	pg := strconv.Itoa(page)
	switch c.p.id {
	case "xiaobao":
		if query != "" {
			return "/search.html?" + attachedParams("wd", query, "submit", "").Encode(), false
		}
		suffix := ""
		if page > 1 {
			suffix = "-" + pg
		}
		return "/vod/type/" + cat.value + suffix + ".html", true
	case "batvideo":
		if query != "" {
			return "/search.php?" + attachedParams("content", query, "type", "1").Encode(), false
		}
		return "/list.php?" + attachedParams("id", cat.value, "page", pg).Encode(), true
	case "dj51":
		path := cat.path
		if page > 1 {
			path = strings.TrimRight(path, "/") + "/page/" + pg + "/"
		}
		return path, true
	case "chengguo", "huanggua":
		path := cat.path
		if query != "" {
			path = "/search?q=" + url.QueryEscape(query)
		}
		parsed, _ := url.Parse(path)
		v := parsed.Query()
		v.Set("page", pg)
		parsed.RawQuery = v.Encode()
		return parsed.String(), true
	case "dj91", "huangdou2":
		if query != "" {
			path := "/search/"
			if c.p.id == "huangdou2" {
				path = "/search"
			}
			return path + "?" + attachedParams("q", query, "page", pg).Encode(), true
		}
		path := "/" + cat.value + "/"
		if strings.HasPrefix(cat.value, "biaoqian:") {
			path = "/biaoqian/" + url.PathEscape(strings.TrimPrefix(cat.value, "biaoqian:")) + "/"
		}
		if cat.value == "home" {
			path = "/"
		}
		if page > 1 {
			if c.p.id == "huangdou2" {
				path += pg + "/"
			} else {
				path += "page/" + pg + "/"
			}
		}
		return path, true
	case "duanjuone":
		v := attachedParams("page", pg)
		if query != "" {
			v.Set("q", query)
		} else {
			v.Set("filter", cat.value)
		}
		return "/dramas?" + v.Encode(), true
	case "wuwu":
		path := "/index.php/vod/type/id/1.html"
		if cat.value != "recommend" {
			path = "/index.php/vod/show/class/" + url.PathEscape(cat.value) + "/id/1.html"
		}
		if query != "" {
			path = "/index.php/vod/search/wd/" + url.PathEscape(query) + ".html"
		}
		return path + "?page=" + pg, true
	}
	return "", false
}
func (c *attachedClient) htmlCardID(address string) string {
	parsed, err := url.Parse(address)
	if err != nil {
		return ""
	}
	path := parsed.Path
	switch c.p.id {
	case "xiaobao":
		re := regexp.MustCompile(`^/vod/detail/([0-9]+)\.html$`)
		if match := re.FindStringSubmatch(path); len(match) > 1 {
			return match[1]
		}
	case "wuwu":
		re := regexp.MustCompile(`^/index\.php/vod/detail/id/([0-9]+)\.html$`)
		if match := re.FindStringSubmatch(path); len(match) > 1 {
			return match[1]
		}
	case "batvideo":
		if path == "/video.php" && webProviderNumericID.MatchString(parsed.Query().Get("id")) {
			return parsed.Query().Get("id")
		}
	case "chengguo", "huanggua", "duanjuone":
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) == 2 && parts[0] == "drama" && attachedSlug.MatchString(parts[1]) {
			return parts[1]
		}
	case "huangdou2":
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) == 2 && parts[0] == "drama" && webProviderNumericID.MatchString(parts[1]) {
			return strings.Join(parts, "/")
		}
	case "dj51":
		re := regexp.MustCompile(`^/play/([0-9]+)/1/?$`)
		if match := re.FindStringSubmatch(path); len(match) > 1 {
			return match[1]
		}
	case "dj91":
		re := regexp.MustCompile(`^/(?:duanju|manju|zhenrenju|shipin|paihang)/[0-9]+-[^/]+/?$`)
		if re.MatchString(path) {
			return strings.Trim(path, "/")
		}
	}
	return ""
}
func attachedCardNode(node *html.Node) *html.Node {
	start := node
	for depth := 0; node != nil && depth < 5; depth++ {
		if node.Data == "li" || node.Data == "dl" || node.Data == "article" || strings.Contains(providerHTMLAttr(node, "class"), "card") || strings.Contains(providerHTMLAttr(node, "class"), "module-item") || providerHTMLAttr(node, "data-track-item-name") != "" {
			return node
		}
		node = node.Parent
	}
	return start
}
func attachedImageAddress(root *html.Node) string {
	for _, node := range providerHTMLNodes(root, func(n *html.Node) bool { return n.Data == "img" }) {
		for _, key := range []string{"data-original", "data-src", "src"} {
			value := providerHTMLAttr(node, key)
			if value != "" && !strings.HasPrefix(value, "data:") {
				return value
			}
		}
	}
	return ""
}
func attachedHTMLHeading(root *html.Node, tags ...string) string {
	for _, tag := range tags {
		for _, node := range providerHTMLNodes(root, func(n *html.Node) bool { return n.Data == tag }) {
			if text := providerHTMLText(node); text != "" {
				return text
			}
		}
	}
	return ""
}
func attachedMeta(root *html.Node, name string) string {
	for _, node := range providerHTMLNodes(root, func(n *html.Node) bool { return n.Data == "meta" }) {
		if providerHTMLAttr(node, "property") == name || providerHTMLAttr(node, "name") == name {
			return providerHTMLAttr(node, "content")
		}
	}
	return ""
}

func (c *attachedClient) htmlCatalog(ctx context.Context, page int, cat attachedCategory, query string) ([]Drama, bool, error) {
	path, paged := c.htmlCatalogPath(page, cat, query)
	if path == "" {
		return nil, false, errors.New("站源目录地址无效")
	}
	if !paged && page > 1 {
		return nil, false, nil
	}
	body, err := c.htmlFetch(ctx, path)
	if err != nil {
		return nil, false, err
	}
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	seen := map[string]bool{}
	out := []Drama{}
	for _, link := range providerHTMLNodes(doc, func(n *html.Node) bool { return n.Data == "a" }) {
		id := c.htmlCardID(providerHTMLAttr(link, "href"))
		if id == "" || seen[id] {
			continue
		}
		block := attachedCardNode(link)
		title := firstNonEmpty(providerHTMLAttr(block, "data-track-item-name"), providerHTMLAttr(link, "title"), attachedHTMLHeading(block, "h3", "h2"))
		if title == "" {
			for _, img := range providerHTMLNodes(block, func(n *html.Node) bool { return n.Data == "img" }) {
				title = providerHTMLAttr(img, "alt")
				if title != "" {
					break
				}
			}
		}
		if title == "" {
			title = providerHTMLText(link)
		}
		row := map[string]any{"id": id, "title": title, "cover": attachedImageAddress(block), "type_name": cat.name}
		drama := c.drama(row, id)
		if drama.ID == "" {
			continue
		}
		seen[id] = true
		out = append(out, drama)
	}
	more := false
	if paged {
		for _, link := range providerHTMLNodes(doc, func(n *html.Node) bool { return n.Data == "a" }) {
			text := providerHTMLText(link)
			if strings.Contains(text, "下一页") || strings.EqualFold(providerHTMLAttr(link, "rel"), "next") {
				href := providerHTMLAttr(link, "href")
				if href != "" && href != "#" && href != "javascript:;" {
					more = true
					break
				}
			}
		}
		if !more {
			re := regexp.MustCompile(`(?:[?&](?:page|pg)=|/page/|/type/[0-9]+-)([0-9]+)`)
			for _, match := range re.FindAllStringSubmatch(body, -1) {
				number, _ := strconv.Atoi(match[1])
				if number > page {
					more = true
					break
				}
			}
		}
	}
	return out, more || paged && len(out) > 0, nil
}

func (c *attachedClient) htmlDetailPath(id string) string {
	switch c.p.id {
	case "xiaobao":
		return "/vod/detail/" + url.PathEscape(id) + ".html"
	case "wuwu":
		return "/index.php/vod/detail/id/" + url.PathEscape(id) + ".html"
	case "batvideo":
		return "/video.php?id=" + url.QueryEscape(id)
	case "dj51":
		return "/play/" + url.PathEscape(id) + "/1/"
	case "dj91", "huangdou2":
		return "/" + attachedEscapePath(id) + "/"
	default:
		return "/drama/" + url.PathEscape(id)
	}
}
func (c *attachedClient) htmlEpisodePath(id string, number int) string {
	seq := strconv.Itoa(number)
	switch c.p.id {
	case "dj91":
		return "/" + attachedEscapePath(id) + "/" + seq + "/"
	case "huangdou2":
		_, leaf, _ := strings.Cut(id, "/")
		if number == 1 {
			return "/video/" + url.PathEscape(leaf) + "/"
		}
		return "/video/" + url.PathEscape(leaf) + "/" + seq + "/"
	case "duanjuone":
		if number == 1 {
			return "/drama/" + url.PathEscape(id)
		}
		return "/drama/" + url.PathEscape(id) + "/ep/" + seq
	case "dj51":
		return "/play/" + url.PathEscape(id) + "/" + seq + "/"
	default:
		return "/play/" + url.PathEscape(id) + "/" + seq
	}
}
func attachedNuxtRows(doc *html.Node) []any {
	for _, node := range providerHTMLNodes(doc, func(n *html.Node) bool { return n.Data == "script" && providerHTMLAttr(n, "id") == "__NUXT_DATA__" }) {
		if node.FirstChild == nil {
			continue
		}
		var rows []any
		decoder := json.NewDecoder(strings.NewReader(node.FirstChild.Data))
		decoder.UseNumber()
		if decoder.Decode(&rows) == nil {
			return rows
		}
	}
	return nil
}
func attachedNuxtRef(rows []any, value any, depth int) any {
	if depth > 16 {
		return nil
	}
	if number, ok := value.(json.Number); ok {
		index, e := strconv.Atoi(string(number))
		if e == nil && index >= 0 && index < len(rows) {
			switch rows[index].(type) {
			case map[string]any, []any:
				return attachedNuxtRef(rows, rows[index], depth+1)
			default:
				return rows[index]
			}
		}
	}
	if list, ok := value.([]any); ok && len(list) == 2 {
		if tag, ok := list[0].(string); ok && (tag == "Reactive" || tag == "ShallowReactive" || tag == "Ref" || tag == "ShallowRef") {
			return attachedNuxtRef(rows, list[1], depth+1)
		}
	}
	return value
}
func attachedNuxtString(rows []any, row map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := attachedNuxtRef(rows, row[key], 0); value != nil {
			if text, ok := value.(string); ok && text != "" {
				return text
			}
			if number, ok := value.(json.Number); ok {
				return string(number)
			}
		}
	}
	return ""
}

func (c *attachedClient) htmlDetail(ctx context.Context, id string) (Drama, []Chapter, error) {
	path := c.htmlDetailPath(id)
	body, err := c.htmlFetch(ctx, path)
	if err != nil {
		return Drama{}, nil, err
	}
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return Drama{}, nil, err
	}
	title := firstNonEmpty(attachedHTMLHeading(doc, "h1"), attachedMeta(doc, "og:title"), attachedHTMLHeading(doc, "title"))
	desc := attachedMeta(doc, "description")
	cover := firstNonEmpty(attachedMeta(doc, "og:image"), attachedImageAddress(doc))
	row := map[string]any{"title": title, "description": desc, "cover": cover}
	chapters := []Chapter{}
	seen := map[string]bool{}
	add := func(key string, number int, label, target string) {
		if number < 1 || number > 2000 || key == "" || seen[key] {
			return
		}
		seen[key] = true
		chapters = append(chapters, attachedChapter(c.p.id, id, key, number, label, "", resolveProviderURL(c.p.base+"/", target)))
	}
	if c.p.id == "batvideo" {
		if match := attachedHLSCall.FindStringSubmatch(body); len(match) > 1 {
			address := resolveProviderURL(c.p.base+"/", stdhtml.UnescapeString(match[1]))
			chapters = append(chapters, attachedChapter(c.p.id, id, "1", 1, "正片", address, c.p.base+path))
		}
	}
	if c.p.id == "dj51" {
		rows := attachedNuxtRows(doc)
		for _, value := range rows {
			item := attachedObject(value)
			if item["drama_name"] != nil {
				row["title"] = attachedNuxtString(rows, item, "drama_name", "video_title")
				row["cover"] = attachedNuxtString(rows, item, "cover_img", "cover")
				row["description"] = attachedNuxtString(rows, item, "description")
			}
			if item["episode_title"] != nil {
				number, _ := strconv.Atoi(attachedNuxtString(rows, item, "sort"))
				add(strconv.Itoa(number), number, attachedNuxtString(rows, item, "episode_title"), c.htmlEpisodePath(id, number))
			}
		}
	}
	for _, link := range providerHTMLNodes(doc, func(n *html.Node) bool { return n.Data == "a" }) {
		href := providerHTMLAttr(link, "href")
		parsed, e := url.Parse(href)
		if e != nil {
			continue
		}
		target := parsed.Path
		label := providerHTMLText(link)
		switch c.p.id {
		case "xiaobao":
			re := regexp.MustCompile(`^/vod/play/` + regexp.QuoteMeta(id) + `-([0-9]+)-([0-9]+)\.html$`)
			if match := re.FindStringSubmatch(target); len(match) > 2 {
				number, _ := strconv.Atoi(match[2])
				add(match[1]+"-"+match[2], number, label, href)
			}
		case "wuwu":
			re := regexp.MustCompile(`^/index\.php/vod/play/id/` + regexp.QuoteMeta(id) + `/sid/([0-9]+)/nid/([0-9]+)\.html$`)
			if match := re.FindStringSubmatch(target); len(match) > 2 {
				number, _ := strconv.Atoi(match[2])
				add(match[1]+"-"+match[2], number, label, href)
			}
		case "chengguo", "huanggua", "duanjuone":
			prefix := "/play/" + id + "/"
			if c.p.id == "duanjuone" {
				prefix = "/drama/" + id + "/ep/"
			}
			if strings.HasPrefix(target, prefix) {
				text := strings.Trim(strings.TrimPrefix(target, prefix), "/")
				number, _ := strconv.Atoi(text)
				add(text, number, label, href)
			}
			if c.p.id == "duanjuone" && strings.TrimRight(target, "/") == "/drama/"+id {
				add("1", 1, "第1集", href)
			}
		case "huangdou2", "dj91":
			prefix := "/" + id + "/"
			if c.p.id == "huangdou2" {
				_, leaf, _ := strings.Cut(id, "/")
				prefix = "/video/" + leaf + "/"
			}
			if strings.HasPrefix(target, prefix) {
				text := strings.Trim(strings.TrimPrefix(target, prefix), "/")
				number, _ := strconv.Atoi(text)
				if text == "" {
					text = "1"
					number = 1
				}
				add(text, number, label, href)
			}
		}
	}
	if (c.p.id == "huangdou2" || c.p.id == "dj91") && len(chapters) == 0 {
		for _, node := range providerHTMLNodes(doc, func(n *html.Node) bool { return providerHTMLAttr(n, "data-total") != "" }) {
			count, _ := strconv.Atoi(providerHTMLAttr(node, "data-total"))
			if count > 0 && count <= 2000 {
				for number := 1; number <= count; number++ {
					add(strconv.Itoa(number), number, "", c.htmlEpisodePath(id, number))
				}
				break
			}
		}
	}
	if c.p.id == "xiaobao" || c.p.id == "wuwu" {
		counts := map[string]int{}
		for _, chapter := range chapters {
			key := strings.TrimPrefix(chapter.ID, providerDramaID(c.p.id, id)+":")
			line, _, _ := strings.Cut(key, "-")
			counts[line]++
		}
		best := ""
		for _, chapter := range chapters {
			key := strings.TrimPrefix(chapter.ID, providerDramaID(c.p.id, id)+":")
			line, _, _ := strings.Cut(key, "-")
			if best == "" || counts[line] > counts[best] {
				best = line
			}
		}
		selected := []Chapter{}
		for _, chapter := range chapters {
			key := strings.TrimPrefix(chapter.ID, providerDramaID(c.p.id, id)+":")
			line, _, _ := strings.Cut(key, "-")
			if line == best {
				selected = append(selected, chapter)
			}
		}
		chapters = selected
	}
	sort.SliceStable(chapters, func(i, j int) bool { return attachedEpisodeNumber(chapters[i]) < attachedEpisodeNumber(chapters[j]) })
	drama := c.drama(row, id)
	if drama.ID == "" || len(chapters) == 0 {
		return Drama{}, nil, fmt.Errorf("%s详情页未返回有效剧集，请刷新重试", c.p.name)
	}
	drama.TotalEpisode = len(chapters)
	drama.EpisodeCount = len(chapters)
	return drama, chapters, nil
}

func attachedPlayerJSON(body string) map[string]any {
	re := regexp.MustCompile(`(?is)(?:var\s+)?player_[a-zA-Z0-9_]+\s*=\s*`)
	if loc := re.FindStringIndex(body); loc != nil {
		var row map[string]any
		decoder := json.NewDecoder(strings.NewReader(body[loc[1]:]))
		if decoder.Decode(&row) == nil {
			return row
		}
	}
	return nil
}
func (c *attachedClient) htmlPlay(ctx context.Context, id, key string, chapter Chapter) (providerMedia, error) {
	media := providerMedia{URL: chapter.VideoURL, Referer: c.p.base + "/"}
	if c.p.id == "batvideo" {
		return c.opaqueHLS(ctx, media)
	}
	body, err := c.htmlFetch(ctx, chapter.PageURL)
	if err != nil {
		return providerMedia{}, err
	}
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return providerMedia{}, err
	}
	address := ""
	if c.p.id == "dj51" {
		rows := attachedNuxtRows(doc)
		for _, raw := range rows {
			row := attachedObject(raw)
			number, _ := strconv.Atoi(attachedNuxtString(rows, row, "sort"))
			if row["episode_title"] != nil && number == attachedEpisodeNumber(chapter) {
				address = attachedNuxtString(rows, row, "video_url")
				break
			}
		}
	}
	for _, node := range providerHTMLNodes(doc, func(n *html.Node) bool {
		return n.Data == "script" && (providerHTMLAttr(n, "id") == "ninePlayData" || providerHTMLAttr(n, "id") == "playInitialData")
	}) {
		if node.FirstChild == nil {
			continue
		}
		data, e := attachedDecode(node.FirstChild.Data)
		if e != nil {
			continue
		}
		current := firstPresent(attachedObject(data), "current", "episode")
		row := attachedObject(current)
		if number := attachedInt(row, "number", "sort", "index", "episodeNumber"); number > 0 && number != attachedEpisodeNumber(chapter) {
			return providerMedia{}, errors.New("播放页返回章节不符，请刷新详情")
		}
		address = attachedBestURL(row)
		if address == "" && strings.Contains(mapString(row, "srcHevc"), ".m3u8") {
			address = mapString(row, "srcHevc")
		}
	}
	if address == "" {
		row := attachedPlayerJSON(body)
		address = mapString(row, "url")
		switch mapString(row, "encrypt") {
		case "1":
			address, _ = url.QueryUnescape(address)
		case "2":
			plain, e := base64.StdEncoding.DecodeString(address)
			if e == nil {
				address = string(plain)
			} else {
				address = ""
			}
		}
	}
	if address == "" {
		for _, node := range providerHTMLNodes(doc, func(n *html.Node) bool { return n.Data == "video" || n.Data == "source" }) {
			if value := providerHTMLAttr(node, "src"); value != "" {
				address = value
				break
			}
		}
	}
	if address == "" && (c.p.id == "chengguo" || c.p.id == "huanggua" || c.p.id == "dj51") {
		normalized := strings.NewReplacer(`\u0026`, "&", `\u002F`, "/", `\/`, "/").Replace(body)
		address = attachedMediaURL.FindString(normalized)
	}
	address = stdhtml.UnescapeString(address)
	if address != "" {
		address = resolveProviderURL(chapter.PageURL, address)
	}
	if !isProviderHTTPMediaURL(address) {
		return providerMedia{}, fmt.Errorf("%s未返回可用播放地址", c.p.name)
	}
	media.URL = address
	return media, nil
}
