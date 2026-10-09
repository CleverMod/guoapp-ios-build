package core

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func yspCatchupFixtureFields(t *testing.T, request *http.Request) map[int]any {
	t.Helper()
	packet, err := io.ReadAll(request.Body)
	if err != nil || len(packet) < 90 {
		t.Fatal("missing JCE request packet", err)
	}
	compressed, err := gzip.NewReader(bytes.NewReader(packet[89 : len(packet)-1]))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(compressed)
	compressed.Close()
	if err != nil || len(payload) < 17 {
		t.Fatal("invalid inner JCE request", err)
	}
	reader := &yspReader{data: payload[16 : len(payload)-1]}
	envelope, err := reader.object(0)
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := envelope[1].([]byte)
	if !ok {
		t.Fatal("JCE request lacks timeshift fields")
	}
	reader = &yspReader{data: raw}
	fields, err := reader.object(0)
	if err != nil {
		t.Fatal(err)
	}
	return fields
}

func yspCatchupFixtureResponse(request *http.Request, address string) *http.Response {
	fields := &yspWriter{}
	fields.number(0, 0)
	fields.text(address, 2)
	return &http.Response{StatusCode: 200, Header: http.Header{}, Request: request,
		Body: io.NopCloser(bytes.NewReader(yspPacket(fields.Bytes(), strings.Repeat("0", 32), 1)))}
}

func TestYSPCatchupQueryFormatsAndLimits(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	start, end := now.Add(-4*time.Hour).Unix(), now.Add(-2*time.Hour).Unix()
	queries := []string{
		"playseek=20261005080000-20261005100000",
		"starttime=202610050800&endtime=202610051000",
		fmt.Sprintf("start=%d&end=%d", start, end),
		fmt.Sprintf("utc=%d&lutc=%d", start*1000, end*1000),
		fmt.Sprintf("utc=%d", start),
	}
	for _, query := range queries {
		window, err := yspParseCatchup(query, now)
		if err != nil || window == nil || window.start != start || window.end != end {
			t.Fatalf("query %q did not preserve the UTC+8 replay window: %+v, %v", query, window, err)
		}
	}
	window, err := yspParseCatchup(fmt.Sprintf("utc=%d", now.Add(-time.Hour).Unix()), now)
	if err != nil || window.end != now.Unix() {
		t.Fatal("the default replay end extends into the future", err)
	}
	invalid := []string{"playseek=", "end=20261005100000", "start=invalid", "start=20260230080000",
		"start=20260927080000", "utc=%zz", fmt.Sprintf("utc=%d", now.Unix()),
		fmt.Sprintf("utc=%d&lutc=%d", start, start), fmt.Sprintf("utc=%d&lutc=%d", start, now.Unix()+1)}
	for _, query := range invalid {
		if window, err := yspParseCatchup(query, now); err == nil || window != nil {
			t.Fatalf("invalid replay query %q silently opened live playback", query)
		}
	}
	if window, err := yspParseCatchup("token=fixture", now); err != nil || window != nil {
		t.Fatal("ordinary live queries were interpreted as replay", err)
	}
}

func TestYSPCatchupFinitePlaylistPreservesHistoryAndEncryption(t *testing.T) {
	window := &yspCatchupRange{start: 1791158400, end: 1791165600}
	var history strings.Builder
	history.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:900\n#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\"\n#EXT-X-MAP:URI=\"init.mp4\"\n")
	for index := 0; index < 80; index++ {
		fmt.Fprintf(&history, "#EXTINF:6,\n#EXT-X-BYTERANGE:1024@%d\nshared.ts\n", index*1024)
	}
	requests := 0
	live := &yspLiveServer{guid: strings.Repeat("0", 32)}
	live.client = &http.Client{Transport: yspFixtureTransport(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.Host == "jacc.ysp.cctv.cn" {
			fields := yspCatchupFixtureFields(t, request)
			if fields[0] != yspChannels[0].PID || fields[1] != yspChannels[0].SID || fields[2] != window.start || fields[3] != window.end {
				t.Error("JCE replay request lost its channel identity or exact time window")
			}
			return yspCatchupFixtureResponse(request, "https://media.test/replay/index.m3u8"), nil
		}
		if request.Header.Get("User-Agent") != yspJCEUA || request.URL.Path != "/replay/index.m3u8" {
			t.Error("replay playlist did not use its JCE address and user agent")
		}
		return yspDeviceFixtureResponse(request, 200, history.String()), nil
	})}
	state := yspLiveState{catchup: window}
	if err := live.refreshState(context.Background(), yspChannels[0], &state); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(yspParseSegments(state.playlist)) != 80 {
		t.Fatal("replay was truncated to the moving live window")
	}
	for _, preserved := range []string{"#EXT-X-MEDIA-SEQUENCE:900", `URI="https://media.test/replay/key.bin"`,
		`URI="https://media.test/replay/init.mp4"`, "#EXT-X-BYTERANGE:1024@80896", "#EXT-X-ENDLIST"} {
		if !strings.Contains(state.playlist, preserved) {
			t.Fatal("replay lost sequence, encryption, byte-range or finite-playlist metadata", preserved)
		}
	}
	life, cancel := context.WithCancel(context.Background())
	defer cancel()
	state.refreshed = time.Now().Add(-time.Hour)
	live.sessions = map[string]*yspLiveSession{"replay": {channel: yspChannels[0], ctx: life, cancel: cancel, yspLiveState: state}}
	response := httptest.NewRecorder()
	live.serve(response, httptest.NewRequest(http.MethodGet, "/live/replay/index.m3u8", nil))
	expected := yspProxyLivePlaylist(state.playlist, "/live/replay/media/", state.media)
	if response.Code != 200 || response.Body.String() != expected || requests != 2 {
		t.Fatal("a finite replay expired under the live refresh deadline")
	}
	live.release("replay")
	response = httptest.NewRecorder()
	live.serve(response, httptest.NewRequest(http.MethodGet, "/live/replay/index.m3u8", nil))
	if response.Code != http.StatusGone {
		t.Fatal("released replay session remained available")
	}
}

func TestYSPCatchupFailureCannotSwitchToLive(t *testing.T) {
	requests := 0
	live := &yspLiveServer{guid: strings.Repeat("0", 32), client: &http.Client{Transport: yspFixtureTransport(func(request *http.Request) (*http.Response, error) {
		requests++
		return yspCatchupFixtureResponse(request, "https://liverecord.video.cloud.cctv.com/failed.m3u8"), nil
	})}}
	state := yspLiveState{mode: "bk", catchup: &yspCatchupRange{start: 1791158400, end: 1791165600}}
	if err := live.refreshState(context.Background(), yspChannels[0], &state); err == nil || requests != 1 || state.playlist != "" {
		t.Fatal("unavailable replay silently fell back to the current live programme")
	}
	query := fmt.Sprintf("utc=%d", time.Now().Add(-time.Hour).Unix())
	if _, err := live.openWithOptions(context.Background(), "cctv4k", query, true); err == nil || requests != 1 || live.server != nil {
		t.Fatal("an unsupported channel created a replay session")
	}
}

func TestYSPWarmupIsIndependentAndRespectsCooldown(t *testing.T) {
	requests := []string{}
	client := &http.Client{Transport: yspFixtureTransport(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, body["id"].(string))
		return yspDeviceFixtureResponse(request, 503, "{}"), nil
	})}
	device := newYSPDeviceResolver(client, t.TempDir())
	session := &yspDeviceSession{client: client, headers: map[string]string{}, created: time.Now()}
	device.session = session
	device.failures["cctv8k"] = yspDeviceFailure{count: 2, retryAt: time.Now().Add(time.Minute)}
	device.keepWarm(context.Background(), session)
	if len(requests) != 2 || requests[0] != yspDeviceLiveIDs["cctv4k"] || requests[1] != yspDeviceLiveIDs["cctv164k"] {
		t.Fatal("one warmup failure blocked another channel or ignored channel cooldown")
	}
	if device.session != session || device.failures["cctv4k"].count != 1 || device.failures["cctv164k"].count != 1 || device.failures["cctv8k"].count != 2 {
		t.Fatal("warmup failures reset a ready session or another channel's cooldown")
	}
	device.keepWarm(context.Background(), session)
	if len(requests) != 2 {
		t.Fatal("background warmup exceeded its thirty-second interval")
	}
	device.reopen("cctv4k")
	if device.failures["cctv4k"].count != 0 || device.failures["cctv8k"].count != 2 {
		t.Fatal("manual reopen reset another channel's cooldown")
	}
}

func TestYSPWarmupRefreshesBeforeExpiryAndStopsInvalidSession(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: yspFixtureTransport(func(request *http.Request) (*http.Response, error) {
		requests++
		return yspDeviceFixtureResponse(request, 401, "{}"), nil
	})}
	device := newYSPDeviceResolver(client, t.TempDir())
	session := &yspDeviceSession{client: client, headers: map[string]string{}, created: time.Now(), lastBeat: time.Now()}
	device.session = session
	for _, channel := range []string{"cctv4k", "cctv164k", "cctv8k"} {
		device.entries[channel] = yspDeviceEntry{address: "https://media.test/valid.m3u8", expires: time.Now().Add(4 * time.Minute),
			headers: map[string]string{"APPSIGN": "valid", "UID": "valid"}, lastUsed: time.Now().Add(-time.Hour)}
	}
	device.pulse(context.Background())
	if requests != 0 || len(device.entries) != 3 {
		t.Fatal("valid signed warm entries were refreshed too early or evicted while idle")
	}
	entry := device.entries["cctv4k"]
	entry.expires = time.Now().Add(2 * time.Minute)
	device.entries["cctv4k"] = entry
	device.lastWarm = time.Now().Add(-time.Minute)
	device.keepWarm(context.Background(), session)
	if requests != 1 || device.session != nil || device.sessionRetry.IsZero() {
		t.Fatal("expiring warm entry was not renewed or an invalid session continued issuing requests")
	}
}
