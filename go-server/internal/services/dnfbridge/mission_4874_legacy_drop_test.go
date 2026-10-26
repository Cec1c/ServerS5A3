package dnfbridge

import (
	"encoding/binary"
	"encoding/hex"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

const (
	mission4874QuestID   int64  = 4874
	mission4874ObjectID  int64  = 109006893
	mission4874ItemID    uint32 = 10164919
	mission4874DungeonID int64  = 52

	// 依照现场布局合成毒草报告：13字节legacy头、74字节正文；校验和、坐标和footer已脱敏。
	mission4874LiveLegacyFrameHex = "01270057000000000000009b012d507f06ffff0000000000000000000000000000000000000000000000000000000000000001003412003200000000000000000000000000000000000000000000000000000012345678"
)

func mission4874DropCase() seekerPassiveDropCase {
	return seekerPassiveDropCase{
		code: mission4874ObjectID, questID: mission4874QuestID,
		itemID: mission4874ItemID, dungeonID: mission4874DungeonID, difficulty: 2,
	}
}

func mission4874LiveLegacyFrame(t *testing.T) []byte {
	t.Helper()
	raw, err := hex.DecodeString(mission4874LiveLegacyFrameHex)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := dnfproto.ParseLegacyGamePacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 87 || dnfproto.LegacyGameHeaderSize != 13 || len(packet.Body) != 74 ||
		packet.Header.Cmd != 1 || packet.Header.Type != uint16(dnfenum.CmdPacketDieMonster) ||
		packet.Header.Sequence != 411 || binary.LittleEndian.Uint32(packet.Body[:4]) != uint32(mission4874ObjectID) ||
		binary.LittleEndian.Uint16(packet.Body[4:6]) != ^uint16(0) || packet.Body[30] != 0 || packet.Body[37] != 1 {
		t.Fatalf("现场legacy报告形状变化：头=%+v 正文=%x", packet.Header, packet.Body)
	}
	return raw
}

func mission4874LegacyFrame(sequence uint16, body []byte) []byte {
	raw := make([]byte, dnfproto.LegacyGameHeaderSize+len(body))
	raw[0] = 1
	binary.LittleEndian.PutUint16(raw[1:3], uint16(dnfenum.CmdPacketDieMonster))
	binary.LittleEndian.PutUint32(raw[3:7], uint32(len(raw)))
	binary.LittleEndian.PutUint16(raw[11:13], sequence)
	copy(raw[dnfproto.LegacyGameHeaderSize:], body)
	return raw
}

func mission4874PassiveUsage(t *testing.T, runtime *runtimeDungeonState) int {
	t.Helper()
	scene, ok := runtime.Session.Scene()
	if !ok {
		t.Fatal("缺少毒草报告所属房间")
	}
	return runtime.passiveQuestDropSourceUsage[runtimeDungeonRoomKeyFromScene(scene)][mission4874ObjectID]
}

func mission4874AssertResolvedReceipt(t *testing.T, session *gameSession, runtime *runtimeDungeonState, sequence uint64) currentDungeonPassiveQuestDropReceipt {
	t.Helper()
	key := currentDungeonPassiveQuestDropEventKeyFor(session, sequence)
	receipt, ok := runtime.passiveQuestDropReceipts[key]
	if !ok || !receipt.Resolved || receipt.EnemyCode != mission4874ObjectID {
		t.Fatalf("legacy报告未到达类别3结算：序号=%d 回执=%+v 存在=%t", sequence, receipt, ok)
	}
	return receipt
}

func TestMission4874LegacyFrameSourceQuotaAndReceipts(t *testing.T) {
	t.Setenv(mapOwnedPassiveObjectModeEnvName, "legacy")
	fixture := newSeekerPassiveDropFixture(t, seekerPassiveDropFixtureOptions{
		drop: mission4874DropCase(), active: true, maxStack: 10, dropRate: 100, difficulty: 2,
		passive: []int64{mission4874ObjectID, mission4874ObjectID},
	})
	raw := mission4874LiveLegacyFrame(t)
	body := append([]byte(nil), raw[dnfproto.LegacyGameHeaderSize:]...)
	request, err := fixture.service.decodeCurrentDungeonMonsterDeath(body)
	if err != nil || request.Layout != dungeoncmd.DieMonsterRequestLayoutVariableCombat || len(request.OpaqueTail) != 0 {
		t.Fatalf("现场毒草正文解析失败：请求=%+v 错误=%v", request, err)
	}
	beforeRoom := fixture.runtime.Room.Snapshot()

	if err := fixture.service.handleLegacyGamePacket(fixture.session, raw); err != nil {
		t.Fatal(err)
	}
	sequence, initialized := fixture.session.guild.purchaseSequence.currentHigh()
	if !initialized || uint16(sequence) != 411 {
		t.Fatalf("legacy连接序号未观测：序号=%d 已初始化=%t", sequence, initialized)
	}
	receipt := mission4874AssertResolvedReceipt(t, fixture.session, fixture.runtime, sequence)
	if !receipt.Granted || !receipt.Refreshed || seekerPassiveDropHeld(t, fixture) != 1 ||
		mission4874PassiveUsage(t, fixture.runtime) != 1 || len(fixture.runtime.passiveQuestDropReceipts) != 1 {
		t.Fatalf("100%%隔离夹具未提交首个来源：回执=%+v 持有=%d 使用=%d 回执数=%d", receipt,
			seekerPassiveDropHeld(t, fixture), mission4874PassiveUsage(t, fixture.runtime), len(fixture.runtime.passiveQuestDropReceipts))
	}
	if got := seekerPassiveDropRefreshCount(t, fixture.connection.write.Bytes()); got != 1 {
		t.Fatalf("首报库存刷新=%d，期望1且没有普通怪死亡通知", got)
	}

	fixture.connection.write.Reset()
	if err := fixture.service.handleLegacyGamePacket(fixture.session, raw); err != nil {
		t.Fatal(err)
	}
	if seekerPassiveDropHeld(t, fixture) != 1 || fixture.connection.write.Len() != 0 ||
		mission4874PassiveUsage(t, fixture.runtime) != 1 ||
		!reflect.DeepEqual(receipt, mission4874AssertResolvedReceipt(t, fixture.session, fixture.runtime, sequence)) {
		t.Fatal("相同legacy序号重放重复发奖、刷新或消费来源")
	}
	changedBody := append([]byte(nil), body...)
	changedBody[39] ^= 1
	if err := fixture.service.handleLegacyGamePacket(fixture.session, mission4874LegacyFrame(411, changedBody)); err != nil {
		t.Fatal(err)
	}
	if seekerPassiveDropHeld(t, fixture) != 1 || mission4874PassiveUsage(t, fixture.runtime) != 1 ||
		fixture.connection.write.Len() != 0 || len(fixture.runtime.passiveQuestDropReceipts) != 1 {
		t.Fatal("相同序号改正文取得了新额度")
	}

	if err := fixture.service.handleLegacyGamePacket(fixture.session, mission4874LegacyFrame(412, body)); err != nil {
		t.Fatal(err)
	}
	mission4874AssertResolvedReceipt(t, fixture.session, fixture.runtime, sequence+1)
	if seekerPassiveDropHeld(t, fixture) != 2 || mission4874PassiveUsage(t, fixture.runtime) != 2 ||
		len(fixture.runtime.passiveQuestDropReceipts) != 2 {
		t.Fatal("不同序号、相同正文未分别结算两个真实来源")
	}
	fixture.connection.write.Reset()
	for _, wireSequence := range []uint16{413, 410} {
		if err := fixture.service.handleLegacyGamePacket(fixture.session, mission4874LegacyFrame(wireSequence, body)); err != nil {
			t.Fatal(err)
		}
	}
	if seekerPassiveDropHeld(t, fixture) != 2 || mission4874PassiveUsage(t, fixture.runtime) != 2 ||
		len(fixture.runtime.passiveQuestDropReceipts) != 2 || fixture.connection.write.Len() != 0 {
		t.Fatal("额度耗尽或未记录旧序号追加了回执/奖励")
	}
	if !reflect.DeepEqual(beforeRoom, fixture.runtime.Room.Snapshot()) {
		t.Fatal("任务对象报告修改了房间怪物或清房状态")
	}
}

func TestMission4874LegacySequenceWrapsFFFFToZero(t *testing.T) {
	t.Setenv(mapOwnedPassiveObjectModeEnvName, "legacy")
	fixture := newSeekerPassiveDropFixture(t, seekerPassiveDropFixtureOptions{
		drop: mission4874DropCase(), active: true, maxStack: 10, dropRate: 100, difficulty: 2,
		passive: []int64{mission4874ObjectID, mission4874ObjectID, mission4874ObjectID},
	})
	body := mission4874LiveLegacyFrame(t)[dnfproto.LegacyGameHeaderSize:]
	for _, wireSequence := range []uint16{0xffff, 0} {
		if err := fixture.service.handleLegacyGamePacket(fixture.session, mission4874LegacyFrame(wireSequence, body)); err != nil {
			t.Fatal(err)
		}
		high, ok := fixture.session.guild.purchaseSequence.currentHigh()
		if !ok || uint16(high) != wireSequence {
			t.Fatalf("回绕序号未透传：wire=%d high=%d", wireSequence, high)
		}
		mission4874AssertResolvedReceipt(t, fixture.session, fixture.runtime, high)
	}
	if seekerPassiveDropHeld(t, fixture) != 2 || mission4874PassiveUsage(t, fixture.runtime) != 2 ||
		len(fixture.runtime.passiveQuestDropReceipts) != 2 {
		t.Fatal("FFFF到0回绕丢失事件身份")
	}
	fixture.connection.write.Reset()
	for _, wireSequence := range []uint16{0xfffe, 0} {
		if err := fixture.service.handleLegacyGamePacket(fixture.session, mission4874LegacyFrame(wireSequence, body)); err != nil {
			t.Fatal(err)
		}
	}
	if seekerPassiveDropHeld(t, fixture) != 2 || mission4874PassiveUsage(t, fixture.runtime) != 2 ||
		len(fixture.runtime.passiveQuestDropReceipts) != 2 || fixture.connection.write.Len() != 0 {
		t.Fatal("回绕后的未知旧序号或已知重放新增奖励")
	}
}

func TestMission4874LegacyRejectsUnplacedObjectAndWrongClass(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func([]byte)
	}{
		{name: "未摆放对象", edit: func(raw []byte) {
			binary.LittleEndian.PutUint32(raw[dnfproto.LegacyGameHeaderSize:], uint32(mission4874ObjectID+1))
		}},
		{name: "非命令分类", edit: func(raw []byte) { raw[0] = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(mapOwnedPassiveObjectModeEnvName, "legacy")
			fixture := newSeekerPassiveDropFixture(t, seekerPassiveDropFixtureOptions{
				drop: mission4874DropCase(), active: true, maxStack: 10, dropRate: 100, difficulty: 2,
			})
			raw := mission4874LiveLegacyFrame(t)
			test.edit(raw)
			beforeRoom := fixture.runtime.Room.Snapshot()
			if err := fixture.service.handleLegacyGamePacket(fixture.session, raw); err != nil {
				t.Fatal(err)
			}
			if seekerPassiveDropHeld(t, fixture) != 0 || len(fixture.runtime.passiveQuestDropReceipts) != 0 ||
				mission4874PassiveUsage(t, fixture.runtime) != 0 || fixture.connection.write.Len() != 0 ||
				!reflect.DeepEqual(beforeRoom, fixture.runtime.Room.Snapshot()) {
				t.Fatal("非法legacy报告发奖、消费来源或改动房间")
			}
		})
	}
}
