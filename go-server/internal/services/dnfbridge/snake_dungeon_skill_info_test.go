package dnfbridge

import (
	"context"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func snakeDungeonSkillFixture(t *testing.T, job string, learned bool) (*Service, *gameSession, *bufferConn, dnfrepo.Group, dnfrepo.CharacterRecord) {
	t.Helper()
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	character := dnfrepo.CharacterRecord{CharacterID: "99", Job: job, Level: 20}
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	skills := map[int64]dnfrepo.SkillState{1: {Level: 1, Enabled: true}}
	if learned {
		skills[31] = dnfrepo.SkillState{Level: 1, Enabled: true}
	}
	if err := repos.Skill.Save(ctx, dnfrepo.SkillRecord{CharacterID: "99", Skills: skills}); err != nil {
		t.Fatal(err)
	}
	catalog, err := buildSkillCatalogFromSource(ctx, initialEquipmentMemSource{
		"skill/skilllist.lst": "11 `job11.lst`\n0 `job0.lst`\n",
		"skill/job11.lst":     "1 `basic.skl`\n31 `snake.skl`\n",
		"skill/job0.lst":      "1 `basic.skl`\n31 `snake.skl`\n",
		"skill/basic.skl":     "[type]\n`[active]`\n[skill class]\n1\n[required level]\n1\n",
		"skill/snake.skl":     "[type]\n`[passive]`\n[skill class]\n3\n[required level]\n15\n[maximum level]\n30\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{
		repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }, skillCatalog: catalog,
		initialSkillsByJob: map[byte][]initialSkillEntry{11: {{SkillID: 1, Level: 1}}, 0: {{SkillID: 1, Level: 1}}},
		initialSPTable:     map[int]int{20: 1000}, initialTPTable: map[int]int{20: 0},
	}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, selectedCharacterID: 99, postStartMapPlayerStateSent: true}
	return service, session, conn, repos, character
}

func TestSnakeDungeonSkillInfoPreservesWeaponAfterLoading(t *testing.T) {
	s, session, conn, _, _ := snakeDungeonSkillFixture(t, "11", true)
	if err := s.sendCurrentPreDungeonContextPlayerState(session, "测试真实入图入口"); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.MsgID != uint16(dnfenum.CmdPacketSetUDPIPPort) || len(packet.Body) == 0 || packet.Body[0] != 0 {
		t.Fatalf("加载前应先发送mode0角色：包=%+v", packet.Header)
	}
	packet, rest = splitGameServerUpperPacket(t, rest)
	if packet.Header.MsgID != uint16(dnfenum.CmdPacketSetUDPIPPort) || len(packet.Body) == 0 || packet.Body[0] != 1 {
		t.Fatalf("技能页之前必须先绑定mode1角色：包=%+v", packet.Header)
	}
	packet, rest = splitGameServerUpperPacket(t, rest)
	if packet.Header.MsgID != currentSkillInfoMsgID || len(rest) != 0 || session.snakeDungeonSkillInfo == nil {
		t.Fatalf("加载前技能页未发送或未提交：包=%+v，余量=%d", packet.Header, len(rest))
	}
	if !session.preDungeonContextPlayerStateSent {
		t.Fatal("加载前入口成功后未提交角色绑定门禁")
	}
	conn.write.Reset()
	if err := s.sendCurrentPostFinishLoadingPlayerState(session, "测试读条完成"); err != nil {
		t.Fatal(err)
	}
	packet, rest = splitGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.MsgID != uint16(dnfenum.CmdPacketRequestBlacklist) || len(rest) != 0 || !session.postFinishLoadingPlayerStateSent {
		t.Fatalf("加载后应只落位且不重建技能：包=%+v，余量=%d", packet.Header, len(rest))
	}
}

func TestSnakeDungeonSkillInfoRequiresMatchingEntrySnapshot(t *testing.T) {
	for _, name := range []string{"未前置", "入图绑定未提交", "角色不符", "技能页变化", "未学习", "其他职业"} {
		t.Run(name, func(t *testing.T) {
			job, learned := "11", true
			if name == "其他职业" {
				job = "0"
			}
			if name == "未学习" {
				learned = false
			}
			s, session, conn, repos, character := snakeDungeonSkillFixture(t, job, learned)
			if name != "未前置" {
				if err := s.prepareCurrentSnakeDungeonSkillInfo(session, context.Background(), character, true, "测试准备"); err != nil {
					t.Fatal(err)
				}
			}
			session.preDungeonContextPlayerStateSent = name != "入图绑定未提交"
			if name == "角色不符" {
				session.snakeDungeonSkillInfo.characterID = "100"
			}
			if name == "技能页变化" {
				record, _, err := repos.Skill.Load(context.Background(), "99")
				if err != nil {
					t.Fatal(err)
				}
				record.Skills[31] = dnfrepo.SkillState{Level: 2, Enabled: true}
				if err := repos.Skill.Save(context.Background(), record); err != nil {
					t.Fatal(err)
				}
			}
			conn.write.Reset()
			if err := s.sendCurrentFinishLoadingSkillInfo(session, context.Background(), character, "测试完成"); err != nil {
				t.Fatal(err)
			}
			packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if packet.Header.MsgID != currentSkillInfoMsgID || len(rest) != 0 {
				t.Fatalf("不满足保留条件时须发送权威完整技能页：包=%+v，余量=%d", packet.Header, len(rest))
			}
		})
	}
}

func TestSnakeDungeonSkillInfoFailedWriteDoesNotCommit(t *testing.T) {
	s, session, conn, _, character := snakeDungeonSkillFixture(t, "11", true)
	session.conn = &failNthDungeonWriteConn{failAt: 1, err: errors.New("测试技能页写入失败")}
	if err := s.prepareCurrentSnakeDungeonSkillInfo(session, context.Background(), character, true, "测试失败"); err == nil || session.snakeDungeonSkillInfo != nil {
		t.Fatalf("失败写入不得提交准备快照：错误=%v", err)
	}
	session.conn = conn
	if err := s.prepareCurrentSnakeDungeonSkillInfo(session, context.Background(), character, true, "测试重试"); err != nil || session.snakeDungeonSkillInfo == nil {
		t.Fatalf("写入重试未恢复：错误=%v", err)
	}
	if err := s.prepareCurrentSnakeDungeonSkillInfo(session, context.Background(), character, false, "测试角色缺失"); err != nil || session.snakeDungeonSkillInfo != nil {
		t.Fatalf("新一轮没有真实角色时须丢弃旧准备：错误=%v", err)
	}
}
