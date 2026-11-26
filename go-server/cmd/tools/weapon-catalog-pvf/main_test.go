package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestSetPriceValueAppendsOnlyOnce(t *testing.T) {
	base := []platformpvf.ScriptToken{{Type: 3, Text: "[name]"}, {Type: 6, Text: "mold"}}
	got, changed, err := setPriceValue(base, 1000000)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || len(got) != len(base)+4 {
		t.Fatalf("changed=%t len=%d", changed, len(got))
	}
	if got[len(got)-4] != (platformpvf.ScriptToken{Type: 3, Text: "[price]"}) || got[len(got)-3].Value != 1000000 || got[len(got)-2] != (platformpvf.ScriptToken{Type: 3, Text: "[value]"}) || got[len(got)-1].Value != 0 {
		t.Fatalf("unexpected price tail: %+v", got[len(got)-4:])
	}
	_, changedAgain, err := setPriceValue(got, 1000000)
	if err != nil || changedAgain {
		t.Fatalf("second application changed=%t err=%v", changedAgain, err)
	}
}

func TestBuildShopTabPreservesSelectedOrder(t *testing.T) {
	tab := buildShopTab(&linusSpec{TabName: "幻化", JobIDs: []int32{0, 11}, ItemIDs: []int32{490704411, 490704427}})
	entries, prices := 0, 0
	for i := 0; i+1 < len(tab); i++ {
		if tab[i] == (platformpvf.ScriptToken{Type: 3, Text: "[category entry]"}) {
			entries++
		}
		if tab[i].Type == 3 && tab[i].Text == "[item list]" {
			for j := i + 1; j < len(tab) && tab[j].Type == 0; j++ {
				prices++
			}
		}
	}
	if entries != 2 || prices != 4 || tab[len(tab)-1] != (platformpvf.ScriptToken{Type: 3, Text: "[/tab]"}) {
		t.Fatalf("entries=%d prices=%d tab=%+v", entries, prices, tab)
	}
}

func TestPlanTokenAndListRowDecode(t *testing.T) {
	var token planToken
	if err := json.Unmarshal([]byte(`[6,"幻化"]`), &token); err != nil || token.Type != 6 || token.Text != "幻化" {
		t.Fatalf("token=%+v err=%v", token, err)
	}
	var row listRow
	if err := json.Unmarshal([]byte(`[490704411,"cash/chn_490704411.stk"]`), &row); err != nil || row.ID != 490704411 || row.Path != "cash/chn_490704411.stk" {
		t.Fatalf("row=%+v err=%v", row, err)
	}
}
func TestZeroTokenContentHash(t *testing.T) {
	if got := tokenSHA(nil); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("zero-token hash=%s", got)
	}
}

func TestPathAliasIsRejected(t *testing.T) {
	dir := t.TempDir()
	path := dir + string(os.PathSeparator) + "candidate.pvf"
	if err := ensureDistinct(map[string]string{"input": path, "output": path}); err == nil {
		t.Fatal("expected input/output alias rejection")
	}
}

func TestBoundarySchemaRejectsWrongIndexPath(t *testing.T) {
	plan := &plan{
		Content: make([]contentSpec, 40032),
		AppendLists: []appendList{
			{Path: equipmentListPath, Rows: make([]listRow, 13329)},
			{Path: stackableListPath, Rows: make([]listRow, 26660)},
		},
		NPC: &npcSpec{Path: npcLinusPath, RoleMarker: "[/role]", InsertTokens: []planToken{{Type: 3, Text: "insert"}}, AppendTokens: []planToken{{Type: 3, Text: "append"}}},
		LinusShop: &linusSpec{
			Path: linusShopPath, TabName: "幻化",
			JobIDs:     []int32{0, 11, 7, 1, 5, 2, 8, 3, 4, 14, 6, 9, 10, 12, 13, 15},
			ItemIDs:    []int32{490704411, 490704427},
			MoldPrices: []moldPrice{{Path: moldMagicPath, Price: 1000000}, {Path: moldSecretPath, Price: 2000000}},
		},
	}
	if err := validatePlanBoundaries(plan); err != nil {
		t.Fatalf("valid boundary plan rejected: %v", err)
	}
	plan.AppendLists[0].Path = "equipment/other.lst"
	if err := validatePlanBoundaries(plan); err == nil {
		t.Fatal("expected wrong index path rejection")
	}
}

func TestCommittedPlanBoundariesAndZeroTokenANI(t *testing.T) {
	planPath := filepath.Join("..", "..", "..", "..", "resource-recipes", "20261127-selected-public", "resources", "weapon-skin", "weapon-catalog.pvf-plan.json.gz")
	p, _, err := loadPlan(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePlanBoundaries(p); err != nil {
		t.Fatal(err)
	}
	zero := 0
	for _, item := range p.Content {
		if len(item.Tokens) == 0 {
			zero++
			if item.SHA256 != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
				t.Fatalf("zero-token hash=%s path=%s", item.SHA256, item.Path)
			}
		}
	}
	if zero != 2 {
		t.Fatalf("zero-token ANI count=%d want=2", zero)
	}
}
func TestCommittedPlanMoldOverlayHashes(t *testing.T) {
	planPath := filepath.Join("..", "..", "..", "..", "resource-recipes", "20261127-selected-public", "resources", "weapon-skin", "weapon-catalog.pvf-plan.json.gz")
	p, _, err := loadPlan(planPath)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		price int32
		sha   string
	}{
		moldMagicPath:  {1000000, "d72b50fdac0a25fb7cfd9aeb666bcc518402221729cd33078d23248c1fbe8afc"},
		moldSecretPath: {2000000, "4d71b15a2b3a232aa26675a7a2694da805278e30ac2cf970a531cc8186eed873"},
	}
	seen := 0
	for _, item := range p.Content {
		entry, ok := want[item.Path]
		if !ok {
			continue
		}
		seen++
		got, changed, err := setPriceValue(toScriptTokens(item.Tokens), entry.price)
		if err != nil || !changed {
			t.Fatalf("%s overlay changed=%t err=%v", item.Path, changed, err)
		}
		if tokenSHA(got) != entry.sha {
			t.Fatalf("%s final overlay hash=%s want=%s", item.Path, tokenSHA(got), entry.sha)
		}
		if tokenSHA(got) == item.SHA256 {
			t.Fatalf("%s overlay did not change content hash", item.Path)
		}
	}
	if seen != 2 {
		t.Fatalf("mold overlay entries=%d want=2", seen)
	}
}
