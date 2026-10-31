package dnfbridge

import (
	"fmt"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// 段106 前半「红尾乔纳森 NPC 强化装备不消耗精炼的时空石」的独立转换 helper。
//
// 已核真档事实（固定 v106 PVF SHA256 ad7d05c70421278f7385729bcba3e81e37d99d0f3df85a87a9f0dc19c92504cb）：
// 476 红尾乔纳森（npc/476_Jonathan.npc）声明 `[upgrade gold convert to material]
// 10158964 100000`：10158964 = stackable/10158001/10158964.stk「精炼的时空石」
// (`[material]`)，100000 = 单位金币。即这只 NPC 的强化费用按金币换算成精炼的时空石。
//
// 生产默认接线由 candidateUpgradeCostResolver 的空值与 candidate 档建立完整原生金币报价，
// 并只把 476 的 reinforce 请求分派给本 helper。修复前的默认档返回 nil，owner 只按存量
// Extra 金币字段 (upgradeGoldCostOf) 收费，精炼的时空石一件不扣（旧行为被本目录历史用例
// legacy_default_control 与 TestR4RealPVFJonathanHandlerPaymentReplay 的历史期望固定）。
//
// 本 helper 的独立范围（普通强化完整报价与便携强化结算不在这里）：
//   - 只认已核的 476（currentVerifiedUpgradeConversionNPCID）：其它任何编号（普通 NPC、
//     不在 npc.lst 的编号、0xFFFF 哨兵）立即返回未解析，连资源查询都不做；生产默认包装器
//     随后交给完整原生报价，普通强化不会因此回落到旧 Extra 费用，未知 NPC 由该报价失败关闭；
//   - 非 reinforce 模式同样立即返回未解析；生产默认包装器交给完整原生报价，增幅取得独立
//     金币报价，其它未支持模式仍保持未解析；
//   - 476 的规则读不出来 / 畸形 ⇒ 报错失败关闭（handler 变成 upgradeErrorInvalidMaterial
//     拒绝，先于 RNG 与任何写入）。固定 PVF 下 476 确有该段，静默回落金币等于让玩家规避材料；
//   - 场景授权与 candidate 档同口径：报价前必须先通过 validateUpgradeNPCScene，否则任意
//     NPC 上下文都能把费用换成材料；
//   - 数量沿用既有候选口径 ceil(原生金币 / 单位金币)，不在这里另立规则，也不读客户端报价。
//     客户端点击侧可用性预检在整数倍时多要一个材料，这条差异仍未原生验收。
func (s *Service) alignedUpgradeNPCConversionCostResolver(identity boundGameSessionCharacter) alignedcmd.UpgradeCostResolver {
	unresolved := alignedcmd.UpgradeCostResolution{}
	return func(mode string, itemID int64, level int, npcPresent bool, npcID uint16) (alignedcmd.UpgradeCostResolution, error) {
		if mode != "reinforce" || !npcPresent || npcID != currentVerifiedUpgradeConversionNPCID {
			return unresolved, nil
		}
		materialID, unitGold, converts, err := s.currentUpgradeNPCConversion(currentVerifiedUpgradeConversionNPCID)
		if err != nil {
			return unresolved, err
		}
		if !converts || materialID <= 0 || unitGold <= 0 {
			return unresolved, fmt.Errorf("verified upgrade conversion NPC %d has no usable rule", currentVerifiedUpgradeConversionNPCID)
		}
		if err := s.validateUpgradeNPCScene(identity, currentVerifiedUpgradeConversionNPCID); err != nil {
			return unresolved, err
		}
		// 476 报价复用整链原生计算：表、稀有度、类型、当前等级权重与上限除法只有一份实现；
		// 生产默认包装器与 candidate 对同一 476 请求因此给出同一份报价。
		return s.alignedNativeUpgradeCostResolver()(mode, itemID, level, npcPresent, npcID)
	}
}

// currentVerifiedUpgradeConversionNPCID 是本轮唯一已核声明
// `[upgrade gold convert to material]` 的 NPC：476 红尾乔纳森（npc/476_Jonathan.npc，
// 段体 10158964 100000）。这是固定 PVF 里既有的资源事实，不是新造 NPC；独立转换 helper
// 只钉这一个编号，其余编号的普通金币报价由生产默认完整报价负责。
const currentVerifiedUpgradeConversionNPCID uint16 = 476
