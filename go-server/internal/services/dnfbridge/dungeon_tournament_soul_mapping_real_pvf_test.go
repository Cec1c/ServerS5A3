package dnfbridge

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Real-PVF resource regression for the two soul tournaments. The DGN file names
// are crossed against the localized names, so id/name/map must be asserted from
// the archive itself rather than inferred from realizeyellowdragon/realizebluedragon.
func TestRealScriptPVFSoulTournamentResourceMapping(t *testing.T) {
	pvfPath := realTournamentClosingPVFPath()
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE (or DNF_WORLDMAP_REAL_PVF_SMOKE)")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}

	type row struct {
		code, strength int64
		name           string
	}
	tests := []struct {
		id       int64
		name     string
		dgn      string
		mapPath  string
		mapID    int64
		rows     []row
		specials []int64
	}{
		{
			id: 123, name: "魂·青龙大会",
			dgn: "realizeyellowdragon.dgn", mapPath: "r_yellowdragon_00.map", mapID: 95265,
			rows: []row{
				{56048, 900, "痞子张海"}, {56047, 300, "文峰"}, {56461, 900, "刘俊"},
				{56043, 900, "寒鹰"}, {59022, 600, "岳非"}, {56463, 600, "格瑞"},
				{59024, 300, "丰胜"}, {56046, 300, "月鉴轩"}, {59021, 900, "文英"},
				{59020, 600, "雪莲"}, {56458, 600, "金菲"}, {56041, 300, "竹影"},
				{56040, 300, "梅玲"}, {56465, 300, "管子维"}, {59027, 300, "解卅"},
			},
		},
		{
			id: 124, name: "魂·黄龙大会",
			dgn: "realizebluedragon.dgn", mapPath: "r_bluedragon_00.map", mapID: 95266,
			rows: []row{
				{56156, 900, "吉秀"}, {56045, 600, "黑风天策"}, {56044, 900, "紫云飞"},
				{63471, 900, "燕南飞"}, {63467, 600, "曹魏"}, {56462, 600, "毒王詹"},
				{59023, 300, "杨明"}, {56042, 600, "洁露娜"}, {56155, 900, "妮娜"},
				{56727, 600, "尤丽"}, {56460, 300, "秋红"}, {56459, 300, "珍妮花"},
				{56464, 300, "寒勇"}, {56729, 600, "阎王峰"}, {59026, 300, "罗宾 三世"},
			},
			specials: []int64{63471, 63467},
		},
	}

	for _, tc := range tests {
		t.Run(strings.TrimSuffix(tc.dgn, ".dgn"), func(t *testing.T) {
			dungeon, ok := table.FindDungeon(tc.id)
			if !ok {
				t.Fatalf("dungeon %d missing", tc.id)
			}
			if dungeon.Metadata.Name != tc.name {
				t.Fatalf("name=%q want %q (path=%s)", dungeon.Metadata.Name, tc.name, dungeon.Path)
			}
			if !strings.Contains(strings.ToLower(dungeon.Path), tc.dgn) {
				t.Fatalf("DGN path=%q want suffix %q", dungeon.Path, tc.dgn)
			}
			if !dungeon.Metadata.Flags["tournament dungeon"] || !dungeon.Metadata.NoFatigue ||
				!dungeon.Metadata.Flags["enter without fatigue"] {
				t.Fatalf("dungeon %d tournament/no-fatigue flags=%+v", tc.id, dungeon.Metadata.Flags)
			}
			if len(dungeon.Metadata.RequiredItems) != 0 {
				t.Fatalf("dungeon %d required items=%+v", tc.id, dungeon.Metadata.RequiredItems)
			}
			if !dungeon.Metadata.BasisLevel.Set || dungeon.Metadata.BasisLevel.Value != 75 ||
				!dungeon.Metadata.LimitPartyCount.Set || dungeon.Metadata.LimitPartyCount.Value != 1 {
				t.Fatalf("dungeon %d basis/party metadata=%+v", tc.id, dungeon.Metadata)
			}

			topology, err := worldmap.BuildDungeonLayout(resolver, tc.id, 0, func(c worldmap.DungeonMapChoice) (int64, error) {
				return c.Candidates[0].ID, nil
			})
			if err != nil {
				t.Fatalf("layout: %v", err)
			}
			if topology == nil || topology.Start == nil {
				t.Fatal("layout has no start room")
			}
			room, ok := topology.Room(*topology.Start)
			if !ok || room.Map == nil {
				t.Fatal("start room unresolved")
			}
			m := room.Map.Map
			if m.ID != tc.mapID || m.ID <= 65535 {
				t.Fatalf("map id=%d want %d (>65535)", m.ID, tc.mapID)
			}
			if !strings.Contains(strings.ToLower(m.Path), tc.mapPath) || m.DungeonID.Value != tc.id {
				t.Fatalf("map path/dungeon=%q/%+v", m.Path, m.DungeonID)
			}
			if m.TournamentDefinitionMalformed || len(m.Monsters) != 0 || len(m.AICharacters) != 0 {
				t.Fatalf("map ordinary actors/malformed=%d/%d/%v", len(m.Monsters), len(m.AICharacters), m.TournamentDefinitionMalformed)
			}
			if len(m.TournamentEnemyCandidates) != 15 {
				t.Fatalf("candidates=%d want 15", len(m.TournamentEnemyCandidates))
			}
			for i, want := range tc.rows {
				got := m.TournamentEnemyCandidates[i]
				if got.PartyCount != 1 || got.IsApc || got.Code != want.code || got.Strength != want.strength || got.Name != want.name {
					t.Fatalf("candidate[%d]=%+v want {%d %d %q}", i, got, want.code, want.strength, want.name)
				}
				if def, found, err := monsters.Find(got.Code); err != nil || !found || def.ID != got.Code {
					t.Fatalf("candidate code %d unresolved: found=%t err=%v", got.Code, found, err)
				}
			}
			for _, code := range tc.specials {
				found := false
				for _, c := range m.TournamentEnemyCandidates {
					found = found || c.Code == code
				}
				if !found {
					t.Fatalf("special candidate %d missing", code)
				}
			}
			areas := m.TournamentStartAreas
			if len(areas) != 2 || areas[0] != (worldmap.TournamentStartAreaInfo{PartyCount: 1, X: 360, Y: 270, Direction: 1}) ||
				areas[1] != (worldmap.TournamentStartAreaInfo{PartyCount: 1, X: 760, Y: 270, Direction: 2}) {
				t.Fatalf("start areas=%+v", areas)
			}

			definition, reason, ok := resolveTournamentDungeonDefinition(dungeon, m, nil)
			if !ok {
				t.Fatalf("definition rejected: %s", reason)
			}
			runtime, reason, ok := newTournamentDungeonRuntime(&definition, 1, deterministicTournamentNext(uint32(tc.id)))
			if !ok {
				t.Fatalf("runtime rejected: %s", reason)
			}
			body386, err := buildTournamentInfoBody(runtime, 2, 402)
			if err != nil {
				t.Fatal(err)
			}
			if len(body386) != 260 {
				t.Fatalf("386 body_len=%d want 260", len(body386))
			}
			if got := binary.LittleEndian.Uint32(body386[:4]); got != uint32(tc.id) || body386[4] != 2 || body386[5] != 1 {
				t.Fatalf("386 header=%d/%d/%d", got, body386[4], body386[5])
			}
			offset := 6
			teamCounts := []int{16, 8, 4, 2}
			for round := 0; round < 4; round++ {
				teams := int(body386[offset+1])
				if body386[offset] != byte(round+1) || teams != teamCounts[round] {
					t.Fatalf("386 bracket round=%d teams=%d", body386[offset], teams)
				}
				offset += 2 + teams*(1+6)
			}
			var types []byte
			var codes []int32
			for round := 0; round < 4; round++ {
				offset++
				if binary.LittleEndian.Uint16(body386[offset:]) != uint16(402+round) {
					t.Fatalf("386 sequence round %d", round+1)
				}
				offset += 2
				codes = append(codes, int32(binary.LittleEndian.Uint32(body386[offset:])))
				offset += 4
				if body386[offset] != 75 {
					t.Fatalf("386 actor level=%d", body386[offset])
				}
				offset++
				types = append(types, body386[offset])
				offset++
			}
			if offset != len(body386) || len(types) != 4 || types[0] != 0 || types[1] != 0 || types[2] != 0 || types[3] != 3 {
				t.Fatalf("386 tail offset=%d/%d types=%v", offset, len(body386), types)
			}
			for i, code := range codes {
				if code != int32(runtime.PathActors[i].Code) {
					t.Fatalf("386 code[%d]=%d path=%d", i, code, runtime.PathActors[i].Code)
				}
			}

			body387 := buildTournamentMapInfoBody(0, 0, 0x5EED1234, m.ID, false)
			if len(body387) != 15 || body387[6] != 0 || body387[7] != 1 || binary.LittleEndian.Uint32(body387[8:12]) != uint32(tc.mapID) ||
				body387[12] != 0 || body387[13] != 0 || body387[14] != 0 {
				t.Fatalf("387 body=%x", body387)
			}
		})
	}
}
