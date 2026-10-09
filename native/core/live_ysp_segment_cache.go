package core

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const yspSegmentCacheItemLimit = 35 << 20

type yspCachedMedia struct {
	key     string
	body    []byte
	headers http.Header
	expires time.Time
}

type yspSegmentCacheStats struct {
	Entries  int     `json:"entries"`
	Bytes    int64   `json:"bytes"`
	LimitMB  int     `json:"limitMB"`
	Hits     uint64  `json:"hits"`
	Misses   uint64  `json:"misses"`
	HitRate  float64 `json:"hitRate"`
	Inflight int     `json:"inflight"`
}

type yspSegmentCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	order   *list.List
	flights map[string]chan struct{}
	bytes   int64
	limit   int64
	hits    uint64
	misses  uint64
}

func newYSPSegmentCache(limitMB int) *yspSegmentCache {
	return &yspSegmentCache{entries: map[string]*list.Element{}, order: list.New(), flights: map[string]chan struct{}{}, limit: int64(limitMB) << 20}
}

func yspSegmentCacheKey(resource yspLiveResource) string {
	keys := make([]string, 0, len(resource.headers))
	for key := range resource.headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var identity strings.Builder
	identity.WriteString(resource.address)
	for _, key := range keys {
		identity.WriteByte(0)
		identity.WriteString(strings.ToLower(key))
		identity.WriteByte(0)
		identity.WriteString(resource.headers[key])
	}
	digest := sha256.Sum256([]byte(identity.String()))
	return hex.EncodeToString(digest[:])
}

func (cache *yspSegmentCache) remove(element *list.Element) {
	entry := element.Value.(yspCachedMedia)
	delete(cache.entries, entry.key)
	cache.order.Remove(element)
	cache.bytes -= int64(len(entry.body))
}

func (cache *yspSegmentCache) get(key string) (yspCachedMedia, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.limit == 0 {
		return yspCachedMedia{}, false
	}
	if element, exists := cache.entries[key]; exists {
		entry := element.Value.(yspCachedMedia)
		if time.Now().Before(entry.expires) {
			cache.hits++
			entry.headers = entry.headers.Clone()
			return entry, true
		}
		cache.remove(element)
	}
	cache.misses++
	return yspCachedMedia{}, false
}

func (cache *yspSegmentCache) put(key string, body []byte, headers http.Header) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(body) == 0 || len(body) > yspSegmentCacheItemLimit || int64(len(body)) > cache.limit {
		return
	}
	if previous, exists := cache.entries[key]; exists {
		cache.remove(previous)
	}
	for element := cache.order.Front(); element != nil; {
		next := element.Next()
		if time.Now().After(element.Value.(yspCachedMedia).expires) {
			cache.remove(element)
		}
		element = next
	}
	for cache.bytes+int64(len(body)) > cache.limit || len(cache.entries) >= 4096 {
		cache.remove(cache.order.Front())
	}
	entry := yspCachedMedia{key: key, body: body, headers: headers.Clone(), expires: time.Now().Add(180 * time.Second)}
	cache.entries[key] = cache.order.PushBack(entry)
	cache.bytes += int64(len(body))
}

func (cache *yspSegmentCache) begin(key string) (chan struct{}, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.limit == 0 {
		return nil, true
	}
	if pending, exists := cache.flights[key]; exists {
		return pending, false
	}
	done := make(chan struct{})
	cache.flights[key] = done
	return done, true
}

func (cache *yspSegmentCache) finish(key string, done chan struct{}) {
	if done == nil {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.flights[key] == done {
		delete(cache.flights, key)
		close(done)
	}
}

func (cache *yspSegmentCache) resize(limitMB int) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.limit = int64(limitMB) << 20
	for cache.bytes > cache.limit && cache.order.Len() > 0 {
		cache.remove(cache.order.Front())
	}
	if cache.limit == 0 {
		cache.entries, cache.order, cache.bytes = map[string]*list.Element{}, list.New(), 0
	}
}

func (cache *yspSegmentCache) clear() {
	cache.mu.Lock()
	cache.entries, cache.order, cache.bytes = map[string]*list.Element{}, list.New(), 0
	cache.hits, cache.misses = 0, 0
	cache.mu.Unlock()
}

func (cache *yspSegmentCache) stats() yspSegmentCacheStats {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	stats := yspSegmentCacheStats{Entries: len(cache.entries), Bytes: cache.bytes, LimitMB: int(cache.limit >> 20), Hits: cache.hits, Misses: cache.misses, Inflight: len(cache.flights)}
	if total := stats.Hits + stats.Misses; total > 0 {
		stats.HitRate = float64(stats.Hits) * 100 / float64(total)
	}
	return stats
}

type yspCachingWriter struct {
	destination http.ResponseWriter
	body        []byte
	limit       int
	complete    bool
}

func (writer *yspCachingWriter) Write(data []byte) (int, error) {
	n, err := writer.destination.Write(data)
	if writer.complete {
		if err != nil || n != len(data) || len(writer.body)+n > writer.limit {
			writer.body, writer.complete = nil, false
		} else {
			writer.body = append(writer.body, data...)
		}
	}
	return n, err
}

func yspCacheResponseHeaders(response *http.Response) http.Header {
	headers := make(http.Header)
	for _, key := range []string{"Content-Type", "Content-Length", "Accept-Ranges", "ETag", "Last-Modified"} {
		if value := response.Header.Get(key); value != "" {
			headers.Set(key, value)
		}
	}
	return headers
}

func yspCachedLengthValid(headers http.Header, body []byte) bool {
	if value := headers.Get("Content-Length"); value != "" {
		length, err := strconv.ParseInt(value, 10, 64)
		return err == nil && length == int64(len(body))
	}
	return true
}
