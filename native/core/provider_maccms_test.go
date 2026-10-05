package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestMaccmsRegistrySeparatesBackendsAndMatchesExactHosts(t *testing.T) {
	if len(maccmsProviders) != 32 {
		t.Fatal("wrong audited collector count")
	}
	ids, endpoints := map[string]bool{}, map[string]bool{}
	for _, provider := range maccmsProviders {
		endpoint := strings.TrimRight(provider.baseURL+provider.apiPath, "/")
		address, err := url.Parse(provider.baseURL)
		if err != nil || address.Host == "" || ids[provider.id] || endpoints[endpoint] {
			t.Fatal("invalid or repeated backend", provider.id)
		}
		ids[provider.id], endpoints[endpoint] = true, true
		if canonicalProviderSource(strings.ToUpper(provider.id)) != provider.id || providerSourceForURL(endpoint) != provider.id || canonicalProviderSource(address.Hostname()) != provider.id {
			t.Fatal("source aliases do not match backend", provider.id)
		}
		if providerSourceForURL(provider.baseURL+".example.test"+provider.apiPath) != "" {
			t.Fatal("accepted a hostname suffix", provider.id)
		}
	}
	for _, excluded := range []string{"huohu", "suonishandian", "kuwo", "yunpan"} {
		if isHuangguoProviderSource(excluded) {
			t.Fatal("registered an excluded or duplicate source", excluded)
		}
	}
}

func TestMaccmsBestLineKeepsDirectEpisodeIdentity(t *testing.T) {
	line := maccmsBestLine(map[string]any{"vod_play_url": "解析$https://media.example.test/player.html?url=a.m3u8#第一集$https://media.example.test/short.mp4$$$" +
		"第一集$https://media.example.test/one.m3u8?token=a$b&amp;part=1#失效$javascript:alert(1)#第三集$https://media.example.test/three.MP4#https://media.example.test/four.flv"})
	if line.index != 1 || len(line.episodes) != 3 {
		t.Fatalf("wrong direct line: %+v", line)
	}
	if line.episodes[0].url != "https://media.example.test/one.m3u8?token=a$b&part=1" || line.episodes[1].index != 2 || line.episodes[2].title != "第4集" {
		t.Fatalf("lost episode identity or signed query: %+v", line.episodes)
	}
	for _, address := range []string{"https://media.example.test/player?url=a.m3u8", "https://user:password@media.example.test/video.mp4", "file:///video.mp4", "//media.example.test/video.m3u8"} {
		if maccmsDirectMediaURL(address) != "" {
			t.Fatalf("accepted an unsupported media address: %s", address)
		}
	}
}

func TestMaccmsResponseValidatesJSONAndSuccessCode(t *testing.T) {
	for _, body := range []string{`{"code":1,"list":[]}`, "\ufeff" + `{"code":"1","page":"2","pagecount":3,"list":[]}`} {
		if _, err := decodeMaccmsResponse(body); err != nil {
			t.Fatal(err)
		}
	}
	for _, body := range []string{`<html>暂不可用</html>`, `{"code":0,"msg":"接口不可用"}`, `{"list":[]}`} {
		if _, err := decodeMaccmsResponse(body); err == nil {
			t.Fatalf("accepted an unsuccessful API result: %s", body)
		}
	}
}

func TestMaccmsCatalogUsesRemoteCategoriesAndPagedSearch(t *testing.T) {
	for _, entry := range maccmsProviders {
		source := entry.id
		t.Run(source, func(t *testing.T) {
			provider, _ := maccmsProviderForSource(source)
			calls := 0
			d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
				calls++
				if providerSourceForURL(request.URL.String()) != source || request.URL.Path != provider.apiPath || request.Method != http.MethodGet {
					t.Fatalf("wrong API route: %s", request.URL.String())
				}
				query := request.URL.Query()
				body := ""
				switch calls {
				case 1:
					if query.Get("ac") != "list" || query.Get("pg") != "1" {
						t.Fatal("wrong category parameters", query)
					}
					body = `{"code":1,"class":[{"type_id":3,"type_name":"合成动漫"},{"type_id":"3","type_name":"重复分类"},{"type_id":0,"type_name":"无效分类"}]}`
				case 2:
					if query.Get("ac") != "detail" || query.Get("pg") != "2" || query.Get("t") != "3" || query.Get("wd") != "" {
						t.Fatal("wrong catalog parameters", query)
					}
					body = `{"code":"1","page":"2","pagecount":3,"list":[{"vod_id":123,"vod_name":"合成影片"},{"vod_id":"123","vod_name":"重复影片"},{"vod_id":"invalid","vod_name":"无效 ID"}]}`
				case 3:
					if query.Get("ac") != "detail" || query.Get("pg") != "3" || query.Get("t") != "" || query.Get("wd") != "合成 & 动漫" {
						t.Fatal("wrong remote search parameters", query)
					}
					body = `{"code":1,"page":3,"pagecount":"3","list":[]}`
				default:
					return nil, errors.New("fixture rejects image, media and extra requests")
				}
				response := sourceFixtureResponse(request, http.StatusOK, body)
				response.Header.Set("Content-Type", "text/html")
				return response, nil
			})
			categories, err := d.fetchMaccmsCategories(context.Background(), source)
			if err != nil || len(categories) != 1 || categories[0].ID != "3" {
				t.Fatalf("wrong remote categories: %+v %v", categories, err)
			}
			items, more, err := d.fetchMaccmsCatalogPage(context.Background(), source, 2, "3", "")
			if err != nil || !more || len(items) != 1 || items[0].ID != source+":123" {
				t.Fatalf("wrong catalog identity or pagination: %+v %v %v", items, more, err)
			}
			items, more, err = d.fetchMaccmsCatalogPage(context.Background(), source, 3, "3", "合成 & 动漫")
			if err != nil || more || len(items) != 0 {
				t.Fatalf("wrong empty search result: %+v %v %v", items, more, err)
			}
			if _, _, err := d.fetchMaccmsCatalogPage(context.Background(), source, 1, "3&wd=invalid", ""); err == nil || calls != 3 {
				t.Fatal("invalid category reached the network")
			}
		})
	}
}

func TestMaccmsCatalogRejectsWrongPageAndPreservesFallbackPagination(t *testing.T) {
	for _, fixture := range []struct {
		name    string
		body    string
		wantErr bool
		more    bool
	}{
		{name: "wrong page", body: `{"code":1,"page":1,"list":[]}`, wantErr: true},
		{name: "limit fallback", body: `{"code":1,"page":"2","limit":"1","list":[{"vod_id":123,"vod_name":"合成影片"}]}`, more: true},
		{name: "last page", body: `{"code":1,"page":2,"pagecount":"2","list":[{"vod_id":123,"vod_name":"合成影片"}]}`},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
				return sourceFixtureResponse(request, http.StatusOK, fixture.body), nil
			})
			_, more, err := d.fetchMaccmsCatalogPage(context.Background(), sourceLiangzi, 2, "", "")
			if (err != nil) != fixture.wantErr || more != fixture.more {
				t.Fatalf("wrong pagination: more=%v err=%v", more, err)
			}
		})
	}
}

func TestMaccmsResolutionRefreshesURLAndRejectsForeignChapters(t *testing.T) {
	for _, entry := range maccmsProviders {
		source := entry.id
		t.Run(source, func(t *testing.T) {
			details, playlists := 0, 0
			d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
				provider, _ := maccmsProviderForSource(source)
				if request.URL.Path == provider.apiPath {
					details++
					if request.URL.Query().Get("ids") != "123" || request.URL.Query().Get("ac") != "detail" {
						t.Fatal("wrong detail identity")
					}
					if details > 1 && request.Header.Get("Cache-Control") != "no-cache" {
						t.Fatal("playback did not request fresh metadata")
					}
					row := map[string]any{"vod_id": 123, "vod_name": "合成影片", "vod_pic": "https://images.example.test/poster.jpg", "vod_content": "<p>合成简介</p>", "vod_play_url": fmt.Sprintf("第一集$https://media.example.test/video.m3u8?version=%d", details)}
					body, _ := json.Marshal(map[string]any{"code": 1, "list": []any{row}})
					return sourceFixtureResponse(request, http.StatusOK, string(body)), nil
				}
				if request.URL.Host == "media.example.test" && request.URL.Path == "/video.m3u8" && request.URL.Query().Get("version") == "2" {
					playlists++
					return sourceFixtureResponse(request, http.StatusOK, "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6,\nsegment.ts\n#EXT-X-ENDLIST\n"), nil
				}
				return nil, errors.New("fixture rejects stale URLs, images and media segments")
			})
			drama, chapters, err := d.fetchMaccmsDetail(context.Background(), source, "123")
			if err != nil || drama.ID != source+":123" || len(chapters) != 1 || chapters[0].ID != source+":123:0-0" || string(chapters[0].CurrentEpisode) != "1" {
				t.Fatalf("wrong namespaced detail: %+v %+v %v", drama, chapters, err)
			}
			task := Task{DramaID: drama.ID, Chapter: chapters[0]}
			media, err := d.resolveProviderMedia(context.Background(), task)
			if err != nil || !strings.HasSuffix(media.URL, "?version=2") || media.Duration != 6*time.Second || details != 2 || playlists != 1 {
				t.Fatalf("stale playback address or incomplete playlist: %+v %v", media, err)
			}
			task.Chapter.Source = sourceHongguo
			if _, err := d.resolveMaccmsMedia(context.Background(), task); err == nil || details != 2 {
				t.Fatal("foreign chapter was accepted or reached the network")
			}
			task.Chapter = chapters[0]
			task.Chapter.ID = source + ":124:0-0"
			if _, err := d.resolveMaccmsMedia(context.Background(), task); err == nil || details != 2 {
				t.Fatal("a chapter from another drama was accepted")
			}
			task.Chapter = chapters[0]
			task.Chapter.Title = "原分集名称"
			if _, err := d.resolveMaccmsMedia(context.Background(), task); err == nil || details != 3 || playlists != 1 {
				t.Fatal("changed episode identity was accepted or fetched media")
			}
		})
	}
}

func TestMaccmsDetailRejectsUnrelatedRecordAndParserOnlyLines(t *testing.T) {
	for _, body := range []string{
		`{"code":1,"list":[{"vod_id":124,"vod_name":"其他影片","vod_play_url":"第一集$https://media.example.test/video.mp4"}]}`,
		`{"code":1,"list":[{"vod_id":123,"vod_name":"合成影片","vod_play_url":"解析$https://media.example.test/player.html?url=video.m3u8"}]}`,
	} {
		d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
			return sourceFixtureResponse(request, http.StatusOK, body), nil
		})
		if _, _, err := d.fetchMaccmsDetail(context.Background(), sourceLiangzi, "123"); err == nil {
			t.Fatal("unrelated or unsupported detail was accepted")
		}
	}
}

func TestMaccmsSourcesRespectEditionAndSeparateIdentity(t *testing.T) {
	for _, entry := range maccmsProviders {
		source := entry.id
		if nativeSourceAvailable(source) != (buildAllSources == "true") {
			t.Fatal("new source changed edition availability", source)
		}
		drama := nativeDrama{ID: source + ":123", Source: source}
		if nativeChapterAvailable(drama, Chapter{ID: sourceHongguo + ":123:1", Source: sourceHongguo}) {
			t.Fatal("new source accepted an old source chapter")
		}
	}
	if providerDramaID(sourceLiangzi, "123") == providerDramaID(sourceJciyuan, "123") || !nativeSourceAvailable(sourceHongguo) {
		t.Fatal("new sources collided or changed the default source")
	}
}
