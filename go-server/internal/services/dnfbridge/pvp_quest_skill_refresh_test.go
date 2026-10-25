package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	pvp "longheng.io/server/internal/modules/dnf/pvp"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skillcmd"
)

func TestPVPTrainingFinishKeepsDojoPageAndAddsTPWithoutReplacingLayouts(t *testing.T) {
	for _, mode := range []byte{0, 1, 2} {
		t.Run(string(rune('0'+mode)), func(t *testing.T) {
			s, g, r, result := skillTreeQuestRefreshFixture(t)
			ctx := context.Background()
			g.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: 19, TownID: 10, AreaID: 4, PositionValid: true}
			g.townSceneReadyCharacterID = 19
			result.QuestID, result.TPDelta = 2674, 2
			result.PostCommitCharacter.Stats[pvp.DojoSkillModeKey] = int64(mode)
			result.PostCommitCharacter.Stats["bonus_tp"] = 2
			if err := r.Character.Save(ctx, result.PostCommitCharacter); err != nil {
				t.Fatal(err)
			}
			s.initialSPTable, s.initialTPTable = map[int]int{20: 200}, map[int]int{20: 10}
			dojo, err := skillcmd.PrepareDojoSkills(ctx, r, g.accountID, "19")
			if err != nil {
				t.Fatal(err)
			}
			dojo.Trees.Active = 0
			dojo.Skills = map[int64]repo.SkillState{46: {Level: 5, Enabled: true}, 48: {Level: 6, Enabled: true}}
			dojo.Layouts = map[int]repo.SkillLayout{0: {198: 48, 199: 46}, 1: {0: 46, 198: 48}}
			dojo.Points = repo.SkillPointState{TotalSP: 200, RemainingSP: 90, TotalTP: 10, RemainingTP: 3, SyncedLevel: 20}
			dojo.Trees.Other.Points = repo.SkillPointState{TotalSP: 200, RemainingSP: 140, TotalTP: 10, RemainingTP: 8, SyncedLevel: 20}
			dr := skillcmd.DojoRepositories(r, g.accountID, "19")
			if err := dr.CharacterSkills.WithinCharacterSkill(ctx, "19", func(store repo.SkillRepository) error { return store.Save(ctx, dojo) }); err != nil {
				t.Fatal(err)
			}
			before, _, _ := r.Skill.Load(ctx, "19")
			if err := s.sendCurrentFinishQuestPostCommitSnapshots(g, result); err != nil {
				t.Fatal(err)
			}
			packets := splitAllFinishBridgePackets(t, g.conn.(*bufferConn).write.Bytes())
			var ids []uint16
			var body []byte
			for _, p := range packets {
				if p.Header.MsgID == 37 || p.Header.MsgID == 19 || p.Header.MsgID == 1229 || p.Header.MsgID == 1230 {
					ids = append(ids, p.Header.MsgID)
					if p.Header.MsgID == 19 {
						body = p.Body
					}
				}
			}
			if mode == 0 {
				if !reflect.DeepEqual(ids, []uint16{37, 1229, 1230, 19}) {
					t.Fatalf("领奖后未恢复道场模式和独立技能: %v", ids)
				}
				_, outer := consumeCurrentSkillInfoFields(t, body[4:])
				fields, skills := consumeCurrentSkillInfoFields(t, outer[2][0])
				if !reflect.DeepEqual(fields[2], []uint64{5}) {
					t.Fatalf("道场新增TP未保留已消费量: %v", fields)
				}
				foundQuick := false
				for _, raw := range skills[3] {
					row, _ := consumeCurrentSkillInfoFields(t, raw)
					if reflect.DeepEqual(row[1], []uint64{6}) {
						foundQuick = reflect.DeepEqual(row[2], []uint64{48}) && reflect.DeepEqual(row[3], []uint64{6})
					}
				}
				if !foundQuick {
					t.Fatal("道场第七格被刷图布局覆盖")
				}
				cold, err := skillcmd.PrepareDojoSkills(ctx, r, g.accountID, "19")
				if err != nil || !reflect.DeepEqual(cold.Layouts, dojo.Layouts) || cold.Points.TotalTP != 12 || cold.Points.RemainingTP != 5 || cold.Trees.Other.Points.RemainingTP != 10 {
					t.Fatalf("道场两页未正确保存: %+v %v", cold, err)
				}
			} else {
				if !reflect.DeepEqual(ids, []uint16{37, 19}) || int(binary.LittleEndian.Uint32(body[:4])) != len(body)-4 {
					t.Fatal("已选择保留/恢复模式被重开道场", ids)
				}
				assertSkillTreeQuestRefreshPackets(t, g)
			}
			after, _, _ := r.Skill.Load(ctx, "19")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("在线投影修改了刷图技能")
			}
		})
	}
}
