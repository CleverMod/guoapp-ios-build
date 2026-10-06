package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const yspEPGURL = "https://live.fanmingming.com/e.xml"

type yspCatchupRange struct {
	start int64
	end   int64
}

func yspLiveChannels() []yspChannel {
	channels := append([]yspChannel(nil), yspChannels...)
	for index := range channels {
		channel := &channels[index]
		channel.EPGID, channel.EPGURL = yspEPGIDs[channel.ID], yspEPGURL
		if yspCatchupSupported[channel.ID] {
			channel.CatchupDays = 7
		}
		switch {
		case strings.HasPrefix(channel.ID, "cctv") || strings.HasPrefix(channel.ID, "cgtn"):
			channel.Group = "央视"
		default:
			channel.Group = "卫视"
		}
	}
	return channels
}

func yspCatchupTime(value string) (int64, error) {
	value = strings.TrimSpace(value)
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return 0, errors.New("回看时间格式无效")
		}
	}
	if len(value) == 10 || len(value) == 13 {
		timestamp, err := strconv.ParseInt(value, 10, 64)
		if len(value) == 13 {
			timestamp /= 1000
		}
		return timestamp, err
	}
	layout := "20060102150405"
	if len(value) == 12 {
		layout = "200601021504"
	} else if len(value) != 14 {
		return 0, errors.New("回看时间格式无效")
	}
	parsed, err := time.ParseInLocation(layout, value, time.FixedZone("CST", 8*60*60))
	return parsed.Unix(), err
}

func yspParseCatchup(query string, now time.Time) (*yspCatchupRange, error) {
	values, err := url.ParseQuery(query)
	if err != nil {
		return nil, errors.New("回看参数无效")
	}
	start, end := "", ""
	if seek, exists := values["playseek"]; exists {
		start, end, _ = strings.Cut(seek[0], "-")
	}
	for _, name := range []string{"start", "utc", "starttime"} {
		if start == "" {
			start = values.Get(name)
		}
	}
	for _, name := range []string{"end", "lutc", "endtime"} {
		if end == "" {
			end = values.Get(name)
		}
	}
	if start == "" {
		for _, name := range []string{"playseek", "start", "utc", "starttime", "end", "lutc", "endtime"} {
			if _, exists := values[name]; exists {
				return nil, errors.New("请选择回看开始时间")
			}
		}
		return nil, nil
	}
	first, err := yspCatchupTime(start)
	if err != nil || first < now.Add(-7*24*time.Hour).Unix() || first >= now.Unix() {
		return nil, errors.New("请选择过去七天内的回看时间")
	}
	last := min(first+7200, now.Unix())
	if end != "" {
		last, err = yspCatchupTime(end)
		if err != nil || last <= first || last > now.Unix() {
			return nil, errors.New("回看结束时间须晚于开始时间且不能超过当前时间")
		}
	}
	return &yspCatchupRange{start: first, end: last}, nil
}

func (live *yspLiveServer) catchupPlaylist(ctx context.Context, channel yspChannel, window *yspCatchupRange) (string, error) {
	if !yspCatchupSupported[channel.ID] {
		return "", errors.New("此频道暂不支持回看")
	}
	address, err := live.timeshiftRange(ctx, channel, window.start, window.end)
	if err != nil {
		return "", err
	}
	playlist, err := live.playlistWithHeaders(ctx, address, 0, live.client, map[string]string{"User-Agent": yspJCEUA})
	if err != nil {
		return "", err
	}
	if len(yspParseSegments(playlist)) == 0 {
		return "", errors.New("此时段暂无可用回看分片")
	}
	if !strings.Contains(playlist, "#EXT-X-ENDLIST") {
		playlist += "\n#EXT-X-ENDLIST\n"
	}
	return playlist, nil
}

func (live *yspLiveServer) serveCatchup(w http.ResponseWriter, r *http.Request, session *yspLiveSession, window *yspCatchupRange) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	detach := context.AfterFunc(session.ctx, cancel)
	defer cancel()
	defer detach()
	playlist, err := live.catchupPlaylist(ctx, session.channel, window)
	if err != nil {
		http.Error(w, "此频道暂不支持该时段回看", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	if r.Method == http.MethodGet {
		fmt.Fprint(w, playlist)
	}
}

var yspEPGIDs = map[string]string{
	"cctv1":    "CCTV1",
	"cctv2":    "CCTV2",
	"cctv3":    "CCTV3",
	"cctv4":    "CCTV4",
	"cctv5":    "CCTV5",
	"cctv5p":   "CCTV5+",
	"cctv6":    "CCTV6",
	"cctv7":    "CCTV7",
	"cctv8":    "CCTV8",
	"cctv9":    "CCTV9",
	"cctv10":   "CCTV10",
	"cctv11":   "CCTV11",
	"cctv12":   "CCTV12",
	"cctv13":   "CCTV13",
	"cctv14":   "CCTV14",
	"cctv15":   "CCTV15",
	"cctv16":   "CCTV16",
	"cctv164k": "CCTV16",
	"cctv17":   "CCTV17",
	"cctv4k":   "CCTV4K",
	"cctv8k":   "CCTV-8K",
	"cgtn":     "CGTN英语",
	"cgtnfr":   "CGTN法语",
	"cgtnru":   "CGTN俄语",
	"cgtnar":   "CGTN阿语",
	"cgtnes":   "CGTN西语",
	"cgtndoc":  "CGTN纪录",
	"cctvdyjc": "CCTV第一剧场",
	"cctvfyjc": "CCTV风云剧场",
	"cctvhjjc": "CCTV怀旧剧场",
	"cetv1":    "CETV1",
	"guoxue":   "国学",
	"bjws":     "北京卫视",
	"jsws":     "江苏卫视",
	"dfws":     "东方卫视",
	"zjws":     "浙江卫视",
	"hnws":     "湖南卫视",
	"hbws":     "湖北卫视",
	"gdws":     "广东卫视",
	"gxws":     "广西卫视",
	"hljws":    "黑龙江卫视",
	"hainanws": "海南卫视",
	"cqws":     "重庆卫视",
	"szws":     "深圳卫视",
	"scws":     "四川卫视",
	"henanws":  "河南卫视",
	"dnws":     "东南卫视",
	"gzws":     "贵州卫视",
	"jxws":     "江西卫视",
	"lnws":     "辽宁卫视",
	"ahws":     "安徽卫视",
	"hebws":    "河北卫视",
	"sdws":     "山东卫视",
	"tjws":     "天津卫视",
	"jlws":     "吉林卫视",
	"saxws":    "陕西卫视",
	"nxws":     "宁夏卫视",
	"nmgws":    "内蒙古卫视",
	"ynws":     "云南卫视",
	"shanxiws": "山西卫视",
	"qhws":     "青海卫视",
	"xizangws": "西藏卫视",
	"xjws":     "新疆卫视",
	"gsws":     "甘肃卫视",
}

var yspCatchupSupported = map[string]bool{
	"ahws":     true,
	"bjws":     true,
	"cctv1":    true,
	"cctv10":   true,
	"cctv13":   true,
	"cctv2":    true,
	"cctv3":    true,
	"cctv4":    true,
	"cctv5":    true,
	"cctv5p":   true,
	"cctv6":    true,
	"cctv7":    true,
	"cctv8":    true,
	"cctv8k":   true,
	"cctv9":    true,
	"cetv1":    true,
	"cgtn":     true,
	"cgtnar":   true,
	"cgtndoc":  true,
	"cgtnes":   true,
	"cgtnfr":   true,
	"cgtnru":   true,
	"cqws":     true,
	"dfws":     true,
	"dnws":     true,
	"gdws":     true,
	"gsws":     true,
	"guoxue":   true,
	"gxws":     true,
	"gzws":     true,
	"hainanws": true,
	"hbws":     true,
	"hebws":    true,
	"henanws":  true,
	"hljws":    true,
	"hnws":     true,
	"jlws":     true,
	"jsws":     true,
	"jxws":     true,
	"lnws":     true,
	"nmgws":    true,
	"nxws":     true,
	"qhws":     true,
	"saxws":    true,
	"scws":     true,
	"sdws":     true,
	"shanxiws": true,
	"szws":     true,
	"tjws":     true,
	"xizangws": true,
	"xjws":     true,
	"ynws":     true,
	"zjws":     true,
}
