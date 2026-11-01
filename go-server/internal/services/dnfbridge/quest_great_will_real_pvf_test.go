package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestRealPVFGreatWillCinematicDeathDispatch(t *testing.T) {
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
	progress, err := progression.Load(ctx, source)
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
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	def, found := quests.Find(3447)
	if !found || def.Type != "[condition under clear]" || def.SubType != 6 ||
		len(def.IntData) != 2 || def.IntData[0] != 7156 || def.IntData[1] != -1 {
		t.Fatalf("great will definition=%+v", def)
	}
	for _, tc := range []struct {
		name   string
		formal bool
		marker byte
	}{
		{"direct_handler_marker1", false, 1},
		{"network_dispatch_marker0", true, 0},
		{"network_dispatch_marker1", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repos := dnfrepomemory.NewMemoryGroup()
			if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "dnf:1", Name: "GREATWILL", Job: "9", Level: 54, Stats: map[string]int64{"fatigue": 156, "exp": 0}}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
				t.Fatal(err)
			}
			sp, err := progress.TotalSkillPoints(54)
			if err != nil {
				t.Fatal(err)
			}
			tp, err := progress.TotalTechniquePoints(54)
			if err != nil {
				t.Fatal(err)
			}
			if err := repos.Skill.Save(ctx, dnfrepo.SkillRecord{CharacterID: "19", Points: dnfrepo.SkillPointState{SyncedLevel: 54, TotalSP: sp, RemainingSP: sp, TotalTP: tp, RemainingTP: tp}}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{3446: {Status: "completed"}}}); err != nil {
				t.Fatal(err)
			}
			owner, err := dnfquest.NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := owner.ApplyAccept(ctx, quests, dnfquest.CharacterEligibility{Level: 54, Job: 9}, dnfquest.NewQuestIDCommand(alignedcmd.Request{AccountID: "dnf:1", SelectedCharacterID: 19}, "accept_quest", dnfquest.QuestIDRequest{QuestID: 3447})); err != nil {
				t.Fatal(err)
			}
			conn := &bufferConn{}
			session := &gameSession{conn: conn, connID: "great-will-" + tc.name, selectedCharacterID: 19}
			svc := &Service{
				options:       options{accountID: "dnf:1", currentClientVersion: "official-24.9", gameUpperBodyCodec: gameUpperBodyCodecPlain},
				worldMapTable: table, worldMapResolver: resolver, dungeonMonsterTable: monsters, dungeonAICharacterTable: ai,
				questCatalog: quests, pvfItemCatalog: items,
				dungeonChoice: func(int) (int, error) { return 0, nil }, dungeonSeed: func() (uint32, error) { return 1, nil },
				repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
				questFinishResourceLoader: func(context.Context) (*progression.Tables, *pvfDungeonDropCatalog, error) {
					return progress, items, nil
				},
			}
			runtime, start, err := svc.prepareDungeonRuntime(ctx, session, dungeoncmd.SelectDungeonRequest{DungeonID: 7156, Difficulty: 0})
			if err != nil {
				t.Fatal(err)
			}
			session.dungeon.runtime = runtime
			if start.Map.Map.ID != 70184 || !start.Cleared {
				t.Fatalf("start map=%d cleared=%t blockers=%v", start.Map.Map.ID, start.Cleared, start.BlockingHostiles)
			}
			seekerQST010EnsureAnnounced(t, runtime)
			move := make([]byte, dungeoncmd.MoveMapRequestSize)
			move[0] = 1
			if err := svc.handleDungeonMoveMapPlanned(session, move, nil, false); err != nil {
				t.Fatal(err)
			}
			scene, _ := runtime.Session.Scene()
			if scene.Map.Map.ID != 70185 || !scene.Boss || !scene.Cleared || len(scene.PassiveObjects) != 1 || scene.PassiveObjects[0].ObjectID != 69292 {
				t.Fatalf("boss scene=%+v", scene)
			}
			if proof := currentDungeonDestroyObjectClearConditionSource(runtime, scene, 69292); proof == "" {
				t.Fatal("real PVF clear target proof missing")
			}
			conn.write.Reset()
			// Bounded native writer output: fixture clock/getters, real u32 key,
			// FFFF owner, zero combat entries, marker 1; four-byte network footer.
			body, err := hex.DecodeString("ac0e0100ffff0000000000000000000000000000000000000000000000000000000000000001003412004433221100000000000000000000000000000000000000000000000000000000")
			if err != nil {
				t.Fatal(err)
			}
			body[37] = tc.marker
			if tc.formal {
				frame, err := dnfproto.BuildChannelPacket(39, body, 100, dnfproto.DefaultChannelClassification)
				if err != nil {
					t.Fatal(err)
				}
				if err := svc.handleGameUpper(session, frame); err != nil {
					t.Fatal(err)
				}
			} else if err := svc.handleDungeonMonsterDeath(session, body); err != nil {
				t.Fatal(err)
			}
			record, _, err := repos.Quest.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			var ids []uint16
			for _, p := range splitAllFinishBridgePackets(t, conn.write.Bytes()) {
				ids = append(ids, p.Header.MsgID)
			}
			t.Logf("map=%d run=%s final=%t settlement=%t quest=%+v opcodes=%v passiveReceipts=%+v", scene.Map.Map.ID, runtime.Session.Snapshot().Run.Status, runtime.ordinaryFinalRoomClearAccepted, runtime.settlementEntrySent, record.States[3447], ids, runtime.passiveQuestDropReceipts)
			if runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted || !runtime.settlementEntrySent {
				t.Fatal("cinematic clear report did not reach dungeon completion")
			}
			if tc.formal {
				for _, seq := range []uint16{100, 101} {
					conn.write.Reset()
					frame, err := dnfproto.BuildChannelPacket(39, body, seq, dnfproto.DefaultChannelClassification)
					if err != nil {
						t.Fatal(err)
					}
					if err := svc.handleGameUpper(session, frame); err != nil || conn.write.Len() != 0 {
						t.Fatalf("duplicate op39 sequence=%d err=%v response=%x", seq, err, conn.write.Bytes())
					}
				}
			}
			// This condition-under-clear objective uses the native follow-up op33.
			trigger := []byte{33, 0, 0, 0, 0, 0}
			binary.LittleEndian.PutUint16(trigger[2:], 3447)
			if err := svc.handleCurrentSetQuestTrigger(session, trigger); err != nil {
				t.Fatal(err)
			}
			record, _, err = repos.Quest.Load(ctx, "19")
			if err != nil || record.States[3447].ProgressValue != 0 {
				t.Fatalf("quest after op33=%+v err=%v", record.States[3447], err)
			}
			packets := qst006CallFinish(t, svc, session, conn, 3447)
			qst006AssertFinishSuccess(t, packets, 3447)
			record, _, err = repos.Quest.Load(ctx, "19")
			if err != nil || record.States[3447].Status != "completed" {
				t.Fatalf("quest after op34=%+v err=%v", record.States[3447], err)
			}
			if packets := qst006CallFinish(t, svc, session, conn, 3447); len(packets) != 0 {
				t.Fatalf("duplicate finish emitted packets=%v", finishBridgePacketIDs(packets))
			}
			t.Log("accept -> 70184 -> 70185 -> native op39 -> op33 -> op34 completed; duplicate op39/op34 suppressed")
		})
	}
}
