package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
)

func yeguoWorkerFixture(t *testing.T, data any) string {
	t.Helper()
	plain, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := aesCBCEncrypt(plain, []byte("2acf7e91e9864673"), []byte("1c29882d3ddfcfd6"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"status": 1, "data": base64.StdEncoding.EncodeToString(ciphertext)})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestYeguoWorkerEncryptedCatalogSearchDetailAndPlayback(t *testing.T) {
	var playRequests int
	d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Host != "www.yeguodj.com" || request.URL.RawQuery != "" {
			t.Fatalf("unexpected worker request: %s %s", request.Method, request.URL)
		}
		if request.Header.Get("Content-Type") != "application/json;charset=UTF-8" ||
			request.Header.Get("Referer") != yeguoWorkerBaseURL+"/" || request.Header.Get("Origin") != yeguoWorkerBaseURL ||
			request.Header.Get("User-Agent") != yeguoWorkerAgent {
			t.Fatalf("missing worker headers: %v", request.Header)
		}
		decoder := json.NewDecoder(request.Body)
		decoder.UseNumber()
		var values map[string]any
		if err := decoder.Decode(&values); err != nil {
			t.Fatal(err)
		}
		var data any
		switch request.URL.Path {
		case "/api.php/api/theater/exploreList":
			if mapString(values, "page") != "2" || mapString(values, "background") != "40" {
				t.Fatalf("wrong category request: %v", values)
			}
			data = map[string]any{"list": []any{map[string]any{"video_id": "123", "video_title": "合成短剧", "episode_count": 2}}, "page": 2, "has_more": false}
		case "/api.php/api/search/result":
			if mapString(values, "page") != "3" || mapString(values, "keyword") != "合成" {
				t.Fatalf("wrong search request: %v", values)
			}
			data = map[string]any{"top_list": []any{}, "page": 3}
		case "/api.php/api/playlet/detail":
			if mapString(values, "video_id") != "123" {
				t.Fatalf("wrong detail request: %v", values)
			}
			data = map[string]any{"data": map[string]any{"video_id": "123", "title": "合成短剧", "intro": "合成简介",
				"episodes": []any{map[string]any{"episode_id": "456", "episode": 2}, map[string]any{"id": "455", "sort": 1}}}}
		case "/api.php/api/playlet/play":
			playRequests++
			if mapString(values, "video_id") != "123" || mapString(values, "episode_id") != "456" || mapString(values, "sort") != "2" {
				t.Fatalf("wrong episode request: %v", values)
			}
			data = map[string]any{"episodeAll": []any{
				map[string]any{"id": "455", "sort": 1, "url": "https://media.example.test/wrong.mp4"},
				map[string]any{"id": "456", "sort": 2, "play_url": "https://media.example.test/right.mp4"},
			}}
		default:
			t.Fatalf("unexpected worker route: %s", request.URL.Path)
		}
		return sourceFixtureResponse(request, http.StatusOK, yeguoWorkerFixture(t, data)), nil
	})
	ctx := context.Background()
	items, more, err := d.fetchYeguoWorkerCatalogPage(ctx, 2, "dushi", "")
	if err != nil || more || len(items) != 1 || items[0].ID != "yeguo-worker:123" || items[0].Source != sourceYeguoWorker {
		t.Fatalf("wrong catalog: items=%+v more=%t error=%v", items, more, err)
	}
	items, more, err = d.fetchYeguoWorkerCatalogPage(ctx, 3, "", "合成")
	if err != nil || more || len(items) != 0 {
		t.Fatalf("wrong empty search: items=%+v more=%t error=%v", items, more, err)
	}
	drama, chapters, err := d.fetchYeguoWorkerDetail(ctx, "123")
	if err != nil || drama.ID != "yeguo-worker:123" || len(chapters) != 2 || chapters[1].ID != "yeguo-worker:123:2:456" {
		t.Fatalf("wrong detail: drama=%+v chapters=%+v error=%v", drama, chapters, err)
	}
	media, err := d.resolveProviderMedia(ctx, Task{DramaID: drama.ID, Chapter: chapters[1]})
	if err != nil || media.URL != "https://media.example.test/right.mp4" || playRequests != 1 || media.credentials == nil {
		t.Fatalf("wrong playback: media=%+v requests=%d error=%v", media, playRequests, err)
	}
	request, _ := http.NewRequest(http.MethodGet, media.URL, nil)
	if err := media.credentials.apply(request); err != nil || request.Header.Get("User-Agent") != yeguoWorkerAgent || request.Header.Get("Origin") != yeguoWorkerBaseURL {
		t.Fatalf("media headers lost: headers=%v error=%v", request.Header, err)
	}
}

func TestYeguoWorkerRejectsInvalidCiphertextAndOtherEpisodes(t *testing.T) {
	for _, body := range []string{`{"data":"invalid"}`, `{"data":"YWJj"}`, `{"status":-1,"data":{}}`} {
		if _, err := decodeYeguoWorkerResponse([]byte(body)); err == nil {
			t.Fatalf("accepted invalid response: %s", body)
		}
	}
	d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
		return sourceFixtureResponse(request, http.StatusOK, `{"data":{"episodeAll":[{"id":455,"sort":1,"url":"https://media.example.test/wrong.mp4"}]}}`), nil
	})
	if _, err := d.resolveYeguoWorkerMedia(context.Background(), Task{DramaID: "yeguo-worker:123", Chapter: Chapter{ID: "yeguo-worker:123:2:456"}}); err == nil {
		t.Fatal("played another episode when requested episode was missing")
	}
}
