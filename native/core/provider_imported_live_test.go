package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type importedLiveResult struct {
	Source      string `json:"source"`
	Categories  int    `json:"categories"`
	Catalog     int    `json:"catalog"`
	Search      int    `json:"search"`
	Chapters    int    `json:"chapters"`
	MediaHost   string `json:"mediaHost,omitempty"`
	MediaStatus int    `json:"mediaStatus,omitempty"`
	Playlist    bool   `json:"playlist"`
	Playable    bool   `json:"playable"`
	Error       string `json:"error,omitempty"`
}

func TestImportedLiveSourceProtocols(t *testing.T) {
	if os.Getenv("CHECK_IMPORTED_PROVIDERS") != "true" {
		t.Skip("set CHECK_IMPORTED_PROVIDERS=true to check source text APIs and media HEAD")
	}
	engine, err := newNativeEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.downloads.close)
	d := engine.downloader
	results := map[string]importedLiveResult{}
	var mu sync.Mutex
	t.Cleanup(func() {
		if target := os.Getenv("IMPORTED_DIAGNOSTICS_OUTPUT"); target != "" {
			body, _ := json.MarshalIndent(results, "", "  ")
			_ = os.MkdirAll(filepath.Dir(target), 0755)
			if err := os.WriteFile(target, body, 0600); err != nil {
				t.Error(err)
			}
		}
	})
	for _, source := range []string{"xiaopingguo", "luoxue", "xiaobao", "jumi", "nnvideo"} {
		t.Run(source, func(t *testing.T) {
			t.Parallel()
			result := importedLiveResult{Source: source}
			defer func() { mu.Lock(); results[source] = result; mu.Unlock() }()
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
			defer cancel()
			fail := func(stage string, err error) {
				result.Error = stage + ": " + publicError(err).Error()
				t.Error(result.Error)
			}
			cats, err := d.fetchAttachedCategories(ctx, source)
			if err != nil {
				fail("categories", err)
				return
			}
			result.Categories = len(cats)
			category := ""
			if len(cats) > 0 {
				category = cats[0].ID
			}
			rows, _, err := d.fetchAttachedCatalogPage(ctx, source, 1, category, "")
			if err != nil {
				fail("catalog", err)
				return
			}
			result.Catalog = len(rows)
			if len(rows) == 0 {
				result.Error = "catalog: no content"
				t.Error(result.Error)
				return
			}
			search, _, searchErr := d.fetchAttachedCatalogPage(ctx, source, 1, category, rows[0].DisplayTitle())
			if searchErr == nil {
				result.Search = len(search)
			}
			var last error
			for index, drama := range rows {
				if index >= 3 {
					break
				}
				_, chapters, err := d.fetchAttachedDetail(ctx, source, drama.SourceID)
				if err != nil {
					last = err
					continue
				}
				result.Chapters = len(chapters)
				for i, chapter := range chapters {
					if i >= 4 {
						break
					}
					media, err := d.resolveAttachedMedia(ctx, Task{DramaID: drama.ID, Chapter: chapter})
					if err != nil {
						last = err
						continue
					}
					parsed, _ := url.Parse(media.URL)
					result.MediaHost = parsed.Host
					result.Playlist = strings.HasPrefix(strings.TrimSpace(media.Playlist), "#EXTM3U")
					target := media.URL
					if result.Playlist {
						for _, line := range strings.Split(media.Playlist, "\n") {
							if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "#") {
								target = resolveProviderURL(media.URL, strings.TrimSpace(line))
								break
							}
						}
					}
					req, _ := http.NewRequestWithContext(providerMediaContext(ctx, media.credentials), http.MethodHead, target, nil)
					req.Header.Set("User-Agent", userAgent)
					if media.Referer != "" {
						req.Header.Set("Referer", media.Referer)
					}
					client := d.client
					if media.credentials != nil {
						if err := media.credentials.apply(req); err != nil {
							last = err
							continue
						}
						client = media.credentials.client(client)
					}
					response, err := client.Do(req)
					if err != nil {
						last = err
						continue
					}
					result.MediaStatus = response.StatusCode
					response.Body.Close()
					if result.MediaStatus >= 200 && result.MediaStatus < 300 {
						result.Playable = true
						if searchErr != nil {
							fail("search", searchErr)
							return
						}
						if result.Search == 0 {
							result.Error = "search: no matching content"
							t.Error(result.Error)
							return
						}
						t.Logf("%s categories=%d catalog=%d search=%d chapters=%d mediaHEAD=%d", source, result.Categories, result.Catalog, result.Search, result.Chapters, result.MediaStatus)
						return
					}
				}
			}
			if last != nil {
				fail("detail/play", last)
			} else {
				result.Error = "media: no successful HEAD"
				t.Error(result.Error)
			}
		})
	}
}
