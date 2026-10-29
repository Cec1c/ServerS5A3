package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"testing"
	"time"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func assertCurrentDayDungeonSchedulePackets(t *testing.T, data []byte, openIDs ...uint32) []byte {
	t.Helper()
	for id := uint32(4906); id <= 4910; id++ {
		if len(data) < 32 || data[0] != 0 || binary.LittleEndian.Uint16(data[1:3]) != 855 ||
			binary.LittleEndian.Uint32(data[3:7]) != 32 || !bytes.Equal(data[11:16], make([]byte, 5)) {
			t.Fatalf("地下城%d的855固定16字节分类0报头错误：%x", id, data)
		}
		wantState := uint32(0)
		for _, opened := range openIDs {
			if id == opened {
				wantState = 1
			}
		}
		want := [4]uint32{0, wantState, id, 0}
		for i, value := range want {
			if got := binary.LittleEndian.Uint32(data[16+i*4:]); got != value {
				t.Fatalf("地下城%d字段%d=%d，预期%d", id, i, got, value)
			}
		}
		data = data[32:]
	}
	return data
}

func TestDayDungeonScheduleWireAllWeekdaysCloseStaleMaps(t *testing.T) {
	s := &Service{options: options{currentClientVersion: "official-24.9"}}
	conn := &bufferConn{}
	p := &gameSession{conn: conn, selectedCharacterID: 31}
	want := [][]uint32{{4906}, {4910}, {4908}, {4907}, {4909}, {4906, 4908, 4910}, {4907, 4909}}
	for i, ids := range want {
		conn.write.Reset()
		if err := s.sendCurrentDayDungeonSchedule(p, uint16(2418+i), "七日契约核对"); err != nil {
			t.Fatal(err)
		}
		if rest := assertCurrentDayDungeonSchedulePackets(t, conn.write.Bytes(), ids...); len(rest) != 0 {
			t.Fatalf("周常以外多发数据：%x", rest)
		}
	}
}

func TestDayDungeonScheduleFailureRetriesWholeSnapshot(t *testing.T) {
	s := &Service{options: options{currentClientVersion: "official-24.9"}}
	for failAt := 1; failAt <= 5; failAt++ {
		failure := errors.New("注入周常开放状态写失败")
		conn := &failNthDungeonWriteConn{failAt: failAt, err: failure}
		p := &gameSession{conn: conn, selectedCharacterID: 31}
		if err := s.sendCurrentDayDungeonSchedule(p, 2421, "第一次"); !errors.Is(err, failure) {
			t.Fatalf("第%d包写失败未传播：%v", failAt, err)
		}
		conn.failAt = 0
		conn.write.Reset()
		if err := s.sendCurrentDayDungeonSchedule(p, 2422, "跨日重试"); err != nil {
			t.Fatal(err)
		}
		assertCurrentDayDungeonSchedulePackets(t, conn.write.Bytes(), 4909)
	}
}

func TestDayDungeonScheduleRequiresVerifiedProfileDayAndCharacter(t *testing.T) {
	for _, tc := range []struct {
		profile string
		day     uint16
		charID  uint16
	}{{"official-24.9", 0, 31}, {"official-24.9", 2417, 31}, {"official-24.9", 2425, 31}, {"legacy", 2421, 31}, {"official-24.9", 2421, 0}} {
		conn := &bufferConn{}
		s := &Service{options: options{currentClientVersion: tc.profile}}
		if err := s.sendCurrentDayDungeonSchedule(&gameSession{conn: conn, selectedCharacterID: tc.charID}, tc.day, "无效上下文"); err != nil || conn.write.Len() != 0 {
			t.Fatal("无效上下文仍发开放状态", tc, err)
		}
	}
}

func TestRealPVFDayDungeonScheduleRestoresTownAndChangedCharacter(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需要现用PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Date(2026, 10, 8, 4, 0, 0, 0, time.UTC)
	s := &Service{options: options{currentClientVersion: "official-24.9", joustEventDisabled: true}, initialEquipmentArchive: archive, gameplayTimers: queue}
	conn := &bufferConn{}
	p := &gameSession{conn: conn, connID: "周常换角", selectedCharacterID: 31, characterGeneration: 1, activityCatalogDayEvent: 2421}
	catalog := &currentSpendTimeRuntimeCatalog{rewardItemIDs: currentSpendTimeJoustCatalogGoldenRewards, totalStageSeconds: 10800}
	// 108已按进程去重，换角仍必须拿到五图开放表。
	for _, id := range []uint16{31, 32} {
		p.selectedCharacterID = id
		p.characterGeneration++
		conn.write.Reset()
		sent, err := s.sendCurrentSpendTimeProtocolState(p, catalog, 0, 0, false)
		if err != nil || sent {
			t.Fatal("同日换角重复目录或未成功同步", sent, err)
		}
		progress, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
		if progress.Header.MsgID != 1206 {
			t.Fatal("在线活动状态顺序改变")
		}
		assertCurrentDayDungeonSchedulePackets(t, rest, 4907)
	}
	p.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: 32, TownID: 107}
	conn.write.Reset()
	if err := s.sendCurrentDayDungeonTownSchedule(p, "区域加载就绪"); err != nil {
		t.Fatal(err)
	}
	assertCurrentDayDungeonSchedulePackets(t, conn.write.Bytes(), 4907)
	for _, position := range []currentTownPositionSnapshot{{CharacterID: 31, TownID: 107}, {CharacterID: 32, TownID: 1}, {CharacterID: 32, TownID: 107, AreaID: 1}} {
		p.townPositionSnapshot = position
		conn.write.Reset()
		if err := s.sendCurrentDayDungeonTownSchedule(p, "非当前周常区域"); err != nil || conn.write.Len() != 0 {
			t.Fatal("错误角色或区域仍发送开放状态", err)
		}
	}
	p.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: 32, TownID: 107}
	p.dungeon.runtime = &runtimeDungeonState{}
	if err := s.sendCurrentDayDungeonTownSchedule(p, "副本内"); err != nil || conn.write.Len() != 0 {
		t.Fatal("副本内错误恢复城镇状态", err)
	}
}
