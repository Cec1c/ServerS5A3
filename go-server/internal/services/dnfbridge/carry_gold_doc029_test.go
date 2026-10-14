package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
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

// failingCarryGoldCommit 让真实事务在 apply 成功之后提交失败，模拟仓储/持久化故障。
type failingCarryGoldCommit struct {
	base    dnfrepo.CharacterAssetUnitOfWork
	failure error
}

func (f failingCarryGoldCommit) WithinCharacterAssets(
	ctx context.Context,
	characterID string,
	apply func(dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error,
) error {
	return f.base.WithinCharacterAssets(ctx, characterID, func(characters dnfrepo.CharacterRepository, inventory dnfrepo.InventoryRepository, equipment dnfrepo.EquipmentRepository) error {
		if err := apply(characters, inventory, equipment); err != nil {
			return err
		}
		return f.failure
	})
}

// DOC029：真实仓储/持久化错误必须继续向外传播。carrygold.Upgrade 在写库之前就把
// result.Known 置为 true，所以“档位已知”不能当作业务拒绝处理；否则保存失败会被
// 当成成功路径，既吞掉错误，又把未提交的档位/钱包当成已提交状态发布出去。
func TestCarryGoldDoc029RepositoryFailurePropagatesWithoutTierPublish(t *testing.T) {
	injected := errors.New("character asset commit failed")
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "17", AccountID: "dnf:1", Level: 90,
		Stats: map[string]int64{"gold": 25_000_000, "carry_gold_stage": 0},
	}); err != nil {
		t.Fatal(err)
	}
	repos.CharacterAssets = failingCarryGoldCommit{base: repos.CharacterAssets, failure: injected}
	conn := &bufferConn{}
	s := &Service{
		options:            options{gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
	}
	session := &gameSession{conn: conn, selectedCharacterID: 17, accountID: "dnf:1"}

	err := s.handleCurrentUpgradeCarryGold(session, carryGoldRequest(1))
	if !errors.Is(err, injected) {
		t.Fatalf("仓储错误被吞掉: err=%v", err)
	}
	for stream := conn.write.Bytes(); len(stream) > 0; {
		var packet dnfproto.ChannelPacket
		packet, stream = splitCurrentGameServerUpperPacketAuto(t, stream)
		if packet.Header.MsgID == currentCarryGoldStageMsgID {
			t.Fatalf("仓储错误后仍发布档位通知: %+v body=%x", packet.Header, packet.Body)
		}
		if packet.Header.MsgID == 978 && len(packet.Body) > 0 && packet.Body[0] == 1 {
			t.Fatalf("仓储错误后仍发布成功回包: %x", packet.Body)
		}
	}
	stored, _, _ := repos.Character.Load(context.Background(), "17")
	if stored.Stats["gold"] != 25_000_000 || stored.Stats["carry_gold_stage"] != 0 {
		t.Fatalf("失败事务改动了资产: %v", stored.Stats)
	}
}
