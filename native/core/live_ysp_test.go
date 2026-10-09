package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func yspFixturePlaylist(first, last int, route, query string) string {
	var text strings.Builder
	fmt.Fprintf(&text, "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:%d\n", first)
	for index := first; index <= last; index++ {
		fmt.Fprintf(&text, "#EXTINF:2,\nhttps://media.test/%s/%d.ts?token=%s\n", route, index, query)
	}
	return text.String() + "#EXT-X-ENDLIST\n"
}

func TestYSPPlaylistDoesNotReplayEvictedHistory(t *testing.T) {
	state := &yspLiveState{}
	for end := 100; end <= 180; end += 8 {
		if err := yspMergePlaylist(state, yspFixturePlaylist(1, end, "main", fmt.Sprint(end)), false); err != nil {
			t.Fatal(err)
		}
		if state.sequence != int64(end) {
			t.Fatalf("historical segments were appended again: sequence=%d want=%d", state.sequence, end)
		}
		if !strings.Contains(state.playlist, fmt.Sprintf("#EXT-X-MEDIA-SEQUENCE:%d\n", end-14)) {
			t.Fatalf("playlist sequence did not advance with its window: %s", state.playlist)
		}
		if strings.Contains(state.playlist, "#EXT-X-ENDLIST") || strings.Contains(state.playlist, "#EXT-X-DISCONTINUITY\n") {
			t.Fatal("overlapping snapshots must stay on the same live timeline")
		}
		for _, segment := range state.segments {
			if !strings.HasSuffix(segment.address, "token="+fmt.Sprint(end)) {
				t.Fatal("overlapping segment addresses did not renew their signatures")
			}
		}
	}
	before := state.sequence
	if err := yspMergePlaylist(state, yspFixturePlaylist(140, 170, "main", "stale"), false); err != nil {
		t.Fatal(err)
	}
	if state.sequence != before || !strings.Contains(state.playlist, "/179.ts?") {
		t.Fatal("a delayed snapshot rewound the live edge")
	}
}

func TestYSPRouteChangeKeepsSequenceAndDiscontinuity(t *testing.T) {
	state := &yspLiveState{}
	for index, snapshot := range []struct {
		first, last int
		route       string
		changed     bool
	}{
		{1, 6, "main", false},
		{1000, 1005, "backup", true},
		{1003, 1008, "backup", false},
		{1006, 1011, "backup", false},
	} {
		if err := yspMergePlaylist(state, yspFixturePlaylist(snapshot.first, snapshot.last, snapshot.route, "valid"), snapshot.changed); err != nil {
			t.Fatal(err)
		}
		if index == 1 && (state.sequence != 9 || !strings.Contains(state.playlist, "#EXT-X-DISCONTINUITY\n")) {
			t.Fatal("route change must append the new live edge and mark its timestamp boundary")
		}
	}
	if state.sequence != 15 || !strings.Contains(state.playlist, "#EXT-X-MEDIA-SEQUENCE:10\n") ||
		!strings.Contains(state.playlist, "#EXT-X-DISCONTINUITY-SEQUENCE:1\n") ||
		strings.Contains(state.playlist, "#EXT-X-DISCONTINUITY\n") {
		t.Fatalf("sliding past a route boundary lost the discontinuity sequence: %s", state.playlist)
	}
}

func TestYSPRemappedSequencesKeepEncryptionIV(t *testing.T) {
	text := "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:410\n#EXT-X-KEY:METHOD=AES-128,URI=\"https://media.test/key\"\n" +
		"#EXTINF:6,\nhttps://media.test/first.ts\n#EXTINF:6,\nhttps://media.test/second.ts\n"
	state := &yspLiveState{}
	if err := yspMergePlaylist(state, text, false); err != nil {
		t.Fatal(err)
	}
	for _, sequence := range []int{410, 411} {
		if !strings.Contains(state.playlist, fmt.Sprintf("IV=0x%032x", sequence)) {
			t.Fatal("implicit upstream AES IV was changed by the local media sequence")
		}
	}
	if err := yspMergePlaylist(state, yspFixturePlaylist(1, 6, "unencrypted", "valid"), true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(state.playlist, "#EXT-X-KEY:METHOD=NONE") {
		t.Fatal("an unencrypted route inherited the previous route's key")
	}
}

func TestYSPTargetDurationStaysStable(t *testing.T) {
	state := &yspLiveState{}
	initial := strings.ReplaceAll(yspFixturePlaylist(1, 6, "main", "first"), "DURATION:6", "DURATION:8")
	if err := yspMergePlaylist(state, initial, false); err != nil {
		t.Fatal(err)
	}
	if err := yspMergePlaylist(state, yspFixturePlaylist(4, 9, "main", "second"), false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(state.playlist, "#EXT-X-TARGETDURATION:8\n") {
		t.Fatal("target duration shrank when the source window changed")
	}
	before := state.playlist
	invalid := strings.ReplaceAll(yspFixturePlaylist(7, 12, "main", "longer"), "#EXTINF:2,", "#EXTINF:12,")
	if err := yspMergePlaylist(state, invalid, false); err == nil || state.playlist != before {
		t.Fatal("an incompatible snapshot changed a published playlist")
	}
}

func TestYSPByteRangesKeepOffsetsWhenTheWindowSlides(t *testing.T) {
	text := "#EXTM3U\n#EXTINF:6,\n#EXT-X-BYTERANGE:10@100\nhttps://media.test/shared.ts\n" +
		"#EXTINF:6,\n#EXT-X-BYTERANGE:20\nhttps://media.test/shared.ts\n" +
		"#EXTINF:6,\n#EXT-X-BYTERANGE:20\nhttps://media.test/shared.ts\n"
	segments := yspParseSegments(text)
	if len(segments) != 3 || segments[1].key == segments[2].key {
		t.Fatal("different byte ranges were identified as the same segment")
	}
	state := &yspLiveState{}
	if err := yspMergePlaylist(state, text, false); err != nil {
		t.Fatal(err)
	}
	for _, byteRange := range []string{"10@100", "20@110", "20@130"} {
		if !strings.Contains(state.playlist, "#EXT-X-BYTERANGE:"+byteRange+"\n") {
			t.Fatal("a retained segment depends on an evicted byte-range offset")
		}
	}
}

type yspFixtureTransport func(*http.Request) (*http.Response, error)

func (transport yspFixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestYSPTransientRefreshFailureKeepsPrimaryRoute(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := &yspLiveSession{
		ctx: ctx, cancel: cancel,
		yspLiveState: yspLiveState{mode: "jce", playlist: "#EXTM3U\n", refreshed: time.Now()},
	}
	requests := 0
	live := &yspLiveServer{guid: strings.Repeat("0", 32), client: &http.Client{Transport: yspFixtureTransport(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("temporary upstream timeout")
	})}}
	if err := live.refresh(ctx, session); err == nil {
		t.Fatal("expected a refresh error")
	}
	if requests != 1 || session.mode != "jce" || session.playlist == "" || session.failures != 1 {
		t.Fatal("a single upstream error discarded the active route")
	}
}

func TestYSPPlaylistReadsDoNotWaitForUpstream(t *testing.T) {
	life, stop := context.WithCancel(context.Background())
	defer stop()
	session := &yspLiveSession{
		ctx: life, cancel: stop,
		yspLiveState: yspLiveState{mode: "jce", playlist: "#EXTM3U\n", refreshed: time.Now()},
	}
	started := make(chan struct{})
	live := &yspLiveServer{
		guid: strings.Repeat("0", 32), sessions: map[string]*yspLiveSession{"fixture": session},
		client: &http.Client{Transport: yspFixtureTransport(func(request *http.Request) (*http.Response, error) {
			close(started)
			<-request.Context().Done()
			return nil, request.Context().Err()
		})},
	}
	ctx, cancel := context.WithCancel(life)
	defer cancel()
	finished := make(chan struct{})
	go func() {
		live.refresh(ctx, session)
		close(finished)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("upstream refresh did not start")
	}
	response := httptest.NewRecorder()
	served := make(chan struct{})
	go func() {
		live.serve(response, httptest.NewRequest(http.MethodGet, "/live/fixture/index.m3u8", nil))
		close(served)
	}()
	select {
	case <-served:
	case <-time.After(time.Second):
		cancel()
		<-finished
		t.Fatal("reading the cached playlist waited for upstream networking")
	}
	if response.Code != http.StatusOK || response.Body.String() != "#EXTM3U\n" {
		t.Fatal("cached playlist was unavailable during a refresh")
	}
	cancel()
	<-finished
}
