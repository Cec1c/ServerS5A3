package dnfbridge

import (
	"context"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件落地 alignedcmd.Result.AuthoritativeListRefreshes：模块自己拼的
// class0/0x000D 整容器 body 换成**会话权威投影**。用的是与登录引导、容器后置动作、
// 商城购买同一个 builder（buildCurrentItemListBodyForSession），所以 0:0 钱包行、
// 账号共享的 0:354..0:365、装备品级种子/孔位/符文、堆叠品使用期限与到期时间
// 一次性全部带上——不需要在领域里复刻第二套投影，也就不会跟登录投影漂移。
//
// 现场依据（2026-10-14 role 106 / conn game-000039 / op272）：模块侧那条整表刷新
// 只按角色 InventoryRecord.Slots 拼，而 op13 是替换语义，于是客户端金币被清 0、
// 材料页金晶被清 0、刚附魔的上衣由最上级变最下级，SQL/GM 侧却完全没变。
// 见 native-next914/enchant-gold-fix/v2/README.md。
//
// 边界：只有被点名的 list type 会重建；未点名（含所有其它 opcode）逐字节保持原样。
// 重建后的包体沿用原响应的信封与 position（sendGameUpperRawClassCodec + 原
// classification/AllowCodec），因此刷新仍然排在同一个 Result 的 class1 ACK 之前，
// 变的只是 body 内容。list 0 两种 builder 的 body 布局逐字段相同
// （listType, u16 槽位状态, u16 行数, 0x77 行）；list 1/2 的额外尾部不涉及本用例。

// currentAuthoritativeListRefreshSet 把 Result 点名的列表收成集合；空 = 不改行为。
func currentAuthoritativeListRefreshSet(listTypes []byte) map[byte]bool {
	if len(listTypes) == 0 {
		return nil
	}
	marked := make(map[byte]bool, len(listTypes))
	for _, listType := range listTypes {
		marked[listType] = true
	}
	return marked
}

// currentAuthoritativeListRefreshListType 回答"这条响应是不是要重建的整容器刷新"。
// 判据刻意收窄：class0 + msg 0x000D + body 首字节是被点名的 list type。
func currentAuthoritativeListRefreshListType(response alignedcmd.UpperResponse, marked map[byte]bool) (byte, bool) {
	if len(marked) == 0 || response.Classification != 0 {
		return 0, false
	}
	if response.MsgID != uint16(dnfenum.CmdPacketLeaveParty) || len(response.Body) == 0 {
		return 0, false
	}
	listType := response.Body[0]
	if !marked[listType] {
		return 0, false
	}
	return listType, true
}

// buildCurrentAuthoritativeListRefreshBody 走与登录/容器后置动作同一个 builder。
// 会话没有选中角色时返回 false：调用方据此跳过这条刷新，**绝不**用模块快照兜底。
func (s *Service) buildCurrentAuthoritativeListRefreshBody(session *gameSession, listType byte) ([]byte, string, int, bool) {
	if session == nil || session.selectedCharacterID == 0 {
		return nil, "", 0, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	body, source, count, ok := s.buildCurrentItemListBodyForSession(ctx, session, listType)
	if !ok {
		return nil, "", 0, false
	}
	// A replacing list-0 refresh must include the wallet from this same build.
	// A failed or missing character read must not clear the client's gold.
	if listType == dnfrepo.MainInventoryListType && !strings.Contains(source, currentGoldWalletProjectionSource) {
		s.logPacketEvent("game-upper-authoritative-list-refresh-wallet-authority-missing", "conn_id", session.connID, "char_id", session.selectedCharacterID, "list_type", listType, "body_source", source)
		return nil, "", 0, false
	}
	return body, source, count, true
}
