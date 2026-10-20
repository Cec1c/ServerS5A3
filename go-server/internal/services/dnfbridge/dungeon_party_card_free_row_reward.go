package dnfbridge

import (
	"os"
	"strconv"
	"strings"
)

// 本文件是运维实测报障「翻牌界面异常 能看到翻但是大家都是 X」里**纯下游的那一半**：
// 牌面内容根本没发出去。
//
// # 现场字节（`packet_log.txt`，2026-09-28 17:55 那一局，conngame-000009 = char1 /
// conngame-000012 = char6，`row_model=vendor` 已在线上默认生效）
//
//	char6 翻牌位 1（免费行 17:55:37.5 / 付费行 17:55:37.9，`card-select-ack-sent
//	side=0/1 card_index=1 flip_party_slot=1`），char1 自动翻牌位 0（17:55:40.9）。
//	三次发奖全是纯金币：`game-dungeon-card-reward-delivered ... item_slots=[]`
//	（gold 110990826→110991266 = **+440**、→110991718 = **+452**、54233→54655 = **+422**）。
//
// 同一批 op71 帧逐槽解出来只有**一个**槽带奖励块：
//
//	01470039...01 ffff0000 | 01 01 01 00000000 c4010000 00 | ffff0000 ×6      ← char6 的那一帧
//	                        ↑slot1: StateA=1 StateB=1 count=1 reward=(0,452)  其余槽 count=0
//	01470039...01 00ff0000 | 01 01 01 00000000 c4010000 00 | ffff0000 ×6      ← char1 的那一帧
//	                        ↑slot0: StateA=0 StateB=ff **count=0（没有任何奖励元组）**
//
// ⇒ 客户端"按线槽画牌、牌面 = 该槽的 reward tuple 列表"，而本仓
// `currentDungeonSelectedCardSlots`（dungeon_card_flow.go:192）**只给付费行挂奖励元组**：
//
//	dungeon_card_flow.go:218-223
//	  if side == dungeonCardSideFree {
//	      slot.StateA = flipPartySlot          ← 免费行只有"谁翻的"，**永远没有内容**
//	  } else {
//	      slot.StateB = flipPartySlot
//	      slot.Rewards = currentDungeonOp71RewardTuplesFromCardBundle(plan.Sides[side])
//	  }
//
// 于是"已经翻开、但服务端一个奖励元组都没给"的那些牌位在客户端就是**空牌面 = 蓝色 X**；
// 唯一有内容的那一张（付费行的金币）正常显示 —— 与运维截图"3 张 X + 1 张 +465 金币"
// 逐项吻合。**X 不是图标解析失败，也不是道具 id 无效，是那一槽的 `count=0`。**
//
// 诚实边界：这一局三次发奖 `item_slots=[]`，即上游掷骰**一件物品都没选出来**
// （见 `_dist/party-return-and-flip-round2.md` §11 引用的 `game-dungeon-hell-party-reward-roll`
// 逐字段：`rarity_drawn=r1:3,r2:4` 但 `rarity_allowed=4` 之后 `selected_count=0 granted=0`）。
// 那一层在 `dungeon_hell_party_reward*`（另一名代理在途），**本文件不动它**：
// 本文件修的是"**即使上游选出了物品，免费行那张牌也永远传不出去**"这条独立缺陷。
//
// # 修法（与厂商同一取舍：付费行优先，付费行还没翻时用免费行自己的内容）
//
// 每个牌位只有**一个**奖励块（厂商 `CardRewardInfoAck` 的
// `writer.WriteByte(2) + (0, PaidGold) + (PaidItemId, PaidItemCount)` 就是同一件事）。
// 所以规则定成：
//
//   - 该牌位的**付费行**已翻 ⇒ 用付费 bundle 的内容（厂商行为，逐字节不变）；
//   - 付费行还没翻、**免费行**已翻 ⇒ 用免费 bundle 的内容（**本轮的修复点**）；
//   - 两行都没翻 ⇒ 留空（`count=0`，与改动前相同）。
//
// 账本（队友投影）同一条规则：免费行只在"该牌位的付费行还不知道"时写入内容。
//
// # 开关
//
//	`DNFBRIDGE_PARTY_CARD_FREE_ROW_REWARD`
//	  - 默认（空/未设置/非法值/vendor/on）= **修复档**：免费行也带上它自己那份内容；
//	  - `off` / `0` / `false` / `no` / `legacy` = **逐字节回到改动前**（免费行永远 `count=0`，
//	    即运维截图里那三张 X）。
//
// 与同包既有开关一致的取值方式：每次调用读一次 env（不改 options.go）。
const dungeonPartyCardFreeRowRewardEnv = "DNFBRIDGE_PARTY_CARD_FREE_ROW_REWARD"

func dungeonPartyCardFreeRowRewardEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(dungeonPartyCardFreeRowRewardEnv))) {
	case "0", "off", "false", "no", "legacy":
		return false
	default:
		return true
	}
}

func dungeonPartyCardFreeRowRewardMode() string {
	if dungeonPartyCardFreeRowRewardEnabled() {
		return cloneAvatarAppearanceOn
	}
	return cloneAvatarAppearanceOff
}

// currentDungeonOp71RewardTuplesForLog 把奖励块渲染成判读串：
// `0=465` 表示金币 465（ValueA=0 = 金币，与厂商 `WriteUInt32(0)+WriteInt32(PaidGold)`
// 同一约定），`101550581x2` 表示道具 id × 数量。空块渲染成 `-`（= 空牌面）。
func currentDungeonOp71RewardTuplesForLog(rewards []currentDungeonOp71RewardTuple) string {
	if len(rewards) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(rewards))
	for _, reward := range rewards {
		if reward.ValueA == 0 {
			parts = append(parts, "0="+strconv.FormatUint(uint64(reward.ValueB), 10))
			continue
		}
		parts = append(parts, strconv.FormatUint(uint64(reward.ValueA), 10)+
			"x"+strconv.FormatUint(uint64(reward.ValueB), 10))
	}
	return strings.Join(parts, ",")
}

// currentDungeonCardSlotRewardTuples 按上面的规则给出**一个牌位**的奖励块。
// 返回空切片 = 这一槽没有内容（客户端会画成空牌面）。
func currentDungeonCardSlotRewardTuples(
	runtime *runtimeDungeonState,
	plan dungeonCardRewardPlan,
	wireIndex int,
) []currentDungeonOp71RewardTuple {
	if runtime == nil || wireIndex < 0 || wireIndex >= dungeonCardSlotsPerSide {
		return nil
	}
	if runtime.settlementCardSideSelectionKnown[dungeonCardSidePaid] &&
		int(runtime.settlementCardSideMember[dungeonCardSidePaid]) == wireIndex {
		return currentDungeonOp71RewardTuplesFromCardBundle(plan.Sides[dungeonCardSidePaid])
	}
	if !dungeonPartyCardFreeRowRewardEnabled() {
		return nil
	}
	if runtime.settlementCardSideSelectionKnown[dungeonCardSideFree] &&
		int(runtime.settlementCardSideMember[dungeonCardSideFree]) == wireIndex {
		return currentDungeonOp71RewardTuplesFromCardBundle(plan.Sides[dungeonCardSideFree])
	}
	return nil
}
