package dnfbridge

import (
	"math"
	"strings"

	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件修的是"操作者自己用自己的副职业分解机 → 客户端弹『公告 / 所持金币不足 / 确认』"。
//
// 一、现场（香港 192.0.2.12，/opt/dnf90/logs/packet_log.txt 2026-09-25，当前这份
// 日志由 19:26 的部署轮转而来；操作者就是在这个会话里点的「全部分解(A)」）：
//
//	:3505 RECV kind=game-legacy conn_id=game-000040 channel_id=253 cmd=1 type=598
//	      runtime_cmd_name=ENUM_CMDPACKET_CREATE_EXPERT_JOB_STORE header_seq=90
//	      body_len=16 len=29 hex=0156021d000000a56824fc5a0000010000000101000000ed065c01ffff
//	:3508 SEND msg_id=598 plain_body_len=1 hex=0156021100000027d13fe3600000000001  （成功）
//	:3509 RECV type=599 ENUM_CMDPACKET_ENTER_EXPERT_JOB_STORE header_seq=92
//	      body_len=2 len=15 hex=0157020f000000ab4811dd5c000300
//	:3512 SEND msg_id=599 plain_body_len=11
//	      hex=0157021b000000662de1ee6100000000010001010000002c010000                （成功）
//	:3513 RECV type=235 ENUM_CMDPACKET_REQUEST_DISJOINT_ITEM header_seq=95
//	      body_len=5 len=18 hex=01eb0012000000bfc6bbc85f0003000d0000
//	:3516 SEND msg_id=235 plain_body_len=2
//	      hex=01eb0012000000372b1afe63000000000015                                      ← 失败
//	:3522 RECV type=600 ENUM_CMDPACKET_CLOSE_EXPERT_JOB_STORE                            （关窗）
//
// 逐字段读：
//   - op598 body（去 13 字节头）= `00 01 00 00 00 01 01 00 00 00 ed 06 5c 01 ff ff`
//     ⇒ Kind=0（currentExpertJobDisjointStoreKind=分解机）、nameLen=1、Name=`01`、
//     **Cost=1**、PositionX=1773、PositionY=348、OpaqueObjectLink=0xFFFF。
//     Cost=1 与操作者截图上的「手续费：1金币」逐值吻合。
//   - op599 body = `03 00` ⇒ 进店时登记的门店主人 = **character_id 3**。
//   - op235 body = `03 00 0d 00 00` ⇒ OwnerID=3、TargetSlot=13、TargetList=0（主背包）。
//     同一会话（conn_id=game-000040）的角色也是 3 ⇒
//     `mutateCurrentExpertJobStorePair` 走 **自身门店** 分支，手续费**不该收**。
//   - op235 回包 body = `00 15` = 失败标志 0 + 失败码 **0x15=21**。
//     `currentExpertJobStoreError`（expert_job_store.go）里只有一条分支产出 21：
//     `errors.Is(err, dnfexpertjob.ErrInsufficientGold) → 21`。
//     客户端没有拿到任何文本，弹窗里的「所持金币不足」是**客户端按失败码本地取的文案**。
//
// 二、根因：金币读错了字段（读了一个根本不存在的背包伪槽 0:0）
//
//	本仓库金币的权威存储是 **`dnf_characters.gold` 列**，在 Go 侧就是
//	`CharacterRecord.Stats["gold"]`（映射见 repository/mysql/mysql_char.go:57
//	`{column: "gold", key: "gold", ddl: "BIGINT NOT NULL DEFAULT 0"}`）。
//	下发给客户端的钱包也是从它投影出来的
//	（current_item_list_session.go:154 `if gold, hasGold := character.Stats["gold"]`，
//	文件里自述 "Go keeps gold in Character.Stats, unlike 86JP's single persisted wallet row"）。
//	全服其它金币链路（NPC 商店 / 邮件 / 拍卖行 / 副本结算 / 修理 / 背包整理）一律
//	读写 `character.Stats["gold"]`。
//
//	但改动前这一条链路（`currentExpertJobWalletGold` / `currentExpertJobSetWalletGold`）
//	读写的却是 **背包记录里的伪槽 `list 0:slot 0`**：
//
//	    wallet, ok := inventory.Slots[currentCeraShopInventorySlotKey(MainInventoryListType, 0)]
//	    if !ok || wallet.ItemID != 0 || wallet.Count < 0 {
//	        return 0, dnfexpertjob.ErrInsufficientGold   // ← 槽不存在 = "金币不足"
//	    }
//
//	香港库实测（2026-09-25，`/root/dbq.sh`）：
//
//	    SELECT character_id, account_id, name, level, gold, expert_job_type, expert_job_exp
//	      FROM dnf_characters ORDER BY character_id;
//	    3  dnf:10001  test2  90  13364032  3  0        ← 操作者角色：1336 万金币、job=3(分解师)
//	    4  dnf:10006  t3     11   3770000  0  0
//
//	    SELECT character_id, collection_name, entry_key, item_id, item_count
//	      FROM dnf_inventory_items WHERE entry_key='0:0';   → **Empty set**
//
//	即：角色 3 身上有 13,364,032 金币，但背包里**从来没有过 0:0 这一行**（`dnf_inventory_items`
//	是 (character_id, collection_name, entry_key) 三主键的新式表，`0:0` 全库 0 行）。
//	于是 `!ok` ⇒ `ErrInsufficientGold` ⇒ 失败码 21 ⇒ 客户端「所持金币不足」。
//	**金币够不够根本没被读，读的是一个不存在的槽。**
//
//	同一分支还有个次生问题：改动前 requester/owner 两侧金币**无条件**先读一遍并在报错时
//	直接 return，所以自身门店（self）即使一分钱都不收，也照样被这个假"余额不足"打死 ——
//	这正是操作者看到的"自己用自己的分解机也说过金币不足"。
//
// 三、与同族 bug 的关系（供父代理的全局排查线参考，本文件不越界）
//
//	本缺陷**不是** epic_production_account_scope.go 那条"s.accountID() 共享兜底账号"的坑：
//	op235 走的是 `mutateCurrentExpertJobStorePair` 的自身门店分支，那里已经用的是
//	`s.accountIDForSession(requesterSession)`（expert_job_store.go:1082），账号口径是对的。
//	本缺陷是**同一字段口径**（金币该读 Stats 还是读背包伪槽）的问题。
//
// 四、开关
//
//	DNFBRIDGE_EXPERT_JOB_GOLD_SOURCE=character_stats（默认）/ inventory_wallet
//	  character_stats（默认）= 修正后行为：读写 `Character.Stats["gold"]`
//	    （= `dnf_characters.gold`），与客户端钱包投影、与全服其它金币链路同源。
//	  inventory_wallet = **一键回退**到改动前行为：读写背包伪槽 `list 0 slot 0`，
//	    槽不存在即判 `ErrInsufficientGold`。在香港这种"金币只在 gold 列"的部署上，
//	    这一档会让每一次分解机/修理/升级/放弃副职业请求都回失败码（分解机 = 21）。
const expertJobGoldSourceEnv = "DNFBRIDGE_EXPERT_JOB_GOLD_SOURCE"

const (
	expertJobGoldSourceValueCharacterStats  = "character_stats"
	expertJobGoldSourceValueInventoryWallet = "inventory_wallet"
)

// expertJobGoldSourceMode 是 expertJobGoldSourceEnv 的归一化取值。
type expertJobGoldSourceMode int

const (
	// expertJobGoldCharacterStats 是默认档（零值）= 修正后行为：
	// 以 `Character.Stats["gold"]`（= `dnf_characters.gold`）为唯一金币来源。
	expertJobGoldCharacterStats expertJobGoldSourceMode = iota
	// expertJobGoldInventoryWallet 是回退档 = 改动前行为：
	// 以背包伪槽 `list 0 slot 0` 为金币来源，缺失即 `ErrInsufficientGold`。
	expertJobGoldInventoryWallet
)

func (mode expertJobGoldSourceMode) String() string {
	if mode == expertJobGoldInventoryWallet {
		return expertJobGoldSourceValueInventoryWallet
	}
	return expertJobGoldSourceValueCharacterStats
}

// readExpertJobGoldSourceMode 解析 DNFBRIDGE_EXPERT_JOB_GOLD_SOURCE。
// **默认 character_stats（= 修正后行为）**：空/未设置/非法值都是 character_stats；
// 只有显式写 `inventory_wallet` 才回到改动前的背包伪槽口径（一键回退）。
func readExpertJobGoldSourceMode(value string) expertJobGoldSourceMode {
	if strings.EqualFold(strings.TrimSpace(value), expertJobGoldSourceValueInventoryWallet) {
		return expertJobGoldInventoryWallet
	}
	return expertJobGoldCharacterStats
}

// expertJobGoldSource 返回当前 Service 的金币口径档位
// （零值 options 的测试/探针路径 = character_stats = 修正后行为）。
func (s *Service) expertJobGoldSource() expertJobGoldSourceMode {
	if s == nil {
		return expertJobGoldCharacterStats
	}
	return s.options.expertJobGoldSource
}

// currentExpertJobWalletGold 读取"要付手续费的那一方"的金币。
//
// 传入 character 与 inventory 两份记录，是因为两个档位用的源不同：
//
//	character_stats 档（默认）：`character.Stats["gold"]`。
//	  **槽缺失 / stats 缺失一律当 0 金币返回，不报错** —— 余额够不够由调用方按
//	  实际手续费判定（`requesterGold < store.Cost`），自身门店（self）不收费时
//	  0 金币也必须放行。改动前那种"读不到钱包 = 金币不足"的短路正是本次缺陷。
//	inventory_wallet 档（回退）：背包伪槽 `list 0 slot 0`，与改动前逐字节相同，
//	  包括"槽不存在 / itemID 非 0 / count 为负 ⇒ ErrInsufficientGold"。
func (s *Service) currentExpertJobWalletGold(character *dnfrepo.CharacterRecord, inventory *dnfrepo.InventoryRecord) (int64, error) {
	if s.expertJobGoldSource() == expertJobGoldInventoryWallet {
		// 回退档：改动前行为原样保留。
		if inventory == nil {
			return 0, dnfexpertjob.ErrInventoryNotFound
		}
		wallet, ok := inventory.Slots[currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, 0)]
		if !ok || wallet.ItemID != 0 || wallet.Count < 0 {
			return 0, dnfexpertjob.ErrInsufficientGold
		}
		return wallet.Count, nil
	}
	if character == nil {
		return 0, dnfexpertjob.ErrCharacterNotFound
	}
	// 读 nil map 是安全的（返回零值），"没有 gold 键"就是 0 金币。
	return character.Stats["gold"], nil
}

// currentExpertJobSetWalletGold 写回该方的金币余额。
// 档位语义与 currentExpertJobWalletGold 一一对应（character_stats 写 Stats，
// inventory_wallet 写背包伪槽）。nil 记录一律静默跳过，不 panic。
func (s *Service) currentExpertJobSetWalletGold(character *dnfrepo.CharacterRecord, inventory *dnfrepo.InventoryRecord, gold int64) {
	if s.expertJobGoldSource() == expertJobGoldInventoryWallet {
		if inventory == nil || inventory.Slots == nil {
			return
		}
		key := currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, 0)
		wallet := inventory.Slots[key]
		wallet.ItemID, wallet.Count = 0, gold
		inventory.Slots[key] = wallet
		return
	}
	if character == nil {
		return
	}
	if character.Stats == nil {
		character.Stats = make(map[string]int64, 8)
	}
	character.Stats["gold"] = gold
}

// currentExpertJobSettleStoreFee 结算"使用副职业门店（分解机/附魔机）"的手续费，
// 返回结算后的 requester / owner 金币。
//
// 两条语义与现场的对应关系：
//
//  1. **自身门店不收费**：`requester.CharacterID == owner.CharacterID` 时直接返回，
//     一分钱都不动。这是 PVF `[disjoint self service]` 的语义，也正是操作者那句
//     "自己使用自己的分解机" 期望的行为。改动前 requester/owner 两侧金币是
//     **无条件先读一遍**、读不到就 `return ErrInsufficientGold`，所以自身门店
//     即使不收费也会先被那个假的"余额不足"短路打死 —— 本次缺陷的第二半。
//  2. **他人门店按 cost 收费**：余额不足回 `ErrInsufficientGold`（→ 失败码 21），
//     owner 侧防止 `> MaxInt32 - cost` 溢出。这一支**没有放松**：修复只改了金币
//     的读取来源，没有让别人的机器变成免费。
//
// disjoint（expert_job_store.go 的 commitCurrentExpertJobStoreDisjoint）与
// enchant（commitCurrentExpertJobStoreEnchant）共用本函数；两者的判定在改动前
// 只在两个 `if` 的先后顺序上不同，结果完全一致，合并后语义不变。
func (s *Service) currentExpertJobSettleStoreFee(
	requesterCharacter *dnfrepo.CharacterRecord, requesterInventory *dnfrepo.InventoryRecord,
	ownerCharacter *dnfrepo.CharacterRecord, ownerInventory *dnfrepo.InventoryRecord,
	cost int64,
) (requesterGold, ownerGold int64, err error) {
	requesterGold, err = s.currentExpertJobWalletGold(requesterCharacter, requesterInventory)
	if err != nil {
		return 0, 0, err
	}
	ownerGold, err = s.currentExpertJobWalletGold(ownerCharacter, ownerInventory)
	if err != nil {
		return 0, 0, err
	}
	self := requesterCharacter != nil && ownerCharacter != nil &&
		requesterCharacter.CharacterID == ownerCharacter.CharacterID
	if self {
		return requesterGold, ownerGold, nil
	}
	if requesterGold < cost || ownerGold > math.MaxInt32-cost {
		return 0, 0, dnfexpertjob.ErrInsufficientGold
	}
	requesterGold -= cost
	ownerGold += cost
	s.currentExpertJobSetWalletGold(requesterCharacter, requesterInventory, requesterGold)
	s.currentExpertJobSetWalletGold(ownerCharacter, ownerInventory, ownerGold)
	return requesterGold, ownerGold, nil
}
