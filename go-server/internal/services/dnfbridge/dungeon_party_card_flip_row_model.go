package dnfbridge

import (
	"os"
	"strings"
)

// 本文件是操作者第 7 条报障的**真因**：
//
//	「组队通关副本还是看不到对方的翻牌。」
//
// 上一轮的投影（`dungeon_party_card_flip_projection.go`）**确实 fire 了**：
//
//	:153526 SEND kind=game-upper conn_id=game-000003 pkt_seq=1682 msg_id=71
//	        classification=1 plain_body_len=33
//	        hex=014700310000009d8fe8c91a0300000001ffff000000ff0000ffff0000ffff...
//	:153528 EVENT kind=game-dungeon-card-flip-party-broadcast conn_id=game-000007
//	        char_id=6 dungeon_id=310 maze_index=0 instance_id=3 side=0 member_index=1
//	        state_a=0 state_b=255 reward_tuple_count=0 party_member_count=2 peer_count=1
//	        delivered=1 source=current_exe_op71_select_request switch=vendor
//
// 队友的会话**收到了**这一帧（`delivered=1`），客户端也**解析**了它 —— 但归错了人。
//
// # 现场客户端实证（`地下城与勇士\90CN_trace.log`，pid=3308 = 队长 char1 的进程）
//
//	[00:14:17.959] settlement-card-row row=first wire_slot=1 state_byte=0 identity=0
//	               ... first_local=1 second_local=-1 taken=[-1 0 -1 ...] first_peer=[-1 ...]
//	[00:14:18.245] settlement-card-row row=second wire_slot=1 state_byte=0 ...
//	[00:14:21.414] settlement-card-row row=first wire_slot=0 state_byte=0 ...
//	               first_local=0 second_local=1 taken=[0 0 -1 ...] first_peer=[-1 ...]
//
// `00:14:17.085`（= 队员 char6 翻免费行）之后 0.9 秒，**队长**的客户端把
// `wire_slot=1` 读成"已被翻开的 1 号牌"，并把 `first_local` 记成 1
// —— 也就是：**客户端认为那张牌是队长自己翻的**，`first_peer` 永远是 -1（队友什么都没翻）。
//
// # 客户端/厂商的线语义（决定性）
//
// 厂商 `CardRewardCoordinator.cs:1073-1142 ProjectParticipant`（本仓唯一已证真源）：
//
//	freeSelectors[selectedFree] = partySlot;     // 下标 = **牌位**，值 = **翻牌人的队伍槽位**
//	paidSelectors[selectedPaid] = partySlot;     // 付费行同一套下标/取值
//	eligibility[partySlot]      = 1;             // op70 的 8×u16 才是"按队伍槽位"
//
// 而本仓 `dungeon_card_flow.go:186 currentDungeonSelectedCardSlots` 写的是
//
//	slots[wireIndex].StateA = 0 / StateB = 0      // 下标 = 牌位（对的），**值恒为 0**（错的）
//
// ⇒ 每一行都被声明成"**0 号槽位**（= 收包者自己）翻的"。于是：
//
//   - 自己翻的牌：自己就是槽位 0 时是对的（现场队长自动翻 0 号牌、队员恰好是 1 号槽位
//     且翻 1 号牌，两边下标与取值巧合相等），所以**本机看着正常**；
//   - 队友翻的牌：值 0 ≠ 队友的槽位 ⇒ 客户端把它算到**收包者自己**头上
//     （trace 的 `first_local`/`taken` 就是这一读法），`first_peer` 永远空
//     ⇒ **永远看不到对方的翻牌** —— 与操作者描述逐字一致。
//
// # 修法
//
// 把值写成**翻牌人的队伍槽位**（与厂商 `partySlot` 同源：本仓既有
// `currentTournamentLocalPartySlot` 用的就是队伍名册下标，现场 char1=0 / char6=1
// 与 `game-party-dungeon-loading-ready ... slot=` 逐字对上）。下标不动（本来就是牌位
// = `request.ValueB`，见 `dungeon_card_flow.go:169-183`）。
//
// 单人在城镇/无队伍时槽位取 0 ⇒ **与改动前逐字节相同**（回退档同样）。
//
// # 开关
//
//	`DNFBRIDGE_PARTY_CARD_FLIP_ROW_MODEL`
//	  - `vendor`（默认：空/未设置/非法值）= 修复档：`slots[牌位] = 翻牌人队伍槽位`；
//	  - `legacy` = **逐字节回到改动前**：`slots[牌位] = 0`（恒 0）。
const dungeonPartyCardFlipRowModelEnv = "DNFBRIDGE_PARTY_CARD_FLIP_ROW_MODEL"

const dungeonPartyCardFlipRowModelLegacy = "legacy"

type dungeonPartyCardFlipRowModel int

const (
	// dungeonPartyCardFlipRowModelVendor 是默认档（零值）= 修复档。
	dungeonPartyCardFlipRowModelVendor dungeonPartyCardFlipRowModel = iota
	// dungeonPartyCardFlipRowModelLegacy 是改动前的"值恒 0"档。
	dungeonPartyCardFlipRowModelLegacyValue
)

func readDungeonPartyCardFlipRowModel(value string) dungeonPartyCardFlipRowModel {
	if strings.EqualFold(strings.TrimSpace(value), dungeonPartyCardFlipRowModelLegacy) {
		return dungeonPartyCardFlipRowModelLegacyValue
	}
	return dungeonPartyCardFlipRowModelVendor
}

func (model dungeonPartyCardFlipRowModel) String() string {
	if model == dungeonPartyCardFlipRowModelLegacyValue {
		return dungeonPartyCardFlipRowModelLegacy
	}
	return "vendor"
}

// carriesFlipperPartySlot 报告该档是否把"翻牌人队伍槽位"写进行状态字节。
func (model dungeonPartyCardFlipRowModel) carriesFlipperPartySlot() bool {
	return model == dungeonPartyCardFlipRowModelVendor
}

func dungeonPartyCardFlipRowModelFromEnv() dungeonPartyCardFlipRowModel {
	return readDungeonPartyCardFlipRowModel(os.Getenv(dungeonPartyCardFlipRowModelEnv))
}

// currentDungeonCardFlipRowStateValue 是本会话要写进 op71 行状态字节的值：
// 修复档 = 本会话在**队伍名册里的下标**（= 客户端认的那种队伍槽位）；
// 回退档 = 0（= 改动前那个恒 0）。取不到名册时回退 0（单人语义，与改动前一致）。
func currentDungeonCardFlipRowStateValue(session *gameSession) byte {
	if !dungeonPartyCardFlipRowModelFromEnv().carriesFlipperPartySlot() {
		return 0
	}
	if session == nil {
		return 0
	}
	slot := currentTournamentLocalPartySlot(session)
	if slot < 0 || slot >= dungeonCardSlotsPerSide {
		return 0
	}
	return byte(slot)
}
