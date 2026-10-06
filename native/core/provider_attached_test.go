package core

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"testing"
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
	if len(seen) != 20 || isHuangguoProviderSource("yeguo-worker") {
		t.Fatal("attached source count or retired worker mismatch")
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

func TestAttachedOpaqueHLS(t *testing.T) {
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

func TestAttachedTheaterArraysRemainPlayable(t *testing.T) {
	d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "synthetic-token" {
			t.Fatal("authorization changed")
		}
		switch request.URL.Path {
		case "/v1/theater/home_page":
			return sourceFixtureResponse(request, 200, `{"code":200,"data":{"list":[{"theater":{"id":42,"title":"合成剧","cover_url":"https://image.example.test/cover.png"}}]}}`), nil
		case "/v2/theater_parent/detail":
			return sourceFixtureResponse(request, 200, `{"code":200,"data":{"id":42,"title":"合成剧","theaters":[{"id":900,"num":1,"son_video_url":"https://media.example.test/1.mp4"}]}}`), nil
		default:
			t.Fatal("unexpected request", request.URL.Path)
			return nil, nil
		}
	})
	d.attachedAccess = map[string]attachedAccess{"qixing": {Headers: map[string]string{"authorization": "synthetic-token"}}}
	rows, _, err := d.fetchAttachedCatalogPage(context.Background(), "qixing", 1, "", "")
	if err != nil || len(rows) != 1 || rows[0].ID != "qixing:42" {
		t.Fatal("theater array was discarded", err)
	}
	_, chapters, err := d.fetchAttachedDetail(context.Background(), "qixing", "42")
	if err != nil || len(chapters) != 1 || chapters[0].ID != "qixing:42:900" {
		t.Fatal("episode identity changed", err)
	}
	media, err := d.resolveAttachedMedia(context.Background(), Task{DramaID: rows[0].ID, Chapter: chapters[0]})
	if err != nil || media.URL != "https://media.example.test/1.mp4" {
		t.Fatal("episode playback URL was discarded", err)
	}
}

func TestAttachedYimiPreservesSignedQueryBytes(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const query = "key=channel_c6f50cd9&p1=synthetic&p35=abc%2fdef*&page=1&pc=10&usr=synthetic-user"
	d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
		if request.URL.RawQuery != query || request.Header.Get("User-Agent") != "synthetic-device-agent" {
			t.Fatal("signed query encoding or device agent changed")
		}
		message := "&" + query + "&" + request.URL.Path + "&" + request.Header.Get("x-sig-timestamp") + "&synthetic-sec"
		sum := sha256.Sum256([]byte(message))
		signature, err := base64.StdEncoding.DecodeString(request.Header.Get("x-sig-sign"))
		if err != nil || rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], signature) != nil {
			t.Fatal("signature no longer matches original query bytes")
		}
		return sourceFixtureResponse(request, 200, `{"body":{"list":[{"short_plays":[{"id":42,"short_play_name":"合成剧"}]}]}}`), nil
	})
	d.attachedAccess = map[string]attachedAccess{"yimi": {
		PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})),
		Headers:    map[string]string{"User-Agent": "synthetic-device-agent"},
		Settings:   map[string]string{"commonQuery": "p1=synthetic&p35=abc%2fdef*", "userQuery": "usr=synthetic-user", "listSec": "synthetic-sec"},
	}}
	rows, _, err := d.fetchAttachedCatalogPage(context.Background(), "yimi", 1, "", "")
	if err != nil || len(rows) != 1 {
		t.Fatal("signed catalog request failed", err)
	}
}

func TestAttachedBatDecodedScriptTitlesRemainVisible(t *testing.T) {
	d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
		body := `<dl><dt><a href="/video.php?id=42"><script>document.write(d('` + base64.StdEncoding.EncodeToString([]byte("合成标题")) + `'));</script></a></dt></dl>`
		return sourceFixtureResponse(request, 200, body), nil
	})
	rows, _, err := d.fetchAttachedCatalogPage(context.Background(), "batvideo", 1, "", "")
	if err != nil || len(rows) != 1 || rows[0].Title != "合成标题" {
		t.Fatal("decoded title was treated as JavaScript", err)
	}
}
