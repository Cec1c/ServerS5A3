package dnfbridge

import (
	"context"
	"encoding/hex"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealScriptPVFJobChangeTrialEntryAndReturn(t *testing.T) {
	pvfPath := os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNF_WORLDMAP_REAL_PVF_SMOKE to run real 转职体验 entry smoke")
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
	aiCharacters, err := newPVFDungeonAICharacterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		level int
		grow  int64
	}{{"15级首次转职体验", 15, 0}, {"25级已转职体验", 25, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			repositories := dnfrepomemory.NewMemoryGroup()
			if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
				CharacterID: "19",
				AccountID:   "account-1",
				Level:       tc.level,
				Job:         "12",
				Stats: map[string]int64{
					"fatigue": 0, "town_id": 38, "area_id": 1, "grow_type": tc.grow, "growup_change_count": 3, "gold": 1234567,
					"pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3,
				},
			}); err != nil {
				t.Fatal(err)
			}
			if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
				CharacterID: "19",
				Slots:       map[string]dnfrepo.ItemStack{},
			}); err != nil {
				t.Fatal(err)
			}
			skillsBefore := dnfrepo.SkillRecord{CharacterID: "19", Skills: map[int64]dnfrepo.SkillState{4: {Enabled: true, Level: 1}}, Trees: dnfrepo.SkillTreeState{Expanded: true, Other: dnfrepo.SkillProfile{Skills: map[int64]dnfrepo.SkillState{4: {Enabled: true, Level: 2}}}}}
			if err := repositories.Skill.Save(context.Background(), skillsBefore); err != nil {
				t.Fatal(err)
			}
			questsBefore := dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{3145: {Status: "active", ProgressValue: 1}}}
			if err := repositories.Quest.Save(context.Background(), questsBefore); err != nil {
				t.Fatal(err)
			}
			service := &Service{
				options:                 options{accountID: "account-1", pvfPath: pvfPath, gameUpperBodyCodec: gameUpperBodyCodecPlain},
				worldMapTable:           table,
				worldMapResolver:        resolver,
				dungeonMonsterTable:     monsters,
				dungeonAICharacterTable: aiCharacters,
				dungeonChoice:           func(int) (int, error) { return 0, nil },
				dungeonSeed:             func() (uint32, error) { return 1, nil },
				repositoryProvider:      func() (dnfrepo.Group, bool) { return repositories, true },
			}
			// 先完成普通登录本来就会做的PVF技能/点数初始化，再冻结体验前状态。
			if err := service.preloadSkillCatalog(context.Background()); err != nil {
				t.Fatal(err)
			}
			character, _, err := repositories.Character.Load(context.Background(), "19")
			if err != nil {
				t.Fatal(err)
			}
			if _, ready, err := service.buildCurrentSceneSkillInfoProjection(&gameSession{selectedCharacterID: 19}, context.Background(), character, "体验夹具登录初始化"); err != nil || !ready {
				t.Fatalf("登录技能目录未就绪：ready=%t error=%v", ready, err)
			}
			skillsBefore, _, err = repositories.Skill.Load(context.Background(), "19")
			if err != nil {
				t.Fatal(err)
			}
			questsBefore, _, err = repositories.Quest.Load(context.Background(), "19")
			if err != nil {
				t.Fatal(err)
			}
			capturedBody, err := hex.DecodeString("e61b00000000000001ffff00000000000000000000")
			if err != nil {
				t.Fatal(err)
			}
			capturedRequest, err := dungeoncmd.DecodeSelectDungeonRequest(capturedBody)
			if err != nil {
				t.Fatalf("decode captured 转职体验 op16 body: %v", err)
			}

			runtime, scene, err := service.prepareDungeonRuntime(
				context.Background(),
				&gameSession{selectedCharacterID: 19},
				capturedRequest,
			)
			if err != nil {
				t.Fatalf("prepare real 转职体验 runtime: %v", err)
			}
			if runtime == nil || runtime.Dungeon.ID != 7142 || runtime.MazeIndex != 0 ||
				scene.Map.Map.ID != 76032 || scene.Coordinate != (worldmap.RoomCoordinate{X: 0, Y: 0}) {
				t.Fatalf("real 转职体验 runtime=%+v scene=%+v", runtime, scene)
			}
			if runtime.Request.RuntimeState != 1 || runtime.Request.RuntimeToken != 0xffff {
				t.Fatalf("real 转职体验 captured request was not retained: %+v", runtime.Request)
			}
			packets, err := buildCurrentDungeonEntryPackets(runtime, scene)
			if err != nil {
				t.Fatalf("build current 转职体验 entry packets: %v", err)
			}
			if len(packets.DungeonInfo) == 0 || len(packets.StartMap) == 0 {
				t.Fatalf("转职体验 entry packets are empty: info=%x start=%x", packets.DungeonInfo, packets.StartMap)
			}

			connection := &bufferConn{}
			channel := channelcatalog.Channel{ServerID: 1, ID: 206, Type: 1, Name: "ch.206", Port: 10206}
			session := &gameSession{
				conn:                  connection,
				channel:               channel,
				residentChannel:       channel,
				selectedCharacterID:   19,
				townActorOwnerChannel: byte(channel.ID),
			}
			bindDungeonSelectorOriginForTestAt(t, service, session, 38, 1, 450, 234)
			if err := service.handleDungeonSelectUpper(session, capturedBody); err != nil {
				t.Fatalf("handle captured 转职体验 op16 body: %v", err)
			}
			var selectACK, dungeonInfo, startMap bool
			remaining := connection.write.Bytes()
			for len(remaining) > 0 {
				packet, rest := splitGameServerUpperPacket(t, remaining)
				switch packet.Header.MsgID {
				case uint16(dnfenum.CmdPacketSelectDungeon):
					selectACK = true
				case currentDungeonInfoNotification:
					dungeonInfo = len(packet.Body) > 0
				case currentDungeonStartNotification:
					startMap = len(packet.Body) > 0
				}
				remaining = rest
			}
			if !selectACK || !dungeonInfo || !startMap {
				t.Fatalf("captured 转职体验 response sequence: op16=%t op28=%t op29=%t", selectACK, dungeonInfo, startMap)
			}
			entered := session.dungeon.runtime
			if entered == nil || entered.Session.Snapshot().Run.DungeonID != 7142 || entered.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
				t.Fatal("体验运行时归属错误")
			}
			connection.write.Reset()
			if err := service.handleDungeonBackToVillage(session, nil); err != nil {
				t.Fatalf("体验返回城镇：%v", err)
			}
			if !session.dungeon.runtime.townReturnPending || !session.dungeon.runtime.townReturnOp24Sent {
				t.Fatal("未发送体验返回城镇")
			}
			if err := service.commitPendingDungeonReturnBeforeTownEnterSelect(session, "转职体验回归确认"); err != nil {
				t.Fatal(err)
			}
			if session.dungeon.runtime != nil || entered.Session.Snapshot().Run.Status != worldmap.DungeonRunAbandoned {
				t.Fatal("体验回城后副本运行时未退休")
			}
			after, found, err := repositories.Character.Load(context.Background(), "19")
			if err != nil || !found {
				t.Fatalf("读取体验后角色：%t %v", found, err)
			}
			if after.Level != tc.level || after.Job != "12" || after.Stats["grow_type"] != tc.grow || after.Stats["growup_change_count"] != 3 || after.Stats["gold"] != 1234567 || after.Stats["fatigue"] != 0 {
				t.Fatalf("体验改动了角色职业/等级/次数/金币/疲劳：%+v", after)
			}
			skillsAfter, found, err := repositories.Skill.Load(context.Background(), "19")
			if err != nil || !found || !reflect.DeepEqual(skillsBefore.Skills, skillsAfter.Skills) || !reflect.DeepEqual(skillsBefore.Trees, skillsAfter.Trees) || !reflect.DeepEqual(skillsBefore.Points, skillsAfter.Points) || !reflect.DeepEqual(skillsBefore.Layouts, skillsAfter.Layouts) {
				t.Fatalf("体验改动了正式技能页：%+v %v", skillsAfter, err)
			}
			questsAfter, found, err := repositories.Quest.Load(context.Background(), "19")
			if err != nil || !found || !reflect.DeepEqual(questsBefore.States, questsAfter.States) || !reflect.DeepEqual(questsBefore.Progress, questsAfter.Progress) {
				t.Fatalf("体验改动了正式任务：%+v %v", questsAfter, err)
			}
		})
	}

}
