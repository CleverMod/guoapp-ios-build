package core

import (
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type yspManifestInfoKey struct{}

var yspVariantResolution = regexp.MustCompile(`(?:^|,)RESOLUTION=(\d+)x(\d+)`)
var yspVariantBandwidth = regexp.MustCompile(`(?:^|,)(?:AVERAGE-)?BANDWIDTH=(\d+)`)

func yspReadManifestInfo(text string, info *yspStreamInfo) {
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#EXT-X-STREAM-INF:") {
			continue
		}
		line = strings.TrimPrefix(strings.TrimSpace(line), "#EXT-X-STREAM-INF:")
		if match := yspVariantResolution.FindStringSubmatch(line); len(match) == 3 {
			info.Width, _ = strconv.Atoi(match[1])
			info.Height, _ = strconv.Atoi(match[2])
		}
		if match := yspVariantBandwidth.FindStringSubmatch(line); len(match) == 2 {
			info.Bandwidth, _ = strconv.ParseInt(match[1], 10, 64)
		}
		return
	}
}

func yspStreamNumber(value any) int64 {
	switch value := value.(type) {
	case float64:
		if value >= 0 && value < 1<<40 {
			return int64(value)
		}
	case int:
		return int64(value)
	case json.Number:
		number, _ := value.Int64()
		return number
	case string:
		number, _ := strconv.ParseInt(value, 10, 64)
		return number
	}
	return 0
}

func (live *yspLiveServer) playbackInfo(token string, observed yspStreamInfo, report bool) (yspStreamInfo, error) {
	live.mu.Lock()
	session := live.sessions[token]
	live.mu.Unlock()
	if session == nil || session.ctx.Err() != nil {
		return yspStreamInfo{}, errors.New("直播会话已结束")
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if report {
		if observed.Width < 1 || observed.Width > 16384 || observed.Height < 1 || observed.Height > 16384 {
			return yspStreamInfo{}, errors.New("播放器分辨率信息无效")
		}
		session.format.Width, session.format.Height, session.format.Decoder = observed.Width, observed.Height, true
	}
	info := session.info
	if session.format.Decoder {
		info.Width, info.Height, info.Decoder = session.format.Width, session.format.Height, true
	}
	info.Route = session.mode
	return info, nil
}

func yspSessionFresh(session *yspDeviceSession, now time.Time) bool {
	return session != nil && !session.created.IsZero() && now.Sub(session.created) < yspDeviceSessionTTL &&
		!session.lastBeat.IsZero() && now.Sub(session.lastBeat) < time.Minute
}
