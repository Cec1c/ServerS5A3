package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件钉住「深渊房间两根柱子」的修复：
// `DNFBRIDGE_SPECIAL_OBJECT_PARENT_ACTOR`（见 dungeon_special_object_parent_actor.go）。
//
// 两档都在这里落地：
//   - 默认档 `map_owned`：op29 的 actor 表里**没有** type-9 父对象行，柱子的
//     code（30528 / `40770000` 小端）**不再出现在整条 START_MAP body 里**；
//   - 回退档 `start_map`：逐字节回到改动前（含那条 type-9 行）。
//
// 现场判据（`_dist/hell-room-two-pillars-diagnosis.md` §1.3 / §4 验收）：
// 进房那条 `SEND msg_id=29` 的 `actor_count` 由 7 变 6、`plain_body_len` 由 170 变 149，
// 且 hex 不再含 `40770000`。

const testSpecialObjectParentActorHellPillarID = 30528

// testHellRoomPillarScene 是一间"有一根柱子"的房间，形状取自真档 map 60083 的
// `[special passive object]`（柱子 30528 + 行内 `[hellparty]` 索引）加上房图自带的
// 6 只小怪 —— 但小怪直接由 scene.Monsters 提供，不需要 PVF。
func testHellRoomPillarScene() worldmap.DungeonRoomScene {
	monsters := make([]worldmap.MonsterSpawn, 0, 6)
	for _, code := range []int64{71008, 71008, 71008, 71010, 71010, 71010} {
		monsters = append(monsters, worldmap.MonsterSpawn{
			MonsterID: code,
			AutoLevel: 90,
			Placement: "[fixed]",
			Rank:      "[normal]",
		})
	}
	return worldmap.DungeonRoomScene{
		Coordinate: worldmap.RoomCoordinate{X: 3, Y: 0},
		Map:        worldmap.ResolvedMap{Map: worldmap.Map{ID: 60083}},
		Monsters:   monsters,
		SpecialPassiveObjects: []worldmap.SpecialPassiveObject{{
			PassiveObject: worldmap.PassiveObject{ObjectID: testSpecialObjectParentActorHellPillarID, X: 594, Y: 301},
			HellParty: []worldmap.HellPartyEntry{
				{GroupID: 125, Rate: 25, Order: 1},
				{GroupID: 211, Rate: 20, Order: 2},
			},
		}},
	}
}

func testHellRoomPillarSource() bridgePVFSource {
	return bridgePVFSource{
		"monster/monster.lst": "71008 `gakcle_hammerer.mob`\n71010 `gakcle_sparker.mob`\n" +
			"30528 `SealedGateOfHellDungeon4.obj`\n",
		"monster/gakcle_hammerer.mob":          "[name]\n`Hammerer`\n",
		"monster/gakcle_sparker.mob":           "[name]\n`Sparker`\n",
		"monster/SealedGateOfHellDungeon4.obj": "[name]\n`Sealed Gate`\n",
	}
}

// TestSpecialObjectParentActorHellRoomStartMapOmitsPillarRow 是缺陷 A 的主用例：
// 默认档下 op29 的 actor 表里没有 type-9 父对象行、body 里没有柱子的 code；
// 回退档下逐字节回到改动前。
func TestSpecialObjectParentActorHellRoomStartMapOmitsPillarRow(t *testing.T) {
	scene := testHellRoomPillarScene()
	monsterCatalog, err := newPVFDungeonMonsterCatalog(testHellRoomPillarSource())
	if err != nil {
		t.Fatal(err)
	}
	buildBody := func(t *testing.T) []byte {
		t.Helper()
		room, nextObjectKey, err := newRuntimeDungeonRoom(scene, monsterCatalog, firstDungeonMonsterObjectKey)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := planRuntimeDungeonExtendedActors(
			scene, monsterCatalog, nil, worldmap.OptionalInt{Value: 90, Set: true}, nextObjectKey)
		if err != nil {
			t.Fatal(err)
		}
		if err := room.AttachExtendedActors(plan); err != nil {
			t.Fatal(err)
		}
		runtime := &runtimeDungeonState{
			Request:        dungeoncmd.SelectDungeonRequest{DungeonID: 310},
			Dungeon:        worldmap.Dungeon{ID: 310},
			Session:        &worldmap.DungeonSession{},
			Room:           room,
			NextObjectKey:  plan.NextObjectKey,
			BossCoordinate: scene.Coordinate,
			BossSet:        true,
			Seed:           1,
		}
		packet, err := currentDungeonStartMapFromRuntime(runtime, scene, currentDungeonStartMapState{
			HellPartyMode: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		body, err := packet.Build()
		if err != nil {
			t.Fatal(err)
		}
		return body
	}

	t.Run("default_map_owned_drops_the_parent_object_row", func(t *testing.T) {
		body := buildBody(t)
		if body[18] != 6 {
			t.Fatalf("actor count=%d body=%x", body[18], body)
		}
		// 6 只小怪 × 21 字节 + 固定前缀 19 字节 + 尾部 4 字节 = 149。
		if len(body) != 149 {
			t.Fatalf("body len=%d want=149 body=%x", len(body), body)
		}
		if bytes.Contains(body, []byte{0x40, 0x77, 0x00, 0x00}) {
			t.Fatalf("default tier still carries the pillar code 30528: %x", body)
		}
		for offset := 19; offset < 19+6*21; offset += 21 {
			// 行布局（`dungeon_start_map.go:179-190`）：TemplateOrder u16 / PacketIndex u32 /
			// ObjectKey u16 / Code u32 / Level / Type / Flag0 / Flag1 / ExtraState u32 / Blocking。
			if body[offset+13] == 9 {
				t.Fatalf("default tier still carries a type-9 actor row at offset=%d body=%x", offset, body)
			}
		}
	})

	t.Run("start_map_rollback_restores_the_type9_row", func(t *testing.T) {
		t.Setenv(specialObjectParentActorEnv, specialObjectParentActorStartMapValue)
		body := buildBody(t)
		if body[18] != 7 {
			t.Fatalf("actor count=%d body=%x", body[18], body)
		}
		// 7 行 × 21 字节 + 19 + 4 = 170（现场那条 op29 的 plain_body_len）。
		if len(body) != 170 {
			t.Fatalf("body len=%d want=170 body=%x", len(body), body)
		}
		if !bytes.Contains(body, []byte{0x40, 0x77, 0x00, 0x00}) {
			t.Fatalf("rollback tier lost the pillar code 30528: %x", body)
		}
		pillarOffset := 19 + 6*21
		if got := binary.LittleEndian.Uint32(body[pillarOffset+8 : pillarOffset+12]); got != testSpecialObjectParentActorHellPillarID {
			t.Fatalf("pillar row code=%d body=%x", got, body)
		}
		if body[pillarOffset+13] != 9 {
			t.Fatalf("pillar row type=%d body=%x", body[pillarOffset+13], body)
		}
	})
}

// TestSpecialObjectParentActorDefaultKeepsInlineMonsterTemplates 钉住"只删父对象行、
// 行内 `[monster]` 模板行一个字段都不动"（厂商 DungeonActorTemplateProjector.cs:279-298：
// Type=0、Level=子模板等级、TemplateOrder=对象下标、PacketIndex=子模板下标、
// Flag0=1、Flag1=对象下标）。
func TestSpecialObjectParentActorDefaultKeepsInlineMonsterTemplates(t *testing.T) {
	catalog, err := newPVFDungeonMonsterCatalog(bridgePVFSource{
		"monster/monster.lst":         "71008 `gakcle_hammerer.mob`\n71010 `gakcle_sparker.mob`\n30528 `pillar.obj`\n9001 `child.mob`\n",
		"monster/gakcle_hammerer.mob": "[name]\n`Hammerer`\n",
		"monster/gakcle_sparker.mob":  "[name]\n`Sparker`\n",
		"monster/pillar.obj":          "[name]\n`Sealed Gate`\n",
		"monster/child.mob":           "[name]\n`Child`\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	scene := testHellRoomPillarScene()
	scene.SpecialPassiveObjects[0].Spawns = []worldmap.SpecialObjectSpawn{{Kind: "[monster]", Code: 9001, Level: 0}}

	for _, tier := range []string{"map_owned", "start_map"} {
		t.Run(tier, func(t *testing.T) {
			if tier == "start_map" {
				t.Setenv(specialObjectParentActorEnv, specialObjectParentActorStartMapValue)
			}
			plan, err := planRuntimeDungeonExtendedActors(
				scene, catalog, nil, worldmap.OptionalInt{Value: 90, Set: true}, 700)
			if err != nil {
				t.Fatal(err)
			}
			var child *runtimeDungeonExtendedActor
			for index := range plan.Actors {
				if plan.Actors[index].Kind == runtimeDungeonActorSpecialMonster {
					child = &plan.Actors[index]
					break
				}
			}
			if child == nil {
				t.Fatalf("inline [monster] template row is missing: %+v", plan.Actors)
			}
			if child.Packet.Code != 9001 || child.Packet.Type != 0 || child.Packet.Level != 90 ||
				child.Packet.TemplateOrder != 0 || child.Packet.PacketIndex != 0 ||
				child.Packet.Flag0 != 1 || child.Packet.Flag1 != 0 || child.Packet.Blocking != 0 {
				t.Fatalf("inline [monster] template row changed: %+v", child.Packet)
			}
			// 父对象行的**有无**是两档唯一差别。
			gotParentRow := plan.Actors[0].Kind == runtimeDungeonActorSpecialObject
			wantParentRow := tier == "start_map"
			if gotParentRow != wantParentRow {
				t.Fatalf("tier=%s parent row=%t want=%t actors=%+v", tier, gotParentRow, wantParentRow, plan.Actors)
			}
		})
	}
}

// TestSpecialObjectParentActorDefaultKeepsChildRetirementPath 钉住默认档的**副作用可控**：
// 父对象行没了 ⇒ 子模板行的 objectKey 前移 1，但既有的
// `RetireSpecialMonsterActor`（收下 op39、回一条 op38）逐字节仍然可用。
func TestSpecialObjectParentActorDefaultKeepsChildRetirementPath(t *testing.T) {
	source := bridgeDungeonPVF(false)
	source["map/dungeon/test/start.map"] += "[special passive object]\n7001 30 20 0 1 `[monster]` 3001 20 0 0 0\n"
	runtime := prepareSyntheticDungeonRuntimeForEntryTest(t, source, nil)
	if count, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil || count != 2 {
		t.Fatalf("announced count=%d error=%v", count, err)
	}
	snapshot := runtime.Room.Snapshot()
	if len(snapshot.ExtendedActors) != 1 ||
		snapshot.ExtendedActors[0].Kind != runtimeDungeonActorSpecialMonster ||
		snapshot.ExtendedActors[0].ObjectKey != 403 ||
		snapshot.ExtendedActors[0].HostileReference != nil ||
		snapshot.ExtendedActors[0].State != runtimeDungeonMonsterAnnounced {
		t.Fatalf("default tier special actors=%+v", snapshot.ExtendedActors)
	}
	target, matched, err := runtime.Room.RetireSpecialMonsterActor(403)
	if err != nil || !matched || target.ObjectKey != 403 || target.ResponseKind != currentDungeonDeathResponseMonster {
		t.Fatalf("default tier retirement target=%+v matched=%t error=%v", target, matched, err)
	}
	if state := runtime.Room.Snapshot().ExtendedActors[0].State; state != runtimeDungeonMonsterDefeated {
		t.Fatalf("default tier retirement state=%s", state)
	}
}

// TestSpecialObjectParentActorSwitchReadsOnlyTheLiteralRollbackValue 钉住开关语义：
// **只有**显式 `start_map` 才回退，其它任何值（含 `on`/`1`/空）都取默认修复档。
func TestSpecialObjectParentActorSwitchReadsOnlyTheLiteralRollbackValue(t *testing.T) {
	if got := readSpecialObjectParentActorMode("").String(); got != "map_owned" {
		t.Fatalf("empty value=%s", got)
	}
	for _, value := range []string{"map_owned", "on", "1", "true", "yes", "startmap", "start map", "legacy"} {
		if got := readSpecialObjectParentActorMode(value); got != specialObjectParentActorMapOwned {
			t.Fatalf("value=%q resolved to %s want map_owned", value, got)
		}
	}
	for _, value := range []string{"start_map", " START_MAP ", "Start_Map"} {
		if got := readSpecialObjectParentActorMode(value); got != specialObjectParentActorStartMap {
			t.Fatalf("value=%q resolved to %s want start_map", value, got)
		}
	}
	t.Setenv(specialObjectParentActorEnv, "start_map")
	if got := specialObjectParentActorModeFromEnv().String(); got != "start_map" {
		t.Fatalf("env rollback value=%s", got)
	}
	t.Setenv(specialObjectParentActorEnv, "")
	if got := specialObjectParentActorModeFromEnv().String(); got != "map_owned" {
		t.Fatalf("env default value=%s", got)
	}
}
