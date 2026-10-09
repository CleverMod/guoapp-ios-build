package core

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type yspChannel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Group       string `json:"group"`
	EPGID       string `json:"epgId"`
	EPGURL      string `json:"epgUrl"`
	CatchupDays int    `json:"catchupDays,omitempty"`
	SID         string `json:"-"`
	PID         string `json:"-"`
	Definition  string `json:"-"`
	Backup      bool   `json:"-"`
}
type yspSegment struct {
	key, address  string
	tags          []string
	duration      float64
	sequence      int64
	discontinuity int64
}
type yspLiveState struct {
	catchup   *yspCatchupRange
	mode      string
	urls      []string
	urlTime   time.Time
	playlist  string
	refreshed time.Time
	attempted time.Time
	segments  []yspSegment
	sequence  int64
	target    float64
	failures  int
	errorText string
	media     map[string]yspLiveResource
	info      yspStreamInfo
}
type yspLiveSession struct {
	mu        sync.Mutex
	refreshMu sync.Mutex
	channel   yspChannel
	ctx       context.Context
	cancel    context.CancelFunc
	lastUsed  time.Time
	gateway   bool
	format    yspStreamInfo
	yspLiveState
}
type yspLiveServer struct {
	mu              sync.Mutex
	client          *http.Client
	mediaClient     *http.Client
	device          yspDeviceSource
	web             *yspWebResolver
	guid            string
	sessions        map[string]*yspLiveSession
	cache           map[string]yspLiveState
	server          *http.Server
	address         string
	settings        nativeLiveSettings
	segmentCache    *yspSegmentCache
	created         time.Time
	eventsMu        sync.Mutex
	events          []yspLiveEvent
	eventTimes      map[string]time.Time
	gatewayServer   *http.Server
	gatewayURLs     []string
	gatewayToken    string
	gatewaySessions map[string]string
	gatewayOpen     map[string]*yspGatewayPending
}

var yspPlaylistURI = regexp.MustCompile(`URI="([^"]+)"`)

func (engine *nativeEngine) liveServer() (*yspLiveServer, error) {
	engine.liveMu.Lock()
	defer engine.liveMu.Unlock()
	if engine.live != nil {
		return engine.live, nil
	}
	random, err := yspRandom(16)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = engine.downloader.proxyRouter.proxy
	transport.MaxIdleConnsPerHost = 16
	transport.MaxConnsPerHost = 64
	transport.IdleConnTimeout = 30 * time.Second
	settings := yspLoadLiveSettings(engine.directory)
	engine.live = &yspLiveServer{client: &http.Client{Transport: transport, Timeout: 20 * time.Second}, guid: hex.EncodeToString(random), sessions: map[string]*yspLiveSession{}, settings: settings, segmentCache: newYSPSegmentCache(settings.CacheMB), created: time.Now()}
	engine.live.mediaClient = newYSPSignedMediaClient(transport)
	engine.live.device = newYSPDevicePool(engine.live.client, engine.directory)
	engine.live.device.(*yspDevicePool).configure(settings.DeviceMode, settings.LinksPerDevice)
	engine.live.device.(*yspDevicePool).logEvent = engine.live.event
	engine.live.web = newYSPWebResolver(engine.live.client, engine.downloader.attachedAccess["ysp_live"])
	return engine.live, nil
}
func (live *yspLiveServer) ensureServingLocked() error {
	if live.server != nil {
		return nil
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return errors.New("无法启动本机直播清单服务")
	}
	srv := &http.Server{Handler: http.HandlerFunc(live.serve), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	live.server = srv
	live.address = "http://" + listener.Addr().String()
	if live.device != nil {
		live.device.start()
	}
	go func() {
		srv.Serve(listener)
		live.mu.Lock()
		if live.server == srv {
			live.server = nil
			if live.device != nil {
				live.device.stop()
			}
		}
		live.mu.Unlock()
	}()
	go live.cleanup(srv)
	return nil
}
func (live *yspLiveServer) cleanup(srv *http.Server) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		live.mu.Lock()
		if live.server != srv {
			live.mu.Unlock()
			return
		}
		for token, s := range live.sessions {
			s.mu.Lock()
			catchup := s.catchup != nil
			s.mu.Unlock()
			if (!catchup || s.gateway) && time.Since(s.lastUsed) > 2*time.Minute {
				s.cancel()
				delete(live.sessions, token)
			}
		}
		if len(live.sessions) == 0 && live.gatewayServer == nil {
			live.server = nil
			if live.device != nil {
				live.device.stop()
			}
			live.mu.Unlock()
			if live.segmentCache != nil {
				live.segmentCache.clear()
			}
			srv.Close()
			return
		}
		live.mu.Unlock()
	}
}
func (live *yspLiveServer) open(ctx context.Context, slug string) (nativePlan, error) {
	return live.openWithOptions(ctx, slug, "", true)
}

func (live *yspLiveServer) openWithOptions(ctx context.Context, slug, query string, fresh bool) (nativePlan, error) {
	if err := ctx.Err(); err != nil {
		return nativePlan{}, err
	}
	var channel yspChannel
	for _, ch := range yspChannels {
		if ch.ID == slug {
			channel = ch
			break
		}
	}
	if channel.ID == "" {
		return nativePlan{}, errors.New("未知直播频道")
	}
	window, err := yspParseCatchup(query, time.Now())
	if err != nil {
		return nativePlan{}, err
	}
	if window != nil && !yspCatchupSupported[channel.ID] {
		return nativePlan{}, errors.New("此频道暂不支持回看")
	}
	random, err := yspRandom(24)
	if err != nil {
		return nativePlan{}, err
	}
	token := hex.EncodeToString(random)
	life, cancel := context.WithCancel(context.Background())
	mode := "jce"
	if channel.Backup {
		mode = "bk"
	}
	s := &yspLiveSession{channel: channel, ctx: life, cancel: cancel, yspLiveState: yspLiveState{mode: mode, catchup: window}, lastUsed: time.Now()}
	live.mu.Lock()
	if len(live.sessions) >= 15 {
		live.mu.Unlock()
		cancel()
		return nativePlan{}, errors.New("直播会话过多，请关闭旧播放页后重试")
	}
	if err := live.ensureServingLocked(); err != nil {
		live.mu.Unlock()
		cancel()
		return nativePlan{}, err
	}
	cached := false
	if window == nil {
		if state, exists := live.cache[channel.ID]; exists && state.playlist != "" && time.Since(state.refreshed) < yspLiveCacheTTL {
			s.yspLiveState = yspCopyLiveState(state)
			cached = true
		}
	}
	live.sessions[token] = s
	address := live.address
	live.mu.Unlock()
	if fresh && window == nil && live.device != nil {
		live.device.reopen(channel.ID)
	}
	if !cached {
		fetch, stop := context.WithCancel(ctx)
		detach := context.AfterFunc(life, stop)
		err = live.refresh(fetch, s)
		detach()
		stop()
		if err != nil {
			live.release(token)
			return nativePlan{}, err
		}
	}
	live.mu.Lock()
	s.lastUsed = time.Now()
	live.mu.Unlock()
	s.mu.Lock()
	playbackUA := yspUA
	if s.mode == "jce" || window != nil {
		playbackUA = yspJCEUA
	} else if s.mode == "web" {
		playbackUA = yspWebUA
	}
	s.mu.Unlock()
	if window == nil {
		go func() {
			if cached {
				ctx, cancel := context.WithTimeout(life, 30*time.Second)
				live.refresh(ctx, s)
				cancel()
			}
			live.keepFresh(s)
		}()
	}
	s.mu.Lock()
	info := s.info
	info.Route = s.mode
	s.mu.Unlock()
	qualities := []int{}
	if info.Height > 0 {
		qualities = []int{info.Height}
	}
	return nativePlan{URL: address + "/live/" + token + "/index.m3u8", Session: token, Quality: info.Height, Qualities: qualities, LiveInfo: &info, Headers: map[string]string{"User-Agent": playbackUA, "Referer": "https://live.cctv.cn/"}, RouteCount: 1}, nil
}
func (live *yspLiveServer) release(token string) {
	live.mu.Lock()
	defer live.mu.Unlock()
	if s := live.sessions[token]; s != nil {
		s.cancel()
		delete(live.sessions, token)
	}
}

func (live *yspLiveServer) playlist(ctx context.Context, address string, depth int) (string, error) {
	return live.playlistWithHeaders(ctx, address, depth, live.client, map[string]string{"User-Agent": yspUA, "Referer": "https://live.cctv.cn/", "Accept": "application/vnd.apple.mpegurl,application/json,*/*"})
}

func (live *yspLiveServer) playlistWithHeaders(ctx context.Context, address string, depth int, client *http.Client, headers map[string]string) (string, error) {
	if depth > 3 {
		return "", errors.New("央视频清单嵌套过深")
	}
	b, final, err := yspRequestClient(ctx, client, "GET", address, nil, headers)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(strings.TrimPrefix(string(b), "\ufeff"))
	if !strings.HasPrefix(text, "#EXTM3U") {
		return "", errors.New("央视频未返回有效直播清单")
	}
	if info, ok := ctx.Value(yspManifestInfoKey{}).(*yspStreamInfo); ok {
		yspReadManifestInfo(text, info)
	}
	base, err := url.Parse(final)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	variant := false
	abs := func(reference string) string {
		u, err := url.Parse(reference)
		if err != nil {
			return ""
		}
		u = base.ResolveReference(u)
		if u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "data" {
			return ""
		}
		return u.String()
	}
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			variant = true
			continue
		}
		if line != "" && !strings.HasPrefix(line, "#") {
			address = abs(line)
			if address == "" {
				return "", errors.New("央视频分片地址无效")
			}
			if variant {
				return live.playlistWithHeaders(ctx, address, depth+1, client, headers)
			}
			lines[i] = address
		} else if strings.Contains(line, `URI="`) {
			invalid := false
			lines[i] = yspPlaylistURI.ReplaceAllStringFunc(line, func(match string) string {
				reference := yspPlaylistURI.FindStringSubmatch(match)[1]
				address := abs(reference)
				if address == "" {
					invalid = true
				}
				return `URI="` + address + `"`
			})
			if invalid {
				return "", errors.New("央视频附属地址无效")
			}
		}
	}
	return strings.Join(lines, "\n") + "\n", nil
}
func yspParseSegments(text string) []yspSegment {
	segments := []yspSegment{}
	tags := []string{}
	keyTag, mapTag, pdt, byteRange := "", "", "", ""
	duration := 6.0
	sequence := int64(0)
	rangeOffset := int64(0)
	rangeResource := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			sequence, _ = strconv.ParseInt(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"), 10, 64)
		case strings.HasPrefix(line, "#EXT-X-KEY:"):
			keyTag = line
		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			mapTag = line
		case strings.HasPrefix(line, "#EXTINF:"):
			duration, _ = strconv.ParseFloat(strings.Split(strings.TrimPrefix(line, "#EXTINF:"), ",")[0], 64)
			if duration <= 0 || math.IsNaN(duration) || math.IsInf(duration, 0) {
				duration = 6
			}
			tags = append(tags, line)
		case strings.HasPrefix(line, "#EXT-X-PROGRAM-DATE-TIME:"):
			pdt = strings.TrimPrefix(line, "#EXT-X-PROGRAM-DATE-TIME:")
			tags = append(tags, line)
		case strings.HasPrefix(line, "#EXT-X-BYTERANGE:"):
			byteRange = line
		case line == "#EXT-X-DISCONTINUITY" || line == "#EXT-X-GAP":
			tags = append(tags, line)
		case line != "" && !strings.HasPrefix(line, "#"):
			identity := line
			if u, err := url.Parse(line); err == nil {
				u.RawQuery = ""
				u.Fragment = ""
				identity = u.String()
			}
			if byteRange != "" {
				parts := strings.SplitN(strings.TrimPrefix(byteRange, "#EXT-X-BYTERANGE:"), "@", 2)
				length, _ := strconv.ParseInt(parts[0], 10, 64)
				offset := rangeOffset
				if len(parts) == 2 {
					offset, _ = strconv.ParseInt(parts[1], 10, 64)
				} else if rangeResource != identity {
					offset = 0
				}
				if length > 0 && offset >= 0 {
					byteRange = fmt.Sprintf("#EXT-X-BYTERANGE:%d@%d", length, offset)
					rangeOffset = offset + length
					rangeResource = identity
				}
				tags = append(tags, byteRange)
			} else {
				rangeResource = ""
				rangeOffset = 0
			}
			if pdt != "" {
				identity = "pdt:" + pdt
			}
			identity += byteRange
			prefix := []string{}
			if keyTag != "" {
				key := keyTag
				if strings.Contains(key, "METHOD=AES-128") && !strings.Contains(key, "IV=") {
					key += fmt.Sprintf(",IV=0x%032x", sequence)
				}
				prefix = append(prefix, key)
			} else {
				prefix = append(prefix, "#EXT-X-KEY:METHOD=NONE")
			}
			if mapTag != "" {
				prefix = append(prefix, mapTag)
			}
			prefix = append(prefix, tags...)
			segments = append(segments, yspSegment{key: identity, address: line, tags: prefix, duration: duration})
			sequence++
			tags = nil
			pdt = ""
			byteRange = ""
		}
	}
	return segments
}
func yspMergePlaylist(s *yspLiveState, text string, changedRoute bool) error {
	fresh := yspParseSegments(text)
	if len(fresh) == 0 {
		return errors.New("央视频直播清单暂无分片")
	}
	target := s.target
	if target == 0 {
		target = 6
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, "#EXT-X-TARGETDURATION:") {
				if value, err := strconv.ParseFloat(strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:"), 64); err == nil && value > 0 && !math.IsInf(value, 0) {
					target = math.Max(target, math.Ceil(value))
				}
			}
		}
		for _, seg := range fresh {
			target = math.Max(target, math.Ceil(seg.duration))
		}
	} else {
		for _, seg := range fresh {
			if math.Round(seg.duration) > target {
				return errors.New("央视频直播分片时长发生变化，请重新取流")
			}
		}
	}
	positions := map[string]int{}
	for i, seg := range s.segments {
		positions[seg.key] = i
	}
	overlap := -1
	for index, seg := range fresh {
		if i, ok := positions[seg.key]; ok && !changedRoute {
			overlap = index
			seg.sequence = s.segments[i].sequence
			seg.discontinuity = s.segments[i].discontinuity
			tags := []string{}
			for _, tag := range seg.tags {
				if tag != "#EXT-X-DISCONTINUITY" {
					tags = append(tags, tag)
				}
			}
			seg.tags = tags
			for _, tag := range s.segments[i].tags {
				if tag == "#EXT-X-DISCONTINUITY" && !yspHasDiscontinuity(seg) {
					seg.tags = append([]string{tag}, seg.tags...)
				}
			}
			s.segments[i] = seg
		}
	}
	appendFrom := overlap + 1
	if len(s.segments) > 0 && (changedRoute || overlap < 0) {
		if len(fresh)-appendFrom > 3 {
			appendFrom = len(fresh) - 3
		}
		if appendFrom < len(fresh) && !yspHasDiscontinuity(fresh[appendFrom]) {
			fresh[appendFrom].tags = append([]string{"#EXT-X-DISCONTINUITY"}, fresh[appendFrom].tags...)
		}
	}
	for _, seg := range fresh[appendFrom:] {
		s.sequence++
		seg.sequence = s.sequence
		if len(s.segments) > 0 {
			seg.discontinuity = s.segments[len(s.segments)-1].discontinuity
		}
		if yspHasDiscontinuity(seg) {
			seg.discontinuity++
		}
		s.segments = append(s.segments, seg)
	}
	if len(s.segments) > 120 {
		s.segments = append([]yspSegment(nil), s.segments[len(s.segments)-120:]...)
	}
	window := s.segments
	windowSize := min(15, len(fresh))
	if len(window) > windowSize {
		window = window[len(window)-windowSize:]
	}
	if len(window) >= 4 {
		window = window[:len(window)-1]
	}
	s.target = target
	discontinuity := window[0].discontinuity
	if yspHasDiscontinuity(window[0]) {
		discontinuity--
	}
	var out strings.Builder
	fmt.Fprintf(&out, "#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-TARGETDURATION:%.0f\n#EXT-X-MEDIA-SEQUENCE:%d\n#EXT-X-DISCONTINUITY-SEQUENCE:%d\n", target, window[0].sequence, discontinuity)
	for _, seg := range window {
		for _, tag := range seg.tags {
			out.WriteString(tag + "\n")
		}
		out.WriteString(seg.address + "\n")
	}
	s.playlist = out.String()
	return nil
}
func yspHasDiscontinuity(segment yspSegment) bool {
	for _, tag := range segment.tags {
		if tag == "#EXT-X-DISCONTINUITY" {
			return true
		}
	}
	return false
}
func (live *yspLiveServer) refresh(ctx context.Context, s *yspLiveSession) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	s.mu.Lock()
	state := yspCopyLiveState(s.yspLiveState)
	s.mu.Unlock()
	err := live.refreshState(ctx, s.channel, &state)
	if s.ctx.Err() == nil {
		s.mu.Lock()
		s.yspLiveState = state
		s.mu.Unlock()
		if err == nil {
			live.remember(s.channel, state)
			live.event("refresh", "直播清单已刷新："+yspRouteLabel(state.mode), s.channel.ID)
		} else if ctx.Err() == nil {
			live.event("failure", state.errorText, s.channel.ID)
		}
	}
	return err
}
func (live *yspLiveServer) keepFresh(s *yspLiveSession) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
			live.refresh(ctx, s)
			cancel()
		}
	}
}
func (live *yspLiveServer) refreshState(ctx context.Context, channel yspChannel, s *yspLiveState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.catchup != nil {
		playlist, err := live.catchupPlaylist(ctx, channel, s.catchup)
		if err != nil {
			return err
		}
		s.playlist, s.refreshed = playlist, time.Now()
		s.info = yspStreamInfo{Route: "jce"}
		yspRegisterMedia(s, playlist, map[string]string{"User-Agent": yspJCEUA})
		return nil
	}
	s.attempted = time.Now()
	if live.device != nil && yspDeviceLiveIDs[channel.ID] != "" && live.deviceEnabled(channel) {
		deviceContext, cancel := context.WithTimeout(ctx, 8*time.Second)
		err := live.refreshDevice(deviceContext, channel, s)
		cancel()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if s.mode == "device" {
			s.failures++
			s.errorText = publicError(err).Error()
			if s.playlist != "" && s.failures < 3 && time.Since(s.refreshed) < 30*time.Second {
				return err
			}
		}
	}
	if s.mode == "web" && live.web != nil {
		webContext, cancel := context.WithTimeout(ctx, 12*time.Second)
		err := live.refreshWeb(webContext, channel, s)
		cancel()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.failures++
		s.errorText = publicError(err).Error()
		if s.playlist != "" && s.failures < 3 && time.Since(s.refreshed) < 30*time.Second {
			return err
		}
	}
	if s.mode == "jce" || (s.mode == "device" || s.mode == "web") && !channel.Backup {
		primaryContext, cancel := context.WithTimeout(ctx, 5*time.Second)
		address, err := live.timeshift(primaryContext, channel)
		if err == nil {
			var text string
			info := yspStreamInfo{Route: "jce"}
			text, err = live.playlistWithHeaders(context.WithValue(primaryContext, yspManifestInfoKey{}, &info), address, 0, live.client, map[string]string{"User-Agent": yspJCEUA})
			if err == nil {
				err = yspMergePlaylist(s, text, s.mode != "jce")
				if err == nil {
					s.info = info
				}
			}
		}
		cancel()
		if err == nil {
			s.mode = "jce"
			yspRegisterMedia(s, s.playlist, map[string]string{"User-Agent": yspJCEUA})
			yspPruneLiveResources(s)
			s.refreshed = time.Now()
			s.errorText = ""
			s.failures = 0
			return nil
		}
		s.failures++
		s.errorText = publicError(err).Error()
		if s.playlist != "" && s.failures < 3 && time.Since(s.refreshed) < 30*time.Second {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		backupContext, cancel := context.WithTimeout(ctx, 4*time.Second)
		if len(s.urls) == 0 || time.Since(s.urlTime) > 5*time.Minute || attempt > 0 {
			urls, err := live.backupURLs(backupContext, channel)
			if err != nil {
				last = err
				cancel()
				break
			}
			s.urls = urls
			s.urlTime = time.Now()
		}
		for _, address := range s.urls {
			info := yspStreamInfo{Route: "bk"}
			text, err := live.playlist(context.WithValue(backupContext, yspManifestInfoKey{}, &info), address, 0)
			if err == nil && len(yspParseSegments(text)) > 0 && !strings.Contains(text, "#EXT-X-ENDLIST") {
				err = yspMergePlaylist(s, text, s.mode != "bk")
				if err == nil {
					s.mode = "bk"
					s.info = info
					yspRegisterMedia(s, s.playlist, map[string]string{"User-Agent": yspUA, "Referer": "https://live.cctv.cn/"})
					yspPruneLiveResources(s)
					s.refreshed = time.Now()
					s.errorText = ""
					s.failures = 0
					cancel()
					return nil
				}
			}
			if err == nil {
				err = errors.New("央视频备用线路未提供直播分片")
			}
			last = err
			if ctx.Err() != nil {
				cancel()
				return ctx.Err()
			}
			if backupContext.Err() != nil {
				break
			}
		}
		cancel()
	}
	s.urls = nil
	if live.web != nil && s.mode != "web" && ctx.Err() == nil {
		webContext, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := live.refreshWeb(webContext, channel, s)
		cancel()
		if err == nil {
			return nil
		}
		last = err
	}
	if last == nil {
		last = errors.New("央视频暂无可用直播线路")
	}
	s.errorText = publicError(last).Error()
	return last
}
func (live *yspLiveServer) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	playlistRequest := len(parts) == 3 && parts[0] == "live" && parts[2] == "index.m3u8"
	mediaRequest := len(parts) == 4 && parts[0] == "live" && parts[2] == "media"
	if !playlistRequest && !mediaRequest {
		http.NotFound(w, r)
		return
	}
	live.mu.Lock()
	s := live.sessions[parts[1]]
	if s != nil {
		s.lastUsed = time.Now()
	}
	live.mu.Unlock()
	if s == nil {
		http.Error(w, "直播已结束", http.StatusGone)
		return
	}
	if mediaRequest {
		live.serveMedia(w, r, s, parts[3])
		return
	}
	if r.URL.RawQuery != "" {
		window, err := yspParseCatchup(r.URL.RawQuery, time.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if window != nil {
			live.serveCatchup(w, r, s, window)
			return
		}
	}
	s.mu.Lock()
	playlist, refreshed, catchup := s.playlist, s.refreshed, s.catchup != nil
	prefix, _ := r.Context().Value(yspGatewayPrefixKey{}).(string)
	playlist = yspProxyLivePlaylist(playlist, prefix+"/live/"+parts[1]+"/media/", s.media)
	s.mu.Unlock()
	if s.ctx.Err() != nil {
		http.Error(w, "直播已结束", http.StatusGone)
		return
	}
	if playlist == "" || !catchup && time.Since(refreshed) > 45*time.Second {
		http.Error(w, "直播清单刷新失败，请重新取流", http.StatusBadGateway)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	if r.Method == "GET" {
		fmt.Fprint(w, playlist)
	}
}
