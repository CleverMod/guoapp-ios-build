package core

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

type yspLiveResource struct {
	address string
	headers map[string]string
	expires time.Time
}

func newYSPSignedMediaClient(transport *http.Transport) *http.Client {
	mediaTransport := transport.Clone()
	mediaTransport.ForceAttemptHTTP2 = false
	mediaTransport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	return &http.Client{Transport: mediaTransport, Timeout: 20 * time.Second, CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("央视频分片重定向次数过多")
		}
		if len(via) > 0 && !strings.EqualFold(request.URL.Host, via[0].URL.Host) {
			for _, header := range []string{"UID", "APPID", "APPSIGN", "APPRANDOMSTR"} {
				request.Header.Del(header)
			}
		}
		return nil
	}}
}

func yspLiveResourceID(address string) string {
	digest := sha256.Sum256([]byte(address))
	return hex.EncodeToString(digest[:])
}

func yspVisitPlaylistURLs(text string, visit func(string)) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			visit(line)
		} else {
			for _, match := range yspPlaylistURI.FindAllStringSubmatch(line, -1) {
				visit(match[1])
			}
		}
	}
}

func yspPruneLiveResources(state *yspLiveState) {
	if len(state.media) == 0 {
		return
	}
	retained := map[string]yspLiveResource{}
	now := time.Now()
	for id, resource := range state.media {
		if now.Before(resource.expires) {
			retained[id] = resource
		}
	}
	yspVisitPlaylistURLs(state.playlist, func(address string) {
		id := yspLiveResourceID(address)
		if resource, exists := state.media[id]; exists && resource.address == address {
			retained[id] = resource
		}
	})
	state.media = retained
}

func yspProxyLivePlaylist(text, prefix string, resources map[string]yspLiveResource) string {
	if len(resources) == 0 {
		return text
	}
	proxy := func(address string) string {
		id := yspLiveResourceID(address)
		if resource, exists := resources[id]; exists && resource.address == address {
			return prefix + id
		}
		return address
	}
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		stripped := strings.TrimSpace(line)
		if stripped != "" && !strings.HasPrefix(stripped, "#") {
			lines[index] = proxy(stripped)
		} else if strings.Contains(line, `URI="`) {
			lines[index] = yspPlaylistURI.ReplaceAllStringFunc(line, func(match string) string {
				address := yspPlaylistURI.FindStringSubmatch(match)[1]
				return `URI="` + proxy(address) + `"`
			})
		}
	}
	return strings.Join(lines, "\n")
}

func (live *yspLiveServer) refreshDevice(ctx context.Context, channel yspChannel, state *yspLiveState) error {
	entry, err := live.device.resolve(ctx, channel)
	if err != nil {
		return err
	}
	client := live.mediaClient
	if client == nil {
		client = live.client
	}
	text, err := live.playlistWithHeaders(ctx, entry.address, 0, client, entry.headers)
	if err == nil && strings.Contains(text, "#EXT-X-ENDLIST") {
		err = errors.New("央视频设备线路未提供直播清单")
	}
	if err == nil {
		err = yspMergePlaylist(state, text, state.mode != "device")
	}
	if err != nil {
		if !errors.Is(ctx.Err(), context.Canceled) && !errors.Is(err, context.Canceled) {
			live.device.reject(channel, entry)
		}
		return err
	}
	if state.media == nil {
		state.media = map[string]yspLiveResource{}
	}
	headers := yspDeviceCopyHeaders(entry.headers)
	yspVisitPlaylistURLs(text, func(address string) {
		if isProviderHTTPMediaURL(address) {
			state.media[yspLiveResourceID(address)] = yspLiveResource{address: address, headers: headers, expires: time.Now().Add(2 * time.Minute)}
		}
	})
	yspPruneLiveResources(state)
	state.mode, state.errorText, state.failures, state.refreshed = "device", "", 0, time.Now()
	live.device.accept(channel, entry)
	return nil
}

func (live *yspLiveServer) serveMedia(w http.ResponseWriter, r *http.Request, session *yspLiveSession, id string) {
	if session.ctx.Err() != nil {
		http.Error(w, "直播已结束", http.StatusGone)
		return
	}
	session.mu.Lock()
	resource, exists := session.media[id]
	session.mu.Unlock()
	if !exists {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	detach := context.AfterFunc(session.ctx, cancel)
	defer cancel()
	defer detach()
	request, err := http.NewRequestWithContext(ctx, r.Method, resource.address, nil)
	if err != nil {
		http.Error(w, "直播分片地址无效", http.StatusBadGateway)
		return
	}
	for key, value := range resource.headers {
		request.Header.Set(key, value)
	}
	for _, key := range []string{"Range", "If-Range"} {
		if value := r.Header.Get(key); value != "" {
			request.Header.Set(key, value)
		}
	}
	client := live.mediaClient
	if client == nil {
		client = live.client
	}
	response, err := client.Do(request)
	if err != nil {
		http.Error(w, "直播分片请求失败", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	for _, key := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
		if value := response.Header.Get(key); value != "" {
			w.Header().Set(key, value)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(response.StatusCode)
	if r.Method == "GET" {
		io.CopyBuffer(w, response.Body, make([]byte, 256<<10))
	}
}
