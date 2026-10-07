package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCatpawRegistryPreservesSourceAndHelperIdentity(t *testing.T) {
	if len(catpawDefinitions) != 33 {
		t.Fatal("unexpected CatPaw entry count")
	}
	scripts := map[string]bool{}
	content, helpers, existing := 0, 0, 0
	for _, entry := range catpawDefinitions {
		scripts[entry.Script] = true
		if len(entry.SHA256) != 64 {
			t.Fatal("missing source fingerprint", entry.ID)
		}
		if entry.Helper {
			helpers++
			if isAttachedSource(entry.ID) || nativeSourceAvailable(entry.ID) {
				t.Fatal("helper became a content source", entry.ID)
			}
		} else if entry.Existing {
			existing++
			if entry.ID != "jumi" {
				t.Fatal("unexpected reused provider", entry.ID)
			}
		} else {
			content++
			p, ok := attachedProviderByID(entry.ID)
			if !ok || p.kind != "catpaw" || !attachedSearchSource(entry.ID) || !validNativeCategory(entry.ID, "movie") || !validNativeCategory(entry.ID, "") {
				t.Fatal("source routing mismatch", entry.ID)
			}
		}
	}
	if len(scripts) != 24 || content != 28 || helpers != 4 || existing != 1 {
		t.Fatal("source roles changed")
	}
}

func TestCatpawXXHashAndBoundedProtobuf(t *testing.T) {
	for value, want := range map[string]uint64{"": 0xef46db3751d8e999, "hello": 0x26c7827d889f6da3} {
		if got := catpawXXHash([]byte(value), 0); got != want {
			t.Fatalf("XXHash fixture mismatch: %x", got)
		}
	}
	item := append(xpgBytes(1, []byte("77")), xpgBytes(2, []byte("合成番剧"))...)
	body := append(xpgBytes(3, item), xpgBytes(3, item)...)
	row, err := catpawPB(body, 0)
	if err != nil || len(catpawObjects(row["3"])) != 2 {
		t.Fatal("repeated message was lost", err)
	}
	if _, err = catpawPB([]byte{0x1a, 0xff}, 0); err == nil {
		t.Fatal("accepted truncated Protobuf")
	}
}

func TestCatpawBajiePreservesLinesRefreshesSelectedEpisodeAndDoesNotFetchCovers(t *testing.T) {
	calls := []string{}
	d := sourceFixtureDownloader(t, func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.Path)
		if strings.Contains(r.URL.Path, "poster") {
			t.Fatal("attempted to load a source image")
		}
		switch r.URL.Path {
		case "/api/v1/app/screen/screenMovie":
			return sourceFixtureResponse(r, 200, "{\"data\":{\"records\":[{\"id\":\"7\",\"name\":\"合成剧集\",\"cover\":\"https://images.test/poster.png\"}]}}"), nil
		case "/api/v1/app/play/movieDetails":
			payload := map[string]any{}
			if json.NewDecoder(r.Body).Decode(&payload) != nil {
				t.Fatal("invalid request")
			}
			if payload["typeId"] == "M16" {
				if payload["playerId"] != "20" || payload["episodeIndex"] != "1" {
					t.Fatal("selected backup episode changed", payload)
				}
				return sourceFixtureResponse(r, 200, "{\"data\":{\"url\":\"play-token\",\"playerId\":\"20\"}}"), nil
			}
			return sourceFixtureResponse(r, 200, "{\"data\":{\"playerId\":\"10\",\"episodeList\":[{\"id\":\"a\",\"episode\":\"第1集\"},{\"id\":\"b\",\"episode\":\"第2集\"}],\"moviePlayerList\":[{\"id\":\"10\",\"moviePlayerName\":\"主线\",\"episodeTotal\":2},{\"id\":\"20\",\"moviePlayerName\":\"备用线\",\"episodeTotal\":2}]}}"), nil
		case "/api/v1/app/play/movieDesc":
			return sourceFixtureResponse(r, 200, "{\"data\":{\"id\":\"7\",\"name\":\"合成剧集\"}}"), nil
		case "/api/v1/app/play/analysisMovieUrl":
			if r.URL.Query().Get("playerId") != "20" || r.URL.Query().Get("playerUrl") != "play-token" {
				t.Fatal("parser lost selected player")
			}
			return sourceFixtureResponse(r, 200, "{\"data\":\"https://media.test/movie.mp4\"}"), nil
		}
		t.Fatal("unexpected request", r.URL.Path)
		return nil, nil
	})
	c, err := d.attachedClient("catpaw_bajie")
	if err != nil {
		t.Fatal(err)
	}
	c.catpaw = &catpawState{script: "八戒影视.py", base: "https://bajie.test", ext: map[string]any{}, headers: map[string]string{"token": "fixture-token"}, values: map[string]string{"userID": "visitor"}, ready: time.Now()}
	rows, _, err := d.fetchAttachedCatalogPage(context.Background(), c.p.id, 1, "1", "")
	if err != nil || len(rows) != 1 {
		t.Fatal("catalog mismatch", err)
	}
	drama, chapters, err := d.fetchAttachedDetail(context.Background(), c.p.id, "7")
	if err != nil || len(chapters) != 4 || chapterEpisodeCount(chapters) != 2 || chapters[0].LineID == chapters[2].LineID {
		t.Fatal("line separation failed", err)
	}
	media, err := d.resolveAttachedMedia(context.Background(), Task{DramaID: drama.ID, Chapter: chapters[3]})
	if err != nil || media.URL != "https://media.test/movie.mp4" {
		t.Fatal("selected episode did not resolve", err)
	}
	if len(calls) != 7 {
		t.Fatal("unexpected metadata or image requests", calls)
	}
}

func TestCatpawHeadersAndSigningStayOnMediaOrigin(t *testing.T) {
	media, err := catpawURLMedia("https://media.test/movie.m3u8", map[string]string{"User-Agent": "fixture-player", "Authorization": "fixture-access", "Origin": "https://site.test"})
	if err != nil {
		t.Fatal(err)
	}
	media.credentials.rewrite = func(address *url.URL) {
		params := address.Query()
		params.Set("signature", "fixture")
		address.RawQuery = params.Encode()
	}
	first, _ := http.NewRequest(http.MethodGet, media.URL, nil)
	if err = media.credentials.apply(first); err != nil || first.Header.Get("Authorization") != "fixture-access" || first.URL.Query().Get("signature") != "fixture" {
		t.Fatal("media credentials missing", err)
	}
	next, _ := http.NewRequest(http.MethodGet, "https://other.test/movie.ts", nil)
	next.Header = first.Header.Clone()
	if err = media.credentials.apply(next); err != nil || next.Header.Get("Authorization") != "" || next.URL.Query().Get("signature") != "" {
		t.Fatal("media credentials leaked across hosts", err)
	}
}
