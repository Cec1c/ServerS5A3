package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/tagtournament"
	dnftown "longheng.io/server/internal/modules/dnf/town"
)

func tagCompleteTownSceneCallback(t *testing.T, s *Service, p *gameSession) {
	t.Helper()
	if _, found := s.townArea(48, 0); !found {
		var err error
		s.townCatalog, err = dnftown.Load(context.Background(), townMoveTestSource{"town/town.lst": "48 `empire.twn`", "town/empire.twn": "[area]\n0 `empire.map` `[normal]`\n[/area]"}, dnftown.Options{})
		if err != nil {
			t.Fatal(err)
		}
	}
	body := make([]byte, currentTownSetUserAreaBodySize)
	body[0], body[1] = 48, 0
	binary.LittleEndian.PutUint16(body[2:4], 100)
	binary.LittleEndian.PutUint16(body[4:6], 200)
	if err := s.handleTownSetUserArea(p, body); err != nil {
		t.Fatal("休息室真实区域回调失败", err)
	}
	position := make([]byte, currentTownSetUserPositionBodySize)
	binary.LittleEndian.PutUint16(position, 100)
	binary.LittleEndian.PutUint16(position[2:], 200)
	if err := s.handleTownSetUserPosition(p, position); err != nil {
		t.Fatal("休息室真实坐标回调失败", err)
	}
	if !currentTagTournamentLobbyReady(p) {
		t.Fatalf("区域回调后帝国休息室仍未就绪: runtime=%v tail=%v/%v pending=%v post=%d", p.dungeon.runtime != nil, p.sceneBootstrapTailSent, p.sceneBootstrapTailDeferred, p.confirmedDungeonReturnStatePending, p.townPostTransition.stage)
	}
}

func TestTagWeeklyWireMatchesNativeThirteenByteLayout(t *testing.T) {
	body, err := buildCurrentTagTournamentWeeklyInfo(240000, nil, false)
	if err != nil || !bytes.Equal(body, []byte{0, 0, 0, 0, 16, 16, 0, 0, 0x80, 0xa9, 3, 0, 0}) {
		t.Fatal(body, err)
	}
	r := &tagtournament.ClearRecord{Mode: tagtournament.Hard, ElapsedMS: 80123, Characters: [2]tagtournament.RecordCharacter{{Job: "14", Grow: 0x21}, {Job: "1", Grow: 0x23}}}
	body, err = buildCurrentTagTournamentWeeklyInfo(240000, r, false)
	if err != nil || !bytes.Equal(body, []byte{0xfb, 0x38, 1, 0, 14, 1, 0x21, 0x23, 0x80, 0xa9, 3, 0, 0}) {
		t.Fatal("最快队伍职业、时间或领取状态错位", body, err)
	}
	r.Characters[0].Job = "16"
	if _, err = buildCurrentTagTournamentWeeklyInfo(240000, r, false); err == nil {
		t.Fatal("真实成绩可用无效职业")
	}
}

// 现场截图上栏03'41'60、下栏04'00'00，旧正文+0=240000、+8=221607。
// 按现用原生控件绑定验证修正正文：上栏读+8、下栏读+0，职业字节原样。
func TestTagWeeklyTimesMatchNativeControlBinding(t *testing.T) {
	r := &tagtournament.ClearRecord{Mode: tagtournament.Hard, ElapsedMS: 221607, Characters: [2]tagtournament.RecordCharacter{{Job: "14", Grow: 0x24}, {Job: "0", Grow: 0x21}}}
	body, err := buildCurrentTagTournamentWeeklyInfo(240000, r, false)
	if err != nil || !bytes.Equal(body, []byte{0xa7, 0x61, 3, 0, 14, 0, 0x24, 0x21, 0x80, 0xa9, 3, 0, 0}) {
		t.Fatal("现场个人成绩与目标字段仍反置", body, err)
	}
	upperChallenge := binary.LittleEndian.Uint32(body[8:12])
	lowerPersonal := binary.LittleEndian.Uint32(body[0:4])
	if upperChallenge != 240000 || lowerPersonal != 221607 {
		t.Fatal("原生控件上栏不是目标或下栏不是个人成绩", upperChallenge, lowerPersonal)
	}
}

func TestTagCompletionUsesPersistentBestAndFreeReplayCanImproveIt(t *testing.T) {
	s, p, _, now := tagBattleBridgeFixture(t, tagtournament.Hard)
	for i := 0; i < 7; i++ {
		tagBattleBridgeClear(t, s, p, now, false)
	}
	first := p.tagTournamentBattle.completionBody
	if binary.LittleEndian.Uint32(first) != 13000 || binary.LittleEndian.Uint32(first[8:]) != 13000 {
		t.Fatal("终局未接入最快记录", first)
	}
	o := p.tagTournamentBattle.owner
	_, weekly, err := o.Records(context.Background(), p.accountID)
	if err != nil || weekly == nil || weekly.ElapsedMS != 13000 {
		t.Fatal(weekly, err)
	}
	// 只读换周不需要停服、重新选角或写玩家资产。
	*now = now.Add(7 * 24 * time.Hour)
	_, weekly, err = o.Records(context.Background(), p.accountID)
	if err != nil || weekly != nil {
		t.Fatal("换周后仍展示旧周时间", weekly, err)
	}
}

func TestTagWeeklyCompletionAndLobbyRefreshUseNativeRecords(t *testing.T) {
	s, p, _, now := tagBattleBridgeFixtureWithWeekly(t, tagtournament.Hard, true)
	for i := 0; i < 7; i++ {
		tagBattleBridgeClear(t, s, p, now, false)
	}
	var sequence []uint16
	var weekly []byte
	for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
		packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
		if packet.Header.MsgID == 1530 || packet.Header.MsgID == 1396 {
			sequence = append(sequence, packet.Header.MsgID)
			if packet.Header.MsgID == 1530 {
				weekly = append([]byte(nil), packet.Body...)
			}
		}
		rest = tail
	}
	if len(sequence) != 2 || sequence[0] != 1530 || sequence[1] != 1396 || len(weekly) != 13 || binary.LittleEndian.Uint32(weekly) != 13000 || binary.LittleEndian.Uint32(weekly[8:]) != 240000 || weekly[12] != 0 {
		t.Fatal("终局周成绩与历史成绩顺序或时间错误", sequence, weekly)
	}
	p.conn.(*bufferConn).write.Reset()
	if err := s.sendCurrentTagTournamentWeeklyInfo(p); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
	if packet.Header.MsgID != 1530 || !bytes.Equal(weekly, packet.Body) || len(rest) != 0 {
		t.Fatal("重新读取账号周成绩丢失职业或时间", packet)
	}
	p.tagTournamentBattle = nil
	p.sceneBootstrapTailSent = true
	p.tagTournamentLobbyDay = 0
	p.conn.(*bufferConn).write.Reset()
	if err := s.refreshCurrentTagTournamentLobby(p, time.Now()); err != nil {
		t.Fatal(err)
	}
	found := false
	for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
		packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
		found = found || packet.Header.MsgID == 1530
		rest = tail
	}
	if !found {
		t.Fatal("06点服务日刷新遗漏周挑战信息")
	}
}

func TestTagCompletedTownReadinessDoesNotRequireOldLoginTail(t *testing.T) {
	s, p, _ := newTagRosterBridgeTest(t)
	p.sceneBootstrapTailSent, p.sceneBootstrapTailDeferred = false, false
	p.townSceneReadyCharacterID = p.selectedCharacterID
	p.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: p.selectedCharacterID, TownID: 48, PositionValid: true}
	p.townPostTransition.characterID = p.selectedCharacterID
	p.townPostTransition.stage = currentTownPostTransitionComplete
	if !currentTagTournamentLobbyReady(p) {
		t.Fatal("已确认休息室被旧登录尾包误拦")
	}
	if err := s.handleCurrentTagTournamentRosterList(p, nil); err != nil || p.conn.(*bufferConn).write.Len() == 0 {
		t.Fatal("回城后队伍设置没有发资料", err)
	}
	for _, scenario := range []string{"未完成HUD", "其他角色", "仍待确认回城", "尾包仍挂起"} {
		t.Run(scenario, func(t *testing.T) {
			p.townPostTransition.stage = currentTownPostTransitionComplete
			p.townPositionSnapshot.CharacterID = p.selectedCharacterID
			p.confirmedDungeonReturnStatePending = false
			p.sceneBootstrapTailDeferred = false
			switch scenario {
			case "未完成HUD":
				p.townPostTransition.stage = currentTownPostTransitionPending
			case "其他角色":
				p.townPositionSnapshot.CharacterID++
			case "仍待确认回城":
				p.confirmedDungeonReturnStatePending = true
			case "尾包仍挂起":
				p.sceneBootstrapTailDeferred = true
			}
			if currentTagTournamentLobbyReady(p) {
				t.Fatal("未完成或旧世代被当作就绪")
			}
		})
	}
}
