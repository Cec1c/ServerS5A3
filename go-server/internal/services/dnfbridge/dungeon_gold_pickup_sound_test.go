package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

func TestBuildCurrentDungeonGoldPickupResultNativeSlotsAndBounds(t *testing.T) {
	for slot := 0; slot < 8; slot++ {
		body, err := buildCurrentDungeonGoldPickupResultBody(403, 99, math.MaxInt32, slot)
		if err != nil {
			t.Fatal(err)
		}
		assertCurrentNativeGoldPickupRows(t, body, 403, 99, slot, math.MaxInt32)
	}
	for _, test := range []struct {
		object uint32
		picker uint16
		amount uint32
		slot   int
	}{
		{0, 99, 120, 0}, {403, 0, 120, 0}, {403, 99, 0, 0},
		{403, 99, math.MaxInt32 + 1, 0}, {403, 99, 120, -1}, {403, 99, 120, 8},
	} {
		if _, err := buildCurrentDungeonGoldPickupResultBody(test.object, test.picker, test.amount, test.slot); !errors.Is(err, errDungeonPickupResponseInvalid) {
			t.Fatalf("invalid native gold values accepted: %+v err=%v", test, err)
		}
	}
}

func TestCurrentDungeonGoldPickupRejectsInvalidProjectionBeforeGrant(t *testing.T) {
	for _, missingMember := range []bool{false, true} {
		s, runtime, repos := prepareCurrentDungeonDropTest(t, 0, 3227)
		object := seedCurrentDungeonPickupDrop(t, s, runtime, 3227, 99)
		drop := runtime.DropOwner.byObjectKey[object]
		drop.Item = dungeonDropItemDefinition{}
		drop.Amount = math.MaxInt32 + 1
		conn := &bufferConn{}
		p := &gameSession{conn: conn, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
		if missingMember {
			drop.Amount = 120
			storeRuntimePartyState(p, alignedcmd.PartyState{PartyID: 1, UserID: 88, Members: []alignedcmd.PartyMemberState{{UserID: 88}}})
		}
		if err := s.handleCurrentDungeonPickup(p, currentDungeonPickupTestBody(object)); err != nil {
			t.Fatal(err)
		}
		character, found, err := repos.Character.Load(context.Background(), "99")
		if err != nil || !found || character.Stats["gold"] != 0 || drop.Status != runtimeDungeonDropAvailable ||
			len(drop.PickupResponseBody) != 0 || conn.write.Len() != 0 {
			t.Fatalf("invalid gold projection committed: missing-member=%t character=%+v drop=%+v wire=%x err=%v", missingMember, character, drop, conn.write.Bytes(), err)
		}
	}
}

// 金币 op39 使用队伍固定槽的八行投影，而普通物品仍是 19 字节单槽结果。
// 每行是 u8 present + u32 amount；present 行再跟 u8 component-count + 组件
// + u32 尾标志；中选行的组件数 1、组件值 0 组合由附件夹具给出。
// 原生解码与实际音效由主控另行核实，本测试只固定线上载荷布局。
func TestCurrentDungeonGoldPickupSoundProjectionAndReplay(t *testing.T) {
	for _, test := range []struct {
		name  string
		state alignedcmd.PartyState
		slot  int
	}{
		{name: "solo"},
		{name: "party member", state: alignedcmd.PartyState{PartyID: 1, UserID: 88, Members: []alignedcmd.PartyMemberState{{UserID: 88}, {UserID: 99, Slot: 1}}}, slot: 1},
		{name: "fixed slot after another member left", state: alignedcmd.PartyState{PartyID: 1, UserID: 88, Members: []alignedcmd.PartyMemberState{{UserID: 88}, {UserID: 99, Slot: 3}}}, slot: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, runtime, repos := prepareCurrentDungeonDropTest(t, 0, 3227)
			source := s.dungeonMonsterTable.source.(bridgePVFSource)
			source[dungeonCardDropRulePath] = currentDungeonGoldDropTestRuleText()
			source[dungeonCardGoldReferencePath] = "[gold drop ref table]\n20 100 0\n"
			currentDungeonMonsterDropRulesByCatalog.Delete(s.dungeonMonsterTable)
			currentDungeonGoldReferencesByCatalog.Delete(s.dungeonMonsterTable)
			conn := &bufferConn{}
			p := &gameSession{conn: conn, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
			storeRuntimePartyState(p, test.state)
			wires, err := s.planCurrentDungeonMonsterDrops(runtime, runtime.Room.Snapshot().Monsters[0], currentSceneActorObjectKey(99))
			if err != nil {
				t.Fatal(err)
			}
			var gold *runtimeDungeonDrop
			for _, wire := range wires {
				if drop := runtime.DropOwner.byObjectKey[wire.SceneObjectKey]; drop != nil && drop.isGold() {
					gold = drop
					break
				}
			}
			if gold == nil || gold.Amount != 100 {
				t.Fatalf("known PVF gold drop=%+v", gold)
			}
			var first []byte
			for attempt := 0; attempt < 2; attempt++ {
				conn.write.Reset()
				if err := s.handleCurrentDungeonPickup(p, currentDungeonPickupTestBody(gold.ObjectKey)); err != nil {
					t.Fatal(err)
				}
				pickup, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
				wallet, tail := splitGameServerUpperPacket(t, rest)
				if pickup.Header.MsgID != 39 || pickup.Header.Classification != 0 ||
					wallet.Header.MsgID != 14 || wallet.Header.Classification != 0 || len(tail) != 0 ||
					!bytes.Equal(wallet.Body, buildCurrentGoldStateBody(100)) {
					t.Fatalf("pickup=%+v wallet=%+v tail=%x", pickup, wallet, tail)
				}
				assertCurrentNativeGoldPickupRows(t, pickup.Body, gold.ObjectKey, 99, test.slot, 100)
				if attempt == 0 {
					first = append([]byte(nil), pickup.Body...)
				} else if !bytes.Equal(first, pickup.Body) {
					t.Fatalf("retry changed frozen gold feedback: first=%x replay=%x", first, pickup.Body)
				}
			}
			character, found, err := repos.Character.Load(context.Background(), "99")
			if err != nil || !found || character.Stats["gold"] != 100 || gold.Status != runtimeDungeonDropConsumed {
				t.Fatalf("pickup/replay wallet=%+v drop=%+v err=%v", character, gold, err)
			}
		})
	}
}

func assertCurrentNativeGoldPickupRows(t *testing.T, body []byte, object uint32, picker uint16, slot int, amount uint32) {
	t.Helper()
	if len(body) != 55 || binary.LittleEndian.Uint32(body[:4]) != object || binary.LittleEndian.Uint16(body[4:6]) != picker {
		t.Fatalf("native gold header/length: body=%x len=%d", body, len(body))
	}
	at := 6
	for row := 0; row < 8; row++ {
		present := body[at]
		value := binary.LittleEndian.Uint32(body[at+1 : at+5])
		at += 5
		if row != slot {
			if present != 0 || value != 0 {
				t.Fatalf("unawarded native party row %d: present=%d amount=%d", row, present, value)
			}
			continue
		}
		if present != 1 || value != amount || body[at] != 1 ||
			binary.LittleEndian.Uint32(body[at+1:at+5]) != 0 || binary.LittleEndian.Uint32(body[at+5:at+9]) != 1 {
			t.Fatalf("native gold effect row %d: present=%d amount=%d effect=%x", row, present, value, body[at:at+9])
		}
		at += 9
	}
	if at != len(body) {
		t.Fatalf("native parser stopped at %d of %d", at, len(body))
	}
}
