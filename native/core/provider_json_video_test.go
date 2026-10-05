package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestJSONVideoCatalogUsesIndependentPaginationAndSearch(t *testing.T) {
	for _, source := range []string{sourceXiaopingguo, sourceXifu, sourceHongdou} {
		t.Run(source, func(t *testing.T) {
			d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
				if providerSourceForURL(request.URL.String()) != source {
					return nil, errors.New("fixture rejects images and media")
				}
				parameters := request.URL.Query()
				body := ""
				switch source {
				case sourceXiaopingguo:
					if request.URL.Path != "/api.php/v2.vod/androidsearch10086" || parameters.Get("page") != "2" || parameters.Get("wd") != "合成 & 名称" || parameters.Get("type") != "" {
						t.Fatal("wrong Xiaopingguo search")
					}
					body = `{"code":200,"data":[{"id":123,"name":"合成电影","pic":"https://images.example.test/one.jpg"}]}`
				case sourceXifu:
					if request.URL.Path != "/web/v1/drama/list" || parameters.Get("currentPage") != "2" || parameters.Get("pageSize") != "24" || parameters.Get("filterCategories[]") != "3" {
						t.Fatal("wrong Xifu category pagination")
					}
					body = `{"code":200,"data":{"data":[{"albumId":123,"title":"合成短剧","total":2}],"pagination":{"currentPage":2,"totalPages":3}}}`
				case sourceHongdou:
					if request.URL.Path != "/api/video/lists" || parameters.Get("offset") != "24" || parameters.Get("limit") != "24" || parameters.Get("key") != "合成 & 名称" || parameters.Get("type") != "" {
						t.Fatal("wrong Hongdou offset search")
					}
					body = `{"total":49,"rows":[{"id":123,"name":"合成短剧","sum":2}]}`
				}
				return sourceFixtureResponse(request, http.StatusOK, body), nil
			})
			query := "合成 & 名称"
			if source == sourceXifu {
				query = ""
			}
			items, more, err := d.fetchJSONVideoCatalogPage(context.Background(), source, 2, "3", query)
			if err != nil || len(items) != 1 || items[0].ID != source+":123" || more != (source != sourceXiaopingguo) {
				t.Fatalf("wrong catalog or pagination: items=%d more=%v error=%v", len(items), more, err)
			}
		})
	}
}

func TestJSONVideoDetailsRefreshStableEpisodeIdentity(t *testing.T) {
	for _, source := range []string{sourceXiaopingguo, sourceHongdou} {
		t.Run(source, func(t *testing.T) {
			calls := 0
			d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
				if providerSourceForURL(request.URL.String()) != source {
					return nil, errors.New("fixture rejects all images and media")
				}
				calls++
				if calls > 1 && request.Header.Get("Cache-Control") != "no-cache" {
					t.Fatal("playback retained stale metadata")
				}
				body := `{"code":200,"data":{"id":123,"name":"合成电影","urls":[{"key":"蓝光","url":"https://media.example.test/one.mp4"},{"key":"解析","url":"javascript:alert(1)"}]}}`
				if source == sourceHongdou {
					body = `{"id":123,"name":"合成短剧","sum":8,"video":[{"id":22,"name":"第二集","weigh":2,"src":"https://media.example.test/two.mp4"},{"id":11,"name":"第一集","weigh":1,"src":"https://media.example.test/one.mp4"}]}`
				}
				return sourceFixtureResponse(request, http.StatusOK, body), nil
			})
			drama, chapters, err := d.fetchJSONVideoDetail(context.Background(), source, "123", nativeDrama{})
			if err != nil || drama.ID != source+":123" || len(chapters) == 0 || chapters[0].VideoURL != "https://media.example.test/one.mp4" {
				t.Fatal("wrong detail or ordering", err)
			}
			if source == sourceHongdou && (len(chapters) != 2 || chapters[0].ID != source+":123:11" || drama.TotalEpisode != 2) {
				t.Fatal("fabricated episodes or unstable backend episode ID")
			}
			task := Task{DramaID: drama.ID, Chapter: chapters[0]}
			media, err := d.resolveJSONVideoMedia(context.Background(), task)
			if err != nil || calls != 2 || media.URL != chapters[0].VideoURL || media.credentials == nil {
				t.Fatal("failed to refresh playback with source headers", err)
			}
			task.Chapter.ID = source + ":124:11"
			if _, err := d.resolveJSONVideoMedia(context.Background(), task); err == nil || calls != 2 {
				t.Fatal("foreign chapter reached the network")
			}
		})
	}
}

func TestXifuTemporaryAuthorizationAndEditionGates(t *testing.T) {
	credentials := map[string]any{"AccessKeyId": "fixture-key", "AccessKeySecret": "fixture-secret", "SecurityToken": "fixture-token", "AuthInfo": "fixture-auth", "Region": "cn-shanghai"}
	encoded, _ := json.Marshal(credentials)
	auth, _ := json.Marshal(map[string]any{"code": 200, "data": map[string]any{"vid": "fixture-video", "playAuth": base64.StdEncoding.EncodeToString(encoded)}})
	calls := 0
	d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
		calls++
		parameters := request.URL.Query()
		if request.URL.Host == "minidrama-api.contentchina.com" && request.URL.Path == "/web/v1/drama/play_auth" && parameters.Get("albumId") == "123" && parameters.Get("seq") == "2" {
			return sourceFixtureResponse(request, http.StatusOK, string(auth)), nil
		}
		if request.URL.Host == "vod.cn-shanghai.aliyuncs.com" && parameters.Get("VideoId") == "fixture-video" {
			signature := parameters.Get("Signature")
			parameters.Del("Signature")
			if signature != xifuVODSignature(parameters, "fixture-secret") || parameters.Get("SecurityToken") != "fixture-token" {
				t.Fatal("temporary playback request signature mismatch")
			}
			return sourceFixtureResponse(request, http.StatusOK, `{"PlayInfoList":{"PlayInfo":[{"PlayURL":"https://media.example.test/one.mp4","Height":720}]}}`), nil
		}
		return nil, errors.New("fixture rejects all unexpected images and media requests")
	})
	drama, chapters, err := d.fetchXifuDetail(context.Background(), "123", nativeDrama{ID: "xifu:123", Title: "合成短剧", Episodes: 2})
	if err != nil || len(chapters) != 2 || calls != 0 {
		t.Fatal("known Xifu metadata triggered extra network requests", err)
	}
	media, err := d.resolveJSONVideoMedia(context.Background(), Task{DramaID: drama.ID, Chapter: chapters[1]})
	if err != nil || media.Quality != 720 || calls != 2 {
		t.Fatal("Xifu temporary authorization failed", err)
	}
	for _, source := range []string{sourceXiaopingguo, sourceXifu, sourceHongdou} {
		if nativeSourceAvailable(source) != (buildAllSources == "true") {
			t.Fatal("JSON source ignored edition gate", source)
		}
	}
}

func TestXifuVODSigningUsesRFC3986AndRejectsInvalidAuthorization(t *testing.T) {
	parameters := url.Values{
		"AccessKeyId": {"fixture-key"}, "Action": {"GetPlayInfo"}, "AuthInfo": {"space + /*~"},
		"Format": {"JSON"}, "SecurityToken": {"fixture-token"}, "SignatureMethod": {"HMAC-SHA1"},
		"SignatureNonce": {"fixture-nonce"}, "SignatureVersion": {"1.0"}, "Timestamp": {"2026-10-05T00:00:00Z"},
		"Version": {"2017-03-21"}, "VideoId": {"fixture-video"},
	}
	if xifuVODSignature(parameters, "fixture-secret") != "Kzj4UAMzxKx1l6Io7WSRDV9/H/k=" {
		t.Fatal("signature differs from the independent RFC3986/HMAC-SHA1 fixture")
	}
	credentials, _ := json.Marshal(map[string]any{"Region": "cn-shanghai.example.test/path", "AccessKeyId": "fixture-key", "AccessKeySecret": "fixture-secret", "SecurityToken": "fixture-token"})
	auth := map[string]any{"data": map[string]any{"vid": "fixture-video", "playAuth": base64.StdEncoding.EncodeToString(credentials)}}
	if address, err := xifuVODRequestURL(auth, time.Now(), "fixture-nonce"); err == nil || address != "" || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatal("invalid authorization exposed credentials or selected an unexpected host")
	}
}
