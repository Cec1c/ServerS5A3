package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// DOC029（金币持有上限重复升级）回归。
//
// 当前 v101 客户端的金币持有上限完全由客户端档位字节决定：档位 0 = 4亿，档位
// 1..4 = 5/6/7/8亿（EXE 0x032EA592 读 [0x057B7008]，档位表 0x4CF88D8）。该字节
// 只有两个写入点：op978 回包处理器（0x01FD4B5A，class1 成功位 + u8 档位 +
// u32 结果 = 0 才写入）与档位通知 op947（0x0203AE86，读 1 字节后写入）。
//
// 只发 op978 回包而不重发 op947 时，一旦回包在客户端被丢弃，客户端档位就停在
// 0：下一次点击重发同一档位，服务端判为重复请求（不再扣费、档位不变），上限
// 显示也完全不动 —— 正是 DOC029 的现象。因此已提交的档位必须在每次处理后
// 通过 op947 重新发布一次。
func TestCarryGoldDoc029CommittedTierIsPublishedAfterUpgrade(t *testing.T) {
	s, session, repos, conn := newCarryGoldFixture(t, 25_000_000, 0)
	if err := s.handleCurrentUpgradeCarryGold(session, carryGoldRequest(1)); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if ack.Header.MsgID != 978 || ack.Header.Classification != 1 || len(ack.Body) != 6 ||
		ack.Body[0] != 1 || ack.Body[1] != 1 || binary.LittleEndian.Uint32(ack.Body[2:]) != 0 {
		t.Fatalf("升级回包 header=%+v body=%x", ack.Header, ack.Body)
	}
	tier, rest := splitCurrentGameServerUpperPacketAuto(t, rest)
	if tier.Header.MsgID != currentCarryGoldStageMsgID || tier.Header.Classification != 0 ||
		!reflect.DeepEqual(tier.Body, []byte{1}) {
		t.Fatalf("已提交档位没有再发布: header=%+v body=%x rest=%x", tier.Header, tier.Body, rest)
	}
	wallet, rest := splitCurrentGameServerUpperPacketAuto(t, rest)
	if wallet.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) ||
		wallet.Header.Classification != 0 || len(rest) != 0 {
		t.Fatalf("钱包刷新 header=%+v rest=%x", wallet.Header, rest)
	}
	stored, _, _ := repos.Character.Load(context.Background(), "17")
	if stored.Stats["gold"] != 20_000_000 || stored.Stats["carry_gold_stage"] != 1 {
		t.Fatalf("提交后 stats=%v", stored.Stats)
	}
}

// DOC029：重复升级必须一直可用 —— 首次购买后即使回包丢失，服务端也要把已提交
// 的档位与钱包重新同步给客户端（不重复扣费），客户端采纳后下一档才能继续购买。
func TestCarryGoldDoc029RepeatUpgradeAfterLostReply(t *testing.T) {
	s, session, repos, conn := newCarryGoldFixture(t, 25_000_000, 0)
	if err := s.handleCurrentUpgradeCarryGold(session, carryGoldRequest(1)); err != nil {
		t.Fatal(err)
	}
	conn.write.Reset() // 第 1 次点击的回包在客户端丢失。

	// 第 2 次点击：客户端档位仍为 0，于是重发第 1 档。不得重复扣费，但必须重发
	// 已提交档位与钱包，让客户端能采纳。
	if err := s.handleCurrentUpgradeCarryGold(session, carryGoldRequest(1)); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if ack.Header.MsgID != 978 || ack.Header.Classification != 1 || len(ack.Body) != 6 ||
		ack.Body[0] != 0 || ack.Body[1] != 1 || binary.LittleEndian.Uint32(ack.Body[2:]) != 2 {
		t.Fatalf("重复请求回包 header=%+v body=%x", ack.Header, ack.Body)
	}
	tier, rest := splitCurrentGameServerUpperPacketAuto(t, rest)
	if tier.Header.MsgID != currentCarryGoldStageMsgID || !reflect.DeepEqual(tier.Body, []byte{1}) {
		t.Fatalf("重复请求档位同步 header=%+v body=%x", tier.Header, tier.Body)
	}
	if _, rest = splitCurrentGameServerUpperPacketAuto(t, rest); len(rest) != 0 {
		t.Fatalf("重复请求多余回包 rest=%x", rest)
	}
	duplicate, _, _ := repos.Character.Load(context.Background(), "17")
	if duplicate.Stats["gold"] != 20_000_000 || duplicate.Stats["carry_gold_stage"] != 1 {
		t.Fatalf("重复请求改变了档位/钱包: %v", duplicate.Stats)
	}

	// 第 3 次点击：客户端采纳档位 1 后请求第 2 档 —— 必须真实扣费并推进档位。
	conn.write.Reset()
	if err := s.handleCurrentUpgradeCarryGold(session, carryGoldRequest(2)); err != nil {
		t.Fatal(err)
	}
	ack, rest = splitGameServerUpperPacket(t, conn.write.Bytes())
	if ack.Header.MsgID != 978 || ack.Body[0] != 1 || ack.Body[1] != 2 ||
		binary.LittleEndian.Uint32(ack.Body[2:]) != 0 {
		t.Fatalf("第二档回包 header=%+v body=%x", ack.Header, ack.Body)
	}
	tier, rest = splitCurrentGameServerUpperPacketAuto(t, rest)
	if tier.Header.MsgID != currentCarryGoldStageMsgID || !reflect.DeepEqual(tier.Body, []byte{2}) {
		t.Fatalf("第二档档位发布 header=%+v body=%x", tier.Header, tier.Body)
	}
	if _, rest = splitCurrentGameServerUpperPacketAuto(t, rest); len(rest) != 0 {
		t.Fatalf("第二档多余回包 rest=%x", rest)
	}
	stored, _, _ := repos.Character.Load(context.Background(), "17")
	if stored.Stats["gold"] != 15_000_000 || stored.Stats["carry_gold_stage"] != 2 {
		t.Fatalf("重复升级后 stats=%v", stored.Stats)
	}
}
