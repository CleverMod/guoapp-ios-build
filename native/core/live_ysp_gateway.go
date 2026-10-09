package core

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type yspGatewayPrefixKey struct{}

type yspGatewayPending struct {
	done  chan struct{}
	token string
	err   error
}

var yspGatewayEPGURLs = []string{"https://epg.zsdc.eu.org/t.xml.gz", "https://epg.pw/xmltv/epg_CN.xml.gz"}

func yspCanonicalChannel(value string) string {
	value = strings.TrimSuffix(strings.Trim(strings.ToLower(strings.TrimSpace(value)), "/"), ".m3u8")
	if value == "cctv16-4k" || value == "cctv16_4k" || value == "cctv16/4k" || value == "cctv16(4k)" {
		value = "cctv164k"
	}
	if alias := yspChannelAliases[value]; alias != "" {
		value = alias
	}
	for _, channel := range yspChannels {
		if channel.ID == value {
			return value
		}
	}
	return ""
}

func (live *yspLiveServer) startGateway() error {
	live.mu.Lock()
	defer live.mu.Unlock()
	if live.gatewayServer != nil {
		return nil
	}
	settings := live.settings
	if err := settings.validate(); err != nil {
		return err
	}
	bind := "127.0.0.1"
	if settings.GatewayLAN {
		bind = "0.0.0.0"
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort(bind, strconv.Itoa(settings.GatewayPort)))
	if err != nil {
		return errors.New("订阅服务端口无法使用，请更换端口后重试")
	}
	noise, err := yspRandom(24)
	if err != nil {
		listener.Close()
		return err
	}
	live.gatewayToken = hex.EncodeToString(noise)
	prefix := "/tv/" + live.gatewayToken
	srv := &http.Server{Handler: http.HandlerFunc(live.gatewayHandler), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	live.gatewayServer = srv
	live.gatewaySessions = map[string]string{}
	live.gatewayOpen = map[string]*yspGatewayPending{}
	addresses := []string{"127.0.0.1"}
	if settings.GatewayLAN {
		interfaces, _ := net.Interfaces()
		for _, device := range interfaces {
			if device.Flags&net.FlagUp == 0 || device.Flags&net.FlagLoopback != 0 {
				continue
			}
			assigned, _ := device.Addrs()
			for _, address := range assigned {
				ip, _, err := net.ParseCIDR(address.String())
				if err == nil && ip.To4() != nil && ip.IsPrivate() {
					addresses = append(addresses, ip.String())
				}
			}
		}
	}
	sort.Strings(addresses)
	live.gatewayURLs = []string{}
	seen := map[string]bool{}
	for _, address := range addresses {
		if !seen[address] {
			live.gatewayURLs = append(live.gatewayURLs, "http://"+net.JoinHostPort(address, strconv.Itoa(settings.GatewayPort))+prefix+"/all.m3u")
			seen[address] = true
		}
	}
	if err := live.ensureServingLocked(); err != nil {
		live.gatewayServer, live.gatewayURLs, live.gatewayToken = nil, nil, ""
		listener.Close()
		return err
	}
	go func() {
		srv.Serve(listener)
		live.mu.Lock()
		current := live.gatewayServer == srv
		live.mu.Unlock()
		if current {
			live.stopGateway()
		}
	}()
	live.event("gateway", "直播订阅服务已开启", "")
	return nil
}

func (live *yspLiveServer) stopGateway() {
	live.mu.Lock()
	srv := live.gatewayServer
	live.gatewayServer, live.gatewayURLs, live.gatewayToken = nil, nil, ""
	for token, session := range live.sessions {
		if session.gateway {
			session.cancel()
			delete(live.sessions, token)
		}
	}
	live.gatewaySessions = map[string]string{}
	live.mu.Unlock()
	if srv != nil {
		srv.Close()
		live.event("gateway", "直播订阅服务已关闭", "")
	}
}

func (live *yspLiveServer) gatewayHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)
	live.mu.Lock()
	token := live.gatewayToken
	live.mu.Unlock()
	if len(parts) < 2 || parts[0] != "tv" || token == "" || subtle.ConstantTimeCompare([]byte(parts[1]), []byte(token)) != 1 {
		http.Error(w, "订阅链接已失效，请重新复制链接", http.StatusUnauthorized)
		return
	}
	prefix := "/tv/" + token
	clone := r.Clone(context.WithValue(r.Context(), yspGatewayPrefixKey{}, prefix))
	copyURL := *r.URL
	copyURL.Path, copyURL.RawPath = "/", ""
	if len(parts) == 3 {
		copyURL.Path += parts[2]
	}
	clone.URL = &copyURL
	live.serveGateway(w, clone, prefix)
}

func (live *yspLiveServer) gatewaySession(ctx context.Context, channel, query string) (string, error) {
	window, err := yspParseCatchup(query, time.Now())
	if err != nil {
		return "", err
	}
	key := channel
	if window != nil {
		key += fmt.Sprintf("?%d-%d", window.start, window.end)
	}
	live.mu.Lock()
	server := live.gatewayServer
	if server == nil {
		live.mu.Unlock()
		return "", errors.New("直播订阅服务已关闭")
	}
	if token := live.gatewaySessions[key]; token != "" {
		if session := live.sessions[token]; session != nil && session.ctx.Err() == nil {
			session.lastUsed = time.Now()
			live.mu.Unlock()
			return token, nil
		}
		delete(live.gatewaySessions, key)
	}
	if pending := live.gatewayOpen[key]; pending != nil {
		live.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-pending.done:
			return pending.token, pending.err
		}
	}
	pending := &yspGatewayPending{done: make(chan struct{})}
	live.gatewayOpen[key] = pending
	if len(live.sessions) >= 15 {
		oldest, lastUsed := "", time.Now()
		for token, session := range live.sessions {
			if session.gateway && session.lastUsed.Before(lastUsed) {
				oldest, lastUsed = token, session.lastUsed
			}
		}
		if session := live.sessions[oldest]; session != nil {
			session.cancel()
			delete(live.sessions, oldest)
		}
	}
	live.mu.Unlock()
	plan, openErr := live.openWithOptions(ctx, channel, query, false)
	live.mu.Lock()
	if openErr == nil && live.gatewayServer != server {
		openErr = errors.New("直播订阅服务已关闭")
	}
	if openErr == nil {
		if session := live.sessions[plan.Session]; session != nil {
			session.gateway = true
			live.gatewaySessions[key], pending.token = plan.Session, plan.Session
		} else {
			openErr = errors.New("直播订阅会话已结束")
		}
	}
	pending.err = openErr
	delete(live.gatewayOpen, key)
	close(pending.done)
	live.mu.Unlock()
	if openErr != nil && plan.Session != "" {
		live.release(plan.Session)
	}
	return pending.token, pending.err
}

func (live *yspLiveServer) serveGateway(w http.ResponseWriter, r *http.Request, prefix string) {
	path := r.URL.Path
	w.Header().Set("Cache-Control", "no-store")
	switch path {
	case "/":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if r.Method == http.MethodGet {
			fmt.Fprintf(w, "电视直播 v9.0\n订阅：%s/all.m3u\n健康状态：%s/health\n诊断：%s/diag\n", prefix, prefix, prefix)
		}
	case "/health":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method == http.MethodGet {
			diagnostics := live.diagnostics()
			ready := 0
			for _, device := range diagnostics.Devices {
				if device.Ready && !device.Standby {
					ready++
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "version": "9.0.0", "uptime_sec": diagnostics.Uptime,
				"engine":   map[string]any{"ready": ready > 0, "active_device_slots": ready, "standby_refilling": diagnostics.Refilling},
				"channels": len(yspChannels), "ts_cache": diagnostics.Cache, "active": diagnostics.Active, "maximum": diagnostics.Maximum})
		}
	case "/diag":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if r.Method == http.MethodGet {
			live.writeDiagnostics(w)
		}
	case "/all.m3u":
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
		if r.Method == http.MethodGet {
			live.writeSubscription(w, r, prefix)
		}
	case "/proxy.ts", "/proxy", "/engine_proxy":
		live.serveRegisteredProxy(w, r, prefix)
	default:
		if strings.HasPrefix(path, "/live/") {
			live.serve(w, r)
			return
		}
		channel := yspCanonicalChannel(strings.TrimSuffix(strings.TrimPrefix(path, "/"), ".m3u8"))
		if channel == "" || !strings.HasSuffix(path, ".m3u8") {
			http.NotFound(w, r)
			return
		}
		token, err := live.gatewaySession(r.Context(), channel, r.URL.RawQuery)
		if err != nil {
			http.Error(w, publicError(err).Error(), http.StatusBadGateway)
			return
		}
		clone := r.Clone(r.Context())
		copyURL := *r.URL
		copyURL.Path, copyURL.RawQuery, copyURL.RawPath = "/live/"+token+"/index.m3u8", "", ""
		clone.URL = &copyURL
		live.serve(w, clone)
	}
}

func (live *yspLiveServer) writeSubscription(w http.ResponseWriter, r *http.Request, prefix string) {
	base := "http://" + r.Host + prefix
	group := r.URL.Query().Get("group")
	k4 := strings.ToLower(r.URL.Query().Get("k4"))
	only4K := k4 == "1" || k4 == "true" || k4 == "yes"
	fmt.Fprintf(w, "#EXTM3U url-tvg=\"%s\" x-tvg-url=\"%s\"\n#EXT-X-APTV-PREVIEW: FALSE\n#EXT-X-APTV-LATENCY: FALSE\n#EXT-X-APTV-LOGO: FALSE\n", strings.Join(yspGatewayEPGURLs, ","), yspGatewayEPGURLs[0])
	for _, channel := range yspLiveChannels() {
		if group != "" && !strings.Contains(channel.Group, group) || only4K && !yspWarmChannel(channel.ID) {
			continue
		}
		fmt.Fprintf(w, "#EXTINF:-1 tvg-id=\"%s\" tvg-name=\"%s\" tvg-logo=\"https://garysclub.sharewithyou.dpdns.org/logos/ysp-live-logos/%s.png\" group-title=\"%s\"", channel.EPGID, channel.Name, channel.ID, channel.Group)
		if channel.CatchupDays > 0 {
			fmt.Fprint(w, " catchup=\"append\" catchup-days=\"7\" catchup-source=\"?playseek=${(b)yyyyMMddHHmmss}-${(e)yyyyMMddHHmmss}\"")
		}
		fmt.Fprintf(w, ",%s\n%s/%s.m3u8\n", channel.Name, base, channel.ID)
	}
}

func (live *yspLiveServer) writeDiagnostics(w http.ResponseWriter) {
	diagnostics := live.diagnostics()
	fmt.Fprintf(w, "电视直播 v9.0 · 运行 %d 秒\n模式：%s · 设备配额：%d · 会话：%d/%d\n", diagnostics.Uptime, diagnostics.Settings.DeviceMode, diagnostics.Settings.LinksPerDevice, diagnostics.Active, diagnostics.Maximum)
	for _, device := range diagnostics.Devices {
		fmt.Fprintf(w, "设备 %d：就绪=%t 型号=%s 会话=%s 链接=%d/%d 心跳=%d 距心跳=%ds 有效期=%ds 错误=%s\n", device.Slot, device.Ready, device.Model, device.Identity, device.Linked, device.Limit, device.HeartbeatCount, device.HeartbeatAge, device.SessionRemaining, device.HeartbeatError)
	}
	fmt.Fprintf(w, "分片缓存：%d 条 · %d/%d MB · 命中 %.1f%% · 命中/未中=%d/%d\n", diagnostics.Cache.Entries, diagnostics.Cache.Bytes>>20, diagnostics.Cache.LimitMB, diagnostics.Cache.HitRate, diagnostics.Cache.Hits, diagnostics.Cache.Misses)
	for _, channel := range diagnostics.Channels {
		fmt.Fprintf(w, "%s：%s · %dx%d · 上次刷新 %ds · 冷却 %ds · %s\n", channel.Name, yspRouteLabel(channel.Info.Route), channel.Info.Width, channel.Info.Height, channel.RefreshAge, channel.Cooldown, channel.Error)
	}
	for _, event := range diagnostics.Events {
		fmt.Fprintf(w, "%s %s %s\n", event.Time.Format(time.RFC3339), event.Channel, event.Message)
	}
}

func (live *yspLiveServer) serveRegisteredProxy(w http.ResponseWriter, r *http.Request, prefix string) {
	address := r.URL.Query().Get("url")
	if !strings.HasPrefix(address, "http://") && !strings.HasPrefix(address, "https://") {
		token := firstNonEmpty(r.URL.Query().Get("ts"), r.URL.Query().Get("token"), address)
		if strings.HasPrefix(r.URL.RawQuery, "=") {
			token = strings.TrimPrefix(r.URL.RawQuery, "=")
		} else if token == "" && !strings.Contains(r.URL.RawQuery, "=") {
			token = r.URL.RawQuery
		}
		data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(token, "="))
		if err == nil {
			address = string(data)
		}
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" {
		http.Error(w, "直播资源地址无效", http.StatusBadRequest)
		return
	}
	id := yspLiveResourceID(address)
	live.mu.Lock()
	token := ""
	for candidate, session := range live.sessions {
		if !session.gateway {
			continue
		}
		session.mu.Lock()
		resource, exists := session.media[id]
		session.mu.Unlock()
		if exists && resource.address == address {
			token = candidate
			break
		}
	}
	live.mu.Unlock()
	if token == "" {
		http.Error(w, "此资源未在直播会话中登记", http.StatusForbidden)
		return
	}
	clone := r.Clone(r.Context())
	copyURL := *r.URL
	copyURL.Path, copyURL.RawQuery = "/live/"+token+"/media/"+id, ""
	clone.URL = &copyURL
	live.serve(w, clone)
}
