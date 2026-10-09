package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestYSPSegmentCacheBoundsExpiryAndAuthenticationIsolation(t *testing.T) {
	cache := newYSPSegmentCache(1)
	headers := http.Header{"Content-Type": {"video/mp2t"}}
	cache.put("first", bytes.Repeat([]byte{1}, 700<<10), headers)
	cache.put("second", bytes.Repeat([]byte{2}, 700<<10), headers)
	if _, exists := cache.get("first"); exists || cache.stats().Bytes > 1<<20 {
		t.Fatal("cache did not evict the oldest entry within its configured memory limit")
	}
	headers.Set("Content-Type", "changed")
	entry, exists := cache.get("second")
	if !exists || entry.headers.Get("Content-Type") != "video/mp2t" {
		t.Fatal("cached response headers changed with the original header map")
	}
	cache.mu.Lock()
	value := cache.entries["second"].Value.(yspCachedMedia)
	value.expires = time.Now().Add(-time.Second)
	cache.entries["second"].Value = value
	cache.mu.Unlock()
	if _, exists := cache.get("second"); exists || cache.stats().Bytes != 0 {
		t.Fatal("expired media remained usable or retained its byte allocation")
	}
	first := yspSegmentCacheKey(yspLiveResource{address: "https://media.test/1.ts", headers: map[string]string{"UID": "first", "APPSIGN": "one"}})
	second := yspSegmentCacheKey(yspLiveResource{address: "https://media.test/1.ts", headers: map[string]string{"UID": "second", "APPSIGN": "two"}})
	if first == second {
		t.Fatal("two device authorization contexts shared the same media cache key")
	}
	cache.resize(0)
	cache.put("disabled", []byte("media"), headers)
	if cache.stats().Entries != 0 {
		t.Fatal("disabled caching retained a completed media response")
	}
}

func TestYSPSegmentCacheServesRangesAndAvoidsDuplicateMediaRequests(t *testing.T) {
	requests := 0
	body := "0123456789abcdef"
	client := &http.Client{Transport: yspFixtureTransport(func(request *http.Request) (*http.Response, error) {
		requests++
		response := yspDeviceFixtureResponse(request, 200, body)
		response.Header.Set("Content-Type", "application/octet-stream")
		response.Header.Set("Content-Length", "16")
		return response, nil
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resource := yspLiveResource{address: "https://media.test/1.ts", headers: map[string]string{"UID": "fixture", "APPSIGN": "fixture"}}
	id := yspLiveResourceID(resource.address)
	session := &yspLiveSession{ctx: ctx, cancel: cancel, yspLiveState: yspLiveState{media: map[string]yspLiveResource{id: resource}}}
	live := &yspLiveServer{client: client, segmentCache: newYSPSegmentCache(1)}
	first := httptest.NewRecorder()
	live.serveMedia(first, httptest.NewRequest(http.MethodGet, "/", nil), session, id)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Range", "bytes=4-7")
	second := httptest.NewRecorder()
	live.serveMedia(second, request, session, id)
	if requests != 1 || first.Body.String() != body || second.Code != 206 || second.Body.String() != "4567" || second.Header().Get("Content-Range") != "bytes 4-7/16" || second.Header().Get("X-Cache") != "HIT-RAM" {
		t.Fatal("cached range request fetched upstream again or returned incorrect range data", requests, second.Code, second.Header())
	}
	head := httptest.NewRecorder()
	live.serveMedia(head, httptest.NewRequest(http.MethodHead, "/", nil), session, id)
	if head.Body.Len() != 0 || head.Header().Get("Content-Length") != "16" || requests != 1 {
		t.Fatal("cached HEAD request returned bytes or fetched upstream")
	}
}

func TestYSPSegmentCacheCoalescesInflightAndRejectsIncompleteBodies(t *testing.T) {
	cache := newYSPSegmentCache(1)
	first, leader := cache.begin("same")
	second, anotherLeader := cache.begin("same")
	if !leader || anotherLeader || first != second {
		t.Fatal("concurrent media requests did not share the same pending fetch")
	}
	var wait sync.WaitGroup
	wait.Add(1)
	go func() { defer wait.Done(); <-second }()
	cache.finish("same", first)
	wait.Wait()
	if cache.stats().Inflight != 0 {
		t.Fatal("completed request retained its pending fetch entry")
	}
	if yspCachedLengthValid(http.Header{"Content-Length": {"20"}}, []byte("short")) {
		t.Fatal("a truncated body qualified for the media cache")
	}
	writer := &yspCachingWriter{destination: httptest.NewRecorder(), limit: 2, complete: true}
	if _, err := writer.Write([]byte("too-large")); err != nil || writer.complete || writer.body != nil {
		t.Fatal("oversized media stopped streaming or retained the accumulation buffer")
	}
}

func TestYSPFullSettingsModesQuotaAndCorruptFilePreservation(t *testing.T) {
	directory := t.TempDir()
	settings := yspLoadLiveSettings(directory)
	if settings.DeviceMode != "all" || settings.LinksPerDevice != 6 || settings.CacheMB != 200 || settings.GatewayLAN || settings.GatewayPort != 8767 {
		t.Fatal("v9.0 application defaults diverged from the source", settings)
	}
	corrupt := []byte("broken setting")
	path := filepath.Join(directory, "live-settings.json")
	if err := os.WriteFile(path, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if recovered := yspLoadLiveSettings(directory); recovered.Warning == "" || recovered.DeviceMode != "all" {
		t.Fatal("corrupt settings were accepted without a recovery notice")
	}
	retained, _ := os.ReadFile(path)
	if !bytes.Equal(retained, corrupt) {
		t.Fatal("reading corrupt settings overwrote the user's original file")
	}
	live := &yspLiveServer{settings: nativeLiveSettings{DeviceMode: "4k"}}
	if live.deviceEnabled(yspChannels[0]) || !live.deviceEnabled(yspChannel{ID: "cctv8k"}) {
		t.Fatal("4K-only mode still used the device protocol for a regular channel")
	}
	live.settings.DeviceMode = "off"
	if live.deviceEnabled(yspChannel{ID: "cctv8k"}) {
		t.Fatal("disabled device mode still selected an 8K device route")
	}
	settings.LinksPerDevice = 0
	if settings.validate() != nil {
		t.Fatal("disabling proactive quota rotation was rejected")
	}
	settings.CacheMB = 513
	if settings.validate() == nil {
		t.Fatal("unbounded media cache allocation was accepted")
	}
}

func TestYSPFullDeviceTemplatesAndDecoderMetadata(t *testing.T) {
	var templates []yspDeviceTemplate
	if json.Unmarshal(yspDeviceProfileData, &templates) != nil || len(templates) != 53 {
		t.Fatal("original device template pool is incomplete")
	}
	for index := 0; index < 12; index++ {
		state, err := yspNewDeviceState()
		if err != nil || !strings.Contains(state.ProfileSource, "8k") || state.Profile.Resolution != "7680*4320" || state.Profile.Model == "" || state.UID != yspDeviceUID(state.Profile) {
			t.Fatal("random 8K template produced an inconsistent device identity", err, state.ProfileSource)
		}
	}
	info := yspStreamInfo{Route: "web"}
	yspReadManifestInfo("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=36000000,RESOLUTION=7680x4320\nmain.m3u8\n", &info)
	if info.Width != 7680 || info.Height != 4320 || info.Bandwidth != 36000000 {
		t.Fatal("master playlist lost its reported 8K resolution or bitrate", info)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := &yspLiveSession{ctx: ctx, yspLiveState: yspLiveState{mode: "device", info: yspStreamInfo{Height: 1080}}}
	live := &yspLiveServer{sessions: map[string]*yspLiveSession{"fixture": session}}
	if _, err := live.playbackInfo("fixture", yspStreamInfo{Width: 3840, Height: 2160}, true); err != nil {
		t.Fatal(err)
	}
	session.info = yspStreamInfo{Height: 1080}
	actual, err := live.playbackInfo("fixture", yspStreamInfo{}, false)
	if err != nil || actual.Height != 2160 || !actual.Decoder {
		t.Fatal("refreshing source metadata erased the decoder's actual resolution", actual, err)
	}
}

func TestYSPFullSubscriptionMetadataAliasesAndProtectedRoutes(t *testing.T) {
	live := &yspLiveServer{settings: defaultLiveSettings(), segmentCache: newYSPSegmentCache(1), created: time.Now(), gatewayToken: "fixture-capability"}
	path := "/tv/fixture-capability"
	all := httptest.NewRecorder()
	live.gatewayHandler(all, httptest.NewRequest(http.MethodGet, path+"/all.m3u", nil))
	if all.Code != 200 || strings.Count(all.Body.String(), "#EXTINF:") != 64 || strings.Count(all.Body.String(), "catchup-days=\"7\"") != 53 || !strings.Contains(all.Body.String(), yspGatewayEPGURLs[1]) || !strings.Contains(all.Body.String(), path+"/cctv1.m3u8") {
		t.Fatal("subscription lost channels, replay configuration, EPG URLs or protected paths")
	}
	filtered := httptest.NewRecorder()
	live.gatewayHandler(filtered, httptest.NewRequest(http.MethodGet, path+"/all.m3u?k4=1", nil))
	if strings.Count(filtered.Body.String(), "#EXTINF:") != 3 {
		t.Fatal("4K subscription filter includes regular channels")
	}
	unauthorized := httptest.NewRecorder()
	live.gatewayHandler(unauthorized, httptest.NewRequest(http.MethodGet, "/tv/wrong/all.m3u", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatal("subscription service accepted an expired or unrelated capability")
	}
	if yspCanonicalChannel("CCTV16-4K.m3u8") != "cctv164k" || yspCanonicalChannel("cctv5plus") != "cctv5p" {
		t.Fatal("original channel aliases were not migrated")
	}
	blocked := httptest.NewRecorder()
	live.gatewayHandler(blocked, httptest.NewRequest(http.MethodGet, path+"/engine_proxy?url=http://127.0.0.1/private", nil))
	if blocked.Code != http.StatusForbidden {
		t.Fatal("proxy forwarded an address absent from the live resource registry")
	}
}

func TestYSPGatewayShutdownOnlyReleasesSubscriptionSessions(t *testing.T) {
	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()
	gatewayCtx, gatewayCancel := context.WithCancel(context.Background())
	defer gatewayCancel()
	live := &yspLiveServer{sessions: map[string]*yspLiveSession{"app": {ctx: appCtx, cancel: appCancel}, "gateway": {ctx: gatewayCtx, cancel: gatewayCancel, gateway: true}}, gatewayToken: "fixture-capability"}
	live.stopGateway()
	if appCtx.Err() != nil || gatewayCtx.Err() == nil || len(live.sessions) != 1 || live.gatewayToken != "" {
		t.Fatal("closing subscription access stopped the application player or retained gateway sessions")
	}
}

func TestYSPDiagnosticsDoNotExportAuthorizationMaterial(t *testing.T) {
	client := &http.Client{Transport: yspFixtureTransport(func(*http.Request) (*http.Response, error) { return nil, io.EOF })}
	pool := newYSPDevicePool(client, t.TempDir())
	session := yspPoolFixtureSession(t, "sensitive-identity")
	session.device.UID = "sensitive-identity"
	session.device.CloudGUID = "sensitive-cloud-guid"
	session.headers["Token"] = "sensitive-token"
	pool.slots[0].session = session
	live := &yspLiveServer{device: pool, settings: defaultLiveSettings(), segmentCache: newYSPSegmentCache(1), sessions: map[string]*yspLiveSession{}}
	encoded, err := json.Marshal(live.diagnostics())
	if err != nil || bytes.Contains(encoded, []byte("sensitive-identity")) || bytes.Contains(encoded, []byte("sensitive-cloud-guid")) || bytes.Contains(encoded, []byte("sensitive-token")) || len(live.diagnostics().Channels) != 64 {
		t.Fatal("diagnostics exposed authorization data or omitted inactive channel state", err)
	}
}
