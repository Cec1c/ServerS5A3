package dnfbridge

import (
	"fmt"
	"os"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// incompleteItemID / incompletePath pin the one row in the pinned runtime
// Script.pvf (sha256 369d072b...) that declares the damage-font action without a
// `[damage font info]` block: of the 25 stackables with `[action type] = [add
// damage font skin]`, 24 carry the block (positive `[index]` 1-11 / 5000-5002 and
// a supported `[expiration info]`) and item 490010086 at
// `stackable/490010001/chn_490010086.stk` does not.
//
// Index 0 is the "no font" sentinel here (parseDamageFontOwnershipStatKey rejects
// 0; SelectDamageFont treats 0 as clearing the selection), so a section-less row
// grants nothing. The audit records that known resource gap and pins the
// production decision: block declared -> positive index and known expiration;
// block absent -> keep failing with the original missing-index error. It does not
// treat the missing section as a legal value, nor as proven corruption.
func TestRealPVFDamageFontDefinitionsAreAuditable(t *testing.T) {
	const (
		incompleteItemID  = 490010086
		incompletePath    = "490010001/chn_490010086.stk"
		incompleteErrText = "damage font: font index 0 invalid"
	)
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("DNFBRIDGE_REAL_PVF_SMOKE not set")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("open PVF: %v", err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatalf("build item catalog: %v", err)
	}
	listText, err := archive.ReadText(dungeonDropStackableList)
	if err != nil {
		t.Fatalf("read stackable list: %v", err)
	}
	listDocument, err := dnfpvf.Parse(dungeonDropStackableList, listText)
	if err != nil {
		t.Fatalf("parse stackable list: %v", err)
	}

	declared, complete := 0, 0
	incomplete := make([]string, 0, 2)
	foundProbe := false
	for _, entry := range dnfpvf.ParseList(listDocument) {
		text, err := archive.ReadText("stackable/" + strings.TrimPrefix(entry.Path, "stackable/"))
		if err != nil {
			continue
		}
		document, err := dnfpvf.Parse(entry.Path, text)
		if err != nil {
			continue
		}
		action, _ := document.Text("action type")
		if !strings.EqualFold(strings.TrimSpace(action), "[add damage font skin]") {
			continue
		}
		declared++
		definition, resolveErr := catalog.ResolveItem(uint32(entry.ID))
		if _, declaresBlock := document.Section("damage font info"); !declaresBlock {
			if resolveErr == nil || !strings.Contains(resolveErr.Error(), incompleteErrText) {
				t.Fatalf("row declaring the action without [damage font info] must stay rejected with %q: item=%d path=%q definition=%+v err=%v",
					incompleteErrText, entry.ID, entry.Path, definition, resolveErr)
			}
			incomplete = append(incomplete, fmt.Sprintf("%d %s", entry.ID, entry.Path))
			continue
		}
		if resolveErr != nil {
			t.Fatalf("resolve damage font item=%d path=%q: %v", entry.ID, entry.Path, resolveErr)
		}
		complete++
		if definition.DamageFontIndex == 0 || definition.DamageFontExpirationMode == 0 {
			t.Fatalf("incomplete damage font definition item=%d: %+v", entry.ID, definition)
		}
		if entry.ID == 10160911 {
			foundProbe = true
			if definition.DamageFontIndex != 2 || definition.DamageFontPeriodDays != 90 {
				t.Fatalf("probe item 10160911 = %+v, want font=2 period=90", definition)
			}
		}
	}
	if declared == 0 {
		t.Fatal("runtime PVF has no [add damage font skin] item")
	}
	if !foundProbe {
		t.Fatal("runtime PVF is missing probe damage-font item 10160911")
	}
	want := fmt.Sprintf("%d %s", incompleteItemID, incompletePath)
	if len(incomplete) != 1 || incomplete[0] != want {
		t.Fatalf("rows declaring the action without [damage font info] = %v, want exactly [%s]", incomplete, want)
	}
	if complete != declared-1 {
		t.Fatalf("complete damage-font rows = %d of %d declared", complete, declared)
	}
}
