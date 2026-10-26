package dnfbridge

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
)

// 使用现用PVF重建3797的起点与第二房，经正式过门和死亡处理器重放脱敏418结构夹具。
// 只沿用现场对象发号范围；不重放首房随机掉落、编译后AI或游戏画面。
func TestRealPVFMission3797InformerCapturedRetirement(t *testing.T) {
	source, table, resolver := loadRealStoryPVF(t)
	ctx := context.Background()
	index, err := dnfpvf.Build(ctx, source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	quests, err := dnfquest.Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	ai, err := newPVFDungeonAICharacterCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("清怪前实测legacy报告", func(t *testing.T) {
		early := prepareMissionInformerService(t, source, table, resolver, monsters, ai, quests)
		early.session.dungeon.runtime.NextObjectKey = 409
		moveMissionInformerToSecondRoom(t, early)
		assertMissionInformerEarlyRetirementAndRoomGate(t, early, 400077)
	})
	for _, clearBefore := range []bool{false, true} {
		label := "对象419清怪前结构帧"
		if clearBefore {
			label = "对象419清怪后结构帧"
		}
		t.Run(label, func(t *testing.T) {
			latest := prepareMissionInformerService(t, source, table, resolver, monsters, ai, quests)
			latest.session.dungeon.runtime.NextObjectKey = 410
			moveMissionInformerToSecondRoom(t, latest)
			if clearBefore {
				clearMissionInformerOrdinaryMonsters(t, latest)
			}
			assertMissionInformerRetirementAndRoomGate(t, latest, 400077, missionInformerLatestFrame(t))
		})
	}
	f := prepareMissionInformerService(t, source, table, resolver, monsters, ai, quests)
	runtime := f.session.dungeon.runtime
	if runtime.Dungeon.Mazes[runtime.MazeIndex].QuestConnection[1] != 3797 || runtime.MazeIndex != 6 {
		t.Fatalf("未选中真实3797迷宫：%d", runtime.MazeIndex)
	}
	// 现场首房一个地面掉落已经占用408；其随机产出不属于本生命周期回归。
	runtime.NextObjectKey = 409
	moveMissionInformerToSecondRoom(t, f)
	actor := missionInformerActor(t, f)
	if actor.ObjectKey != 418 || actor.AICharacterMetadata == nil || actor.AICharacterMetadata.Name != "情报员因弗" || actor.Packet.Blocking != 0 {
		t.Fatalf("真实情报员身份不符：%+v", actor)
	}
	f.conn.write.Reset()
	before := missionInformerAssets(t, f)
	// 保留第一份清怪后报告的回归；退场顺序本身不再作为演员身份。
	clearMissionInformerOrdinaryMonsters(t, f)
	scene, _ := runtime.Session.Scene()
	if !scene.Cleared || len(scene.BlockingHostiles) != 9 || len(scene.DefeatedObjects) != 9 {
		t.Fatalf("未形成现场九怪已清的前提：%+v", scene)
	}
	if err := f.svc.handleDungeonMonsterDeath(f.session, missionInformerCapturedBody(t, 418)); err != nil {
		t.Fatal(err)
	}
	first := assertMissionInformerOnlyDeath(t, f.conn.write.Bytes(), 418)
	f.conn.write.Reset()
	if err := f.svc.handleDungeonMonsterDeath(f.session, missionInformerCapturedBody(t, 418)); err != nil {
		t.Fatal(err)
	}
	if again := assertMissionInformerOnlyDeath(t, f.conn.write.Bytes(), 418); !bytes.Equal(first, again) {
		t.Fatal("真实房间重放改变通知")
	}
	if !reflect.DeepEqual(before, missionInformerAssets(t, f)) {
		t.Fatal("真实情报员离场改动角色、库存或3797目标进度")
	}
	f.conn.write.Reset()
	if err := f.svc.handleDungeonMonsterDeath(f.session, missionInformerCapturedBody(t, 853)); err != nil {
		t.Fatal(err)
	}
	if f.conn.write.Len() != 0 {
		t.Fatal("853装饰对象被误作情报员确认")
	}
	move := make([]byte, dungeoncmd.MoveMapRequestSize)
	move[0] = 1
	if err := f.svc.handleDungeonMoveMap(f.session, move); err != nil {
		t.Fatal(err)
	}
	after, _ := runtime.Session.Scene()
	if after.Map.Map.ID != 400077 {
		t.Fatalf("不能进入真实第三房：map=%d", after.Map.Map.ID)
	}
	f.conn.write.Reset()
	if err := f.svc.handleDungeonMonsterDeath(f.session, missionInformerCapturedBody(t, 418)); err != nil {
		t.Fatal(err)
	}
	if f.conn.write.Len() != 0 {
		t.Fatal("真实离房后重放了旧418")
	}
}
