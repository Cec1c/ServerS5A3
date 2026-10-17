package dnfbridge

import (
	"context"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 镜像用例（native-next916/enchant-review）。目标位置：internal/services/dnfbridge/。
//
// op272 成功路径那条被点名的 class0/0x000D list 0 是 op13 替换语义：body 一上线就会替换
// 客户端整个主背包。所以"角色钱包权威读不到"必须按"建不出来"处理 —— 跳过这条刷新、保留
// class1 ACK，而**绝不能**发一份没有 0:0 钱包行的 list 0（客户端金币被清 0，SQL 侧却完好）。
//
// 缺口在 buildCurrentItemListBodyForSession 的 list-0 分支（current_item_list_session.go:163-209）：
// Character.Load 报错 / 角色记录不存在 / Stats 里没有 gold / 保留格被非钱包行占用，四种情况
// 都只写一行日志，照样返回一份可上线的 body。
//
// TestPair916AuthoritativeList0RefreshSkippedWhenWalletAuthorityUnreadable 在**未打补丁**的
// own-source838 上是红的（这就是缺口），打上 wallet-authority-fail-closed.patch 后应为绿。
// TestPair916CharacterizationWalletlessList0RefreshReachesClient 用于未打补丁时先确认缺口存在；
// 打补丁后它会 skip，那时可以直接删除。

type pair916FailingCharacterRepository struct {
	dnfrepo.CharacterRepository
	err error
}

func (r pair916FailingCharacterRepository) Load(context.Context, string) (dnfrepo.CharacterRecord, bool, error) {
	return dnfrepo.CharacterRecord{}, false, r.err
}

func pair916FixtureWithUnreadableWallet(t *testing.T) *refreshCostFixture {
	t.Helper()
	fixture := newRefreshCostFixture(t)
	fixture.warmUp(t)
	fixture.buffer.write.Reset()
	group := fixture.group
	group.Character = pair916FailingCharacterRepository{
		CharacterRepository: group.Character,
		err:                 errors.New("character wallet read timeout"),
	}
	fixture.service.repositoryProvider = func() (dnfrepo.Group, bool) { return group, true }
	return fixture
}

func pair916SendMarkedList0Refresh(t *testing.T, fixture *refreshCostFixture) dnfproto.ChannelPacket {
	t.Helper()
	if err := fixture.service.sendAlignedUpperResponses(
		fixture.session,
		pair914ModuleResult(pair914ModuleSnapshotBody(), []byte{dnfrepo.MainInventoryListType}),
	); err != nil {
		t.Fatalf("sendAlignedUpperResponses error = %v", err)
	}
	raw := append([]byte(nil), fixture.buffer.write.Bytes()...)
	frame, _ := splitGameServerUpperPacketWithHeader(t, raw, dnfproto.GameServerUpperHeaderSize16)
	return frame
}

// 打补丁后必须成立：钱包权威读不到 => 不替换客户端容器 => 第一帧直接是 op272 ACK。
func TestPair916AuthoritativeList0RefreshSkippedWhenWalletAuthorityUnreadable(t *testing.T) {
	fixture := pair916FixtureWithUnreadableWallet(t)
	frame := pair916SendMarkedList0Refresh(t, fixture)
	if frame.Header.MsgID != uint16(dnfenum.CmdPacketEnchantByBead) {
		t.Fatalf("wallet-authority-less list-0 refresh must not reach the client: first frame = class%d msg0x%04X body_len=%d",
			frame.Header.Classification, frame.Header.MsgID, len(frame.Body))
	}
}
