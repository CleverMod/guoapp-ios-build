package core

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func yspWebFixtureAccess() attachedAccess {
	return attachedAccess{Settings: map[string]string{
		"appID": "123456789", "videoAppID": "fixture-video-app", "videoSecret": "fixture-video-secret",
		"authSalt": "fixture-auth-salt", "liveSalt": "fixture-live-salt", "cKeyMarker": "fixture-marker", "version": "V1.0.0",
		"cKeyKey": "000102030405060708090a0b0c0d0e0f", "cKeyIV": "101112131415161718191a1b1c1d1e1f", "cookie": "pc_version=fixture",
	}}
}

func TestYSPV81ChannelIdentifiersAndGroups(t *testing.T) {
	channels := yspLiveChannels()
	counts := map[string]int{}
	updated := map[string]string{"cctvfyjc": "2025637102", "cctvdyjc": "2026874202", "cctvhjjc": "2026874302"}
	catchup := 0
	for _, channel := range channels {
		counts[channel.Group]++
		if channel.CatchupDays == 7 {
			catchup++
		}
		if channel.EPGID == "" || channel.EPGURL != yspEPGURL {
			t.Fatal("channel lost its EPG identity", channel.ID)
		}
		if sid, ok := updated[channel.ID]; ok && channel.SID != sid {
			t.Fatal("theatre channel still uses the retired SID", channel.ID)
		}
	}
	if len(channels) != 64 || len(counts) != 2 || counts["央视"] != 30 || counts["卫视"] != 34 || catchup != 53 {
		t.Fatal("v8.1 channel groups or replay configuration drifted", counts, catchup)
	}
}

func TestYSPWebSigningModulesExecuteWithoutExternalMemory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	signer, err := newYSPWebSigner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer signer.runtime.Close(context.Background())
	values := map[string]string{
		"cctvh5openapi.state.guid": "fixture-guid", "cctvh5openapi.state.yspappid": "123456789", "cctvh5openapi.state.version": "v1",
		"window.location.host": "www.yangshipin.cn", "window.location.protocol": "https:", "cctvh5openapi.state.ts": "1791244800000",
		"cctvh5openapi.state.token": "fixture-token", "cctvh5openapi.state.input": "fixture-input",
	}
	for _, function := range []string{"get_token_rnd", "get_signature"} {
		value, err := signer.key(ctx, values, function)
		if err != nil || value == "" {
			t.Fatal("upstream Web signing module failed", function, err)
		}
	}
	ticket, err := signer.playerTicket(ctx, "600001859", "1791244800", "2024078201", "fixture-guid", "123456789", "V1.0.0")
	decoded, decodeErr := hex.DecodeString(ticket)
	if err != nil || decodeErr != nil || len(decoded) != 54 {
		t.Fatal("ticket module failed with module-local memory and table", err, decodeErr, len(decoded))
	}
}

func TestYSPWebFallbackPreservesCredentialsAndProxiesHLSResources(t *testing.T) {
	access := yspWebFixtureAccess()
	requests := map[string]int{}
	client := &http.Client{Transport: yspFixtureTransport(func(request *http.Request) (*http.Response, error) {
		requests[request.URL.Host+request.URL.Path]++
		switch request.URL.Host {
		case "bkliveinfo.ysp.cctv.cn":
			return nil, errors.New("fixture unavailable backup")
		case "h5access.yangshipin.cn":
			query := request.URL.Query()
			if query.Get("vsecret") != access.Settings["videoSecret"] || query.Get("rnd") == "" || query.Get("guid") == "" {
				t.Error("Web token request lost its signing inputs")
			}
			return yspDeviceFixtureResponse(request, 200, `{"data":{"token":"fixture-open-token","ts":1791244800000}}`), nil
		case "player-api.yangshipin.cn":
			if request.Header.Get("yspappid") != access.Settings["appID"] || !strings.Contains(request.Header.Get("Cookie"), "pc_version=fixture") {
				t.Error("Web request lost its app identity or cookie")
			}
			if request.URL.Path == "/v1/player/auth" {
				if err := request.ParseForm(); err != nil || request.Form.Get("pid") != "600099658" || request.Form.Get("signature") == "" {
					t.Error("auth request lost its channel or signature")
				}
				return yspDeviceFixtureResponse(request, 200, `{"data":{"token":"fixture-player-token","ts":1791244800}}`), nil
			}
			var body map[string]any
			decoder := json.NewDecoder(request.Body)
			decoder.UseNumber()
			if decoder.Decode(&body) != nil || body["cnlid"] != "2025637102" || body["livepid"] != "600099658" || body["encryptVer"] != "8.1" || body["stream"] != "2" {
				t.Error("Web live request changed the channel or protocol")
			}
			unsigned := map[string]any{}
			for key, value := range body {
				if key != "rand_str" && key != "signature" {
					unsigned[key] = value
				}
			}
			hash := yspWebMD5(yspWebPairs(unsigned, true))
			if request.Header.Get("yspsdkinput") != hash || request.Header.Get("yspPlayerToken") != "fixture-player-token" || request.Header.Get("yspticket") == "" || !strings.Contains(request.Header.Get("yspsdksign"), "-"+hash+"-") {
				t.Error("Web request lost its SDK signature, player token or ticket")
			}
			return yspDeviceFixtureResponse(request, 200, `{"data":{"playurl":"https://web-media.test/index.m3u8","extended_param":"?fixture=1"}}`), nil
		case "web-media.test":
			if request.Header.Get("User-Agent") != yspWebUA || request.Header.Get("Referer") != "https://www.yangshipin.cn/" {
				t.Error("Web media lost its playback headers")
			}
			if request.URL.Path == "/index.m3u8" {
				return yspDeviceFixtureResponse(request, 200, "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\"\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:6,\n1.ts\n"), nil
			}
			return yspDeviceFixtureResponse(request, 200, "fixture-media"), nil
		}
		t.Fatal("unexpected external request", request.URL.Host, request.URL.Path)
		return nil, errors.New("unexpected request")
	})}
	live := &yspLiveServer{client: client, web: newYSPWebResolver(client, access)}
	channel := yspChannel{ID: "cctvfyjc", SID: "2025637102", PID: "600099658", Definition: "shd", Backup: true}
	state := yspLiveState{mode: "bk"}
	if err := live.refreshState(context.Background(), channel, &state); err != nil || state.mode != "web" || len(state.media) != 3 {
		t.Fatal("failed backups did not reach Web or lost HLS resources", state.mode, len(state.media), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	live.sessions = map[string]*yspLiveSession{"fixture": {channel: channel, ctx: ctx, cancel: cancel, yspLiveState: state}}
	response := httptest.NewRecorder()
	live.serve(response, httptest.NewRequest(http.MethodGet, "/live/fixture/index.m3u8", nil))
	if response.Code != 200 || strings.Contains(response.Body.String(), "https://web-media.test/") || !strings.Contains(response.Body.String(), `URI="/live/fixture/media/`) {
		t.Fatal("Web media or encrypted resources bypassed the local gateway")
	}
	keyID := yspLiveResourceID("https://web-media.test/key.bin")
	response = httptest.NewRecorder()
	live.serve(response, httptest.NewRequest(http.MethodGet, "/live/fixture/media/"+keyID, nil))
	if response.Code != 200 || response.Body.String() != "fixture-media" {
		t.Fatal("Web key could not be fetched through the session gateway")
	}
	if requests["player-api.yangshipin.cn/v1/player/auth"] != 1 || requests["player-api.yangshipin.cn/v1/player/get_live_info"] != 1 {
		t.Fatal("Web fallback duplicated authorization requests")
	}
	if err := live.refreshState(context.Background(), channel, &state); err != nil || requests["player-api.yangshipin.cn/v1/player/auth"] != 1 {
		t.Fatal("active Web route did not reuse its short-lived playback address", err)
	}
	if live.web.signer != nil {
		live.web.signer.runtime.Close(context.Background())
	}
}

func TestYSPLiveCacheIsolatesSessionsAndExcludesReplay(t *testing.T) {
	channel := yspChannels[0]
	state := yspLiveState{mode: "web", refreshed: time.Now(), segments: []yspSegment{{tags: []string{"original"}}},
		playlist: "#EXTM3U\n", media: map[string]yspLiveResource{"fixture": {address: "https://media.test/segment.ts", headers: map[string]string{"Referer": "original"}}}}
	live := &yspLiveServer{}
	live.remember(channel, state)
	state.segments[0].tags[0] = "changed"
	state.media["fixture"].headers["Referer"] = "changed"
	cached := live.cache[channel.ID]
	if cached.segments[0].tags[0] != "original" || cached.media["fixture"].headers["Referer"] != "original" {
		t.Fatal("a session changed the shared cached resource snapshot")
	}
	copy := yspCopyLiveState(cached)
	copy.media["fixture"].headers["Referer"] = "another-session"
	if cached.media["fixture"].headers["Referer"] != "original" {
		t.Fatal("cached media headers leaked between sessions")
	}
	replay := yspLiveState{catchup: &yspCatchupRange{start: 1, end: 2}, playlist: "replay", refreshed: time.Now()}
	live.remember(channel, replay)
	if live.cache[channel.ID].playlist != "#EXTM3U\n" {
		t.Fatal("a replay replaced the live SWR cache")
	}
	old := cached
	old.refreshed = time.Now().Add(-yspLiveCacheTTL)
	live.cache[channel.ID] = old
	live.remember(yspChannels[1], state)
	if _, exists := live.cache[channel.ID]; exists {
		t.Fatal("expired channel snapshots remained reusable")
	}
	live.forget(yspChannels[1])
	if len(live.cache) != 0 {
		t.Fatal("a failed media request retained its cached channel")
	}
}

func TestYSPWebUnavailableConfigurationAndCancellationAreExplicit(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: yspFixtureTransport(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, io.EOF
	})}
	web := newYSPWebResolver(client, attachedAccess{})
	if _, err := web.resolve(context.Background(), yspChannels[0]); err == nil || requests != 0 {
		t.Fatal("missing private signing configuration issued requests or succeeded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&yspLiveServer{}).openWithOptions(ctx, yspChannels[0].ID, "", true); !errors.Is(err, context.Canceled) {
		t.Fatal("cached open ignored cancellation", err)
	}
}
