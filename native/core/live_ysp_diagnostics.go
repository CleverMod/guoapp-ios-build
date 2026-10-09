package core

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

type yspStreamInfo struct {
	Route     string `json:"route"`
	Rate      string `json:"rate,omitempty"`
	RateName  string `json:"rateName,omitempty"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	Bandwidth int64  `json:"bandwidth,omitempty"`
	Decoder   bool   `json:"decoder"`
}

type yspLiveEvent struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
	Channel string    `json:"channel,omitempty"`
}

type yspDeviceStatus struct {
	Slot              int    `json:"slot"`
	Enabled           bool   `json:"enabled"`
	Ready             bool   `json:"ready"`
	Standby           bool   `json:"standby"`
	Model             string `json:"model,omitempty"`
	Brand             string `json:"brand,omitempty"`
	Identity          string `json:"identity,omitempty"`
	Linked            int    `json:"linked"`
	Limit             int    `json:"limit"`
	HeartbeatCount    int    `json:"heartbeatCount"`
	HeartbeatAge      int64  `json:"heartbeatAge"`
	HeartbeatError    string `json:"heartbeatError,omitempty"`
	SessionRemaining  int64  `json:"sessionRemaining"`
	RegistrationRetry int64  `json:"registrationRetry"`
}

type yspChannelStatus struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Active     bool          `json:"active"`
	Info       yspStreamInfo `json:"info"`
	RefreshAge int64         `json:"refreshAge"`
	Cooldown   int64         `json:"cooldown"`
	Error      string        `json:"error,omitempty"`
	Catchup    bool          `json:"catchup"`
	Sequence   int64         `json:"sequence"`
	Segments   int           `json:"segments"`
}

type yspLiveDiagnostics struct {
	Version       string               `json:"version"`
	Uptime        int64                `json:"uptime"`
	Settings      nativeLiveSettings   `json:"settings"`
	Cache         yspSegmentCacheStats `json:"cache"`
	Devices       []yspDeviceStatus    `json:"devices"`
	Channels      []yspChannelStatus   `json:"channels"`
	Events        []yspLiveEvent       `json:"events"`
	Gateway       []string             `json:"gateway"`
	GatewayActive bool                 `json:"gatewayActive"`
	Refilling     bool                 `json:"refilling"`
	Active        int                  `json:"active"`
	Maximum       int                  `json:"maximum"`
}

func yspTimeRemaining(deadline time.Time) int64 {
	if deadline.IsZero() {
		return 0
	}
	return max(0, int64(time.Until(deadline).Seconds()))
}

func yspTimeAge(timestamp time.Time) int64 {
	if timestamp.IsZero() {
		return -1
	}
	return max(0, int64(time.Since(timestamp).Seconds()))
}

func (live *yspLiveServer) event(kind, message, channel string) {
	key := kind + ":" + channel
	live.eventsMu.Lock()
	defer live.eventsMu.Unlock()
	if live.eventTimes == nil {
		live.eventTimes = map[string]time.Time{}
	}
	if time.Since(live.eventTimes[key]) < 5*time.Second {
		return
	}
	live.eventTimes[key] = time.Now()
	live.events = append(live.events, yspLiveEvent{Time: time.Now(), Kind: kind, Message: message, Channel: channel})
	if len(live.events) > 30 {
		live.events = append([]yspLiveEvent(nil), live.events[len(live.events)-30:]...)
	}
}

func yspDeviceSnapshot(device *yspDeviceResolver, index, limit int) yspDeviceStatus {
	device.mu.Lock()
	defer device.mu.Unlock()
	status := yspDeviceStatus{Slot: index, Enabled: true, Standby: index < 0, Limit: limit, RegistrationRetry: yspTimeRemaining(device.sessionRetry), HeartbeatAge: -1}
	if session := device.session; session != nil {
		status.Ready = yspSessionFresh(session, time.Now())
		status.Model, status.Brand = session.device.Profile.Model, session.device.Profile.Brand
		digest := sha256.Sum256([]byte(session.device.UID))
		status.Identity = hex.EncodeToString(digest[:4])
		status.Linked = len(session.linked)
		status.HeartbeatCount, status.HeartbeatAge, status.HeartbeatError = session.heartbeatCount, yspTimeAge(session.lastBeat), session.heartbeatError
		status.SessionRemaining = yspTimeRemaining(session.created.Add(yspDeviceSessionTTL))
	}
	return status
}

func (live *yspLiveServer) diagnostics() yspLiveDiagnostics {
	live.mu.Lock()
	result := yspLiveDiagnostics{Version: "9.0.0", Uptime: yspTimeAge(live.created), Settings: live.settings, Maximum: 15,
		Channels: []yspChannelStatus{}, Devices: []yspDeviceStatus{}, Gateway: append([]string(nil), live.gatewayURLs...),
		GatewayActive: live.gatewayServer != nil, Active: len(live.sessions)}
	sessions := make([]*yspLiveSession, 0, len(live.sessions))
	for _, session := range live.sessions {
		sessions = append(sessions, session)
	}
	live.mu.Unlock()
	if live.segmentCache != nil {
		result.Cache = live.segmentCache.stats()
	}
	cooldowns := map[string]int64{}
	if pool, ok := live.device.(*yspDevicePool); ok {
		pool.mu.Lock()
		limit := pool.linkLimit
		result.Refilling = pool.refilling
		pool.mu.Unlock()
		for index, device := range append(pool.slots[:], pool.standby) {
			if index == 3 {
				index = -1
			}
			status := yspDeviceSnapshot(device, index, limit)
			status.Enabled = result.Settings.DeviceMode != "off" && (result.Settings.DeviceMode != "4k" || index <= 0)
			status.Ready = status.Ready && status.Enabled
			result.Devices = append(result.Devices, status)
			device.mu.Lock()
			for channel, failure := range device.failures {
				cooldowns[channel] = max(cooldowns[channel], yspTimeRemaining(failure.retryAt))
			}
			device.mu.Unlock()
		}
	}
	for _, session := range sessions {
		session.mu.Lock()
		state := yspChannelStatus{ID: session.channel.ID, Name: session.channel.Name, Active: session.ctx.Err() == nil,
			Info: session.info, RefreshAge: yspTimeAge(session.refreshed), Cooldown: cooldowns[session.channel.ID], Error: session.errorText,
			Catchup: session.catchup != nil, Sequence: session.sequence, Segments: len(session.segments)}
		state.Info.Route = session.mode
		if session.format.Decoder {
			state.Info.Width, state.Info.Height, state.Info.Decoder = session.format.Width, session.format.Height, true
		}
		session.mu.Unlock()
		result.Channels = append(result.Channels, state)
	}
	known := map[string]bool{}
	for _, channel := range result.Channels {
		known[channel.ID] = true
	}
	for _, channel := range yspChannels {
		if !known[channel.ID] {
			result.Channels = append(result.Channels, yspChannelStatus{ID: channel.ID, Name: channel.Name, RefreshAge: -1, Cooldown: cooldowns[channel.ID]})
		}
	}
	sort.Slice(result.Channels, func(i, j int) bool { return result.Channels[i].ID < result.Channels[j].ID })
	live.eventsMu.Lock()
	result.Events = append([]yspLiveEvent(nil), live.events...)
	live.eventsMu.Unlock()
	return result
}

func yspRouteLabel(route string) string {
	switch route {
	case "device":
		return "设备高码率"
	case "jce":
		return "标准直播"
	case "bk":
		return "备用直播"
	case "web":
		return "网页直播"
	}
	return strings.TrimSpace(route)
}
