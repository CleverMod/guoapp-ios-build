package core

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

func (c *attachedClient) importedWebFetch(ctx context.Context, path string) (string, string, error) {
	if isProviderHTTPMediaURL(path) {
		address, _ := url.Parse(path)
		base, _ := url.Parse(c.p.base)
		if address.Host != base.Host {
			return "", "", errors.New("站源页面地址不属于已登记域名")
		}
		path = address.RequestURI()
	}
	address := c.p.base + path
	body, err := c.importedRaw(ctx, http.MethodGet, address, "", "", map[string]string{
		"Referer": c.p.base + "/", "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
	})
	if err != nil {
		return "", "", err
	}
	if strings.Contains(body, "Just a moment") || strings.Contains(body, "cf-chl-") {
		return "", "", errors.New("站源暂需浏览器验证，请稍后重试")
	}
	if !strings.Contains(body, "<html") && !strings.Contains(body, "<a") && !strings.Contains(body, "<script") {
		return "", "", errors.New("站源未返回有效网页")
	}
	return body, address, nil
}

func (c *attachedClient) importedWebCatalog(ctx context.Context, page int, category, query string) ([]Drama, bool, error) {
	path := "/vod/type/" + url.PathEscape(category)
	if page > 1 {
		path += "-" + strconv.Itoa(page)
	}
	path += ".html"
	if query != "" {
		if page > 1 {
			return []Drama{}, false, nil
		}
		path = "/search.html?" + attachedParams("wd", query, "submit", "").Encode()
	}
	body, address, err := c.importedWebFetch(ctx, path)
	if err != nil {
		return nil, false, err
	}
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	rows := []Drama{}
	seen := map[string]bool{}
	pattern := regexp.MustCompile(`^/vod/detail/([0-9]+)\.html$`)
	for _, a := range providerHTMLNodes(doc, func(n *html.Node) bool { return n.Data == "a" && providerHTMLClass(n, "myui-vodlist__thumb") }) {
		parsed, _ := url.Parse(providerHTMLAttr(a, "href"))
		if parsed == nil {
			continue
		}
		match := pattern.FindStringSubmatch(parsed.Path)
		if len(match) < 2 || seen[match[1]] {
			continue
		}
		id := match[1]
		block := attachedCardNode(a)
		name := firstNonEmpty(providerHTMLAttr(a, "title"), attachedHTMLHeading(block, "h3", "h4"))
		if name == "" {
			for _, img := range providerHTMLNodes(block, func(n *html.Node) bool { return n.Data == "img" }) {
				name = providerHTMLAttr(img, "alt")
				if name != "" {
					break
				}
			}
		}
		pic := firstNonEmpty(providerHTMLAttr(a, "data-original"), attachedImageAddress(block))
		if pic != "" {
			pic = resolveProviderURL(address, pic)
		}
		drama := c.importedDrama(map[string]any{"name": name, "pic": pic}, id)
		if drama.ID != "" {
			rows = append(rows, drama)
			seen[id] = true
		}
	}
	more := false
	if query == "" {
		for _, a := range providerHTMLNodes(doc, func(n *html.Node) bool { return n.Data == "a" }) {
			label := providerHTMLText(a)
			if n, e := strconv.Atoi(label); e == nil && n > page {
				more = true
			}
			if strings.Contains(label, "下一页") && providerHTMLAttr(a, "href") != "#" {
				more = true
			}
		}
	}
	return rows, more, nil
}

func (c *attachedClient) importedWebDetail(ctx context.Context, id string) (Drama, []Chapter, error) {
	if !webProviderNumericID.MatchString(id) {
		return Drama{}, nil, errors.New("小宝影片标识无效")
	}
	body, address, err := c.importedWebFetch(ctx, "/vod/detail/"+id+".html")
	if err != nil {
		return Drama{}, nil, err
	}
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return Drama{}, nil, err
	}
	row := map[string]any{
		"name":    attachedHTMLHeading(doc, "h1"),
		"pic":     firstNonEmpty(attachedMeta(doc, "og:image"), attachedImageAddress(providerHTMLFirstClass(doc, "myui-content__thumb", "img"))),
		"content": attachedMeta(doc, "description"),
	}
	lines := []importedLine{}
	for _, tab := range providerHTMLNodes(doc, func(n *html.Node) bool {
		return n.Data == "a" && strings.HasPrefix(providerHTMLAttr(n, "href"), "#playlist")
	}) {
		key := strings.TrimPrefix(providerHTMLAttr(tab, "href"), "#")
		line := importedLine{key: key + "~" + providerHTMLText(tab), name: providerHTMLText(tab)}
		nodes := providerHTMLNodes(doc, func(n *html.Node) bool { return providerHTMLAttr(n, "id") == key })
		if len(nodes) == 0 {
			continue
		}
		for _, a := range providerHTMLNodes(nodes[0], func(n *html.Node) bool { return n.Data == "a" }) {
			href := providerHTMLAttr(a, "href")
			match := regexp.MustCompile(`^/vod/play/([0-9]+)-[0-9]+-[0-9]+\.html$`).FindStringSubmatch(href)
			if len(match) > 1 && match[1] == id {
				line.episodes = append(line.episodes, importedEpisode{name: providerHTMLText(a), url: resolveProviderURL(address, href)})
			}
		}
		if len(line.episodes) > 0 {
			lines = append(lines, line)
		}
	}
	return c.importedDetailResult(id, row, lines)
}

func (c *attachedClient) importedWebPlay(ctx context.Context, p importedPayload) (providerMedia, error) {
	body, address, err := c.importedWebFetch(ctx, p.URL)
	if err != nil {
		return providerMedia{}, err
	}
	row := attachedPlayerJSON(body)
	target := mapString(row, "url")
	switch mapString(row, "encrypt") {
	case "1":
		target, _ = url.PathUnescape(target)
	case "2":
		plain, e := importedUnbase64(target)
		if e != nil {
			return providerMedia{}, errors.New("站源播放地址解码失败")
		}
		target = string(plain)
		if !strings.HasPrefix(target, "http") {
			target, _ = url.PathUnescape(target)
		}
	}
	target = resolveProviderURL(address, target)
	if maccmsDirectMediaURL(target) == "" {
		return providerMedia{}, errors.New("此在线线路未返回直连媒体，站源要求外部解析")
	}
	return importedURLMedia(target, c.agent(), c.p.base+"/", nil)
}
