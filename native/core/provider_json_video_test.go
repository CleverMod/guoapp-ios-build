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

func TestXifuCatalogUsesIndependentPagination(t *testing.T) {
	d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
		parameters := request.URL.Query()
		if request.URL.Host != "minidrama-api.contentchina.com" || request.URL.Path != "/web/v1/drama/list" || parameters.Get("currentPage") != "2" || parameters.Get("pageSize") != "24" || parameters.Get("filterCategories[]") != "3" {
			t.Fatal("wrong Xifu category pagination")
		}
		return sourceFixtureResponse(request, http.StatusOK, `{"code":200,"data":{"data":[{"albumId":123,"title":"合成短剧","total":2}],"pagination":{"currentPage":2,"totalPages":3}}}`), nil
	})
	items, more, err := d.fetchJSONVideoCatalogPage(context.Background(), sourceXifu, 2, "3", "")
	if err != nil || len(items) != 1 || items[0].ID != "xifu:123" || !more {
		t.Fatalf("wrong Xifu catalog or pagination: items=%d more=%v error=%v", len(items), more, err)
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
	if nativeSourceAvailable(sourceXifu) != (buildAllSources == "true") {
		t.Fatal("Xifu source ignored edition gate")
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
