package core

import "time"

const yspLiveCacheTTL = 15 * time.Second

func yspCopyLiveState(state yspLiveState) yspLiveState {
	state.urls = append([]string(nil), state.urls...)
	segments := append([]yspSegment(nil), state.segments...)
	for index := range segments {
		segments[index].tags = append([]string(nil), segments[index].tags...)
	}
	state.segments = segments
	media := make(map[string]yspLiveResource, len(state.media))
	for id, resource := range state.media {
		resource.headers = yspDeviceCopyHeaders(resource.headers)
		media[id] = resource
	}
	state.media = media
	return state
}

func (live *yspLiveServer) remember(channel yspChannel, state yspLiveState) {
	if state.catchup != nil || state.playlist == "" {
		return
	}
	live.mu.Lock()
	defer live.mu.Unlock()
	if live.cache == nil {
		live.cache = map[string]yspLiveState{}
	}
	for id, cached := range live.cache {
		if time.Since(cached.refreshed) >= yspLiveCacheTTL {
			delete(live.cache, id)
		}
	}
	if cached, exists := live.cache[channel.ID]; !exists || state.refreshed.After(cached.refreshed) {
		live.cache[channel.ID] = yspCopyLiveState(state)
	}
}

func (live *yspLiveServer) forget(channel yspChannel) {
	live.mu.Lock()
	delete(live.cache, channel.ID)
	live.mu.Unlock()
}
