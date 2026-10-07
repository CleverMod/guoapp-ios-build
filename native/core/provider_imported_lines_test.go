package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestImportedLinesKeepMetadataAndLogicalEpisodeCounts(t *testing.T) {
	c := &attachedClient{p: attachedProvider{id: "nnvideo"}}
	lines := []importedLine{}
	for index, size := range []int{7, 7, 8} {
		line := importedLine{key: fmt.Sprintf("line-%d", index), name: fmt.Sprintf("线路%d", index+1)}
		for number := 1; number <= size; number++ {
			line.episodes = append(line.episodes, importedEpisode{name: fmt.Sprintf("第%d集", number), url: "opaque"})
		}
		lines = append(lines, line)
	}
	chapters, count := c.importedChapters("73", lines)
	if len(chapters) != 22 || count != 8 || chapterEpisodeCount(chapters) != 8 {
		t.Fatal("line alternatives inflate episode count")
	}
	if chapters[0].LineID == chapters[7].LineID || chapters[7].LineID == chapters[14].LineID {
		t.Fatal("source lines merged")
	}
	for _, chapter := range chapters {
		body, _ := json.Marshal(chapter)
		var decoded Chapter
		if json.Unmarshal(body, &decoded) != nil || decoded.LineID == "" || decoded.LineName == "" {
			t.Fatal("line metadata lost at bridge")
		}
	}
}

func TestImportedNativeMovieDetailCountsOneEpisodeAcrossThreeLines(t *testing.T) {
	engine := sourceFixtureEngine(t, func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/vod/detail/321.html" {
			t.Fatal("unexpected request", request.URL.Path)
		}
		body := `<h1>合成电影</h1>`
		for line := 1; line <= 3; line++ {
			body += fmt.Sprintf(`<a href="#playlist%d">线路%d</a><div id="playlist%d"><a href="/vod/play/321-%d-1.html">正片</a></div>`, line, line, line, line)
		}
		return sourceFixtureResponse(request, 200, body), nil
	})
	value, err := engine.nativeDetail(context.Background(), nativeDrama{ID: "xiaobao:321", Source: "xiaobao", Title: "合成电影"})
	if err != nil {
		t.Fatal(err)
	}
	row := value.(map[string]any)
	if row["drama"].(nativeDrama).Episodes != 1 || len(row["chapters"].([]Chapter)) != 3 {
		t.Fatal("three routes counted as three movie episodes")
	}
}
