package core

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const yspDeviceAPI = "https://ytpaddr.cctv.cn/gsnw/"
const yspDeviceReportURL = "https://ytpdata.cctv.cn/das/app/data/message/single"
const yspDeviceCloudURL = "https://ytpcloudws.cctv.cn/cloudps/wssapi/device/v2/"
const yspDeviceSessionTTL = 2 * time.Hour
const yspDeviceEntryTTL = 10 * time.Minute

type yspDeviceError struct {
	stage      string
	status     int
	invalidate bool
}

func (err *yspDeviceError) Error() string {
	if err.status != 0 {
		return fmt.Sprintf("央视频设备协议 %s HTTP %d", err.stage, err.status)
	}
	return "央视频设备协议 " + err.stage + " 失败"
}

func yspDeviceInvalidates(err error) bool {
	var deviceError *yspDeviceError
	return errors.As(err, &deviceError) && deviceError.invalidate
}

type yspDeviceSession struct {
	client   *http.Client
	headers  map[string]string
	key      string
	device   yspDeviceState
	created  time.Time
	lastBeat time.Time
}

type yspDeviceEntry struct {
	address  string
	headers  map[string]string
	expires  time.Time
	lastUsed time.Time
}

type yspDeviceResolver struct {
	mu           sync.Mutex
	control      chan struct{}
	client       *http.Client
	path         string
	loaded       bool
	device       yspDeviceState
	session      *yspDeviceSession
	entries      map[string]yspDeviceEntry
	retryAt      map[string]time.Time
	sessionRetry time.Time
	lastRequest  time.Time
	cancel       context.CancelFunc
}

func newYSPDeviceResolver(client *http.Client, directory string) *yspDeviceResolver {
	jar, _ := cookiejar.New(nil)
	generic := *client
	generic.Jar = jar
	return &yspDeviceResolver{client: &generic, path: filepath.Join(directory, "ysp-device-state.json"), control: make(chan struct{}, 1), entries: map[string]yspDeviceEntry{}, retryAt: map[string]time.Time{}}
}

func (device *yspDeviceResolver) start() {
	device.mu.Lock()
	defer device.mu.Unlock()
	if device.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	device.cancel = cancel
	go device.run(ctx)
}

func (device *yspDeviceResolver) stop() {
	device.mu.Lock()
	defer device.mu.Unlock()
	if device.cancel != nil {
		device.cancel()
		device.cancel = nil
	}
}

func (device *yspDeviceResolver) run(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		work, cancel := context.WithTimeout(ctx, 3*time.Minute)
		device.pulse(work)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (device *yspDeviceResolver) lockControl(ctx context.Context) error {
	select {
	case device.control <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-device.control
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (device *yspDeviceResolver) pulse(ctx context.Context) {
	if err := device.lockControl(ctx); err != nil {
		return
	}
	defer func() { <-device.control }()
	device.mu.Lock()
	session, retry := device.session, device.sessionRetry
	device.mu.Unlock()
	now := time.Now()
	if (session == nil || now.Sub(session.created) >= yspDeviceSessionTTL-5*time.Minute) && !now.Before(retry) {
		fresh, err := device.bootstrap(ctx)
		device.mu.Lock()
		if ctx.Err() == nil {
			if err == nil {
				device.session, session = fresh, fresh
				device.sessionRetry = time.Time{}
			} else {
				device.sessionRetry = time.Now().Add(30 * time.Second)
			}
		}
		device.mu.Unlock()
	}
	if session == nil || ctx.Err() != nil {
		return
	}
	if time.Since(session.created) >= yspDeviceSessionTTL {
		device.mu.Lock()
		if device.session == session {
			device.session = nil
		}
		device.mu.Unlock()
		return
	}
	if time.Since(session.lastBeat) >= 30*time.Second {
		err := device.heartbeat(ctx, session)
		device.mu.Lock()
		if device.session == session {
			if yspDeviceInvalidates(err) {
				device.session = nil
				device.sessionRetry = time.Now().Add(30 * time.Second)
			} else if err == nil {
				session.lastBeat = time.Now()
			}
		}
		device.mu.Unlock()
	}
	device.mu.Lock()
	for id, entry := range device.entries {
		if time.Since(entry.lastUsed) > 2*time.Minute {
			delete(device.entries, id)
			delete(device.retryAt, id)
		}
	}
	device.mu.Unlock()
}

func (device *yspDeviceResolver) resolve(ctx context.Context, channel yspChannel) (yspDeviceEntry, error) {
	if err := ctx.Err(); err != nil {
		return yspDeviceEntry{}, err
	}
	now := time.Now()
	device.mu.Lock()
	entry, exists := device.entries[channel.ID]
	if exists {
		entry.lastUsed = now
		device.entries[channel.ID] = entry
	}
	session, retry := device.session, device.retryAt[channel.ID]
	device.mu.Unlock()
	if exists && now.Before(entry.expires) {
		return entry, nil
	}
	stale := exists && now.Before(entry.expires.Add(2*time.Minute))
	if session == nil || now.Sub(session.created) >= yspDeviceSessionTTL || now.Before(retry) {
		if stale {
			return entry, nil
		}
		return yspDeviceEntry{}, errors.New("央视频设备协议暂未就绪")
	}
	select {
	case device.control <- struct{}{}:
		defer func() { <-device.control }()
	default:
		if stale {
			return entry, nil
		}
		return yspDeviceEntry{}, errors.New("央视频设备协议正在更新会话")
	}
	device.mu.Lock()
	session = device.session
	device.mu.Unlock()
	if session == nil || ctx.Err() != nil {
		return yspDeviceEntry{}, errors.New("央视频设备协议暂未就绪")
	}
	fresh, err := device.resolveChannel(ctx, session, yspDeviceLiveIDs[channel.ID])
	device.mu.Lock()
	defer device.mu.Unlock()
	if err != nil {
		device.retryAt[channel.ID] = time.Now().Add(30 * time.Second)
		if yspDeviceInvalidates(err) && device.session == session {
			device.session = nil
			device.sessionRetry = time.Now().Add(30 * time.Second)
		}
		if stale {
			return entry, nil
		}
		return yspDeviceEntry{}, err
	}
	fresh.lastUsed = time.Now()
	device.entries[channel.ID] = fresh
	delete(device.retryAt, channel.ID)
	return fresh, nil
}

func (device *yspDeviceResolver) reject(channel yspChannel, rejected yspDeviceEntry) {
	device.mu.Lock()
	defer device.mu.Unlock()
	entry, exists := device.entries[channel.ID]
	if exists && entry.address == rejected.address && entry.headers["APPSIGN"] == rejected.headers["APPSIGN"] {
		delete(device.entries, channel.ID)
		device.retryAt[channel.ID] = time.Now().Add(30 * time.Second)
	}
}

func yspDeviceHeaders(state yspDeviceState, timestamp int64) map[string]string {
	return map[string]string{
		"Accept": "application/json", "Accept-Language": "zh-CN,zh;q=0.8", "Referer": "api.cctv.cn", "User-Agent": "cctv_app_tv",
		"UID": state.Profile.AndroidID, "appChannel": "dangbei", "X-Uid": state.UID, "X-Fingerprint": yspDeviceFingerprint(state.UID, timestamp), "X-Version": yspDeviceVersion,
		"Content-Type": "application/json", "Connection": "Keep-Alive", "Accept-Encoding": "gzip", "Cache-Control": "no-cache",
	}
}

func yspDeviceCopyHeaders(headers map[string]string) map[string]string {
	copy := make(map[string]string, len(headers))
	for key, value := range headers {
		copy[key] = value
	}
	return copy
}

func (device *yspDeviceResolver) request(ctx context.Context, client *http.Client, stage, method, address string, body []byte, headers map[string]string) ([]byte, error) {
	if delay := time.Until(device.lastRequest.Add(time.Second)); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	defer func() { device.lastRequest = time.Now() }()
	req, err := http.NewRequestWithContext(ctx, method, address, bytes.NewReader(body))
	if err != nil {
		return nil, &yspDeviceError{stage: stage}
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if headers["User-Agent"] == "cctv_app_tv" {
		nonce, err := yspDeviceNonce()
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-Nonce", nonce)
		if req.Header.Get("X-Timestamp") == "" {
			req.Header.Set("X-Timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &yspDeviceError{stage: stage}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		core := stage == "app/start" || stage == "live/v1/01" || stage == "live/v1/02" || stage == "VDN" || stage == "heartbeat"
		return nil, &yspDeviceError{stage: stage, status: resp.StatusCode, invalidate: core && (resp.StatusCode == 400 || resp.StatusCode == 401 || resp.StatusCode == 403)}
	}
	var reader io.Reader = resp.Body
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		compressed, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, &yspDeviceError{stage: stage}
		}
		defer compressed.Close()
		reader = compressed
	}
	b, err := io.ReadAll(io.LimitReader(reader, (4<<20)+1))
	if err != nil || len(b) > 4<<20 {
		return nil, &yspDeviceError{stage: stage}
	}
	return b, nil
}

func (device *yspDeviceResolver) post(ctx context.Context, client *http.Client, stage, address string, body any, headers map[string]string) ([]byte, error) {
	b, err := yspDeviceJSON(body)
	if err != nil {
		return nil, err
	}
	return device.request(ctx, client, stage, "POST", address, b, headers)
}

func (device *yspDeviceResolver) postJSON(ctx context.Context, client *http.Client, stage, address string, body any, headers map[string]string) (map[string]any, error) {
	b, err := device.post(ctx, client, stage, address, body, headers)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if json.Unmarshal(b, &result) != nil {
		return nil, &yspDeviceError{stage: stage}
	}
	return result, nil
}

func yspDeviceReport(state yspDeviceState, sdk string, timestamp int64) map[string]string {
	p := state.Profile
	value := map[string]string{
		"cctv_id": state.UID, "device_id": p.AndroidID, "idfa": "", "idfv": "", "user_id": "", "app_key": "1178c84d-4818-44ff-b415-02106e87e144",
		"imei": "", "android_id": p.AndroidID, "mac": p.MAC, "device_builder_type": p.BuildType, "device_hardware": p.Hardware, "device_board": p.Board,
		"device_brand": p.Brand, "device_params": p.Device, "device_display": p.Display, "device_version_id": p.VersionID, "device_host": p.Host,
		"device_product": p.Product, "device_tags": p.Tags, "device_user": p.User, "device_fingerprint": p.Fingerprint, "device_manufacturer": p.Manufacturer,
		"device_model": p.ReportModel, "device_resolution": p.Resolution, "system_type": "Android", "device_type": "TV", "app_language": "CHINESE",
		"app_version": yspDeviceVersion, "sdk_version": sdk, "os_version": "13", "app_channel": "dangbei", "data_time": strconv.FormatInt(timestamp, 10),
	}
	if value["device_model"] == "" {
		value["device_model"] = strings.ReplaceAll(strings.ReplaceAll(p.Model, p.Manufacturer, ""), " ", "")
	}
	limits := map[string]int{"device_host": 128, "device_fingerprint": 128, "device_user": 30, "device_model": 50, "device_resolution": 20, "app_version": 30, "sdk_version": 30, "os_version": 20, "app_channel": 50, "data_time": 13}
	for key, text := range value {
		limit, exists := limits[key]
		if !exists {
			limit = 64
		}
		if len(text) > limit {
			value[key] = text[:limit]
		}
	}
	return value
}

func (device *yspDeviceResolver) collect(ctx context.Context, state yspDeviceState, body any) error {
	b, err := yspDeviceJSON(body)
	if err != nil {
		return err
	}
	form := url.Values{"info": {string(b)}}
	headers := map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Charset": "UTF-8", "User-Agent": "Dalvik/2.1.0 (Linux; U; Android 13; " + state.Profile.Model + " Build/" + state.Profile.VersionID + ")", "Connection": "Keep-Alive", "Accept-Encoding": "gzip"}
	_, err = device.request(ctx, device.client, "collect", "POST", "https://collect.cctv.cn/cctvmobileinf/rest/cctv/receive/new/app", []byte(form.Encode()), headers)
	return err
}

func (device *yspDeviceResolver) bootstrap(ctx context.Context) (*yspDeviceSession, error) {
	if !device.loaded {
		state, err := yspLoadDeviceState(device.path)
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
	headers := yspDeviceHeaders(state, time.Now().UnixMilli())
	if err := device.collect(ctx, state, map[string]any{"key": "app_start_d1", "value": yspDeviceReport(state, "1.0.0", time.Now().UnixMilli())}); err != nil {
		return nil, err
	}
	root := map[string]string{"X-Uid": "ROOT", "X-Fingerprint": "ROOT", "X-Version": yspDeviceVersion, "UID": "ROOT", "Referer": "api.cctv.cn", "User-Agent": "cctv_app_tv", "appChannel": "ROOT", "Connection": "Keep-Alive", "Accept-Encoding": "gzip"}
	if _, err := device.request(ctx, device.client, "dictionary", "POST", yspDeviceAPI+"player/dictionary/obtain/v1", nil, root); err != nil {
		return nil, err
	}
	var key string
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		timestamp := time.Now().UnixMilli()
		headers = yspDeviceHeaders(state, timestamp)
		startHeaders := yspDeviceCopyHeaders(headers)
		startHeaders["UID"] = ""
		startHeaders["X-Timestamp"] = strconv.FormatInt(timestamp, 10)
		body, err := yspDeviceJSON(map[string]any{"key": "app_start_d1", "value": yspDeviceReport(state, "", time.Now().UnixMilli())})
		if err != nil {
			return nil, err
		}
		body = bytes.ReplaceAll(body, []byte{'/'}, []byte{'\\', '/'})
		var raw []byte
		raw, last = device.request(ctx, &client, "app/start", "POST", yspDeviceAPI+"api/app/start/v1/01", body, startHeaders)
		if last == nil {
			var reply map[string]any
			if json.Unmarshal(raw, &reply) != nil {
				last = &yspDeviceError{stage: "app/start", invalidate: true}
			} else {
				encrypted, _ := reply["data"].(string)
				if data, ok := reply["data"].(map[string]any); ok {
					encrypted, _ = data["key"].(string)
				}
				if encrypted == "" {
					last = &yspDeviceError{stage: "app/start", invalidate: true}
				} else {
					key, last = yspDeviceDecrypt(encrypted, headers["X-Fingerprint"][:32])
				}
			}
		}
		if last == nil && key != "" {
			break
		}
	}
	if key == "" || last != nil {
		if last == nil {
			last = &yspDeviceError{stage: "app/start", invalidate: true}
		}
		return nil, last
	}
	value := yspDeviceReport(state, "", time.Now().UnixMilli())
	value["event_id"], value["event_name"], value["event_time"] = "app_start", "应用启动", value["data_time"]
	value["network_type"], value["cur_version"], value["channel"], value["pre_version"] = "WIFI", yspDeviceVersion, "dangbei", yspDeviceVersion
	eventHeaders := yspDeviceCopyHeaders(headers)
	eventHeaders["UID"] = ""
	device.post(ctx, device.client, "app event", yspDeviceReportURL, map[string]any{"key": "event", "value": value}, eventHeaders)
	pageNonce, err := yspDeviceNonce()
	if err != nil {
		return nil, err
	}
	pageEnd := time.Now().UnixMilli()
	value = yspDeviceReport(state, "", pageEnd+2)
	value["start_time"], value["end_time"], value["duration"] = strconv.FormatInt(pageEnd-1000, 10), strconv.FormatInt(pageEnd, 10), "1000"
	value["page_name"], value["session_id"], value["network_type"] = "com.cctv.tv.mvp.ui.activity.MainActivity", pageNonce, "WIFI"
	page := map[string]any{"key": "page_d1", "value": value}
	device.collect(ctx, state, page)
	device.post(ctx, device.client, "page event", yspDeviceReportURL, page, headers)
	if guid, err := device.cloudGUID(ctx, state, headers); err == nil && guid != "" {
		state.CloudGUID = guid
		if state.RegisteredAt == 0 {
			state.RegisteredAt = float64(time.Now().UnixMilli()) / 1000
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err := yspSaveDeviceState(device.path, state); err != nil {
		return nil, errors.New("无法保存央视频设备注册状态")
	}
	device.device = state
	if state.CloudGUID != "" {
		value = yspDeviceReport(state, "", time.Now().UnixMilli())
		value["version"], value["network_status"], value["device_info"] = yspDeviceVersion, "WiFi", state.Profile.Brand+"-"+state.Profile.Model
		value["manufacturer"], value["cpu_info"], value["chip_info"] = state.Profile.Manufacturer, "", state.Profile.Hardware
		value["ram_info"], value["memory_info"], value["system_info"], value["guid"] = "", "", "13/33", state.CloudGUID
		device.post(ctx, device.client, "device info", yspDeviceReportURL, map[string]any{"key": "app_device_info", "value": value}, headers)
	}
	session := &yspDeviceSession{client: &client, headers: headers, key: key, device: state}
	if err := device.heartbeat(ctx, session); err != nil {
		return nil, err
	}
	if _, err := device.post(ctx, &client, "index", yspDeviceAPI+"api/index/v1/01", map[string]string{"channel": "dangbei", "source": "application"}, headers); err != nil {
		return nil, err
	}
	form := url.Values{"appcommon": {yspDeviceAppCommon()}}
	formHeaders := yspDeviceCopyHeaders(headers)
	formHeaders["Content-Type"] = "application/x-www-form-urlencoded"
	delete(formHeaders, "Accept")
	if _, err := device.request(ctx, &client, "DRM config", "POST", yspDeviceAPI+"drm/config/obtain/v1", []byte(form.Encode()), formHeaders); err != nil {
		return nil, err
	}
	delete(formHeaders, "Content-Type")
	if _, err := device.request(ctx, &client, "version config", "GET", yspDeviceAPI+"version/config/obtain/v1?"+form.Encode(), nil, formHeaders); err != nil {
		return nil, err
	}
	session.created, session.lastBeat = time.Now(), time.Now()
	return session, nil
}

func yspDeviceResult(value map[string]any) (int, bool) {
	for _, key := range []string{"result", "code", "errCode", "errcode", "ret"} {
		switch raw := value[key].(type) {
		case float64:
			return int(raw), true
		case string:
			if code, err := strconv.Atoi(raw); err == nil {
				return code, true
			}
		}
	}
	for _, key := range []string{"data", "error", "response"} {
		if nested, ok := value[key].(map[string]any); ok {
			if code, found := yspDeviceResult(nested); found {
				return code, true
			}
		}
	}
	return 0, false
}

func yspDeviceGUID(value map[string]any) string {
	data, _ := value["data"].(map[string]any)
	guid, _ := data["guid"].(string)
	return guid
}

func (device *yspDeviceResolver) cloudGUID(ctx context.Context, state yspDeviceState, headers map[string]string) (string, error) {
	encrypted, err := yspDeviceRSA(state.UID)
	if err != nil {
		return "", err
	}
	body := map[string]string{"device_name": yspDeviceAppName, "device_id": encrypted}
	reply, err := device.postJSON(ctx, device.client, "cloud/get", yspDeviceCloudURL+"get", body, headers)
	if err != nil {
		return "", err
	}
	code, found := yspDeviceResult(reply)
	if found && code == 0 {
		return yspDeviceGUID(reply), nil
	}
	if !found || code != 601 && code != 2 {
		return "", &yspDeviceError{stage: "cloud/get"}
	}
	for attempt := 0; attempt < 2; attempt++ {
		reply, err = device.postJSON(ctx, device.client, "cloud/register", yspDeviceCloudURL+"register", body, headers)
		if err != nil {
			return "", err
		}
		if guid := yspDeviceGUID(reply); guid != "" {
			return guid, nil
		}
		code, found = yspDeviceResult(reply)
		if !found || code != 695 {
			break
		}
	}
	if found && (code == 0 || code == 694 || code == 2) {
		reply, err = device.postJSON(ctx, device.client, "cloud/get", yspDeviceCloudURL+"get", body, headers)
		if err == nil {
			return yspDeviceGUID(reply), nil
		}
	}
	return "", &yspDeviceError{stage: "cloud/register"}
}

func (device *yspDeviceResolver) heartbeat(ctx context.Context, session *yspDeviceSession) error {
	value := yspDeviceReport(session.device, "", time.Now().UnixMilli())
	value["network_type"], value["guid"], value["other"] = "WiFi", session.device.CloudGUID, ""
	_, err := device.post(ctx, device.client, "heartbeat", yspDeviceReportURL, map[string]any{"key": "app_heartbeat", "value": value}, session.headers)
	return err
}

func yspDeviceAppCommon() string {
	b, _ := yspDeviceJSON(map[string]string{"adid": "", "av": yspDeviceVersion, "an": yspDeviceAppName, "ap": "cctv_app_tv"})
	return string(b)
}

func (device *yspDeviceResolver) resolveChannel(ctx context.Context, session *yspDeviceSession, liveID string) (yspDeviceEntry, error) {
	body := map[string]any{"screenParam": session.device.ScreenParam, "rate": "", "systemType": "ios", "model": session.device.CastModel, "id": liveID,
		"userId": "BAEBFF2B-C516-4F34-ABC0-A824A6461CBD", "clientSign": "cctvVideo", "deviceId": map[string]string{"serial": "", "imei": "", "android_id": ""}}
	reply, err := device.postJSON(ctx, session.client, "live/v1/01", yspDeviceAPI+"api/live/v1/01", body, session.headers)
	if err != nil {
		return yspDeviceEntry{}, err
	}
	data, _ := reply["data"].(map[string]any)
	videos, _ := data["videoList"].([]any)
	if len(videos) == 0 {
		videos, _ = data["videos"].([]any)
	}
	var video map[string]any
	for _, item := range videos {
		candidate, ok := item.(map[string]any)
		address, _ := candidate["url"].(string)
		if !ok || address == "" {
			continue
		}
		if video == nil {
			video = candidate
		}
		if candidate["rate"] == "36p" {
			video = candidate
			break
		}
	}
	liveURL, _ := video["url"].(string)
	if liveURL == "" {
		return yspDeviceEntry{}, &yspDeviceError{stage: "live/v1/01"}
	}
	if !isProviderHTTPMediaURL(liveURL) {
		liveURL, err = yspDeviceDecrypt(liveURL, session.key)
		if err != nil {
			return yspDeviceEntry{}, err
		}
	}
	if !isProviderHTTPMediaURL(liveURL) {
		return yspDeviceEntry{}, &yspDeviceError{stage: "live/v1/01"}
	}
	guid, err := yspDeviceEncrypt("", session.key)
	if err != nil {
		return yspDeviceEntry{}, err
	}
	reply, err = device.postJSON(ctx, session.client, "live/v1/02", yspDeviceAPI+"api/live/v1/02", map[string]string{"guid": guid}, session.headers)
	if err != nil {
		return yspDeviceEntry{}, err
	}
	encrypted, _ := reply["data"].(string)
	if data, ok := reply["data"].(map[string]any); ok {
		encrypted, _ = data["appSecret"].(string)
		if encrypted == "" {
			encrypted, _ = data["app_secret"].(string)
		}
	}
	if encrypted == "" {
		return yspDeviceEntry{}, &yspDeviceError{stage: "live/v1/02", invalidate: true}
	}
	secret, err := yspDeviceDecrypt(encrypted, session.key)
	if err != nil {
		return yspDeviceEntry{}, err
	}
	random, err := yspDeviceRandomString()
	if err != nil {
		return yspDeviceEntry{}, err
	}
	digest := md5.Sum([]byte(yspDeviceAppID + secret + random))
	sign := hex.EncodeToString(digest[:])
	headers := yspDeviceCopyHeaders(session.headers)
	headers["APPID"], headers["APPSIGN"], headers["APPRANDOMSTR"] = yspDeviceAppID, sign, random
	headers["Content-Type"] = "application/x-www-form-urlencoded"
	delete(headers, "Accept")
	form := url.Values{"appcommon": {yspDeviceAppCommon()}, "url": {liveURL}}
	raw, err := device.request(ctx, device.client, "VDN", "POST", "https://ytpvdn.cctv.cn/cctvmobileinf/rest/cctv/videoliveUrl/getstream", []byte(form.Encode()), headers)
	if err != nil {
		return yspDeviceEntry{}, err
	}
	var vdn map[string]any
	if json.Unmarshal(raw, &vdn) != nil || vdn["succeed"] != "1" && vdn["succeed"] != float64(1) {
		return yspDeviceEntry{}, &yspDeviceError{stage: "VDN"}
	}
	address, _ := vdn["url"].(string)
	if !isProviderHTTPMediaURL(address) {
		return yspDeviceEntry{}, &yspDeviceError{stage: "VDN"}
	}
	playbackHeaders := map[string]string{"UID": session.device.Profile.AndroidID, "APPID": yspDeviceAppID, "APPSIGN": sign, "APPRANDOMSTR": random,
		"Referer": "api.cctv.cn", "User-Agent": "cctv_app_tv", "Accept": "*/*", "Accept-Encoding": "identity", "Connection": "close"}
	return yspDeviceEntry{address: address, headers: playbackHeaders, expires: time.Now().Add(yspDeviceEntryTTL)}, nil
}
