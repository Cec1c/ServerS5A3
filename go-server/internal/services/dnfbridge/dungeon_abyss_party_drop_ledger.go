package dnfbridge

import (
	"os"
	"sort"
	"strings"
)

// 本文件是 P0 第二轮（运维第 4 次发版后实测）的修复：**组队深渊的掉落归属**。
//
// ── 现场（`packet_log.txt`，4 次发版对比，全部是原始行/字节）────────────────────
//
//	① 第 3 次发版（peer 行所有者 = 击杀者，`…PARTY_DROP_OWNER` 尚未存在）：
//	   17:54:33 队长（game-000009/char1）打死最后一波，7 条碎片行以 896 字节帧发给队员
//	   （owner=1）；同一窗口里队员（game-000012/char6）的 RECV 只有 `msg_id=39 ×18`，
//	   **一条拾取请求都没有** ⇒ 客户端认为那不是自己的掉落，连 op43 都不发。
//	② 第 4 次发版（peer 行所有者改写成收件人，`owner_switch=party`）：
//	   18:49:15.903 `game-dungeon-hell-party-wave-peer-notify conn_id=game-000021 … object_key=440
//	   body_len=1150 drop_rows=9 drop_owner_restamped=true owner_actor_object_key=6`；
//	   紧接着 18:49:17.295–18:49:20.708 队员发了 **10 条 `RECV kind=game-legacy … type=43
//	   ENUM_CMDPACKET_GET_ITEM body_len=20`**，服务端 8 条回
//	   `game-dungeon-pickup-blocked … reason=runtime_drop_owner_rejected
//	   error=dnf dungeon drop object was not found: object_key=452/448/445/449/450/446/447/453`
//	   ⇒ **客户端确实按"行里的所有者"决定能不能捡**（改写成它，它就发请求）；
//	   而服务端**找不到那些对象**，因为每个人的 `runtime.DropOwner` 是**各自一份**的。
//	③ 队长那一侧同一批 key（445–453）在 18:49:40–41 全部拾取成功 ⇒ 对象只存在于杀者的账本里。
//
// ── 根因（两条，都在本文件里修）──────────────────────────────────────────────
//
//	(a) **队伍副本实例的"共享掉落账本"从来没有真正共享过**：
//	    `dungeon_party_instance.go:250` 在**建房那一刻**把 `dropOwner: runtime.DropOwner`
//	    抄了一份，而那一刻双方都还没有任何掉落（`DropOwner == nil`）⇒ 实例里存的是 nil；
//	    队员入伙时 `:338 runtime.DropOwner = instance.dropOwner` 拿到的也是 nil。
//	    之后谁先掉落谁就 `newRuntimeDungeonDropOwner()` 建一份**自己的**账本
//	    （`dungeon_hell_party_reward.go:590-592` / `dungeon_drop_runtime.go:523`），
//	    于是同一间房里两个人各看各的账本，跨人拾取必然 `errDungeonDropNotFound`。
//	    （本文件**不改** `dungeon_party_instance.go`：只在建房**之后**、第一次用到账本时
//	    把它补写进实例并让所有会话指向同一份。）
//	(b) **掉落只有一份，而且所有者是击杀者**：队员那条 op38 上的行是**同一批对象**
//	    （同一 scene slot / objectKey），只是所有者被改写 ⇒ 两个人看到"一模一样"的掉落、
//	    谁先捡谁得（第 4 次发版的实际行为）。
//
// ── 厂商口径（读 `ServerS4A21-master`，只读取证）与操作者口径的分歧（必须写明）──
//
//	厂商 = **共享一份，服务端不校验归属**：
//	  · `Game/Dungeon/DropInfo.cs:6-19` 的 `DropInfo` **没有任何 owner/participant 字段**；
//	  · `Game/Dungeon/DropService.cs:430,482`（`GenerateAbyssPartyAndRegister` 走 `:170`）
//	    一律 `run.Drops[drop.SceneSlot] = drop` ——**每个 run 一份**地面掉落表；
//	  · `Game/Dungeon/DropService.cs:226-254 TryPickup(run, srcSlot, lease)` 只查
//	    `run.Drops[srcSlot]` 然后发给**拾取者**的背包，**没有任何"所有者 == 拾取者"校验**；
//	  · 行里的所有者是**击杀者**（`Network/Builders/Dungeon/DungeonNotificationBuilder.cs:284`
//	    `w.WriteUInt16(ownerActorId); // +44 ownerCharacterId`，调用方
//	    `Network/Handlers/Dungeon/DungeonKillApplicationService.cs:1378-1381` 传
//	    `context.SourceUserId` = 击杀者）。
//	操作者口径 = **独立掉落**（「各掉各的，互相看得到但只能捡自己的」）。
//	**本实现按操作者口径**，理由是可验证的客户端行为：上面 ①②③ 证明**本客户端按行内所有者
//	决定能不能捡**，所以"共享一份 + 不校验归属"在这份客户端上根本无法实现（队员要么看不到
//	（所有者是别人），要么抢同一批对象（所有者改写成本人，谁先谁得、且对方只能收到 not found））。
//	独立掉落同时也给服务端一个干净的不变量：**每个对象恰好属于一个人**。
//
// ── 开关 ─────────────────────────────────────────────────────────────────────
//
// DNFBRIDGE_ABYSS_PARTY_DROP_OWNER
//
//	未设置/空/`independent`/任意其它值 = 修复档（默认）：
//	    ① 队伍副本实例里**一份共享账本**（首次用到时补写进实例）；
//	    ② 每次深渊奖励结算给**每名成员各注册一份**（各自 objectKey/sceneSlot、
//	       各自的所有者），同一 instance 内所有成员都收到**全部行**（别人的行也在，
//	       但所有者不是自己 ⇒ 客户端不让捡）；
//	    ③ 拾取仍按**严格所有者校验**（`owned()`）：只能捡自己的。
//	`shared` = **第 4 次发版的行为**：只有一份掉落、行内所有者改写成收件人、
//	    拾取校验放宽（谁先谁得）。逐字节回退用。
//	`legacy` = **第 3 次发版的行为**：只有一份掉落、行内所有者恒为击杀者、
//	    严格所有者校验（队员连请求都不发）。逐字节回退用。
const abyssPartyDropOwnerEnv = "DNFBRIDGE_ABYSS_PARTY_DROP_OWNER"

const (
	abyssPartyDropOwnerIndependentValue = "independent"
	abyssPartyDropOwnerSharedValue      = "shared"
	abyssPartyDropOwnerLegacyValue      = "legacy"
)

// abyssPartyDropOwnerMode 是掉落归属档位。
type abyssPartyDropOwnerMode int

const (
	// abyssPartyDropOwnerIndependent 是默认档，也是修复档（零值 = 修复）。
	abyssPartyDropOwnerIndependent abyssPartyDropOwnerMode = iota
	// abyssPartyDropOwnerShared 是第 4 次发版：一份掉落 + 收件人所有者 + 放宽校验。
	abyssPartyDropOwnerShared
	// abyssPartyDropOwnerLegacy 是第 3 次发版：一份掉落 + 击杀者所有者 + 严格校验。
	abyssPartyDropOwnerLegacy
)

func readAbyssPartyDropOwnerMode(value string) abyssPartyDropOwnerMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case abyssPartyDropOwnerSharedValue:
		return abyssPartyDropOwnerShared
	case abyssPartyDropOwnerLegacyValue:
		return abyssPartyDropOwnerLegacy
	default:
		return abyssPartyDropOwnerIndependent
	}
}

func (mode abyssPartyDropOwnerMode) String() string {
	switch mode {
	case abyssPartyDropOwnerShared:
		return abyssPartyDropOwnerSharedValue
	case abyssPartyDropOwnerLegacy:
		return abyssPartyDropOwnerLegacyValue
	default:
		return abyssPartyDropOwnerIndependentValue
	}
}

// perMember 报告该档位是否为每名成员各注册一份掉落（独立掉落）。
func (mode abyssPartyDropOwnerMode) perMember() bool {
	return mode == abyssPartyDropOwnerIndependent
}

// relaxed 报告该档位是否放宽拾取的所有者校验（第 4 次发版的行为）。
func (mode abyssPartyDropOwnerMode) relaxed() bool {
	return mode == abyssPartyDropOwnerShared
}

// receiverOwned 报告投影给队员时是否把行内所有者改写成收件人（第 4 次发版的行为）。
func (mode abyssPartyDropOwnerMode) receiverOwned() bool {
	return mode == abyssPartyDropOwnerShared
}

// sharesLedger 报告该档位是否使用队伍实例的共享掉落账本。
//
// 第 3 次发版的语义里"每个人的账本就是自己的"（跨人拾取必然失败）也是它行为的一部分，
// 所以 `legacy` 下**不**碰账本指针，逐字节等于那时。
func (mode abyssPartyDropOwnerMode) sharesLedger() bool {
	return mode != abyssPartyDropOwnerLegacy
}

func abyssPartyDropOwnerModeFromEnv() abyssPartyDropOwnerMode {
	return readAbyssPartyDropOwnerMode(os.Getenv(abyssPartyDropOwnerEnv))
}

// ── 操作者缺陷「队员拾取**偶发**失败（看得到掉落、拾取后地上还在、无法再次拾取）」
//
//	的根因修复 ────────────────────────────────────────────────────────────────────
//
// 现场（`packet_log.txt`，2026-09-29，party_id=1，`conn_id=game-002698` char1 +
// `conn_id=game-002700` char4）：**全日志只有 10 条** `reason=runtime_drop_owner_rejected`，
// 其中 9 条是 2026-09-28T18:49 那条已经修掉的旧根因，剩下**唯一一条**是本次：
//
//	240149: `EVENT kind=game-dungeon-pickup-blocked conn_id=game-002698 channel_id=10
//	        dungeon_id=310 room=1:0 map_id=36752 drop_object_key=422
//	        picker_actor_object_key=1 reason=runtime_drop_owner_rejected
//	        error=dnf dungeon drop object was not found: object_key=422
//	        t=2026-09-29T05:53:10.347854913Z`
//
// 时间线（同一局，逐行）：
//
//	05:52:50.247 `game-dungeon-party-instance-created conn_id=game-002698 … reason=first_party_member_published_shared_room_and_drop_ledger`
//	05:52:50.247 `game-dungeon-party-instance-joined conn_id=game-002700 char_id=4 member_count=2`
//	（此刻 `instance.dropOwner == nil`：建房那一刻谁都没有掉落）
//	239926 char4 第一次拾取 ⇒ `partySharedDungeonDropOwnerLocked` 把 **char4 自己那份账本**
//	       升格为共享账本（`instance.dropOwner != nil`）
//	240149 char1 第一次拾取 ⇒ 同一函数先执行 `runtime.DropOwner = instance.dropOwner`，
//	       **char1 自己那份账本（房间里已登记的掉落，含 422）指针当场被覆盖丢失**，
//	       随后的 `lookupOwners` 只剩共享账本一份 ⇒ `errDungeonDropNotFound: object_key=422`
//
// 这就是"**偶发**、且**下一张地图再拾取却正常**"的成因：丢指针只发生在
// "本会话已经建过自己的账本、而共享账本已被**队友**建出来"的那一个窗口里，一局至多一次；
// 覆盖之后 `runtime.DropOwner` 恒等于共享账本，本会话后续的掉落登记与查找走的是同一份，
// 于是下一张地图一切正常。
//
// 修复 = **合并而不是丢弃**：把本会话私有账本里共享账本还没有的条目并进共享账本
// （同键以共享账本为准；`nextSceneSlot` 取两者较大值），再把指针指向共享账本。
// 掉落的 `Status`/`DestinationSlot` 都是**指针共享**的，所以两条会话看到的是同一个对象，
// 不存在重复入账。
//
// 开关 `DNFBRIDGE_PARTY_DROP_LEDGER_ADOPT`：未设置/空/任意值 = 合并档（默认，修复）；
// 只有字面量 `legacy` 才回到"直接覆盖指针"（= 逐字节复现这条缺陷，便于 A/B）。
const partyDropLedgerAdoptEnv = "DNFBRIDGE_PARTY_DROP_LEDGER_ADOPT"

const partyDropLedgerAdoptLegacyValue = "legacy"

// partyDropLedgerAdoptMode 是"本会话私有账本并入队伍共享账本"的档位。
type partyDropLedgerAdoptMode int

const (
	// partyDropLedgerAdoptMerge 是默认档，也是修复档（零值 = 修复）。
	partyDropLedgerAdoptMerge partyDropLedgerAdoptMode = iota
	// partyDropLedgerAdoptLegacy 是改动前的"覆盖指针"档。
	partyDropLedgerAdoptLegacy
)

func readPartyDropLedgerAdoptMode(value string) partyDropLedgerAdoptMode {
	if strings.EqualFold(strings.TrimSpace(value), partyDropLedgerAdoptLegacyValue) {
		return partyDropLedgerAdoptLegacy
	}
	return partyDropLedgerAdoptMerge
}

func (mode partyDropLedgerAdoptMode) String() string {
	if mode == partyDropLedgerAdoptLegacy {
		return partyDropLedgerAdoptLegacyValue
	}
	return "merge"
}

// merges 报告该档位是否把本会话私有账本并进共享账本（而不是丢弃指针）。
func (mode partyDropLedgerAdoptMode) merges() bool {
	return mode == partyDropLedgerAdoptMerge
}

func partyDropLedgerAdoptModeFromEnv() partyDropLedgerAdoptMode {
	return readPartyDropLedgerAdoptMode(os.Getenv(partyDropLedgerAdoptEnv))
}

// mergeRuntimeDungeonDropOwner 把 src 里 dst 还没有的条目并进 dst（同键以 dst 为准）。
// 两边都只做 map 写入，调用方必须已经持有队伍实例锁（与 `registerBatch` 同一把）。
func mergeRuntimeDungeonDropOwner(dst *runtimeDungeonDropOwner, src *runtimeDungeonDropOwner) int {
	if dst == nil || src == nil || dst == src {
		return 0
	}
	if dst.byObjectKey == nil {
		dst.byObjectKey = make(map[uint32]*runtimeDungeonDrop)
	}
	if dst.bySceneSlot == nil {
		dst.bySceneSlot = make(map[uint16]*runtimeDungeonDrop)
	}
	merged := 0
	for key, drop := range src.byObjectKey {
		if drop == nil {
			continue
		}
		if _, exists := dst.byObjectKey[key]; exists {
			continue
		}
		dst.byObjectKey[key] = drop
		merged++
	}
	for slot, drop := range src.bySceneSlot {
		if drop == nil {
			continue
		}
		if _, exists := dst.bySceneSlot[slot]; exists {
			continue
		}
		dst.bySceneSlot[slot] = drop
	}
	if src.nextSceneSlot > dst.nextSceneSlot {
		dst.nextSceneSlot = src.nextSceneSlot
	}
	return merged
}

// ── 共享账本（在 `dungeon_party_instance.go` 之外补齐它的接线）──────────────────

// partySharedDungeonDropOwnerLocked 返回队伍副本实例的共享掉落账本；实例里还是 nil 时
// **就在这里补一份并写回实例**，让后到的会话也指向同一份。
//
// 调用方必须**已经持有该实例的锁**（`runtime.lockPartyDungeonInstance()` /
// `handleCurrentDungeonPickup` 的 `unlockPartyDungeonInstance` 配对的同一把锁）——
// 本函数不取锁，避免自锁。
//
// 单人（无实例）返回 nil，调用方保持今天的行为。
func partySharedDungeonDropOwnerLocked(runtime *runtimeDungeonState) *runtimeDungeonDropOwner {
	if runtime == nil || runtime.partyInstance == nil {
		return nil
	}
	instance := runtime.partyInstance
	if instance.dropOwner == nil {
		if runtime.DropOwner != nil {
			instance.dropOwner = runtime.DropOwner
		} else {
			instance.dropOwner = newRuntimeDungeonDropOwner()
		}
	} else if runtime.DropOwner != nil && runtime.DropOwner != instance.dropOwner &&
		partyDropLedgerAdoptModeFromEnv().merges() {
		// 修复档：并进来，**绝不丢指针**（丢了就等于把本会话已经公告给客户端的
		// 掉落对象一起丢掉 ⇒ 队员那条 `runtime_drop_owner_rejected`）。
		mergeRuntimeDungeonDropOwner(instance.dropOwner, runtime.DropOwner)
	}
	runtime.DropOwner = instance.dropOwner
	return instance.dropOwner
}

// dropWiresOwnedBy 取"属于某一名成员的那几行"（行内所有者 == 该成员的 actor key），
// 顺序保持原样。独立掉落档下每个人的 op38 只发自己那一份。
func dropWiresOwnedBy(
	wires []currentDungeonDeathDropWire,
	ownerActorObjectKey uint16,
) []currentDungeonDeathDropWire {
	if len(wires) == 0 {
		return nil
	}
	owned := make([]currentDungeonDeathDropWire, 0, len(wires))
	for _, wire := range wires {
		if wire.OwnerActorObjectKey != ownerActorObjectKey {
			continue
		}
		owned = append(owned, wire)
	}
	return owned
}

// partyDungeonMemberCharacterIDsLocked 返回同一队伍副本实例内的**全部**成员角色 id
// （含自己），升序、去重。调用方必须已持有实例锁。
//
// 只有 `instance.members` 里有会话的成员才可能收到 op38，所以这里要求 `sessions` 里有值。
func partyDungeonMemberCharacterIDsLocked(runtime *runtimeDungeonState, selfCharacterID uint16) []uint16 {
	if runtime == nil || runtime.partyInstance == nil {
		return []uint16{selfCharacterID}
	}
	instance := runtime.partyInstance
	ids := make([]uint16, 0, len(instance.members)+1)
	seen := make(map[uint16]struct{}, len(instance.members)+1)
	add := func(characterID uint16) {
		if characterID == 0 {
			return
		}
		if _, duplicate := seen[characterID]; duplicate {
			return
		}
		seen[characterID] = struct{}{}
		ids = append(ids, characterID)
	}
	add(selfCharacterID)
	for characterID := range instance.members {
		if instance.sessions[characterID] == nil {
			continue
		}
		add(characterID)
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
	return ids
}
