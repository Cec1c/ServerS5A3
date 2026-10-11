package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	onlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	worldmap "longheng.io/server/internal/modules/dnf/worldmap"
	"strings"
	"testing"
)

func TestEpicRoadActualBootstrapAdvertisesCurrent350(t *testing.T) {
	s, a, _, conn := prepareMonthlyBridge(t)
	s.options.joustEventDisabled = true
	catalog := &currentSpendTimeRuntimeCatalog{rewardItemIDs: currentSpendTimeJoustCatalogGoldenRewards, totalStageSeconds: 10800}
	sent, e := s.sendCurrentSpendTimeProtocolState(a, catalog, 0, 0, true)
	if e != nil || !sent {
		t.Fatalf("sent%t err%v", sent, e)
	}
	packet, _ := splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), 16)
	plain, e := zlibDecompress(packet.Body)
	if e != nil {
		t.Fatal(e)
	}
	shape := parseCurrentSpendTimeEventInfoBody(t, plain)
	if !shape.hasEvent(350) {
		t.Fatal("actual350 NPC/quest availability absent from current op108")
	}
}

type epicRoadForeignQuestRepo struct{ repo.QuestRepository }

func (q epicRoadForeignQuestRepo) Load(ctx context.Context, id string) (repo.QuestRecord, bool, error) {
	v, found, e := q.QuestRepository.Load(ctx, id)
	v.CharacterID = "foreign"
	return v, found, e
}
func TestEpicRoadQuestOwnerMismatchCannotAuthorizeTown(t *testing.T) {
	s, _, g := newTownMoveTest(t)
	g.Quest.Save(context.Background(), repo.QuestRecord{CharacterID: "29", States: map[int64]repo.QuestState{5579: {Status: "completed"}}})
	g.Quest = epicRoadForeignQuestRepo{g.Quest}
	ok, e := s.townAreaNeedQuestsCompleted(context.Background(), g, "29", []int64{5579})
	if e != nil {
		t.Fatal(e)
	}
	if ok {
		t.Fatal("foreign quest record authorized selected char")
	}
}

func TestEpicRoadActualTownTransportRetainsQuestLevelAndControl(t *testing.T) {
	base, _, _, _ := prepareMonthlyBridge(t)
	archive := base.initialEquipmentArchive
	table, e := dnftown.Load(context.Background(), archive, dnftown.Options{})
	if e != nil {
		t.Fatal(e)
	}
	var sourceTown, sourceArea int64
	for _, town := range table.Towns() {
		for _, a := range town.Areas {
			if sourceTown == 0 && strings.Contains(strings.ToLower(a.MapPath), "new_seria_room") {
				sourceTown = town.ID
				sourceArea = a.ID
			}
		}
	}
	if sourceTown == 0 {
		t.Fatal("actual Seria area unavailable")
	}
	for _, name := range []string{"valid", "native-coordinate-normalize", "active-quest", "missing-quest", "low-level", "foreign-record", "wrong-npc", "wrong-source", "disabled"} {
		t.Run(name, func(t *testing.T) {
			s, a, g := newTownMoveTest(t)
			s.options.currentClientVersion = "official-24.9"
			s.initialEquipmentArchive = archive
			s.townCatalog = table
			if e := s.initializeActivityControls(context.Background()); e != nil {
				t.Fatal(e)
			}
			c, _, _ := g.Character.Load(context.Background(), "29")
			c.Level = 17
			if name == "low-level" {
				c.Level = 16
			}
			c.Stats["town_id"], c.Stats["area_id"] = sourceTown, sourceArea
			if e := g.Character.Save(context.Background(), c); e != nil {
				t.Fatal(e)
			}
			status := "completed"
			if name == "active-quest" {
				status = "active"
			}
			if name != "missing-quest" {
				if e := g.Quest.Save(context.Background(), repo.QuestRecord{CharacterID: "29", States: map[int64]repo.QuestState{5579: {Status: status}}}); e != nil {
					t.Fatal(e)
				}
			}
			if name == "foreign-record" {
				g.Quest = epicRoadForeignQuestRepo{g.Quest}
				s.repositoryProvider = func() (repo.Group, bool) { return g, true }
			}
			if name == "disabled" {
				if _, e := s.activityControls.Save(context.Background(), s.activityControls.Snapshot().Revision, []onlineevent.ControlChange{{ID: 350, Enabled: false}}); e != nil {
					t.Fatal(e)
				}
				if e := s.initializeActivityControls(context.Background()); e != nil {
					t.Fatal(e)
				}
			}
			b := buildTownMoveRequest(50, 0, 441, 207, 5)
			binary.LittleEndian.PutUint16(b[7:9], uint16(sourceTown))
			binary.LittleEndian.PutUint16(b[9:11], uint16(sourceArea))
			binary.LittleEndian.PutUint32(b[11:15], 3136)
			if name == "wrong-npc" {
				binary.LittleEndian.PutUint32(b[11:15], 3135)
			}
			if name == "wrong-source" {
				binary.LittleEndian.PutUint16(b[9:11], uint16(sourceArea+1))
			}
			if name == "native-coordinate-normalize" {
				binary.LittleEndian.PutUint16(b[2:4], 123)
				binary.LittleEndian.PutUint16(b[4:6], 234)
			}
			if e := s.handleTownSetUserArea(a, b); e != nil {
				t.Fatal(e)
			}
			got, _, _ := g.Character.Load(context.Background(), "29")
			data := a.conn.(*bufferConn).write.Bytes()
			if name == "valid" || name == "native-coordinate-normalize" {
				if got.Stats["town_id"] != 50 || got.Stats["pos_x"] != 441 || got.Stats["pos_y"] != 207 || len(data) == 0 {
					t.Fatalf("legitimate route not committed %+v len%d", got.Stats, len(data))
				}
			} else if got.Stats["town_id"] != sourceTown || len(data) != 0 {
				t.Fatalf("rejected route mutated %+v len%d", got.Stats, len(data))
			}
		})
	}
}
func TestEpicRoadDisabledBuilderPreservesPreviousCatalog(t *testing.T) {
	ids := currentSpendTimeJoustCatalogGoldenRewards
	a, e := buildCurrentActivityEventInfoBodyWithWeekly(ids, 10800, false, false, true, true, true, 2418, true, true, true)
	if e != nil {
		t.Fatal(e)
	}
	b, e := buildCurrentActivityEventInfoBodyWithEpicRoad(ids, 10800, false, false, true, true, true, 2418, true, true, true, false, false)
	if e != nil || !bytes.Equal(a, b) {
		t.Fatal("disabled epic changes existing catalog", e)
	}
	b, e = buildCurrentActivityEventInfoBodyWithEpicRoad(ids, 10800, false, false, true, true, true, 2418, true, true, true, false, true)
	if e != nil {
		t.Fatal(e)
	}
	shape := parseCurrentSpendTimeEventInfoBody(t, b)
	if !shape.hasEvent(350) || shape.hasEvent(286) {
		t.Fatal("wrong current epic owner")
	}
	for _, r := range shape.activeRows {
		if r.eventID == 350 && r.params != [12]uint32{} {
			t.Fatal("invented epic event parameters")
		}
	}
}

type epicRoadOverrideSource struct {
	dnfpvf.Source
	path, text string
}

func (v epicRoadOverrideSource) ReadText(path string) (string, error) {
	if path == v.path {
		if v.text == "" {
			return "", fmt.Errorf("resource temporarily unavailable")
		}
		return v.text, nil
	}
	return v.Source.ReadText(path)
}
func TestEpicRoadActualResourceGateRejectsForeignOrMissingContracts(t *testing.T) {
	s, _, _, _ := prepareMonthlyBridge(t)
	archive := s.initialEquipmentArchive
	if e := validateCurrentEpicRoadActivity(archive); e != nil {
		t.Fatal(e)
	}
	for _, spec := range []struct{ path, old, replacement string }{{"npc/event_npc_soryu_2018.npc", "350", "351"}, {"npc/event_npc_soryu_2018.npc", "3136", "3137"}, {"n_quest/event/18_epciroad2_2.qst", "5578", "5577"}, {"itemshop/2018_epicroad_kairyu.shp", "3136", "3135"}, {"dungeon/event/2018_epciroad2/forestofdesire.dgn", "3330 30 1", "3330 30 0"}} {
		text, e := archive.ReadText(spec.path)
		if e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(text, spec.old) {
			t.Fatal("wrong fixture")
		}
		text = strings.Replace(text, spec.old, spec.replacement, 1)
		if e := validateCurrentEpicRoadActivity(epicRoadOverrideSource{archive, spec.path, text}); e == nil {
			t.Fatalf("foreign resource accepted %s", spec.path)
		}
	}
	if e := validateCurrentEpicRoadActivity(epicRoadOverrideSource{archive, "npc/event_npc_soryu_2018.npc", ""}); e == nil {
		t.Fatal("missing resource not failclosed")
	}
}

func TestEpicRoadActualTicketCatalogAndLegacyVendorConsumer(t *testing.T) {
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	s, _, _, _ := prepareMonthlyBridge(t)
	table, e := worldmap.LoadSource(context.Background(), s.initialEquipmentArchive, worldmap.Options{})
	if e != nil {
		t.Fatal(e)
	}
	for i, id := range []int64{6063, 6064, 6065} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			d, ok := table.FindDungeon(id)
			if !ok {
				t.Fatal("actual dungeon missing")
			}
			need := int64(30 + i*15)
			if len(d.Metadata.RequiredItems) != 1 || d.Metadata.RequiredItems[0].ItemID != 3330 || d.Metadata.RequiredItems[0].Count != need || !d.Metadata.RequiredItems[0].ConsumeOnEntry {
				t.Fatalf("actual contract %+v", d.Metadata.RequiredItems)
			}
			// Legacy unscoped bridge consumer remains covered here; official-24.9
			// deferred payment is covered by the preserved r86 prepared-entry tests.
			bridge, a, _ := newBackToVillageRuntime(t)
			g, _ := bridge.repositoryGroup()
			if e := bridge.consumeDungeonEntryItemsWithTournament(context.Background(), d, a); !errors.Is(e, dnfdungeon.ErrEntryItemConsumeInsufficient) {
				t.Fatal("missing tickets accepted", e)
			}
			if e := g.Inventory.Save(context.Background(), repo.InventoryRecord{CharacterID: "99", Slots: map[string]repo.ItemStack{"0:1": {ItemID: 3330, Count: need - 1}, "0:2": {ItemID: 3329, Count: 7}}}); e != nil {
				t.Fatal(e)
			}
			if e := bridge.consumeDungeonEntryItemsWithTournament(context.Background(), d, a); !errors.Is(e, dnfdungeon.ErrEntryItemConsumeInsufficient) {
				t.Fatal("insufficient ticket accepted", e)
			}
			v, _, _ := g.Inventory.Load(context.Background(), "99")
			if v.Slots["0:1"].Count != need-1 || v.Slots["0:2"].Count != 7 {
				t.Fatal("rejection changed inventory")
			}
			v.Slots["0:1"] = repo.ItemStack{ItemID: 3330, Count: need}
			if e := g.Inventory.Save(context.Background(), v); e != nil {
				t.Fatal(e)
			}
			if e := bridge.consumeDungeonEntryItemsWithTournament(context.Background(), d, a); e != nil {
				t.Fatal(e)
			}
			v, _, _ = g.Inventory.Load(context.Background(), "99")
			if _, ok := v.Slots["0:1"]; ok || v.Slots["0:2"].Count != 7 {
				t.Fatal("actual tickets not exactly consumed")
			}
			if e := bridge.consumeDungeonEntryItemsWithTournament(context.Background(), d, a); !errors.Is(e, dnfdungeon.ErrEntryItemConsumeInsufficient) {
				t.Fatal("repeated consume manufactured tickets", e)
			}
		})
	}
}

func TestEpicRoadRebaseRetainsOrdinaryAndDoesNotInventMenuTail(t *testing.T) {
	ids := currentSpendTimeJoustCatalogGoldenRewards
	old, err := buildCurrentActivityEventInfoBodyWithOrdinary(ids, 10800, false, false, true, true, true, 2418, true, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := buildCurrentActivityEventInfoBodyWithEpicRoad(ids, 10800, false, false, true, true, true, 2418, true, true, true, true, false)
	if err != nil || !bytes.Equal(old, disabled) {
		t.Fatal("disabled350 altered r86ordinary", err)
	}
	both, err := buildCurrentActivityEventInfoBodyWithEpicRoad(ids, 10800, false, false, true, true, true, 2418, true, true, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	shape := parseCurrentSpendTimeEventInfoBody(t, both)
	if !shape.hasEvent(331) || !shape.hasEvent(350) {
		t.Fatal("ordinary or350 absent")
	}
	for _, row := range shape.activeRows {
		if row.eventID == 350 && row.params != [12]uint32{} {
			t.Fatal("invented350descriptor")
		}
	}
}
