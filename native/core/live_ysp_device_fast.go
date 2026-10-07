package core

import (
	"context"
	"errors"
	"net/http/cookiejar"
	"net/url"
	"time"
)

func (device *yspDeviceResolver) bootstrapFast(ctx context.Context) (*yspDeviceSession, error) {
	if !device.loaded {
		var state yspDeviceState
		var err error
		if device.path == "" {
			state, err = yspNewDeviceState()
		} else {
			state, err = yspLoadDeviceState(device.path)
		}
		if err != nil {
			return nil, err
		}
		device.device, device.loaded = state, true
	}
	state := device.device
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	client := *device.client
	client.Jar = jar
	key, headers, err := device.appStartKey(ctx, &client, state)
	if err != nil {
		return nil, err
	}
	if guid, err := device.cloudGUID(ctx, state, headers); err == nil && guid != "" {
		state.CloudGUID = guid
		if state.RegisteredAt == 0 {
			state.RegisteredAt = float64(time.Now().UnixMilli()) / 1000
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if device.path != "" {
		if err := yspSaveDeviceState(device.path, state); err != nil {
			return nil, errors.New("无法保存央视频设备注册状态")
		}
	}
	device.device = state
	session := &yspDeviceSession{client: &client, headers: headers, key: key, device: state, linked: map[string]bool{}}
	if err := device.heartbeat(ctx, session); err != nil {
		return nil, err
	}
	session.created, session.lastBeat = time.Now(), time.Now()
	device.mu.Lock()
	life := device.life
	device.mu.Unlock()
	if life != nil {
		go device.reportSession(life, session)
	}
	return session, nil
}

func (device *yspDeviceResolver) reportSession(life context.Context, session *yspDeviceSession) {
	ctx, cancel := context.WithTimeout(life, 30*time.Second)
	defer cancel()
	reporter := &yspDeviceResolver{client: session.client, fast: true}
	state := session.device
	reporter.collect(ctx, state, map[string]any{"key": "app_start_d1", "value": yspDeviceReport(state, "1.0.0", time.Now().UnixMilli())})
	root := map[string]string{"X-Uid": "ROOT", "X-Fingerprint": "ROOT", "X-Version": yspDeviceVersion, "UID": "ROOT", "Referer": "api.cctv.cn", "User-Agent": "cctv_app_tv", "appChannel": "ROOT", "Connection": "Keep-Alive", "Accept-Encoding": "gzip"}
	reporter.request(ctx, session.client, "dictionary", "POST", yspDeviceAPI+"player/dictionary/obtain/v1", nil, root)
	if state.CloudGUID != "" {
		value := yspDeviceReport(state, "", time.Now().UnixMilli())
		value["version"], value["network_status"], value["device_info"] = yspDeviceVersion, "WiFi", state.Profile.Brand+"-"+state.Profile.Model
		value["manufacturer"], value["cpu_info"], value["chip_info"] = state.Profile.Manufacturer, "", state.Profile.Hardware
		value["ram_info"], value["memory_info"], value["system_info"], value["guid"] = "", "", "13/33", state.CloudGUID
		reporter.post(ctx, session.client, "device info", yspDeviceReportURL, map[string]any{"key": "app_device_info", "value": value}, session.headers)
	}
	reporter.post(ctx, session.client, "index", yspDeviceAPI+"api/index/v1/01", map[string]string{"channel": "dangbei", "source": "application"}, session.headers)
	form := url.Values{"appcommon": {yspDeviceAppCommon()}}
	headers := yspDeviceCopyHeaders(session.headers)
	headers["Content-Type"] = "application/x-www-form-urlencoded"
	delete(headers, "Accept")
	reporter.request(ctx, session.client, "DRM config", "POST", yspDeviceAPI+"drm/config/obtain/v1", []byte(form.Encode()), headers)
	delete(headers, "Content-Type")
	reporter.request(ctx, session.client, "version config", "GET", yspDeviceAPI+"version/config/obtain/v1?"+form.Encode(), nil, headers)
}
