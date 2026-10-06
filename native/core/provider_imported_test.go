package core

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestImportedSourcesRestoreNewProtocolsAndRetireOldEndpoints(t *testing.T) {
	for _, source := range []string{"xiaopingguo", "luoxue", "xiaobao", "jumi", "nnvideo"} {
		p, ok := attachedProviderByID(source)
		if !ok || p.kind != "imported" || !attachedSearchSource(source) || !validNativeCategory(source, p.categories[0].id) {
			t.Fatal("missing imported protocol", source)
		}
	}
	for _, source := range []string{"wuwu", "uku", "zy1080", "yingtan", "qiwei"} {
		if isHuangguoProviderSource(source) {
			t.Fatal("retired source still registered", source)
		}
	}
}

func TestImportedJSONDoesNotMistakeCiphertextPrefixesForNumbers(t *testing.T) {
	for _, input := range []string{"56encrypted-response", "123abc", "200", "\"ciphertext\""} {
		if _, err := importedDecode(input); err == nil {
			t.Fatal("accepted primitive or partial encrypted JSON", input)
		}
	}
	if _, err := importedDecode(`{"status":1,"data":[]}`); err != nil {
		t.Fatal("rejected valid empty catalog", err)
	}
}

func TestImportedWebKeepsPlaylistIdentityAcrossLineReordering(t *testing.T) {
	reversed := false
	d := sourceFixtureDownloader(t, func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, ".png") {
			t.Fatal("requested source image")
		}
		switch r.URL.Path {
		case "/vod/detail/321.html":
			tabs := `<a href="#playlist1">主线</a><a href="#playlist2">备用</a>`
			if reversed {
				tabs = `<a href="#playlist2">备用</a><a href="#playlist1">主线</a>`
			}
			body := `<h1>合成剧</h1>` + tabs + `<div id="playlist1"><a href="/vod/play/321-1-1.html">第1集</a><a href="/vod/play/321-1-2.html">第2集</a></div><div id="playlist2"><a href="/vod/play/321-2-1.html">第1集</a></div>`
			return sourceFixtureResponse(r, 200, attachedJSON(body)), nil
		case "/vod/play/321-1-2.html":
			return sourceFixtureResponse(r, 200, `<script>var player_aaaa={"encrypt":2,"url":"`+base64.StdEncoding.EncodeToString([]byte("https://media.example.test/second.mp4?token=a%2Bb"))+`"};</script>`), nil
		default:
			t.Fatal("unexpected source request", r.URL.Path)
			return nil, nil
		}
	})
	_, chapters, err := d.fetchAttachedDetail(context.Background(), "xiaobao", "321")
	if err != nil || len(chapters) != 3 {
		t.Fatal("playlist parsing failed", err)
	}
	selected := chapters[1]
	reversed = true
	media, err := d.resolveAttachedMedia(context.Background(), Task{DramaID: "xiaobao:321", Chapter: selected})
	if err != nil || media.URL != "https://media.example.test/second.mp4?token=a%2Bb" {
		t.Fatal("selected episode changed", media.URL, err)
	}
	if _, err := d.resolveAttachedMedia(context.Background(), Task{DramaID: "xiaobao:321", Chapter: Chapter{ID: "xiaobao:999:1", Source: "xiaobao"}}); err == nil {
		t.Fatal("accepted unrelated chapter")
	}
}

func TestImportedNNDecryptsAPIAndPropagatesIdentity(t *testing.T) {
	d := sourceFixtureDownloader(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/list" || r.URL.Query().Get("wd") != "合成" || len(r.Header.Get("d")) != 16 || r.Header.Get("pkg") != "com.qingbian.jz" {
			t.Fatal("NN protocol mismatch")
		}
		key := []byte(r.URL.RequestURI())[:16]
		cipher, err := attachedECB([]byte(`{"code":200,"data":[{"vod_id":"73","vod_name":"合成片","vod_pic":"https://image.example.test/test.png"}]}`), key)
		if err != nil {
			t.Fatal(err)
		}
		return sourceFixtureResponse(r, 200, base64.StdEncoding.EncodeToString(cipher)), nil
	})
	rows, _, err := d.fetchAttachedCatalogPage(context.Background(), "nnvideo", 1, "", "合成")
	if err != nil || len(rows) != 1 || rows[0].ID != "nnvideo:73" {
		t.Fatal("NN encrypted catalog failed", err)
	}
}

func TestImportedLuoxueLocallyPaginatesAndFiltersActualTypes(t *testing.T) {
	calls := 0
	d := sourceFixtureDownloader(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/proxy.php" || r.URL.Query().Get("action") != "search" {
			t.Fatal("unexpected request")
		}
		calls++
		rows := []map[string]any{}
		for i := 1; i <= 100; i++ {
			typ := "国产剧"
			if i%2 == 0 {
				typ = "动作片"
			}
			rows = append(rows, map[string]any{"source": "mj", "id": i, "name": "合成影视", "type": typ, "pic": "https://image.example.test/cover.png"})
		}
		return sourceFixtureResponse(r, 200, attachedJSON(map[string]any{"success": true, "results": rows})), nil
	})
	first, more, err := d.fetchAttachedCatalogPage(context.Background(), "luoxue", 1, "movie", "")
	second, _, err2 := d.fetchAttachedCatalogPage(context.Background(), "luoxue", 2, "movie", "")
	if err != nil || err2 != nil || !more || len(first) != 20 || len(second) != 20 || calls != 1 || first[0].SourceID != "mj@2" || second[0].SourceID != "mj@42" {
		t.Fatal("Luoxue filter or local pagination mismatch", calls, err, err2)
	}
}

func TestImportedXPGSignsAndEncryptsProtobufRequests(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	public, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	settings := map[string]string{"PUB1": base64.StdEncoding.EncodeToString(public), "NATIVE": "0123456789abcdef", "DATAIV": "12345678901234567890123456789012", "DATAKEY": "12345678901234567890123456789012"}
	d := sourceFixtureDownloader(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/proto/v5/drama/search" || r.Header.Get("Content-Type") != "application/x-protobuf" {
			t.Fatal("XPG endpoint mismatch")
		}
		var envelope map[string]string
		if json.Unmarshal([]byte(r.Header.Get("publicParams")), &envelope) != nil {
			t.Fatal("XPG public parameters missing")
		}
		wire, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		fields, err := xpgFields(wire)
		if err != nil {
			t.Fatal(err)
		}
		m := xpgFlat(wire)
		encoded := m[1] + m[2]
		cipher, err := importedUnbase64(encoded[8:])
		if err != nil {
			t.Fatal(err)
		}
		plain, err := importedECBDecrypt(cipher, []byte(settings["DATAKEY"]))
		if err != nil || !strings.Contains(string(plain), "searchKeys=合成") || !strings.HasSuffix(string(plain), m[4]) || len(fields) != 5 {
			t.Fatal("XPG body did not preserve original query contract")
		}
		card := append(xpgBytes(3, []byte("51")), xpgBytes(5, []byte("合成片"))...)
		reply := xpgBytes(3, xpgBytes(1, card))
		return sourceFixtureResponse(r, 200, string(reply)), nil
	})
	d.attachedAccess = map[string]attachedAccess{"xiaopingguo": {Settings: settings}}
	rows, _, err := d.fetchAttachedCatalogPage(context.Background(), "xiaopingguo", 1, "", "合成")
	if err != nil || len(rows) != 1 || rows[0].ID != "xiaopingguo:51" {
		t.Fatal("XPG catalog failed", err)
	}
	for _, bad := range [][]byte{{0}, {10, 255}, {8, 128}, {15, 1}} {
		if _, err := xpgFields(bad); err == nil {
			t.Fatal("accepted malformed Protobuf")
		}
	}
}

func TestImportedJumiRC4AndSignedSessionQuery(t *testing.T) {
	pg, yry := "fixture-pg", "fixture-yry"
	d := sourceFixtureDownloader(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api.php/fixture/Category" || r.Header.Get("Authorization") != "fixture-auth" {
			t.Fatal("Jumi authenticated request mismatch")
		}
		form := r.URL.Query()
		ts := form.Get("tt")
		plain, err := jumiRC4(form.Get("key"), ts, true)
		if err != nil || plain != ts {
			t.Fatal("Jumi timestamp signature mismatch")
		}
		cipher, err := jumiRC4(`[{"type_status":1,"type_en":"movies","type_name":"电影"}]`, pg, false)
		if err != nil {
			t.Fatal(err)
		}
		return sourceFixtureResponse(r, 200, cipher), nil
	})
	c, err := d.attachedClient("jumi")
	if err != nil {
		t.Fatal(err)
	}
	c.imported.session = map[string]string{"pgURL": "https://fixture.example.test", "basePath": "fixture", "pgKey": pg, "yryKey": yry, "authorization": "fixture-auth"}
	data, err := c.jumiRequest(context.Background(), "Category", url.Values{})
	if err != nil || len(attachedRows(data)) != 1 {
		t.Fatal("Jumi RC4 response failed", err)
	}
	value, err := jumiRC4("中文字节", yry, false)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := jumiRC4(value, yry, true)
	if err != nil || plain != "中文字节" {
		t.Fatal("Jumi Unicode round trip failed")
	}
}

func TestImportedEpisodeKeysRejectRemoteRenameAndPreserveLineOrder(t *testing.T) {
	c := &attachedClient{p: attachedProvider{id: "nnvideo"}}
	lines := []importedLine{{key: "first", episodes: []importedEpisode{{name: "第1集", url: "opaque"}}}, {key: "second", episodes: []importedEpisode{{name: "第1集", url: "opaque2"}}}}
	first, _ := c.importedChapters("73", lines)
	reversed, _ := c.importedChapters("73", []importedLine{lines[1], lines[0]})
	if !reflect.DeepEqual(first[0], reversed[1]) || first[0].ID == first[1].ID {
		t.Fatal("line identity depends on response order")
	}
	lines[0].episodes[0].name = "第2集"
	changed, _ := c.importedChapters("73", lines)
	if first[0].ID == changed[0].ID {
		t.Fatal("renamed episode retains stale identity")
	}
}
