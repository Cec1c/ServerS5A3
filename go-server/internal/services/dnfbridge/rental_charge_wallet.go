package dnfbridge

import (
	"os"
	"strings"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 现场：租赁武器系统里用金币购买幸运星（op1000 充值）成功后，背包金币显示不变，
// 只有重选角色（重登会话重建容器）之后才减少。服务端事务本身是对的——金币已经落在
// 角色仓储里，缺的是成功路径上的即时钱包投影：改动前 op1000 成功只回厂商那 13 字节
// （01 + mode + quantity + totalLuckyStar）加一条 op985 面板刷新，没有任何一条报文携带
// 已提交的金币。客户端本地的金币行因此保持旧值，直到整容器重建。
//
// 已提交钱包值：purchaseCurrentRentalPoints 返回的 currentRentalAssetResult.Gold，
// 就是同一次事务里写进 character.Stats["gold"] 的值（权威、提交后）。
//
// 客户端接受形状：当前 v101 客户端用 class0/op14（CmdPacketWalkoutPartyMember）的
// single-row 容器更新读取 list0/slot0 的金币行（itemID=0，余额在 stack_count 字段），
// 与登录引导、金库整理、普通 NPC 商店、掉落拾取、carry-gold 上限升级用的是同一个
// buildCurrentGoldStateBody 行。DOC029 的现场证据明确记录过“钱包行 op14 被接收”，
// 所以这里沿用它，不发明新包、不改厂商回包字节、不动 op985 面板体。
//
// 边界：
//   - 只在成功提交之后追加，且排在“op1000 应答 → op985 面板”之后；
//     事务拒绝（金币不足、越限、仓储失败）一条都不发，失败仍然不扣。
//   - 不改变任何既有报文的字节、顺序或 sequence 分配：这条是严格增量。
//   - 一键回退 DNFBRIDGE_RENTAL_CHARGE_WALLET=off（或 0/false/no/legacy/disable/
//     disabled）就逐字节回到改动前。既有 DNFBRIDGE_RENTAL_REJECT_ACK=off
//     同样抑制本投影，保证那条开关的“逐字节回到今天”承诺继续成立。
//   - 不碰普通 NPC 商店、金币上限、租赁点面板体与 op999 租用路径（租用花的是幸运星，
//     不是金币，钱包不变）。
const currentRentalChargeWalletEnv = "DNFBRIDGE_RENTAL_CHARGE_WALLET"

// currentRentalChargeWalletProjectionEnabled 归一化开关取值。空值 = 默认开启（本条修复）；
// 非法值不放大行为，一律回落默认档（与既有开关约定一致）。
func currentRentalChargeWalletProjectionEnabled() bool {
	if currentRentalRejectAckMode() == currentRentalRejectAckOff {
		return false
	}
	value := strings.ToLower(strings.TrimSpace(os.Getenv(currentRentalChargeWalletEnv)))
	if value == "" || !currentRentalRejectAckOffValues[value] {
		return true
	}
	return false
}

// publishCurrentRentalChargeWallet 把已提交的角色金币行重新投影给客户端，
// 让购买幸运星后的背包金币立即刷新，而不必重选角色。
//
// 形状与登录/金库/商店/掉落同源：class0 + op14 + buildCurrentGoldStateBody(gold)，
// 即“list0 单行：slot0、itemID 0、余额”。开关关闭时返回 nil 且一个字节都不写。
func (s *Service) publishCurrentRentalChargeWallet(session *gameSession, gold int64) error {
	if !currentRentalChargeWalletProjectionEnabled() {
		return nil
	}
	body := buildCurrentGoldStateBody(gold)
	s.logGameEvent(session, "game-upper-rental-charge-wallet-send",
		"msg_id", uint16(dnfenum.CmdPacketWalkoutPartyMember),
		"classification", 0,
		"list_type", dnfrepo.MainInventoryListType,
		"slot", currentGoldWalletReservedSlot,
		"gold", gold,
		"body_len", len(body),
		"entry_count", 1,
		"entry_size", currentItemListEntryWireSize,
		"body_source", "current_exe_class0_op14_list0_slot0_committed_gold_wallet",
		"env", currentRentalChargeWalletEnv)
	return s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember), body, 0)
}
