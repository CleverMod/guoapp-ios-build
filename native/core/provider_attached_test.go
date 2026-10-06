package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestAttachedSourceRegistryCategoriesAndSearch(t *testing.T) {
	seen := map[string]bool{}
	for _, provider := range attachedProviders {
		if seen[provider.id] || !isHuangguoProviderSource(provider.id) || canonicalProviderSource(provider.id) != provider.id {
			t.Fatal("source registry mismatch", provider.id)
		}
		seen[provider.id] = true
		categories := map[string]bool{}
		for _, category := range provider.categories {
			if categories[category.id] || !validNativeCategory(provider.id, category.id) {
				t.Fatal("invalid category", provider.id, category.id)
			}
			categories[category.id] = true
		}
		if attachedSearchSource(provider.id) != provider.search {
			t.Fatal("search capability mismatch", provider.id)
		}
	}
	if len(seen) != 30 || !isHuangguoProviderSource(sourceYeguoWorker) {
		t.Fatal("attached source count or restored worker mismatch")
	}
}

func TestAttachedCatalogPreservesIdentityAndDoesNotFetchCovers(t *testing.T) {
	d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/drama/home/search" {
			t.Fatal("unexpected request", request.URL.Path)
		}
		var body map[string]any
		if json.NewDecoder(request.Body).Decode(&body) != nil || body["subject"] != "全部主题" || body["searchWord"] != "合成" {
			t.Fatal("catalog payload mismatch")
		}
		return sourceFixtureResponse(request, 200, `{"code":200,"data":[{"oneId":"123","title":"合成目录","vertPoster":"https://image.example.test/poster.png"}]}`), nil
	})
	rows, more, err := d.fetchAttachedCatalogPage(context.Background(), "weiguan", 1, "", "合成")
	if err != nil || len(rows) != 1 || rows[0].ID != "weiguan:123" || !more {
		t.Fatal("catalog identity mismatch", rows, more, err)
	}
}

func TestAttachedHTMLUsesLongestLineAndBoundEpisode(t *testing.T) {
	d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/vod/detail/321.html":
			return sourceFixtureResponse(request, 200, `<h1>合成剧</h1><a href="/vod/play/321-1-1.html">第1集</a><a href="/vod/play/321-2-1.html">第1集</a><a href="/vod/play/321-2-2.html">第2集</a>`), nil
		case "/vod/play/321-2-2.html":
			return sourceFixtureResponse(request, 200, `<script>var player_aaaa={"encrypt":2,"url":"`+base64.StdEncoding.EncodeToString([]byte("https://media.example.test/2.mp4"))+`"};</script>`), nil
		default:
			t.Fatal("unexpected request", request.URL.Path)
			return nil, nil
		}
	})
	_, chapters, err := d.fetchAttachedDetail(context.Background(), "xiaobao", "321")
	if err != nil || len(chapters) != 2 || chapters[1].ID != "xiaobao:321:2-2" {
		t.Fatal("line or episode mismatch", chapters, err)
	}
	media, err := d.resolveAttachedMedia(context.Background(), Task{DramaID: "xiaobao:321", Chapter: chapters[1]})
	if err != nil || media.URL != "https://media.example.test/2.mp4" {
		t.Fatal("player decoding mismatch", media, err)
	}
	if _, err = d.resolveAttachedMedia(context.Background(), Task{DramaID: "xiaobao:321", Chapter: Chapter{ID: "xiaobao:999:2-2", Source: "xiaobao"}}); err == nil {
		t.Fatal("accepted unrelated episode")
	}
}

func TestAttachedNuxtReferencesAndOpaqueHLS(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader(`<script id="__NUXT_DATA__">[{"episode_title":1,"sort":2,"video_url":3},"第2集",2,"https://media.example.test/2.m3u8"]</script>`))
	rows := attachedNuxtRows(doc)
	row := attachedObject(rows[0])
	if attachedNuxtString(rows, row, "sort") != "2" || attachedNuxtString(rows, row, "video_url") != "https://media.example.test/2.m3u8" {
		t.Fatal("Nuxt reference resolution changed")
	}
	d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/play.php" || request.Header.Get("Referer") != "https://site.example.test/" {
			t.Fatal("opaque HLS request mismatch")
		}
		return sourceFixtureResponse(request, 200, "#EXTM3U\n#EXTINF:4,\npart.ts\n#EXT-X-ENDLIST\n"), nil
	})
	c, _ := d.attachedClient("batvideo")
	media, err := c.opaqueHLS(context.Background(), providerMedia{URL: "https://site.example.test/play.php?id=7", Referer: "https://site.example.test/"})
	if err != nil || media.Playlist == "" || media.Duration == 0 {
		t.Fatal("opaque playlist was not prepared", err)
	}
}
