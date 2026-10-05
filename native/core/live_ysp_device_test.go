package core

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func yspDeviceFixtureState(t *testing.T) yspDeviceState {
	t.Helper()
	state, err := yspNewDeviceState()
	if err != nil {
		t.Fatal(err)
	}
	state.Profile.AndroidID = "0123456789abcdef"
	state.Profile.MAC = "02:00:00:00:00:01"
	state.UID = yspDeviceUID(state.Profile)
	return state
}

func TestYSPDeviceIdentityMatchesV6Fixture(t *testing.T) {
	state := yspDeviceFixtureState(t)
	if state.UID != "AB140D91F9F25A07730204DD393DFAAC4E4BDC6F" {
		t.Fatal("device UID differs from the v6 Java hash and UUID fixture")
	}
	if yspDeviceFingerprint(state.UID, 1791158400123) != "cf7c131ef40b3b1e86e5018dcf0bf08d2b2219c7a1d4049d41be107f46a00fe7" {
		t.Fatal("device fingerprint differs from the v6 UTC+8 fixture")
	}
}

func TestYSPBackupFlowIDMatchesV6Encoding(t *testing.T) {
	query, err := yspKey(yspChannel{SID: "2024078201"})
	if err != nil {
		t.Fatal(err)
	}
	flow := query.Get("flowid")
	if !strings.HasSuffix(flow, "_4330403") {
		t.Fatal("backup flow ID lost its platform suffix")
	}
	identity := strings.TrimSuffix(flow, "_4330403")
	if raw, err := hex.DecodeString(identity); err != nil || len(raw) != 16 || strings.ToUpper(identity) != identity {
		t.Fatal("backup flow ID does not use the v6 uppercase 32-digit hexadecimal format")
	}
	if identity[12] != '4' || !strings.ContainsRune("89AB", rune(identity[16])) {
		t.Fatal("backup flow ID lost the v6 UUID version and variant bits")
	}
}

func TestYSPDeviceGCMMatchesAES256VectorAndRejectsTampering(t *testing.T) {
	vector := "AAAAAAAAAAAAAAAAUw+K+8dFNrmpY7TxxMtziw=="
	if plain, err := yspDeviceDecrypt(vector, ""); err != nil || plain != "" {
		t.Fatal("AES-256-GCM empty-plaintext vector failed", err)
	}
	raw, _ := base64.StdEncoding.DecodeString(vector)
	raw[len(raw)-1] ^= 1
	if _, err := yspDeviceDecrypt(base64.StdEncoding.EncodeToString(raw), ""); !yspDeviceInvalidates(err) {
		t.Fatal("tampered ciphertext did not invalidate the session")
	}
	key := strings.Repeat("a", 64)
	encrypted, err := yspDeviceEncrypt("央视频 https://media.test/live.m3u8", key)
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := yspDeviceDecrypt(encrypted, key[:32]); err != nil || plain != "央视频 https://media.test/live.m3u8" {
		t.Fatal("session keys were not truncated to 32 UTF-8 bytes", err)
	}
}

func TestYSPDeviceIdentityPersistsAndInvalidFileIsPreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ysp-device-state.json")
	first, err := yspLoadDeviceState(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := yspLoadDeviceState(path)
	if err != nil || first.Profile != second.Profile || first.UID != second.UID {
		t.Fatal("reopening the device state changed its identity", err)
	}
	corrupt := []byte("{unfinished-device-state")
	if err := os.WriteFile(path, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := yspLoadDeviceState(path); err == nil {
		t.Fatal("invalid device state was accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, corrupt) {
		t.Fatal("invalid device state was overwritten", err)
	}
}

func yspDeviceFixtureResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

func TestYSPDeviceEncryptedResolutionAndVDNSignature(t *testing.T) {
	state := yspDeviceFixtureState(t)
	key := strings.Repeat("k", 40)
	liveURL := "http://liveali.media.test/4k/index.m3u8"
	encryptedURL, err := yspDeviceEncrypt(liveURL, key)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := yspDeviceEncrypt("fixture-app-secret", key)
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	client := &http.Client{Transport: yspFixtureTransport(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.Header.Get("UID") != state.Profile.AndroidID || request.Header.Get("X-Nonce") == "" || request.Header.Get("X-Timestamp") == "" {
			t.Error("control request lost its device identity or fresh nonce")
		}
		switch request.URL.Path {
		case "/gsnw/api/live/v1/01":
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body["id"] != yspDeviceLiveIDs["cctv4k"] || body["screenParam"] != state.ScreenParam {
				t.Error("device-channel request does not match v6", err)
			}
			return yspDeviceFixtureResponse(request, 200, fmt.Sprintf(`{"data":{"videoList":[{"rate":"18p","url":"http://media.test/low.m3u8"},{"rate":"36p","url":%q}]}}`, encryptedURL)), nil
		case "/gsnw/api/live/v1/02":
			var body map[string]string
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if plain, err := yspDeviceDecrypt(body["guid"], key); err != nil || plain != "" {
				t.Error("live/v1/02 did not encrypt the empty GUID", err)
			}
			return yspDeviceFixtureResponse(request, 200, fmt.Sprintf(`{"data":{"appSecret":%q}}`, secret)), nil
		case "/cctvmobileinf/rest/cctv/videoliveUrl/getstream":
			if err := request.ParseForm(); err != nil || request.Form.Get("url") != liveURL {
				t.Error("VDN did not receive the selected, decrypted URL", err)
			}
			random := request.Header.Get("APPRANDOMSTR")
			digest := md5.Sum([]byte(yspDeviceAppID + "fixture-app-secret" + random))
			if request.Header.Get("APPID") != yspDeviceAppID || request.Header.Get("APPSIGN") != hex.EncodeToString(digest[:]) {
				t.Error("VDN signature does not match its app secret and nonce")
			}
			return yspDeviceFixtureResponse(request, 200, `{"succeed":1,"url":"http://liveali.media.test/4k/final.m3u8"}`), nil
		default:
			t.Fatal("unexpected control-plane endpoint")
			return nil, nil
		}
	})}
	device := newYSPDeviceResolver(client, t.TempDir())
	session := &yspDeviceSession{client: client, headers: yspDeviceHeaders(state, time.Now().UnixMilli()), key: key, device: state}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entry, err := device.resolveChannel(ctx, session, yspDeviceLiveIDs["cctv4k"])
	if err != nil || requests != 3 || entry.address != "http://liveali.media.test/4k/final.m3u8" || entry.headers["APPSIGN"] == "" || entry.headers["UID"] != state.Profile.AndroidID {
		t.Fatal("encrypted device resolution did not retain signed playback headers", err)
	}
}

func TestYSPDeviceRenewalAndCancellationReleaseControl(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: yspFixtureTransport(func(request *http.Request) (*http.Response, error) {
		requests++
		return yspDeviceFixtureResponse(request, http.StatusServiceUnavailable, "{}"), nil
	})}
	device := newYSPDeviceResolver(client, t.TempDir())
	old := &yspDeviceSession{created: time.Now().Add(-yspDeviceSessionTTL + 4*time.Minute), lastBeat: time.Now()}
	device.session = old
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	device.pulse(ctx)
	if ctx.Err() != nil || requests != 1 || device.session != old || len(device.control) != 0 {
		t.Fatal("renewal reacquired its own control lock or discarded a still-valid session")
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if err := device.lockControl(canceled); err == nil || len(device.control) != 0 {
		t.Fatal("cancellation leaked the device control slot")
	}
}

func TestYSPSignedPlaylistAndMediaRelayKeepHeadersRangeAndHEAD(t *testing.T) {
	headers := map[string]string{"UID": "fixture-device", "APPID": yspDeviceAppID, "APPSIGN": "fixture-sign", "APPRANDOMSTR": "fixture-random", "Accept-Encoding": "identity"}
	mediaRequests := 0
	client := &http.Client{Transport: yspFixtureTransport(func(request *http.Request) (*http.Response, error) {
		for key, value := range headers {
			if request.Header.Get(key) != value {
				t.Error("signed playlist or media request lost", key)
			}
		}
		switch request.URL.Path {
		case "/master.m3u8":
			return yspDeviceFixtureResponse(request, 200, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=35000000\nnested/index.m3u8\n"), nil
		case "/nested/index.m3u8":
			return yspDeviceFixtureResponse(request, 200, "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\"\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:2,\n1.ts\n"), nil
		case "/nested/1.ts":
			mediaRequests++
			if request.Method == "HEAD" {
				response := yspDeviceFixtureResponse(request, 200, "")
				response.Header.Set("Content-Length", "4")
				return response, nil
			}
			if request.Header.Get("Range") != "bytes=0-3" || request.Header.Get("If-Range") != "fixture-etag" {
				t.Error("media byte-range request was not forwarded")
			}
			response := yspDeviceFixtureResponse(request, 206, "data")
			response.Header.Set("Content-Type", "video/mp2t")
			response.Header.Set("Content-Length", "4")
			response.Header.Set("Content-Range", "bytes 0-3/4")
			return response, nil
		default:
			t.Fatal("unexpected media URL")
			return nil, nil
		}
	})}
	device := newYSPDeviceResolver(client, t.TempDir())
	device.entries["cctv4k"] = yspDeviceEntry{address: "http://media.test/master.m3u8", headers: headers, expires: time.Now().Add(time.Minute)}
	live := &yspLiveServer{client: client, mediaClient: client, device: device}
	state := yspLiveState{mode: "bk"}
	if err := live.refreshDevice(context.Background(), yspChannel{ID: "cctv4k"}, &state); err != nil {
		t.Fatal(err)
	}
	life, stop := context.WithCancel(context.Background())
	defer stop()
	live.sessions = map[string]*yspLiveSession{"fixture": {ctx: life, cancel: stop, yspLiveState: state}}
	playlist := httptest.NewRecorder()
	live.serve(playlist, httptest.NewRequest("GET", "/live/fixture/index.m3u8", nil))
	for _, address := range []string{"http://media.test/nested/1.ts", "http://media.test/nested/key.bin", "http://media.test/nested/init.mp4"} {
		if !strings.Contains(playlist.Body.String(), "/live/fixture/media/"+yspLiveResourceID(address)) {
			t.Fatal("signed HLS resource was not moved to the authenticated local relay")
		}
	}
	path := "/live/fixture/media/" + yspLiveResourceID("http://media.test/nested/1.ts")
	request := httptest.NewRequest("GET", path, nil)
	request.Header.Set("Range", "bytes=0-3")
	request.Header.Set("If-Range", "fixture-etag")
	response := httptest.NewRecorder()
	live.serve(response, request)
	if response.Code != 206 || response.Body.String() != "data" || response.Header().Get("Content-Range") != "bytes 0-3/4" {
		t.Fatal("the relay changed the partial-content response")
	}
	head := httptest.NewRecorder()
	live.serve(head, httptest.NewRequest("HEAD", path, nil))
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") != "4" || mediaRequests != 2 {
		t.Fatal("the relay did not preserve HEAD metadata")
	}
	unknown := httptest.NewRecorder()
	live.serve(unknown, httptest.NewRequest("GET", "/live/fixture/media/unknown", nil))
	if unknown.Code != 404 || mediaRequests != 2 {
		t.Fatal("the relay fetched an unregistered resource")
	}
	live.release("fixture")
	released := httptest.NewRecorder()
	live.serve(released, httptest.NewRequest("GET", path, nil))
	if released.Code != 410 || mediaRequests != 2 {
		t.Fatal("a released session continued serving signed resources")
	}
}

func TestYSPLiveResourceRenewalRetainsPreviousSignatureBriefly(t *testing.T) {
	oldURL, freshURL := "http://media.test/1.ts?token=old", "http://media.test/1.ts?token=fresh"
	oldID, freshID := yspLiveResourceID(oldURL), yspLiveResourceID(freshURL)
	state := yspLiveState{playlist: "#EXTM3U\n#EXTINF:2,\n" + freshURL + "\n", media: map[string]yspLiveResource{
		oldID:     {address: oldURL, headers: map[string]string{"APPSIGN": "old-sign"}, expires: time.Now().Add(time.Minute)},
		freshID:   {address: freshURL, headers: map[string]string{"APPSIGN": "fresh-sign"}},
		"expired": {address: "http://media.test/expired.ts", expires: time.Now().Add(-time.Minute)},
	}}
	yspPruneLiveResources(&state)
	if len(state.media) != 2 || state.media[oldID].headers["APPSIGN"] != "old-sign" || state.media[freshID].headers["APPSIGN"] != "fresh-sign" {
		t.Fatal("URL renewal discarded or overwrote the signature for an outstanding segment")
	}
}

func TestYSPDevicePlaylistFailureFallsBackToExistingRoute(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: yspFixtureTransport(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.Path == "/device.m3u8" {
			return yspDeviceFixtureResponse(request, 403, ""), nil
		}
		if request.URL.Path != "/fallback.m3u8" {
			t.Fatal("unexpected fallback request")
		}
		return yspDeviceFixtureResponse(request, 200, "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:2,\n1.ts\n"), nil
	})}
	device := newYSPDeviceResolver(client, t.TempDir())
	device.entries["cctv11"] = yspDeviceEntry{address: "http://media.test/device.m3u8", headers: map[string]string{"APPSIGN": "fixture"}, expires: time.Now().Add(time.Minute)}
	live := &yspLiveServer{client: client, mediaClient: client, device: device}
	state := yspLiveState{mode: "bk", urls: []string{"http://media.test/fallback.m3u8"}, urlTime: time.Now()}
	if err := live.refreshState(context.Background(), yspChannel{ID: "cctv11", Backup: true}, &state); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || state.mode != "bk" || state.playlist == "" || len(device.entries) != 0 || !device.failures["cctv11"].retryAt.After(time.Now()) {
		t.Fatal("an unavailable device route interrupted the existing fallback")
	}
}

func TestYSPDeviceCooldownEscalatesAndIsolatesChannels(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: yspFixtureTransport(func(request *http.Request) (*http.Response, error) {
		requests++
		return yspDeviceFixtureResponse(request, 503, ""), nil
	})}
	device := newYSPDeviceResolver(client, t.TempDir())
	device.session = &yspDeviceSession{client: client, created: time.Now()}
	channel := yspChannel{ID: "cctv4k"}
	for index, delay := range []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute, 5 * time.Minute} {
		previous := device.failures[channel.ID]
		previous.retryAt = time.Now().Add(-time.Second)
		device.failures[channel.ID] = previous
		device.lastRequest = time.Time{}
		started := time.Now()
		if _, err := device.resolve(context.Background(), channel); err == nil || requests != index+1 {
			t.Fatal("failed device resolution did not perform exactly one control request", err)
		}
		failure := device.failures[channel.ID]
		if failure.count != min(index+1, 5) || failure.retryAt.Before(started.Add(delay)) || failure.retryAt.After(time.Now().Add(delay)) {
			t.Fatal("device cooldown did not grow to the expected capped duration", index, failure)
		}
		if _, err := device.resolve(context.Background(), channel); err == nil || requests != index+1 || device.failures[channel.ID] != failure {
			t.Fatal("cooldown performed another request or counted a second failure", err)
		}
	}
	device.lastRequest = time.Time{}
	if _, err := device.resolve(context.Background(), yspChannel{ID: "cctv5"}); err == nil || requests != 8 || device.failures["cctv5"].count != 1 || device.failures[channel.ID].count != 5 {
		t.Fatal("one channel's cooldown affected another channel", err)
	}
}

func TestYSPDeviceCooldownSurvivesNewURLsUntilPlaylistRecovery(t *testing.T) {
	key := strings.Repeat("k", 32)
	secret, err := yspDeviceEncrypt("fixture-secret", key)
	if err != nil {
		t.Fatal(err)
	}
	controlRequests, playlistRequests := 0, 0
	ready := false
	client := &http.Client{Transport: yspFixtureTransport(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/gsnw/api/live/v1/01":
			controlRequests++
			return yspDeviceFixtureResponse(request, 200, `{"data":{"videoList":[{"rate":"36p","url":"http://media.test/live.m3u8"}]}}`), nil
		case "/gsnw/api/live/v1/02":
			controlRequests++
			return yspDeviceFixtureResponse(request, 200, fmt.Sprintf(`{"data":{"appSecret":%q}}`, secret)), nil
		case "/cctvmobileinf/rest/cctv/videoliveUrl/getstream":
			controlRequests++
			return yspDeviceFixtureResponse(request, 200, `{"succeed":1,"url":"http://media.test/device.m3u8"}`), nil
		case "/device.m3u8":
			playlistRequests++
			if !ready {
				return yspDeviceFixtureResponse(request, 503, ""), nil
			}
		case "/fallback.m3u8":
		default:
			t.Fatal("unexpected cooldown fixture request")
		}
		return yspDeviceFixtureResponse(request, 200, "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:2,\n1.ts\n"), nil
	})}
	device := newYSPDeviceResolver(client, t.TempDir())
	device.session = &yspDeviceSession{client: client, key: key, created: time.Now()}
	live := &yspLiveServer{client: client, mediaClient: client, device: device}
	channel := yspChannel{ID: "cctv4k", Backup: true}
	state := yspLiveState{mode: "bk", urls: []string{"http://media.test/fallback.m3u8"}, urlTime: time.Now()}
	for attempt := 1; attempt <= 2; attempt++ {
		failure := device.failures[channel.ID]
		failure.retryAt = time.Now().Add(-time.Second)
		device.failures[channel.ID] = failure
		device.lastRequest = time.Time{}
		if err := live.refreshState(context.Background(), channel, &state); err != nil || state.mode != "bk" || device.failures[channel.ID].count != attempt || controlRequests != 3*attempt || playlistRequests != attempt {
			t.Fatal("a new signed URL reset failures before the playlist recovered", err)
		}
		failure = device.failures[channel.ID]
		if err := live.refreshState(context.Background(), channel, &state); err != nil || state.mode != "bk" || device.failures[channel.ID] != failure || controlRequests != 3*attempt || playlistRequests != attempt {
			t.Fatal("cooldown interrupted fallback playback or retried the device route", err)
		}
	}
	failure := device.failures[channel.ID]
	failure.retryAt = time.Now().Add(-time.Second)
	device.failures[channel.ID] = failure
	device.lastRequest = time.Time{}
	ready = true
	if err := live.refreshState(context.Background(), channel, &state); err != nil || state.mode != "device" || len(device.failures) != 0 || controlRequests != 9 || playlistRequests != 3 {
		t.Fatal("a recovered device playlist did not clear its cooldown", err)
	}
	ready = false
	if err := live.refreshState(context.Background(), channel, &state); err == nil || device.failures[channel.ID].count != 1 || controlRequests != 9 || playlistRequests != 4 {
		t.Fatal("the first failure after recovery did not restart at the initial cooldown", err)
	}
}

func TestYSPDeviceCooldownIgnoresCanceledAndOutdatedRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests := 0
	client := &http.Client{Transport: yspFixtureTransport(func(request *http.Request) (*http.Response, error) {
		requests++
		cancel()
		return nil, context.Canceled
	})}
	device := newYSPDeviceResolver(client, t.TempDir())
	device.session = &yspDeviceSession{client: client, created: time.Now()}
	channel := yspChannel{ID: "cctv4k"}
	if _, err := device.resolve(ctx, channel); !errors.Is(err, context.Canceled) || requests != 1 || len(device.failures) != 0 || len(device.control) != 0 {
		t.Fatal("canceling a control request counted a failure or leaked the control slot", err)
	}
	device.control <- struct{}{}
	if _, err := device.resolve(context.Background(), channel); err == nil || requests != 1 || len(device.failures) != 0 {
		t.Fatal("a busy control slot counted a channel failure", err)
	}
	<-device.control
	entry := yspDeviceEntry{address: "http://media.test/device.m3u8", headers: map[string]string{"APPSIGN": "new-sign"}, expires: time.Now().Add(time.Minute)}
	device.entries[channel.ID] = entry
	failure := yspDeviceFailure{count: 3, retryAt: time.Now().Add(2 * time.Minute)}
	device.failures[channel.ID] = failure
	playlistContext, stop := context.WithCancel(context.Background())
	defer stop()
	client.Transport = yspFixtureTransport(func(request *http.Request) (*http.Response, error) {
		stop()
		return nil, context.Canceled
	})
	live := &yspLiveServer{client: client, mediaClient: client, device: device}
	if err := live.refreshDevice(playlistContext, channel, &yspLiveState{}); err == nil || device.failures[channel.ID] != failure || len(device.entries) != 1 {
		t.Fatal("canceling a playlist request rejected the route or changed its cooldown", err)
	}
	old := entry
	old.headers = map[string]string{"APPSIGN": "old-sign"}
	device.accept(channel, old)
	device.reject(channel, old)
	if device.failures[channel.ID] != failure || len(device.entries) != 1 {
		t.Fatal("an outdated response changed the current route's failure state")
	}
	device.reject(channel, entry)
	failure = device.failures[channel.ID]
	device.reject(channel, entry)
	device.accept(channel, entry)
	if failure.count != 4 || device.failures[channel.ID] != failure || len(device.entries) != 0 {
		t.Fatal("duplicate or already-rejected responses changed the cooldown")
	}
}
