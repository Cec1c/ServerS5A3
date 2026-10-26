package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/channelinfo"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/progression"
	protocol "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Authentication/selected character and completed historical unlock quests are
// fixture boundaries. All entry costs, room deaths and transitions use handlers.
func TestR145AbyssDoorFullHandlers(t *testing.T) {
	ctx := context.Background()
	pvfPath, channelPath := os.Getenv("DNFBRIDGE_ABYSS_DOOR_PVF"), os.Getenv("DNFBRIDGE_ABYSS_DOOR_CHANNEL")
	if pvfPath == "" && channelPath == "" {
		t.Skip("set DNFBRIDGE_ABYSS_DOOR_PVF and DNFBRIDGE_ABYSS_DOOR_CHANNEL for real-resource regression")
	}
	if pvfPath == "" || channelPath == "" {
		t.Fatal("both explicit real-resource inputs are required")
	}
	out := os.Getenv("R145_OUT")
	if out == "" {
		out = t.TempDir()
	}
	a, e := platformpvf.Open(pvfPath)
	if e != nil {
		t.Fatal(e)
	}
	if a.Snapshot().Checksum != "25675fba8711216b72800e5ff034df61209b37e7c22d3e4c3b3ad7b1906fc348" {
		t.Fatal("wrong PVF")
	}
	table, e := worldmap.LoadSource(ctx, a, worldmap.Options{})
	if e != nil {
		t.Fatal(e)
	}
	resolver, e := worldmap.NewResolver(table)
	if e != nil {
		t.Fatal(e)
	}
	mc, e := newPVFDungeonMonsterCatalog(a)
	if e != nil {
		t.Fatal(e)
	}
	ac, e := newPVFDungeonAICharacterCatalog(a)
	if e != nil {
		t.Fatal(e)
	}
	towns, e := dnftown.Load(ctx, a, dnftown.Options{})
	if e != nil {
		t.Fatal(e)
	}
	progress, e := progression.Load(ctx, a)
	if e != nil {
		t.Fatal(e)
	}
	hc, e := newPVFDungeonHellPartyCatalog(a)
	if e != nil {
		t.Fatal(e)
	}
	sp, e := progress.TotalSkillPoints(90)
	if e != nil {
		t.Fatal(e)
	}
	tp, e := progress.TotalTechniquePoints(90)
	if e != nil {
		t.Fatal(e)
	}
	channelRaw, e := os.ReadFile(channelPath)
	if e != nil {
		t.Fatal(e)
	}
	ci, e := channelinfo.Parse(channelRaw)
	if e != nil {
		t.Fatal(e)
	}
	cc, e := channelcatalog.New(ci, channelcatalog.Options{ServerID: 1})
	if e != nil {
		t.Fatal(e)
	}
	ch, ok := cc.Channel(17)
	if !ok {
		t.Fatal("channel17 missing")
	}
	// 86/70 resource mappings remain in ResourceProbe. Their town landing
	// fixture is unresolved; do not present them as full-handler coverage.
	for _, id := range []int64{101, 190, 310} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			d, ok := table.FindDungeon(id)
			if !ok {
				t.Fatal("dungeon missing")
			}
			area, ok := table.FindAreaByDungeon(id)
			if !ok {
				t.Fatal("area missing")
			}
			var townID, areaID int64
			var pos *dnftown.Gate
			for _, town := range towns.Towns() {
				for _, ar := range town.Areas {
					if ar.DungeonGate != nil && *ar.DungeonGate == area.ID {
						townID, areaID = town.ID, ar.ID
						// Dungeon-gate areas do not declare a same-area [gate].
						// Use the existing town return-position fallback for this
						// selected-character fixture; no physical town walk is claimed.
						x, y, found := (&Service{townCatalog: towns}).townFirstDeclaredPosition(townID)
						if found {
							pos = &dnftown.Gate{X: x, Y: y}
						}
						break
					}
				}
				if pos != nil {
					break
				}
			}
			if pos == nil {
				t.Fatalf("no true town gate for area%d", area.ID)
			}
			tickets, e := currentHellPartyTicketAlternatives(area, d)
			if e != nil || len(tickets) == 0 {
				t.Fatalf("ticket alternatives %v %v", tickets, e)
			}
			price := tickets[len(tickets)-1]
			slots := map[string]repo.ItemStack{}
			for n, item := range price {
				slots[fmt.Sprintf("0:%d", 121+n)] = repo.ItemStack{ItemID: item.ItemID, Count: item.Count * 3}
			}
			r := memory.NewMemoryGroup()
			if e = r.Account.Save(ctx, repo.AccountRecord{AccountID: "r145-offline"}); e != nil {
				t.Fatal(e)
			}
			if e = r.Character.Save(ctx, repo.CharacterRecord{CharacterID: "99", AccountID: "r145-offline", Name: "P127-offline", Job: "0", Level: 90, Stats: map[string]int64{"fatigue": 156, "exp": 0, "grow_type": 33, "town_id": townID, "area_id": areaID, "pos_x": pos.X, "pos_y": pos.Y, "direction": 5, "area_state": 3}}); e != nil {
				t.Fatal(e)
			}
			if e = r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "99", Slots: slots}); e != nil {
				t.Fatal(e)
			}
			if e = r.Skill.Save(ctx, repo.SkillRecord{CharacterID: "99", Points: repo.SkillPointState{SyncedLevel: 90, TotalSP: sp, RemainingSP: sp, TotalTP: tp, RemainingTP: tp}}); e != nil {
				t.Fatal(e)
			}
			qs := map[int64]repo.QuestState{}
			for _, q := range area.HellQuestIDs {
				qs[q] = repo.QuestState{Status: "completed"}
			}
			if e = r.Quest.Save(ctx, repo.QuestRecord{CharacterID: "99", States: qs}); e != nil {
				t.Fatal(e)
			}
			svc := &Service{options: options{accountID: "r145-offline", currentClientVersion: "official-24.9", gameUpperBodyCodec: gameUpperBodyCodecPlain, hellPartyEntry: hellPartyEntryVendor, hellBlockedSlots: hellBlockedSlotsOn, dungeonEntrySelectionMode: readDungeonEntrySelectionMode("allow")}, worldMapTable: table, worldMapResolver: resolver, dungeonMonsterTable: mc, dungeonAICharacterTable: ac, dungeonHellPartyTable: hc, townCatalog: towns, initialEquipmentArchive: a, dungeonChoice: func(int) (int, error) { return 0, nil }, dungeonSeed: func() (uint32, error) { return 1, nil }, repositoryProvider: func() (repo.Group, bool) { return r, true }}
			svc.options.gameUpperHeader = gameUpperHeaderServer16
			dir := filepath.Join(out, "handler-"+fmt.Sprint(id))
			if e = os.MkdirAll(dir, 0700); e != nil {
				t.Fatal(e)
			}
			log, e := openPacketLogger(filepath.Join(dir, "events.log"))
			if e != nil {
				t.Fatal(e)
			}
			svc.packetLog = log
			t.Cleanup(func() { log.Close() })
			conn := &bufferConn{}
			se := &gameSession{conn: conn, connID: "r145-" + fmt.Sprint(id), accountID: "r145-offline", selectedCharacterID: 99, channel: ch, residentChannel: ch, townActorOwnerChannel: byte(ch.ID), initialTownRouteCharacterID: 99, initialTownRouteStage: currentInitialTownRoutePlayerStateSent, itemListMigrationsDone: true}
			bindDungeonSelectorOriginForTestAt(t, svc, se, byte(townID), byte(areaID), uint16(pos.X), uint16(pos.Y))
			seq := uint16(1)
			send := func(op uint16, body []byte) {
				t.Helper()
				conn.write.Reset()
				frame, err := protocol.BuildChannelPacket(op, body, seq, protocol.DefaultChannelClassification)
				if err != nil {
					t.Fatal(err)
				}
				seq++
				err = svc.handleGameUpper(se, frame)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(dir, fmt.Sprintf("%03d-op%d.bin", seq, op)), conn.write.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
			}
			enter := make([]byte, 21)
			binary.LittleEndian.PutUint32(enter, uint32(id))
			enter[7] = 1
			binary.LittleEndian.PutUint16(enter[9:], 0xffff)
			send(16, enter)
			rt := se.dungeon.runtime
			if rt == nil {
				t.Fatal("entry rejected; see events, not a door RED")
			}
			if !rt.HellRoomSet || !rt.hellEntryItemsCommitted {
				t.Fatal("no frozen hell room or paid entry")
			}
			if len(rt.HellPartyWaves) != 2 {
				t.Fatalf("fixture: expected loaded real wave catalog, got %+v", rt.HellPartyWaves)
			}
			t.Logf("paid entry town=%d/%d worldmap=%d dungeon=%d waves=%s", townID, areaID, area.ID, id, hellPartyWaveDetail(rt.HellPartyWaves))
			inv, _, _ := r.Inventory.Load(ctx, "99")
			for n, item := range price {
				if inv.Slots[fmt.Sprintf("0:%d", 121+n)].Count != item.Count*2 {
					t.Fatal("entry cost not charged once")
				}
			}
			scene, _ := rt.Session.Scene()
			start := scene.Coordinate
			goal := worldmap.RoomCoordinate{X: int64(rt.HellRoom.X), Y: int64(rt.HellRoom.Y)}
			rooms := map[worldmap.RoomCoordinate]worldmap.DungeonRoom{}
			for _, room := range rt.Session.Rooms() {
				rooms[room.Coordinate] = room
			}
			queue := []worldmap.RoomCoordinate{start}
			parents := map[worldmap.RoomCoordinate]worldmap.RoomCoordinate{start: start}
			for len(queue) > 0 {
				at := queue[0]
				queue = queue[1:]
				if at == goal {
					break
				}
				for _, n := range rooms[at].Neighbors {
					if _, seen := parents[n.Coordinate]; seen {
						continue
					}
					if rooms[n.Coordinate].Boss && n.Coordinate != goal {
						continue
					}
					parents[n.Coordinate] = at
					queue = append(queue, n.Coordinate)
				}
			}
			if _, ok := parents[goal]; !ok {
				t.Fatalf("no nonboss route start%s hell%s", start, goal)
			}
			route := []worldmap.RoomCoordinate{goal}
			for route[len(route)-1] != start {
				route = append(route, parents[route[len(route)-1]])
			}
			for i := len(route) - 1; i > 0; i-- {
				scene, _ = rt.Session.Scene()
				if scene.Coordinate != route[i] {
					t.Fatal("route changed")
				}
				target := route[i-1]
				move := make([]byte, dungeoncmd.MoveMapRequestSize)
				move[0], move[1] = byte(target.X), byte(target.Y)
				{
					owner, next := rt.Room, rt.NextObjectKey
					send(45, []byte{move[0], move[1], 0, 0})
					still, _ := rt.Session.Scene()
					if still.Coordinate != scene.Coordinate || rt.Room != owner || rt.NextObjectKey != next || conn.write.Len() != 0 {
						t.Fatal("unproven 4B op45 accepted")
					}
				}
				if !scene.Cleared {
					owner, next := rt.Room, rt.NextObjectKey
					send(45, move)
					still, _ := rt.Session.Scene()
					if still.Coordinate != scene.Coordinate || rt.Room != owner || rt.NextObjectKey != next || conn.write.Len() != 0 {
						t.Fatal("uncleared move changed owner or sent a transition")
					}
				}
				snap := rt.Room.Snapshot()
				keys := []uint32{}
				for _, m := range snap.Monsters {
					if m.State != runtimeDungeonMonsterDefeated {
						keys = append(keys, m.ObjectKey)
					}
				}
				for _, m := range snap.ExtendedActors {
					if m.HostileReference != nil && m.State != runtimeDungeonMonsterDefeated {
						keys = append(keys, m.ObjectKey)
					}
				}
				for _, key := range keys {
					body := make([]byte, dungeoncmd.Official249DieMonsterBaseSize)
					binary.LittleEndian.PutUint32(body, key)
					binary.LittleEndian.PutUint16(body[4:], 99)
					send(39, body)
				}
				scene, _ = rt.Session.Scene()
				if !scene.Cleared {
					t.Fatalf("source room not clear %s map%d; fixture/other gate", scene.Coordinate, scene.Map.Map.ID)
				}
				if target == goal && id != 310 {
					owner, next := rt.Room, rt.NextObjectKey
					before, _, err := r.Inventory.Load(ctx, "99")
					if err != nil {
						t.Fatal(err)
					}
					// Dependency failure injection, without modifying PVF/run owner.
					for _, broken := range []*pvfDungeonAICharacterCatalog{nil, {source: a, paths: map[int64]string{}, cache: map[int64]pvfDungeonAICharacterDefinition{}}} {
						svc.dungeonAICharacterTable = broken
						send(45, move)
						still, _ := rt.Session.Scene()
						after, _, err := r.Inventory.Load(ctx, "99")
						if err != nil {
							t.Fatal(err)
						}
						if still.Coordinate != scene.Coordinate || rt.Room != owner || rt.NextObjectKey != next || conn.write.Len() != 0 || !reflect.DeepEqual(before, after) {
							t.Fatal("missing APC dependency changed room/asset state")
						}
					}
					svc.dungeonAICharacterTable = ac
				}
				send(45, move)
				after, _ := rt.Session.Scene()
				if after.Coordinate != target {
					_, diagnostic := svc.planCurrentDungeonMove(rt, dungeoncmd.MoveMapRequest{NextX: byte(target.X), NextY: byte(target.Y)})
					t.Fatalf("legal cleared-room op45 stalled dungeon%d source%s map%d target%s hellMap%d; preflight=%v", id, scene.Coordinate, scene.Map.Map.ID, target, rt.HellRoom.MapID, diagnostic)
				}
			}
			scene, _ = rt.Session.Scene()
			if scene.Map.Map.ID != rt.HellRoom.MapID {
				t.Fatal("wrong hell map")
			}
			// Inspect the actual complete-handler output, not rebuilt packets.
			wire := append([]byte(nil), conn.write.Bytes()...)
			var startBody []byte
			startCount, infoCount := 0, 0
			for len(wire) > 0 {
				p, rest := splitGameServerUpperPacketWithHeader(t, wire, protocol.GameServerUpperHeaderSize16)
				wire = rest
				if p.Header.MsgID == 29 && p.Header.Classification == 0 {
					startBody = p.Body
					startCount++
				}
				if p.Header.MsgID == 679 && p.Header.Classification == 0 {
					if startCount != 1 {
						t.Fatal("679 before op29")
					}
					infoCount++
				}
			}
			if startCount != 1 || infoCount != 1 || len(startBody) < 23 || binary.LittleEndian.Uint32(startBody[14:18]) != uint32(rt.HellRoom.MapID) {
				t.Fatal("missing/wrong op29 or 679")
			}
			hidden := []dungeonHellPartyMonster{}
			keys := map[uint16]bool{}
			for n := 0; n < int(startBody[18]); n++ {
				o := 19 + n*21
				if o+21 > len(startBody) {
					t.Fatal("truncated actor row")
				}
				row := startBody[o : o+21]
				key := binary.LittleEndian.Uint16(row[6:8])
				if keys[key] {
					t.Fatal("duplicate object key")
				}
				keys[key] = true
				if row[14] == hellPartyHiddenTemplateFlag && row[15] == hellPartyAttachAllWavesSelector {
					hidden = append(hidden, dungeonHellPartyMonster{Code: int64(binary.LittleEndian.Uint32(row[8:12])), Type: row[13], Level: row[12]})
				}
			}
			want := []dungeonHellPartyMonster{}
			for _, w := range rt.HellPartyWaves {
				want = append(want, w.Monsters...)
			}
			if !reflect.DeepEqual(hidden, want) {
				t.Fatalf("actual hidden rows %+v want %+v", hidden, want)
			}
			owner, next := rt.Room, rt.NextObjectKey
			replay := make([]byte, dungeoncmd.MoveMapRequestSize)
			replay[0], replay[1] = byte(goal.X), byte(goal.Y)
			send(45, replay)
			if rt.Room != owner || rt.NextObjectKey != next || conn.write.Len() != 0 {
				t.Fatal("same-room replay recreated room or resent start-map")
			}
			inv, _, e = r.Inventory.Load(ctx, "99")
			if e != nil {
				t.Fatal(e)
			}
			for n, item := range price {
				if inv.Slots[fmt.Sprintf("0:%d", 121+n)].Count != item.Count*2 {
					t.Fatal("move/replay charged ticket again")
				}
			}
			t.Logf("complete paid route dungeon%d area%d maze%d hellMap%d rooms%d waves%d", id, area.ID, rt.MazeIndex, rt.HellRoom.MapID, len(route), len(rt.HellPartyWaves))
		})
	}
}

func TestR145AbyssTemplateCatalogBoundaries(t *testing.T) {
	source := dungeonAICatalogSource{defaultDungeonAICharacterList: "4001 `Test/Actor.aic`\n", "AICharacter/Test/Actor.aic": "[minimum info]\n`PVF APC` 1 2 3 4 25 99\n"}
	ai, err := newPVFDungeonAICharacterCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []byte{0, 4, 6, 8, 255} {
		t.Run(fmt.Sprint(typ), func(t *testing.T) {
			plan := runtimeDungeonExtendedActorPlan{NextObjectKey: 1}
			svc := &Service{dungeonAICharacterTable: ai}
			mc := &pvfDungeonMonsterCatalog{source: source, paths: map[int64]string{}}
			// Non-Type5 must not gain admission from the APC directory.
			err := svc.appendCurrentHellPartyTemplateRows([]dungeonHellPartyWave{{Monsters: []dungeonHellPartyMonster{{Code: 4001, Type: typ, Level: 25}}}}, worldmap.DungeonRoomScene{}, mc, &plan)
			if !errors.Is(err, errDungeonMonsterDefinitionMiss) || len(plan.Actors) != 0 || plan.NextObjectKey != 1 {
				t.Fatalf("other type bypassed monster directory: %v", err)
			}
		})
	}
	for _, tc := range []struct {
		name    string
		catalog *pvfDungeonAICharacterCatalog
		code    int64
		want    error
	}{
		{"missing_catalog", nil, 4001, errDungeonAICharacterUnavailable},
		{"missing_id", ai, 4002, errDungeonAICharacterDefinitionMiss},
		{"valid_apc", ai, 4001, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := runtimeDungeonExtendedActorPlan{NextObjectKey: 1}
			svc := &Service{dungeonAICharacterTable: tc.catalog}
			err := svc.appendCurrentHellPartyTemplateRows([]dungeonHellPartyWave{{Monsters: []dungeonHellPartyMonster{{Code: tc.code, Type: 5, Level: 25}}}}, worldmap.DungeonRoomScene{}, nil, &plan)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			if tc.want != nil && (len(plan.Actors) != 0 || plan.NextObjectKey != 1) {
				t.Fatal("failed lookup appended actor")
			}
			if tc.want == nil && (len(plan.Actors) != 1 || plan.Actors[0].Packet.Type != 5) {
				t.Fatal("valid APC missing")
			}
		})
	}
}
