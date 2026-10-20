package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfhonor "longheng.io/server/internal/modules/dnf/honor"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestCurrentFinishQuestAtCapPublishesHonorWithoutLevelOrSkillAward(t *testing.T) {
	for _, rate := range []int{1, 3} {
		t.Run(fmt.Sprintf("rate%d", rate), func(t *testing.T) {
			ctx := context.Background()
			repos := finishBridgeSeed(t)
			character, _, _ := repos.Character.Load(ctx, "19")
			character.Level = 90
			character.Stats["exp"] = 899999
			character.Stats[currentHonorExpertLevelStatKey] = 0
			character.Stats[currentHonorExpertProgressExperienceStatKey] = 90
			if err := repos.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			skill, _, _ := repos.Skill.Load(ctx, "19")
			skill.Points = dnfrepo.SkillPointState{TotalSP: 1000, RemainingSP: 500, TotalTP: 41, RemainingTP: 1, SyncedLevel: 90}
			if err := repos.Skill.Save(ctx, skill); err != nil {
				t.Fatal(err)
			}
			tables := finishBridgeCapProgression(t)
			honorTables, err := dnfhonor.LoadTables(honorServiceTestSource{dnfhonor.TablePath: honorServiceTestDocument()})
			if err != nil {
				t.Fatal(err)
			}
			catalog := finishBridgeCatalog(t)
			connection := &bufferConn{}
			service := &Service{
				options:      options{accountID: defaultAccountPrefix + "1", gameUpperBodyCodec: gameUpperBodyCodecPlain, mainQuestExperienceMultiplier: rate},
				questCatalog: catalog, honorTable: honorTables,
				repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
			}
			session := &gameSession{conn: connection, connID: "finish-cap-honor", selectedCharacterID: 19}
			request := dnfquest.FinishQuestRequest{QuestID: 3145, RewardSelectIndex: ^uint16(0), Multiplier: 1, Reserved: dnfquest.CurrentFinishQuestObservedTailMarker}
			if err := service.handleCurrentFinishQuestWithResources(session, request, repos, catalog, tables, finishBridgeAllocator(nil)); err != nil {
				t.Fatal(err)
			}
			packets := splitAllFinishBridgePackets(t, connection.write.Bytes())
			wantGain := uint32(110 * rate)
			if len(packets) == 0 || packets[0].Header.MsgID != uint16(dnfenum.CmdPacketFinishQuest) || len(packets[0].Body) < 8 || binary.LittleEndian.Uint32(packets[0].Body[4:8]) != wantGain {
				t.Fatalf("ack=%+v", packets)
			}
			got, _, err := repos.Character.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			wantHonorLevel, wantHonorProgress := int64(1), int64(100)
			if rate == 3 {
				wantHonorLevel, wantHonorProgress = 2, 116
			}
			if got.Level != 90 || got.Stats["exp"] != 899999+int64(wantGain) || got.Stats[currentHonorExpertLevelStatKey] != wantHonorLevel || got.Stats[currentHonorExpertProgressExperienceStatKey] != wantHonorProgress {
				t.Fatalf("post character=%+v", got)
			}
			gotSkill, _, _ := repos.Skill.Load(ctx, "19")
			if gotSkill.Points != skill.Points {
				t.Fatalf("skill award at cap=%+v want=%+v", gotSkill.Points, skill.Points)
			}
			stateCount := 0
			for i, packet := range packets {
				if packet.Header.Classification != 0 || packet.Header.MsgID != currentDungeonCharacterStateMsgID {
					continue
				}
				stateCount++
				if i == 0 || len(packet.Body) != currentFinishLoadingCharacterStateBodySize || packet.Body[0] != 90 || binary.LittleEndian.Uint32(packet.Body[1:5]) != uint32(got.Stats["exp"]) || binary.LittleEndian.Uint32(packet.Body[55:59]) != uint32(wantHonorLevel) || binary.LittleEndian.Uint64(packet.Body[59:67]) != uint64(wantHonorProgress) {
					t.Fatalf("state packet=%x", packet.Body)
				}
			}
			if stateCount != 1 {
				t.Fatalf("authoritative state count=%d ids=%v", stateCount, finishBridgePacketIDs(packets))
			}
			assertFinishBridgeNoPersonalInfoSnapshots(t, packets)
			// A receipt replay does not require loading honor tables again.
			service.honorTable = nil
			service.honorLoadErr = errors.New("unexpected honor reload on replay")
			connection.write.Reset()
			if err := service.handleCurrentFinishQuestWithResources(session, request, repos, catalog, tables, finishBridgeAllocator(nil)); err != nil {
				t.Fatal(err)
			}
			if connection.write.Len() != 0 {
				t.Fatal("same-session duplicate emitted another award")
			}
			nextConnection := &bufferConn{}
			nextSession := &gameSession{conn: nextConnection, connID: "finish-cap-honor-reconnect", selectedCharacterID: 19}
			if err := service.handleCurrentFinishQuestWithResources(nextSession, request, repos, catalog, tables, finishBridgeAllocator(nil)); err != nil {
				t.Fatal(err)
			}
			replayPackets := splitAllFinishBridgePackets(t, nextConnection.write.Bytes())
			if len(replayPackets) == 0 || replayPackets[0].Header.MsgID != uint16(dnfenum.CmdPacketFinishQuest) {
				t.Fatal("reconnect omitted receipt")
			}
			after, _, _ := repos.Character.Load(ctx, "19")
			if after.Level != got.Level || after.Stats["exp"] != got.Stats["exp"] || after.Stats[currentHonorExpertProgressExperienceStatKey] != wantHonorProgress {
				t.Fatalf("replay changed reward: %+v", after)
			}
		})
	}
}

func TestCurrentFinishQuestExistingAboveCapRepliesWithoutMutation(t *testing.T) {
	ctx := context.Background()
	repos := finishBridgeSeed(t)
	character, _, _ := repos.Character.Load(ctx, "19")
	character.Level = 91
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	skill, _, _ := repos.Skill.Load(ctx, "19")
	skill.Points.SyncedLevel = 91
	if err := repos.Skill.Save(ctx, skill); err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	service := &Service{options: options{accountID: defaultAccountPrefix + "1", gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	session := &gameSession{conn: connection, selectedCharacterID: 19}
	request := dnfquest.FinishQuestRequest{QuestID: 3145, RewardSelectIndex: ^uint16(0), Multiplier: 1, Reserved: dnfquest.CurrentFinishQuestObservedTailMarker}
	if err := service.handleCurrentFinishQuestWithResources(session, request, repos, finishBridgeCatalog(t), finishBridgeProgression(t), finishBridgeAllocator(nil)); err != nil {
		t.Fatal(err)
	}
	packets := splitAllFinishBridgePackets(t, connection.write.Bytes())
	if len(packets) != 1 || packets[0].Header.MsgID != uint16(dnfenum.CmdPacketFinishQuest) || len(packets[0].Body) != 2 || packets[0].Body[0] != 0 || packets[0].Body[1] != 22 {
		t.Fatalf("failure reply=%+v", packets)
	}
	got, _, _ := repos.Character.Load(ctx, "19")
	state, _, _ := repos.Quest.Load(ctx, "19")
	if got.Level != 91 || got.Stats["exp"] != 0 || state.States[3145].Status != "active" {
		t.Fatal("rejected settlement changed character or quest")
	}
}

func finishBridgeCapProgression(t *testing.T) *progression.Tables {
	t.Helper()
	var thresholds, sp strings.Builder
	for level := 1; level <= 92; level++ {
		fmt.Fprintf(&thresholds, "%d ", level*10000)
		fmt.Fprintf(&sp, "%d 10\n", level)
	}
	tables, err := progression.Load(context.Background(), finishBridgePVFSource{
		progression.ExperienceTablePath: thresholds.String(),
		progression.SkillPointTablePath: "[sp table]\n" + sp.String() + "[/sp table]\n[tp table]\n50 1\n[/tp table]\n",
		progression.QuestParameterPath:  "[difficulty]\n`N` 100\n[/difficulty]\n[exp reward table]\n100 -1\n[gold reward table]\n10 -1\n[epic grey level penalty]\n100\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	return tables
}
