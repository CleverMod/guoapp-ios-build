package core

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func yspPoolFixtureSession(t *testing.T, identity string) *yspDeviceSession {
	t.Helper()
	state, err := yspNewDeviceState()
	if err != nil {
		t.Fatal(err)
	}
	return &yspDeviceSession{device: state, created: time.Now(), lastBeat: time.Now(), key: strings.Repeat("K", 32),
		headers: map[string]string{"X-Uid": identity, "User-Agent": "cctv_app_tv"}, linked: map[string]bool{}}
}

func TestYSPPoolReservesUHDSlotAndBalancesHighRateChannels(t *testing.T) {
	pool := newYSPDevicePool(&http.Client{}, t.TempDir())
	pool.slots[1].session = yspPoolFixtureSession(t, "first")
	pool.slots[2].session = yspPoolFixtureSession(t, "second")
	for _, channel := range []string{"cctv4k", "cctv164k", "cctv8k"} {
		if pool.slotFor(channel) != 0 {
			t.Fatal("UHD stream consumed a regular device slot")
		}
	}
	if pool.slotFor("cctv1") != 1 {
		t.Fatal("first regular channel did not use the first slot")
	}
	pool.slots[1].session.linked["cctv1"] = true
	if pool.slotFor("cctv2") != 2 || pool.slotFor("cctv1") != 1 {
		t.Fatal("regular channels did not balance or lost their assigned slot")
	}
	if pool.slots[0].path == pool.slots[1].path || pool.slots[1].path == pool.slots[2].path || pool.standby.path != "" {
		t.Fatal("device identities share a state file or standby identity was persisted before promotion")
	}
}

func TestYSPPoolQuotaPromotesStandbyWithoutNewRegistration(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: yspFixtureTransport(func(*http.Request) (*http.Response, error) {
		requests++
		t.Fatal("ready standby promotion unexpectedly registered another device")
		return nil, context.Canceled
	})}
	pool := newYSPDevicePool(client, t.TempDir())
	first, second := yspPoolFixtureSession(t, "first"), yspPoolFixtureSession(t, "second")
	for index := 0; index < yspDeviceLinkLimit; index++ {
		first.linked[strings.Repeat("a", index+1)] = true
		second.linked[strings.Repeat("b", index+1)] = true
	}
	pool.slots[1].session, pool.slots[2].session = first, second
	standby := yspPoolFixtureSession(t, "standby")
	pool.standby.session = standby
	pool.assignments["old-channel"] = 1
	if err := pool.rotate(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if pool.slots[1].session != standby || pool.slots[2].session != second || pool.standby.session != nil || requests != 0 {
		t.Fatal("quota rotation failed to transfer the ready standby or modified another slot")
	}
	if _, retained := pool.assignments["old-channel"]; retained {
		t.Fatal("old device channel assignments survived rotation")
	}
	if err := pool.rotate(context.Background(), 1); err == nil {
		t.Fatal("a second immediate rotation ignored the eight-second guard")
	}
}

func TestYSPPoolHTTP400RetriesWithStandbyAndKeepsDeviceMode(t *testing.T) {
	pool := newYSPDevicePool(&http.Client{}, t.TempDir())
	old, standby := yspPoolFixtureSession(t, "old"), yspPoolFixtureSession(t, "standby")
	pool.slots[1].session = old
	pool.standby.session = standby
	secret, err := yspDeviceEncrypt("fixture-secret", standby.key)
	if err != nil {
		t.Fatal(err)
	}
	oldRequests, newRequests := 0, 0
	client := &http.Client{Transport: yspFixtureTransport(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("X-Uid") == "old" {
			oldRequests++
			return yspDeviceFixtureResponse(request, 400, "{}"), nil
		}
		newRequests++
		switch request.URL.Path {
		case "/gsnw/api/live/v1/01":
			return yspDeviceFixtureResponse(request, 200, `{"data":{"videoList":[{"rate":"36p","url":"https://media.test/source"}]}}`), nil
		case "/gsnw/api/live/v1/02":
			return yspDeviceFixtureResponse(request, 200, `{"data":"`+secret+`"}`), nil
		case "/cctvmobileinf/rest/cctv/videoliveUrl/getstream":
			return yspDeviceFixtureResponse(request, 200, `{"succeed":"1","url":"https://media.test/index.m3u8"}`), nil
		}
		t.Fatal("unexpected registration or fallback request", request.URL.Path)
		return nil, context.Canceled
	})}
	old.client, standby.client = client, client
	for _, device := range pool.slots {
		device.client = client
	}
	entry, err := pool.resolve(context.Background(), yspChannels[0])
	if err != nil || oldRequests != 1 || newRequests != 3 || entry.owner != pool.slots[1] || entry.address != "https://media.test/index.m3u8" {
		t.Fatal("HTTP 400 did not retry the same channel through the hot standby", oldRequests, newRequests, err)
	}
	if !standby.linked["cctv1"] || time.Until(entry.expires) > time.Minute || len(pool.slots[1].failures) != 0 {
		t.Fatal("recovered device link lost quota tracking, short expiry or cooldown reset")
	}
}

func TestYSPV90FailureCooldownIsBounded(t *testing.T) {
	device := newYSPDeviceResolver(&http.Client{}, t.TempDir())
	device.fast = true
	for _, delay := range []time.Duration{15 * time.Second, time.Minute, 2 * time.Minute, 2 * time.Minute} {
		device.recordFailureLocked("cctv1")
		actual := time.Until(device.failures["cctv1"].retryAt)
		if actual > delay || actual < delay-time.Second {
			t.Fatal("v9.0 device cooldown diverged from the source", actual, delay)
		}
	}
}
