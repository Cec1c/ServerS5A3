package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// 联合安装后的现场：情报员先于九只普通怪报告，服务端拒绝后没有再次上报。
// 按13字节legacy头重建结构；校验和及不透明运行数据为合成值，ac 00 05 01不解释为原因。
const missionInformerEarlyLegacyFrame = "01270057000000000000007600a2010000ffff00000000000000000000000000000000000000000000000000ac0005014d5e6f8091a2b3c4d5e6f708192a3b4c5d6e7f90a1b2c3d4e5f60718293a4b5c6d7e8fa0b1c2d3"

// 对象419及不同运行块前缀的脱敏结构夹具；不含私人原始录包。
const missionInformerLatestLegacyFrame = "01270057000000000000009700a3010000ffff00000000000000000000000000000000000000000000000000a40005014d5e6f8091a2b3c4d5e6f708192a3b4c5d6e7f90a1b2c3d4e5f60718293a4b5c6d7e8fa0b1c2d3"

func missionInformerEarlyFrame(t *testing.T) []byte {
	t.Helper()
	return missionInformerLegacyFrame(t, missionInformerEarlyLegacyFrame, 418, 118)
}

func missionInformerLatestFrame(t *testing.T) []byte {
	t.Helper()
	return missionInformerLegacyFrame(t, missionInformerLatestLegacyFrame, 419, 151)
}

func missionInformerLegacyFrame(t *testing.T, captured string, key uint32, sequence uint16) []byte {
	t.Helper()
	raw, err := hex.DecodeString(captured)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := dnfproto.ParseLegacyGamePacket(raw)
	if err != nil || len(raw) != 87 || len(packet.Body) != 74 || packet.Header.Sequence != sequence ||
		packet.Header.Cmd != 1 || packet.Header.Type != 39 || binary.LittleEndian.Uint32(packet.Body) != key {
		t.Fatalf("情报员现场帧不符：packet=%+v err=%v", packet, err)
	}
	return raw
}

func assertMissionInformerEarlyRetirementAndRoomGate(t *testing.T, f missionInformerFixture, nextMap int64) {
	t.Helper()
	assertMissionInformerRetirementAndRoomGate(t, f, nextMap, missionInformerEarlyFrame(t))
}

func assertMissionInformerRetirementAndRoomGate(t *testing.T, f missionInformerFixture, nextMap int64, raw []byte) {
	t.Helper()
	runtime := f.session.dungeon.runtime
	actor := missionInformerActor(t, f)
	if binary.LittleEndian.Uint32(raw[dnfproto.LegacyGameHeaderSize:]) != actor.ObjectKey {
		t.Fatal("原生报文没有绑定当前在册情报员")
	}
	initialScene, _ := runtime.Session.Scene()
	before := missionInformerAssets(t, f)
	monsters := runtime.Room.Snapshot().Monsters
	f.conn.write.Reset()
	if err := f.svc.handleLegacyGamePacket(f.session, raw); err != nil {
		t.Fatal(err)
	}
	first := assertMissionInformerOnlyDeath(t, f.conn.write.Bytes(), actor.ObjectKey)
	scene, _ := runtime.Session.Scene()
	if scene.Cleared != initialScene.Cleared || len(scene.BlockingHostiles) != 9 || len(scene.DefeatedObjects) != len(initialScene.DefeatedObjects)+1 ||
		!dungeonSceneObjectDefeated(scene.DefeatedObjects, actor.ObjectKey) ||
		!reflect.DeepEqual(monsters, runtime.Room.Snapshot().Monsters) || !reflect.DeepEqual(before, missionInformerAssets(t, f)) {
		t.Fatal("情报员退场改变了九怪、清房状态或玩家资产")
	}
	f.conn.write.Reset()
	if err := f.svc.handleLegacyGamePacket(f.session, raw); err != nil {
		t.Fatal(err)
	}
	if replay := assertMissionInformerOnlyDeath(t, f.conn.write.Bytes(), actor.ObjectKey); !bytes.Equal(first, replay) ||
		!reflect.DeepEqual(before, missionInformerAssets(t, f)) {
		t.Fatal("同一退场重报改变通知或资产")
	}
	move := make([]byte, dungeoncmd.MoveMapRequestSize)
	move[0] = 1
	if !initialScene.Cleared {
		f.conn.write.Reset()
		if err := f.svc.handleDungeonMoveMap(f.session, move); err != nil {
			t.Fatal(err)
		}
		scene, _ = runtime.Session.Scene()
		if scene.Map.Map.ID != 400076 || scene.Cleared {
			t.Fatal("九怪未清即允许离开第二房")
		}
	}
	clearMissionInformerOrdinaryMonsters(t, f)
	scene, _ = runtime.Session.Scene()
	if !scene.Cleared || len(scene.DefeatedObjects) != 10 {
		t.Fatal("九怪清完后房间未正常清空")
	}
	// 不再重报情报员；九怪自身清完即可过门。
	f.conn.write.Reset()
	if err := f.svc.handleDungeonMoveMap(f.session, move); err != nil {
		t.Fatal(err)
	}
	scene, _ = runtime.Session.Scene()
	if scene.Map.Map.ID != nextMap {
		t.Fatalf("九怪清完后不能进入第三房：map=%d", scene.Map.Map.ID)
	}
	f.conn.write.Reset()
	if err := f.svc.handleLegacyGamePacket(f.session, raw); err != nil {
		t.Fatal(err)
	}
	if f.conn.write.Len() != 0 {
		t.Fatal("离房后旧情报员报告仍被确认")
	}
}

func TestMissionInformerLatestCaptured419Retirement(t *testing.T) {
	f := prepareMissionInformerFixtureAtKey(t, true, 410)
	assertMissionInformerRetirementAndRoomGate(t, f, 400080, missionInformerLatestFrame(t))
}

func TestMissionInformerRuntimeFieldsAndObjectKeys(t *testing.T) {
	for _, nextKey := range []uint32{409, 410, 1091} {
		for _, defeatedCount := range []int{0, 4, 9} {
			for _, variant := range []string{"最新样本运行块", "全零运行块", "全FF运行块", "全部字节变化"} {
				t.Run(fmt.Sprintf("对象%d/已清%d/%s", nextKey+9, defeatedCount, variant), func(t *testing.T) {
					f := prepareMissionInformerFixtureAtKey(t, false, nextKey)
					runtime := f.session.dungeon.runtime
					for _, monster := range runtime.Room.Snapshot().Monsters[:defeatedCount] {
						if _, _, err := runtime.Room.CommitActorDeathReport(monster.ObjectKey, runtime.Session); err != nil {
							t.Fatal(err)
						}
					}
					body := append([]byte(nil), missionInformerLatestFrame(t)[dnfproto.LegacyGameHeaderSize:]...)
					binary.LittleEndian.PutUint32(body, missionInformerActor(t, f).ObjectKey)
					// 合成夹具只改变不参与身份、战斗或资产校验的43字节运行块/footer。
					for i := 31; i < len(body); i++ {
						switch variant {
						case "全零运行块":
							body[i] = 0
						case "全FF运行块":
							body[i] = 255
						case "全部字节变化":
							body[i] = byte(i*37 + 71)
						}
					}
					assertMissionInformerRetirementAndRoomGate(t, f, 400080, mission4874LegacyFrame(151, body))
				})
			}
		}
	}
}

func TestMissionInformerEarlyLegacyRetirementKeepsRoomGate(t *testing.T) {
	f := prepareMissionInformerFixture(t, false)
	assertMissionInformerEarlyRetirementAndRoomGate(t, f, 400080)
}

func TestMissionInformerEarlyRetirementRejectsUnprovedBindings(t *testing.T) {
	for _, name := range []string{"wrong character", "wrong run", "wrong account", "wrong room", "no active quest", "wrong definition path", "wrong packet code", "blocking actor", "unknown object", "oversized tail", "combat prefix", "unannounced monster"} {
		t.Run(name, func(t *testing.T) {
			f := prepareMissionInformerFixture(t, false)
			runtime := f.session.dungeon.runtime
			body := append([]byte(nil), missionInformerEarlyFrame(t)[dnfproto.LegacyGameHeaderSize:]...)
			switch name {
			case "wrong character":
				f.session.selectedCharacterID = 2
			case "wrong run":
				runtime.lifecycleToken++
			case "wrong account":
				f.svc.options.accountID = "other-account"
			case "wrong room":
				runtime.Room.mapID++
			case "no active quest":
				q, _, _ := f.repos.Quest.Load(context.Background(), "1")
				delete(q.States, 3797)
				if err := f.repos.Quest.Save(context.Background(), q); err != nil {
					t.Fatal(err)
				}
			case "wrong definition path":
				ai := f.svc.dungeonAICharacterTable.cache[11117]
				ai.Path = "AICharacter/MissionQuest/Gunner/CodenameInfo/CodenameInfo.aic"
				f.svc.dungeonAICharacterTable.cache[11117] = ai
				runtime.Room.extendedActors[0].AICharacterMetadata = &ai
			case "wrong packet code":
				runtime.Room.extendedActors[0].Packet.Code++
			case "blocking actor":
				runtime.Room.extendedActors[0].Packet.Blocking = 1
			case "unknown object":
				binary.LittleEndian.PutUint32(body, 853)
			case "oversized tail":
				body = append(body, 0)
			case "combat prefix":
				body[18] = 1
			case "unannounced monster":
				runtime.Room.monsters[0].State = "created"
			}
			before := missionInformerAssets(t, f)
			if err := f.svc.handleLegacyGamePacket(f.session, mission4874LegacyFrame(118, body)); err != nil {
				t.Fatal(err)
			}
			if _, defeated := runtime.Room.DefeatedDeathTarget(418); defeated || f.conn.write.Len() != 0 ||
				!reflect.DeepEqual(before, missionInformerAssets(t, f)) {
				t.Fatal("未证明的清怪前报告改动演员、资产或通知")
			}
		})
	}
}
