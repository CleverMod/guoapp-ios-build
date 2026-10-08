package core

import (
	"encoding/json"
	"fmt"
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
