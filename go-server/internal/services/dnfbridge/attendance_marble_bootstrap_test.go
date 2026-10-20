package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/onlineevent"
	proto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestAttendanceMarbleProductionCatalogAdds145OnlyWhenConfigured(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		s := &Service{options: options{joustEventDisabled: true}}
		if enabled {
			s.attendanceMarbleCatalog = &attendanceMarbleCatalog{}
		}
		conn := &bufferConn{}
		session := &gameSession{conn: conn}
		catalog := &currentSpendTimeRuntimeCatalog{rewardItemIDs: []uint32{100}, totalStageSeconds: 10800}
		if _, err := s.sendCurrentSpendTimeProtocolState(session, catalog, 0, 0, true); err != nil {
			t.Fatal(err)
		}
		packet, _ := splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), proto.GameServerUpperHeaderSize16)
		body, err := zlibDecompress(packet.Body)
		if err != nil {
			t.Fatal(err)
		}
		shape := parseCurrentSpendTimeEventInfoBody(t, body)
		if shape.hasEvent(145) != enabled || !shape.hasEvent(currentSeriaLuckEventID) {
			t.Fatalf("生产目录开关不符：%+v", shape)
		}
	}
}

func TestAttendanceMarbleCatalogPreservesAllOtherRows(t *testing.T) {
	old, err := buildCurrentActivityEventInfoBodyWithRuntimeActivities([]uint32{100}, 10800, true, true, true, true, true, 2418, true, true, true, true, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	next, err := buildCurrentActivityEventInfoBodyWithMarble([]uint32{100}, 10800, true, true, true, true, true, 2418, true, true, true, true, true, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	a, b := parseCurrentSpendTimeEventInfoBody(t, old), parseCurrentSpendTimeEventInfoBody(t, next)
	offset := 2
	for _, row := range a.baseRows {
		offset += len(row.raw)
	}
	want := append([]byte(nil), old[:offset]...)
	binary.LittleEndian.PutUint16(want, a.baseCount+1)
	want = append(want, []byte{0x91, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}...)
	want = append(want, a.activeCount+1)
	want = append(want, old[offset+1:]...)
	want = append(want, 0x91, 0)
	want = append(want, make([]byte, 48)...)
	if !bytes.Equal(next, want) || len(b.baseRows) != len(a.baseRows)+1 || len(b.activeRows) != len(a.activeRows)+1 {
		t.Fatal("新增145行改变了既有目录或不符合独立夹具")
	}
}

func TestAttendanceMarbleLoginRestoresBuffsWithoutOpeningWindow(t *testing.T) {
	s, session := newDeferredTailResumeFixture(t)
	s.attendanceMarbleCatalog = &attendanceMarbleCatalog{ClearRewards: [3][2]int64{{4, 2}, {8, 2}, {12, 2}}}
	repos, _ := s.repositoryGroup()
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	character, _, _ := repos.Character.Load(context.Background(), characterID)
	character.Stats[attendanceMarbleBuffKey(30)] = 2
	if err := repos.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	if err := s.restoreAttendanceMarbleBuffs(session); err != nil {
		t.Fatal(err)
	}
	packets := deferredTailPacketSignatures(t, session.conn.(*bufferConn).write.Bytes())
	if len(packets) != 2 || packets[0].msgID != 488 || packets[1].msgID != 487 || binary.LittleEndian.Uint32(packets[1].body[5:]) != 2 {
		t.Fatalf("登录Buff恢复错误或意外打开窗口：%+v", packets)
	}
	after, _, _ := repos.Character.Load(context.Background(), characterID)
	if after.Stats[attendanceMarbleBuffKey(30)] != 2 {
		t.Fatal("登录显示消耗了Buff")
	}
}

func TestAttendanceMarbleMonsterBonusCommitsToActualCharacter(t *testing.T) {
	for _, tc := range []struct {
		mask  byte
		bonus int64
	}{{0, 0}, {2, 200}, {8, 100}, {10, 300}} {
		s, runtime, repos := prepareCurrentDungeonMonsterExperienceTest(t)
		runtime.attendanceMarbleBuffs = tc.mask
		session := &gameSession{accountID: runtime.Character.AccountID, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
		result, err := s.awardCurrentDungeonMonsterExperience(context.Background(), session, runtime, runtime.Room.Snapshot().Monsters[0])
		if err != nil {
			t.Fatal(err)
		}
		character, _, _ := repos.Character.Load(context.Background(), "99")
		if result.AttendanceMarbleBonus != uint32(tc.bonus) || character.Stats["exp"] != 200+tc.bonus {
			t.Fatalf("活动经验未提交：mask=%d bonus=%d exp=%d", tc.mask, result.AttendanceMarbleBonus, character.Stats["exp"])
		}
	}
}

func TestAttendanceMarbleTrainingDoesNotConsumePendingBuff(t *testing.T) {
	s, runtime, repos := prepareCurrentDungeonMonsterExperienceTest(t)
	runtime.Dungeon = worldmap.Dungeon{ID: 5000, Path: "dungeon/PoongjinTrainingRoom/PoongjinTrainingRoom.dgn"}
	runtime.Character.Stats[attendanceMarbleBuffKey(30)] = 1
	if err := repos.Character.Save(context.Background(), runtime.Character); err != nil {
		t.Fatal(err)
	}
	session := &gameSession{accountID: runtime.Character.AccountID, selectedCharacterID: 99}
	if err := s.activateAttendanceMarbleDungeonBuffs(session, runtime); err != nil {
		t.Fatal(err)
	}
	character, _, _ := repos.Character.Load(context.Background(), "99")
	if runtime.attendanceMarbleBuffs != 0 || character.Stats[attendanceMarbleBuffKey(30)] != 1 {
		t.Fatal("修炼道场消耗活动Buff")
	}
}

func TestAttendanceMarbleActivityControlRejectsForgedRollAfterRestart(t *testing.T) {
	ctx := context.Background()
	s, session := newDeferredTailResumeFixture(t)
	s.attendanceMarbleCatalog = &attendanceMarbleCatalog{ClearRewards: [3][2]int64{{4, 2}, {8, 2}, {12, 2}}}
	if err := s.initializeActivityControls(ctx); err != nil {
		t.Fatal(err)
	}
	if !s.currentAttendanceMarbleAvailable() {
		t.Fatal("默认配置未启用大富翁")
	}
	if _, err := s.activityControls.Save(ctx, 0, []onlineevent.ControlChange{{ID: 145, Enabled: false}}); err != nil {
		t.Fatal(err)
	}
	if err := s.initializeActivityControls(ctx); err != nil {
		t.Fatal(err)
	}
	if s.currentAttendanceMarbleAvailable() {
		t.Fatal("重启加载后活动145未关闭")
	}
	repos, _ := s.repositoryGroup()
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	before, _, _ := repos.Character.Load(ctx, characterID)
	if _, err := s.settleAttendanceMarble(session, &[2]byte{1, 2}, false); err == nil {
		t.Fatal("关闭活动后伪造掷骰仍被接受")
	}
	after, _, _ := repos.Character.Load(ctx, characterID)
	if after.Stats["attendance_marble_dice_used"] != before.Stats["attendance_marble_dice_used"] || after.Stats["attendance_marble_serial"] != before.Stats["attendance_marble_serial"] {
		t.Fatal("关闭活动后请求仍修改骰子进度")
	}
}

func TestAttendanceMarbleMissingTableDoesNotBlockLogin(t *testing.T) {
	s, session, conn, _, _ := onlinePanelFixture(t)
	if s.currentAttendanceMarbleAvailable() {
		t.Fatal("缺少棋盘表却公布了大富翁入口")
	}
	if err := s.restoreAttendanceMarbleBuffs(session); err != nil {
		t.Fatalf("缺少可选活动表阻断了登录：%v", err)
	}
	if conn.write.Len() != 0 {
		t.Fatal("缺少棋盘表时发送了伪造活动状态")
	}
}
