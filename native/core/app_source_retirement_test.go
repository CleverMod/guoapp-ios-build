package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCatalogRestoreDiscardsUnregisteredSources(t *testing.T) {
	for _, version := range []int{0, 2, 3} {
		engine := &nativeEngine{directory: t.TempDir()}
		catalogs := map[string][]nativeDrama{}
		states := map[string]nativeCatalogState{}
		categories := map[string][]nativeCategory{}
		known := []string{sourceHongguo, sourceHuangdou, sourceXifu, "xiaopingguo", "xiaobao", sourceLiangzi + "|1"}
		removed := []string{"wuwu", "wuwu|1", "uku", "uku|1", "zy1080", "zy1080|1", "hongdou", "hongdou|334"}
		for _, key := range append(append([]string{}, known...), removed...) {
			catalogs[key] = []nativeDrama{{ID: key + ":123", Title: "合成缓存"}}
			states[key] = nativeCatalogState{Page: 2}
			categories[key] = []nativeCategory{{ID: "1", Name: "合成分类"}}
		}
		var disk any = nativeCatalogDisk{Version: version, Catalogs: catalogs, States: states, Categories: categories}
		if version == 0 {
			disk = catalogs
		}
		body, err := json.Marshal(disk)
		if err != nil || os.WriteFile(filepath.Join(engine.directory, "catalogs.json"), body, 0600) != nil {
			t.Fatal("cannot create synthetic cache", err)
		}
		engine.loadCatalogCache()
		if len(engine.catalogs) != len(known) {
			t.Fatal("catalog restore retained a removed source or discarded a known one", version)
		}
		for _, key := range known {
			if len(engine.catalogs[key]) != 1 || version > 0 && engine.catalogStates[key].Page != 2 {
				t.Fatal("known source cache changed", version, key)
			}
		}
		for _, key := range removed {
			_, catalogFound := engine.catalogs[key]
			_, stateFound := engine.catalogStates[key]
			_, categoryFound := engine.categoryOptions[key]
			if catalogFound || stateFound || categoryFound {
				t.Fatal("unregistered source remained available in the restored cache", version, key)
			}
		}
	}
}
