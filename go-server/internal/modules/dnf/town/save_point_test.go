package town

import (
	"context"
	"testing"
)

func TestTownSavePointMarkerIsIndependentFromPreTownCacheMarker(t *testing.T) {
	table, err := Load(context.Background(), memorySource{
		"town/town.lst":      "38 `normal.twn` 107 `temporary.twn`",
		"town/normal.twn":    "[exclude user pretown info]\n[area]\n0 `normal.map` `[normal]`\n[/area]",
		"town/temporary.twn": "[disable save point]\n[area]\n0 `temporary.map` `[normal]`\n[/area]",
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	normal, normalFound := table.Find(38)
	temporary, temporaryFound := table.Find(107)
	if !normalFound || !temporaryFound || normal.DisableSavePoint || !temporary.DisableSavePoint {
		t.Fatalf("normal=%+v temporary=%+v", normal, temporary)
	}
	clone := table.Towns()
	clone[1].DisableSavePoint = false
	again, _ := table.Find(107)
	if !again.DisableSavePoint {
		t.Fatal("a catalog snapshot changed the town's save-point policy")
	}
}
