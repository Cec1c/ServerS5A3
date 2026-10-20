package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/skillcmd"
)

func dojoSlotTestScene(t *testing.T, mode byte) (*Service, *gameSession, repo.Group, repo.SkillRecord) {
	t.Helper()
	ctx := context.Background()
	catalog, err := buildSkillCatalogFromSource(ctx, initialEquipmentMemSource{
		"skill/skilllist.lst": "0 `job0.lst`\n",
		"skill/job0.lst":      "46 `job0/a.skl`\n47 `job0/b.skl`\n",
		"skill/job0/a.skl":    "[type]\n`[active]`\n",
		"skill/job0/b.skl":    "[type]\n`[active]`\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	r := memory.NewMemoryGroup()
	if err := r.Character.Save(ctx, repo.CharacterRecord{CharacterID: "31", AccountID: "dojo-slot", Job: "0", Level: 1}); err != nil {
		t.Fatal(err)
	}
	ordinary := repo.SkillRecord{CharacterID: "31", Skills: map[int64]repo.SkillState{46: {Level: 1, Enabled: true}},
		Points:  repo.SkillPointState{TotalSP: 100, RemainingSP: 100, SyncedLevel: 1},
		Layouts: map[int]repo.SkillLayout{0: {198: 46}}}
	if err := r.Skill.Save(ctx, ordinary); err != nil {
		t.Fatal(err)
	}
	dojo, err := skillcmd.PrepareDojoSkills(ctx, r, "dojo-slot", "31")
	if err != nil {
		t.Fatal(err)
	}
	dojo.Skills = map[int64]repo.SkillState{46: {Level: 1, Enabled: true}, 47: {Level: 2, Enabled: true}}
	dojo.Points = repo.SkillPointState{TotalSP: 100, RemainingSP: 70, SyncedLevel: 1}
	dojo.Layouts = map[int]repo.SkillLayout{0: {199: 46, 198: 47}, 1: {199: 47, 198: 46}}
	dojo.Trees.Other = repo.SkillProfile{Skills: map[int64]repo.SkillState{46: {Level: 3, Enabled: true}, 47: {Level: 4, Enabled: true}},
		Points: repo.SkillPointState{TotalSP: 200, RemainingSP: 150, SyncedLevel: 1}}
	if err := skillcmd.DojoRepositories(r, "dojo-slot", "31").CharacterSkills.WithinCharacterSkill(ctx, "31", func(store repo.SkillRepository) error { return store.Save(ctx, dojo) }); err != nil {
		t.Fatal(err)
	}
	s := &Service{options: options{accountID: "dojo-slot", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider: func() (repo.Group, bool) { return r, true }, skillCatalog: catalog,
		initialSkillsByJob: map[byte][]initialSkillEntry{0: {{SkillID: 46, Level: 1}}}, initialSPTable: map[int]int{1: 100}}
	g := &gameSession{conn: &bufferConn{}, accountID: "dojo-slot", selectedCharacterID: 31,
		townPositionSnapshot: currentTownPositionSnapshot{CharacterID: 31, TownID: 10, AreaID: 4, PositionValid: true}, townSceneReadyCharacterID: 31}
	g.pvpRoom = &currentPVPRoom{id: 1, battleMode: mode, started: true}
	g.pvpRoom.members[0] = &currentPVPMember{session: g, characterID: 31}
	return s, g, r, dojo
}

func TestPVPDojoRoomDragPersistsSelectedPageAndPreservesOrdinarySkills(t *testing.T) {
	for _, mode := range []byte{6, 10, 2} {
		for _, tree := range []byte{0, 1} {
			t.Run(fmt.Sprintf("模式%d页%d", mode, tree), func(t *testing.T) {
				s, g, r, before := dojoSlotTestScene(t, mode)
				ctx := context.Background()
				ordinary, _, err := r.Skill.Load(ctx, "31")
				if err != nil {
					t.Fatal(err)
				}
				// 19:14现场原始请求：线上7与6互换，context=-1，mode=2。
				body := []byte{tree, 7, 6, 255, 255, 255, 255, 2}
				handled, err := s.handleAlignedGameCommand(g, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketChangeSkillslot), body)
				if err != nil || !handled {
					t.Fatalf("房内拖动处理失败: handled=%t err=%v", handled, err)
				}
				packet, rest := splitCurrentGameServerUpperPacketAuto(t, g.conn.(*bufferConn).write.Bytes())
				if packet.Header.MsgID != 28 || packet.Header.Classification != 1 || !bytes.Equal(packet.Body, []byte{1, tree, 7, 6}) || len(rest) != 0 {
					t.Fatalf("房内拖动未回实际成功应答: class=%d id=%d body=%x rest=%d", packet.Header.Classification, packet.Header.MsgID, packet.Body, len(rest))
				}
				cold, err := skillcmd.PrepareDojoSkills(ctx, r, g.accountID, "31")
				if err != nil {
					t.Fatal(err)
				}
				want := repo.CloneSkill(before)
				want.Layouts[int(tree)][198], want.Layouts[int(tree)][199] = before.Layouts[int(tree)][199], before.Layouts[int(tree)][198]
				if !reflect.DeepEqual(cold.Layouts, want.Layouts) {
					t.Fatalf("独立两页布局未正确持久化: %+v", cold.Layouts)
				}
				for page := 0; page < 2; page++ {
					oldPage, _ := repo.SkillTree(before, page)
					newPage, _ := repo.SkillTree(cold, page)
					if !reflect.DeepEqual(oldPage, newPage) {
						t.Fatal("拖动改变了技能等级或SP/TP")
					}
				}
				after, _, err := r.Skill.Load(ctx, "31")
				if err != nil || !reflect.DeepEqual(ordinary, after) {
					t.Fatal("道场拖动改写普通技能")
				}
				gotWire, err := buildCurrentPVPDojoSkills(cold)
				if err != nil {
					t.Fatal(err)
				}
				wantWire, err := buildCurrentPVPDojoSkills(want)
				if err != nil || !bytes.Equal(gotWire, wantWire) {
					t.Fatal("重新进入道场没有还原已保存位置")
				}
			})
		}
	}
}

func TestPVPDojoRoomStillRejectsLearningAndReset(t *testing.T) {
	for _, request := range []struct {
		opcode uint16
		body   []byte
	}{{29, []byte{0, 1, 46, 0, 0, 1, 3}}, {491, []byte{0, 3}}} {
		t.Run(fmt.Sprint(request.opcode), func(t *testing.T) {
			s, g, r, before := dojoSlotTestScene(t, 6)
			handled, err := s.handleAlignedGameCommand(g, byte(dnfenum.GameCmdCommand), request.opcode, request.body)
			if err != nil || !handled {
				t.Fatalf("房内学习/重置处理失败: %v", err)
			}
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, g.conn.(*bufferConn).write.Bytes())
			if packet.Header.MsgID != request.opcode || !bytes.Equal(packet.Body, []byte{0, 1}) || len(rest) != 0 {
				t.Fatalf("意外放行房内学习/重置: %x", packet.Body)
			}
			after, err := skillcmd.PrepareDojoSkills(context.Background(), r, g.accountID, "31")
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("被拒绝的请求改动道场技能")
			}
		})
	}
}
