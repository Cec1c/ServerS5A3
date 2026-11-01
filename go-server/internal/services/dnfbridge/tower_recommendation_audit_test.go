package dnfbridge

// This is a regression probe for a missing owner, NOT an implementation of
// recommendation books. Never manufacture tower allowance keys in this fixture.
import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	archive "longheng.io/server/internal/platform/pvf"
)

func TestTowerRecommendationAudit002(t *testing.T) {
	pvf := os.Getenv("TOWER_AUDIT_PVF")
	if pvf == "" {
		t.Skip("explicit fixed-PVF memory audit only")
	}
	t.Setenv("DNFBRIDGE_REAL_PVF_SMOKE", "")
	s, session, repos := guildMileageFixture(t)
	a, err := archive.Open(pvf)
	if err != nil {
		t.Fatal(err)
	}
	c, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog, s.initialEquipmentArchive = c, a
	ctx := context.Background()
	table, err := worldmap.LoadSource(ctx, a, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	s.worldMapTable, s.worldMapResolver = table, resolver
	now := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
	s.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return now }}
	b, err := os.ReadFile(os.Getenv("TOWER_AUDIT_ITEMS"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		ID   uint32 `json:"itemId"`
		Path string `json:"pvfPath"`
	}
	if err = json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 72 {
		t.Fatal(len(rows))
	}
	base, _, err := repos.Character.Load(ctx, "29")
	if err != nil {
		t.Fatal(err)
	}
	accountID := s.accountIDForSession(session)
	account := dnfrepo.AccountRecord{AccountID: accountID, Metadata: map[string]string{}}
	body := make([]byte, 19)
	binary.LittleEndian.PutUint16(body, 87)
	binary.LittleEndian.PutUint32(body[7:], 71)
	if _, e := dnfinventory.DecodeUseStackableActionRequest(body); e == nil {
		t.Fatal("action71 unexpectedly has generic owner; re-audit")
	}
	report := []map[string]any{}
	for _, r := range rows {
		text, e := a.ReadText(r.Path)
		if e != nil {
			t.Fatal(e)
		}
		doc, e := dnfpvf.Parse(r.Path, text)
		if e != nil {
			t.Fatal(e)
		}
		item, e := c.ResolveItem(r.ID)
		if e != nil {
			t.Fatal(e)
		}
		grant, e := currentPVFItemDefinitionForGrantAt(item, now)
		if e != nil {
			t.Fatal(e)
		}
		stack := dnfrepo.ItemStack{ItemID: int64(r.ID), Count: 2, Extra: map[string]string{"item_kind": "stackable", "pvf_path": r.Path}}
		stack, _ = applyCurrentPVFItemExpirationAt(stack, grant, now)
		for _, highest := range []int64{0, 1, 100} {
			ch := dnfrepo.CloneCharacter(base)
			ch.AccountID = accountID
			ch.Level = 90
			ch.Stats = map[string]int64{dnfdungeon.GriefHighestClearedStatKey: highest, dnfdungeon.DespairHighestClearedStatKey: highest}
			inv := dnfrepo.InventoryRecord{CharacterID: "29", Slots: map[string]dnfrepo.ItemStack{"0:87": stack}}
			for _, e := range []error{repos.Account.Save(ctx, account), repos.Character.Save(ctx, ch), repos.Inventory.Save(ctx, inv)} {
				if e != nil {
					t.Fatal(e)
				}
			}
			// Compare durable snapshots, after repository normalization, not the
			// caller's unnormalized nil maps/timestamps.
			inv, _, _ = repos.Inventory.Load(ctx, "29")
			account, _, _ = repos.Account.Load(ctx, accountID)
			ch, _, _ = repos.Character.Load(ctx, "29")
			for replay := 0; replay < 2; replay++ {
				session.conn.(*bufferConn).write.Reset()
				e = s.handleGameCommand(session, 1, 515, body)
				after, _, _ := repos.Inventory.Load(ctx, "29")
				ac, _, _ := repos.Account.Load(ctx, accountID)
				cc, _, _ := repos.Character.Load(ctx, "29")
				unchanged := reflect.DeepEqual(inv, after) && reflect.DeepEqual(account, ac) && reflect.DeepEqual(ch, cc)
				if !unchanged {
					t.Fatalf("action71 changed assets for %d: re-audit", r.ID)
				}
				report = append(report, map[string]any{"itemId": r.ID, "highestCleared": highest, "replay": replay, "allAssetsUnchanged": unchanged, "error": fmt.Sprint(e), "wireBytes": len(session.conn.(*bufferConn).write.Bytes()), "nativeAction": 71, "actionParameters": doc.Ints("action type")})
			}
		}
		report = append(report, map[string]any{"itemId": r.ID, "pvfPath": r.Path, "script": text, "definitionRead": true})
	}
	// Exercise the CURRENT admission classifiers for every real tower floor.
	// No invented account/day allowances are preloaded to make this test pass.
	noDailyGate := 0
	for _, first := range []int64{5115, 11008} {
		for floor := int64(0); floor < 100; floor++ {
			d, ok := table.FindDungeon(first + floor)
			if !ok {
				t.Fatal(first + floor)
			}
			daily, e := s.currentDailyCountEntryEnabled(d)
			if e != nil {
				t.Fatal(e)
			}
			if daily || s.currentRiftEntryEnabled(d.ID) || s.currentDayDungeonEntryEnabled(d.ID) || s.currentDimensionGateDailyEntryEnabled(d) {
				t.Fatalf("tower now has a quota owner %d; re-audit", d.ID)
			}
			noDailyGate++
		}
	}
	ch := dnfrepo.CloneCharacter(base)
	ch.AccountID = accountID
	ch.Stats = map[string]int64{dnfdungeon.GriefHighestClearedStatKey: 1, dnfdungeon.DespairHighestClearedStatKey: 1}
	if err = repos.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	for replay := 0; replay < 2; replay++ {
		grief, e := s.resolveCurrentGriefSelection(session, 5115)
		if e != nil || grief != 5116 {
			t.Fatal(grief, e)
		}
		despair, e := s.resolveCurrentDespairSelection(session, 11008)
		if e != nil || despair != 11009 {
			t.Fatal(despair, e)
		}
	}
	out := map[string]any{"rows": report, "towerFloorsWithoutDailyOwner": noDailyGate, "selectionWithoutBook": [2]int{5116, 11009}, "successfulBookTransactions": 0, "status": "gap_reproduced_not_fixed"}
	b, _ = json.MarshalIndent(out, "", "  ")
	if err = os.WriteFile(os.Getenv("TOWER_AUDIT_REPORT"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("TOWER_AUDIT_EXPECT_EFFECT") == "1" {
		t.Fatal("RED: 432 real action71 dispatches never debit a book or change any authoritative tower allowance; owner missing")
	}
}
