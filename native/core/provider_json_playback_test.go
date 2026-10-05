package core

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func xiaopingguoNoticeFixture() string {
	files := []string{
		"568a321778bf", "3fc6b7fee9af", "53cb0f8fff47", "076308a6f159", "f88580ed50f5",
		"d2618b879997", "7e6187534a03", "270629f965b8", "912aaf614656", "cbc047c2c01d",
		"aab8122493dd", "64ee3db0803b", "b419396ad0ed", "852bc262c684", "3d16faecbce5",
		"48d5d2dfea3e", "e6fa7d3bc9c0", "1dacecf77cf2", "2f9163ff6e16",
	}
	var body strings.Builder
	body.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:1\n")
	for _, file := range files {
		body.WriteString("#EXTINF:1,\n/file159/" + file + "_0.ts?fixture=true\n")
	}
	body.WriteString("#EXT-X-ENDLIST\n")
	return body.String()
}

func TestXiaopingguoNoticeIsRejectedBeforeMediaRequests(t *testing.T) {
	for _, nested := range []bool{false, true} {
		calls := 0
		d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
			calls++
			if request.URL.Host != "media.example.test" || request.URL.Path != "/notice.m3u8" {
				return nil, errors.New("fixture rejects images and media bytes")
			}
			return sourceFixtureResponse(request, http.StatusOK, xiaopingguoNoticeFixture()), nil
		})
		media := providerMedia{URL: "https://media.example.test/notice.m3u8", Playlist: xiaopingguoNoticeFixture()}
		if nested {
			media.URL = "https://media.example.test/master.m3u8"
			media.Playlist = "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000,RESOLUTION=640x480\nnotice.m3u8\n"
		}
		if resolved, err := d.checkXiaopingguoMedia(context.Background(), media); err == nil || !strings.Contains(err.Error(), "提示片") || resolved.URL != "" || calls > 1 {
			t.Fatal("notice reached playback or issued a media request", err)
		}
		if nested != (calls == 1) {
			t.Fatal("wrong number of metadata-only requests")
		}
	}
}

func TestXiaopingguoNoticeSignatureDoesNotRejectOtherContent(t *testing.T) {
	fixture := xiaopingguoNoticeFixture()
	if xiaopingguoPlaylistSignature(fixture, "https://different.example.test/folder/list.m3u8?token=fixture") != xiaopingguoNoticeSignature {
		t.Fatal("CDN host or query change hid the same notice")
	}
	for _, body := range []string{
		strings.Replace(fixture, "568a321778bf", "other-segment", 1),
		strings.Replace(fixture, "#EXT-X-ENDLIST", "", 1),
		"#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000\nnotice.m3u8\n",
	} {
		d := sourceFixtureDownloader(t, func(*http.Request) (*http.Response, error) {
			return nil, errors.New("fixture rejects all network requests")
		})
		if xiaopingguoPlaylistSignature(body, "https://media.example.test/one.m3u8") == xiaopingguoNoticeSignature {
			t.Fatal("other content was classified as the known notice")
		}
		if strings.Contains(body, "#EXT-X-STREAM-INF") {
			continue
		}
		media := providerMedia{URL: "https://media.example.test/one.m3u8", Playlist: body}
		if result, err := d.checkXiaopingguoMedia(context.Background(), media); err != nil || result.URL != media.URL {
			t.Fatal("other content was rejected", err)
		}
	}
}

func TestHongdouHLSUsesCurrentGuestPlaybackAndStableEpisodeIdentity(t *testing.T) {
	for _, scenario := range []string{"playable", "empty", "foreign", "paid"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
				calls++
				if request.URL.Host == "api.dramaplay.shop" {
					parameters := request.URL.Query()
					if request.Header.Get("Cache-Control") != "no-cache" {
						t.Fatal("playback reused stale metadata")
					}
					switch request.URL.Path {
					case "/api/video/info":
						if parameters.Get("id") != "123" {
							t.Fatal("wrong drama identity")
						}
						return sourceFixtureResponse(request, http.StatusOK, `{"id":123,"name":"合成短剧","video":[{"id":11,"name":"第一集","weigh":1,"src":"https://old.example.test/deleted.mp4","has_hls":1}]}`), nil
					case "/api/video/videoinfo":
						if parameters.Get("vid") != "123" || parameters.Get("mid") != "11" || parameters.Get("page") != "1" || parameters.Get("uid") != "0" || parameters.Get("token") != "" {
							t.Fatal("wrong chapter or fabricated authorization")
						}
						body := `{"isempty":1,"data":[{"vid":124,"mid":11,"src":"https://foreign.example.test/wrong.m3u8"},{"vid":123,"mid":11,"pays":0,"src":"https://media.example.test/fresh.m3u8"}]}`
						if scenario == "empty" {
							body = `{"isempty":2,"data":[]}`
						} else if scenario == "foreign" {
							body = `{"isempty":1,"data":[{"vid":124,"mid":11,"src":"https://foreign.example.test/wrong.m3u8"}]}`
						} else if scenario == "paid" {
							body = `{"isempty":1,"data":[{"vid":123,"mid":11,"pays":1,"src":"https://media.example.test/fresh.m3u8"}]}`
						}
						return sourceFixtureResponse(request, http.StatusOK, body), nil
					}
				}
				if scenario == "playable" && request.URL.Host == "media.example.test" && request.URL.Path == "/fresh.m3u8" {
					return sourceFixtureResponse(request, http.StatusOK, "#EXTM3U\n#EXTINF:1,\nsegment.ts\n#EXT-X-ENDLIST\n"), nil
				}
				return nil, errors.New("fixture rejects stale MP4, foreign content, images and media bytes")
			})
			task := Task{DramaID: "hongdou:123", Chapter: Chapter{ID: "hongdou:123:11", Source: sourceHongdou, Title: "第一集", VideoURL: "https://old.example.test/deleted.mp4"}}
			media, err := d.resolveJSONVideoMedia(context.Background(), task)
			if scenario == "playable" {
				if err != nil || calls != 3 || media.URL != "https://media.example.test/fresh.m3u8" {
					t.Fatal("failed to refresh the migrated episode", err)
				}
			} else if err == nil || calls != 2 || media.URL != "" {
				t.Fatal("unavailable, foreign or paid media reached playback", err)
			}
		})
	}
}
