package dnfbridge

import (
	"bytes"
	"context"
	"encoding/hex"
	alignedcmd "longheng.io/server/internal/modules/dnf/alignedcmd"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skillcmd"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPVPDojoResetPublishesLiveSkillWindowWithoutOverwritingOrdinaryPages(t *testing.T) {
	s := pvpRoomTestService(t)
	g := &gameSession{conn: &bufferConn{}, accountID: "pvp-29", selectedCharacterID: 29, channel: s.catalog.Channels()[1]}
	g.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: 29, TownID: 10, AreaID: 4, PositionValid: true}
	g.townSceneReadyCharacterID = 29
	ctx := context.Background()
	r, _ := s.repositoryGroup()
	before, _, err := r.Skill.Load(ctx, "29")
	if err != nil {
		t.Fatal(err)
	}
	dojo, err := skillcmd.PrepareDojoSkills(ctx, r, g.accountID, "29")
	if err != nil {
		t.Fatal(err)
	}
	dojo.Skills = map[int64]repo.SkillState{46: {Level: 1, Enabled: true}}
	dojo.Points = repo.SkillPointState{TotalSP: 300, RemainingSP: 300}
	dojo.Layouts = map[int]repo.SkillLayout{0: {198: 46}, 1: {198: 46}}
	dojo.Trees.Other = repo.SkillProfile{Skills: map[int64]repo.SkillState{46: {Level: 2, Enabled: true}}, Points: repo.SkillPointState{TotalSP: 400, RemainingSP: 400}}
	if err := skillcmd.DojoRepositories(r, g.accountID, "29").CharacterSkills.WithinCharacterSkill(ctx, "29", func(store repo.SkillRepository) error {
		return store.Save(ctx, dojo)
	}); err != nil {
		t.Fatal(err)
	}
	result := alignedcmd.Result{Operation: "skill_init"}
	if err := s.sendAlignedPostAction(g, result, alignedcmd.PostActionRefreshSelectedActorSkills); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitCurrentGameServerUpperPacketAuto(t, g.conn.(*bufferConn).write.Bytes())
	want, _, err := buildCurrentSceneSkillInfoBody(dojo, dojo.Layouts[0], nil)
	if err != nil || packet.Header.MsgID != currentSkillInfoMsgID || !bytes.Equal(packet.Body, want) || len(rest) != 0 {
		t.Fatalf("未使用道场数据刷新实际技能窗口: id=%d, rest=%d, err=%v", packet.Header.MsgID, len(rest), err)
	}
	after, _, err := r.Skill.Load(ctx, "29")
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatal("道场刷新改写普通技能")
	}
}

func TestPVPDojoSkillsWireUsesCurrentTwoPagesAndTwelveQuickSlots(t *testing.T) {
	record := repo.SkillRecord{Skills: map[int64]repo.SkillState{46: {Level: 2, Enabled: true}},
		Points:  repo.SkillPointState{RemainingSP: 91, RemainingTP: 9},
		Trees:   repo.SkillTreeState{Expanded: true, Other: repo.SkillProfile{Skills: map[int64]repo.SkillState{48: {Level: 1, Enabled: true}}, Points: repo.SkillPointState{RemainingSP: 92, RemainingTP: 10}}},
		Layouts: map[int]repo.SkillLayout{0: {198: 46}, 1: {150: 48}}}
	body, err := buildCurrentPVPDojoSkills(record)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := hex.DecodeString("01062e0002019c3000015b005c0009000a00")
	if !bytes.Equal(body, want) {
		t.Fatalf("现用道场技能布局: %x，期望 %x", body, want)
	}
	if dir := os.Getenv("DNF_PVP_DOJO_EXPORT"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "道场技能1230.bin"), body, 0644); err != nil {
			t.Fatal(err)
		}
	}
	record.Layouts[0][204] = 46
	if _, err := buildCurrentPVPDojoSkills(record); err == nil {
		t.Fatal("允许超过204槽位")
	}
	delete(record.Layouts[0], 204)
	record.Points.RemainingSP = 65536
	if _, err := buildCurrentPVPDojoSkills(record); err == nil {
		t.Fatal("技能点发生u16截断")
	}
}

func TestPVPDojoModeRequiresOwnedCompletedScene(t *testing.T) {
	s, g, _, _ := dojoSlotTestScene(t, 6)
	g.pvpRoom = nil
	g.channel.Group = "pvp"
	g.townPositionSnapshot = currentTownPositionSnapshot{}
	if err := s.handleCurrentPVPDojoSkillMode(g, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if g.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("普通场景接受道场模式")
	}
	g.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: 31, TownID: 10, AreaID: 4, PositionValid: true}
	g.townSceneReadyCharacterID = 31
	if err := s.handleCurrentPVPDojoSkillMode(g, []byte{1}); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitCurrentGameServerUpperPacketAuto(t, g.conn.(*bufferConn).write.Bytes())
	if packet.Header.MsgID != 1229 || !bytes.Equal(packet.Body, []byte{1}) {
		t.Fatal("道场模式未持久化后通知")
	}
	skills, trailing := splitCurrentGameServerUpperPacketAuto(t, rest)
	if len(trailing) != 0 || skills.Header.MsgID != 19 || currentSkillInfoFirstTreeSlots(t, skills.Body)[6] != 47 {
		t.Fatal("复制后未保持PK技能页")
	}
	active, err := s.currentPVPDojoSkillsActive(g)
	if err != nil || active {
		t.Fatal("已退出临时加点仍写道场技能")
	}
}
