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
	ID         string `json:"id"`
	Name       string `json:"name"`
	Group      string `json:"group"`
	SID        string `json:"-"`
	PID        string `json:"-"`
	Definition string `json:"-"`
	Backup     bool   `json:"-"`
}
type yspSegment struct {
	key, address string
	tags         []string
	duration     float64
	sequence     int64
}
type yspLiveSession struct {
	mu        sync.Mutex
	channel   yspChannel
	ctx       context.Context
	cancel    context.CancelFunc
	mode      string
	urls      []string
	urlTime   time.Time
	playlist  string
	refreshed time.Time
	attempted time.Time
	segments  []yspSegment
	sequence  int64
	lastUsed  time.Time
	errorText string
}
type yspLiveServer struct {
	mu       sync.Mutex
	client   *http.Client
	guid     string
	sessions map[string]*yspLiveSession
	server   *http.Server
	address  string
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
	engine.live = &yspLiveServer{client: &http.Client{Transport: transport, Timeout: 20 * time.Second}, guid: hex.EncodeToString(random), sessions: map[string]*yspLiveSession{}}
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
	go func() {
		srv.Serve(listener)
		live.mu.Lock()
		if live.server == srv {
			live.server = nil
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
			if time.Since(s.lastUsed) > 2*time.Minute {
				s.cancel()
				delete(live.sessions, token)
			}
		}
		if len(live.sessions) == 0 {
			live.server = nil
			live.mu.Unlock()
			srv.Close()
			return
		}
		live.mu.Unlock()
	}
}
func (live *yspLiveServer) open(ctx context.Context, slug string) (nativePlan, error) {
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
	s := &yspLiveSession{channel: channel, ctx: life, cancel: cancel, mode: mode, lastUsed: time.Now()}
	live.mu.Lock()
	if len(live.sessions) >= 4 {
		live.mu.Unlock()
		cancel()
		return nativePlan{}, errors.New("直播会话过多，请关闭旧播放页后重试")
	}
	if err := live.ensureServingLocked(); err != nil {
		live.mu.Unlock()
		cancel()
		return nativePlan{}, err
	}
	live.sessions[token] = s
	address := live.address
	live.mu.Unlock()
	fetch, stop := context.WithCancel(ctx)
	detach := context.AfterFunc(life, stop)
	s.mu.Lock()
	err = live.refresh(fetch, s)
	s.mu.Unlock()
	detach()
	stop()
	if err != nil {
		live.release(token)
		return nativePlan{}, err
	}
	live.mu.Lock()
	s.lastUsed = time.Now()
	live.mu.Unlock()
	return nativePlan{URL: address + "/live/" + token + "/index.m3u8", Session: token, Quality: 1080, Qualities: []int{1080}, Headers: map[string]string{"User-Agent": yspUA, "Referer": "https://live.cctv.cn/"}, RouteCount: 1}, nil
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
	if depth > 3 {
		return "", errors.New("央视频清单嵌套过深")
	}
	b, final, err := live.request(ctx, "GET", address, nil, map[string]string{"User-Agent": yspUA, "Referer": "https://live.cctv.cn/", "Accept": "application/vnd.apple.mpegurl,*/*"})
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(strings.TrimPrefix(string(b), "\ufeff"))
	if !strings.HasPrefix(text, "#EXTM3U") {
		return "", errors.New("央视频未返回有效直播清单")
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
				return live.playlist(ctx, address, depth+1)
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
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
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
			tags = append(tags, line)
		case line == "#EXT-X-DISCONTINUITY":
			tags = append(tags, line)
		case line != "" && !strings.HasPrefix(line, "#"):
			identity := line
			if u, err := url.Parse(line); err == nil {
				u.RawQuery = ""
				u.Fragment = ""
				identity = u.String()
			}
			if pdt != "" {
				identity = "pdt:" + pdt
			}
			identity += byteRange
			prefix := []string{}
			if keyTag != "" {
				prefix = append(prefix, keyTag)
			}
			if mapTag != "" {
				prefix = append(prefix, mapTag)
			}
			prefix = append(prefix, tags...)
			segments = append(segments, yspSegment{key: identity, address: line, tags: prefix, duration: duration})
			tags = nil
			pdt = ""
			byteRange = ""
		}
	}
	return segments
}
func yspMergePlaylist(s *yspLiveSession, text string) error {
	fresh := yspParseSegments(text)
	if len(fresh) == 0 {
		return errors.New("央视频直播清单暂无分片")
	}
	positions := map[string]int{}
	for i, seg := range s.segments {
		positions[seg.key] = i
	}
	for _, seg := range fresh {
		if i, ok := positions[seg.key]; ok {
			seg.sequence = s.segments[i].sequence
			s.segments[i] = seg
		} else {
			s.sequence++
			seg.sequence = s.sequence
			positions[seg.key] = len(s.segments)
			s.segments = append(s.segments, seg)
		}
	}
	if len(s.segments) > 60 {
		s.segments = append([]yspSegment(nil), s.segments[len(s.segments)-60:]...)
	}
	window := s.segments
	if len(window) > 30 {
		window = window[len(window)-30:]
	}
	target := 6.0
	for _, seg := range window {
		target = math.Max(target, math.Ceil(seg.duration))
	}
	var out strings.Builder
	fmt.Fprintf(&out, "#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-TARGETDURATION:%.0f\n#EXT-X-MEDIA-SEQUENCE:%d\n", target, window[0].sequence)
	for _, seg := range window {
		for _, tag := range seg.tags {
			out.WriteString(tag + "\n")
		}
		out.WriteString(seg.address + "\n")
	}
	s.playlist = out.String()
	return nil
}
func (live *yspLiveServer) refresh(ctx context.Context, s *yspLiveSession) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.attempted = time.Now()
	if s.mode == "jce" {
		address, err := live.timeshift(ctx, s.channel)
		if err == nil {
			var text string
			text, err = live.playlist(ctx, address, 0)
			if err == nil {
				err = yspMergePlaylist(s, text)
			}
		}
		if err == nil {
			s.refreshed = time.Now()
			s.errorText = ""
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.mode = "bk"
		s.segments = nil
	}
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		if len(s.urls) == 0 || time.Since(s.urlTime) > 10*time.Minute || attempt > 0 {
			urls, err := live.backupURLs(ctx, s.channel)
			if err != nil {
				last = err
				break
			}
			s.urls = urls
			s.urlTime = time.Now()
		}
		for _, address := range s.urls {
			text, err := live.playlist(ctx, address, 0)
			if err == nil && len(yspParseSegments(text)) > 0 && !strings.Contains(text, "#EXT-X-ENDLIST") {
				s.playlist = text
				s.refreshed = time.Now()
				s.errorText = ""
				return nil
			}
			if err == nil {
				err = errors.New("央视频备用线路未提供直播分片")
			}
			last = err
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
	s.urls = nil
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
	if len(parts) != 3 || parts[0] != "live" || parts[2] != "index.m3u8" {
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
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		http.Error(w, "直播已结束", http.StatusGone)
		return
	}
	interval := 15 * time.Second
	if s.mode == "bk" {
		interval = 3 * time.Second
	}
	if time.Since(s.refreshed) >= interval && time.Since(s.attempted) >= 3*time.Second {
		live.refresh(ctx, s)
	}
	if s.playlist == "" || time.Since(s.refreshed) > 45*time.Second {
		http.Error(w, "直播清单刷新失败，请重新取流", http.StatusBadGateway)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	if r.Method == "GET" {
		fmt.Fprint(w, s.playlist)
	}
}
