package dnfbridge

import (
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// 穿脱装备/时装（op19 移动物品栏位置）的"回包后刷新"口径。
//
// 现场（香港 `packet_log.txt:2232-2277`，2026-09-25 19:41，角色 3，conn_id=game-000016）
// 每一次 op19 都在 ACK 之后发**两串重的东西**：
//
//	op19 ACK（11B body / 27B 包）
//	class0/op13 list_type=0 entry_count=45 body_len=5360   ← 整容器
//	class0/op13 list_type=1 entry_count=32 body_len=4069   ← 整容器
//	class0/op13 list_type=2 body_len=6 / list_type=7 body_len=598 / list_type=38 body_len=3
//	class0/op2  mode0 body_len=468                          ← 本地 actor 整题重建
//	class0/op413 战力 70B
//
// 即**每次穿/脱一件 ≈ 10.7 KB、5 个整容器 + 一次本地角色重建**（`currentSelectedItemListTypes`
// = {0,1,2,7,38}，见 current_item_list_types.go:17）。
//
// 参考口径（同一血统的厂商 9.16 二进制，`D:\DNF90-test\server\runtime\logs\packet_log.txt`
// 的 6 次 op19，逐条列出前后 SEND）：
//
//	op19#1..#5 之间只有 `msg19` + `msg413` 两条 SEND；事件自述
//	`item_slot_refresh_count=0` 与
//	`...; opened current-EXE op19 ACK then event-suppressed complete mode0 paper-doll
//	appearance projection without item-container replay`。
//
// 86JP C# `InventoryHandler.Move.cs:15-97` 同样**一条物品栏刷新都不发**（ACK →
// 仅被排序锁定的槽位信号 → 只有外观槽位才补 NOTI 2），并且明写
// "客户端收到移动应答后不会自行刷新外观，这些槽位变化必须跟发 subtype0"
// （`IsAppearanceEquipmentSlot`，含全部时装槽）。
//
// 家族 Go 服务端（`90原神开源0.1.8\...\dnfbridge\game_aligned.go:575-589`）在同一个
// post action 上**只发 mode0**（`sendSelectedActorMode0AppearanceRefresh`），
// 既没有 mode1 档也没有连点合并 —— 本仓的 mode1 默认档与 200ms 合并是后来分叉出来的。
//
// 另外：整容器重发里**从来没有 list 3（已穿戴）**——`currentSelectedItemListTypes` 不含 3，
// 而 op19 的落点正是 list 3。于是客户端先被 list 0/1 的整容器告知"这件已经不在背包/装扮栏"，
// 又拿不到任何 list 3 的权威行 ⇒ 操作者看到的"**穿上了装备、身上没有、背包也不存在，
// 重新选角后正常**"。库里最终状态是对的 ⇒ 是**下发投影问题，不是写库时序问题**；
// 域侧 `equip.Owner.Move` 本来就在同一个 `items.WithinCharacterItems` 事务里完成
// "删背包行 + 建装备行"，ACK 只在事务提交后才产生。
//
// 2026-09-28（本轮，操作者报"穿时装模型不实时刷新 + 装扮栏很卡"）：
// 上一批把外观刷新默认改成 mode1 后，**模型不再刷新**——客户端 trace（`90CN_trace.log`，
// 2026-09-28 00:48，pid=47756，local_owner=0/11）逐条对照：
//
//	mode1（我们发的）：`town-copresence-dispatch phase=before/after opcode=2 mode=1 owner=11 key=1`
//	    → `combat-power equipment snapshot … mode=1 rows=24` —— 只更新**装备数据快照**，
//	    既没有 `local-actor-create` 也没有 `visual_synced`；
//	mode0（登录时发的）：`mode0-owner-compare scene_channel=11 local_owner_channel=11
//	    branch=current-scene-create` → `local-actor-create phase=before/after object_key=1`
//	    → `game-options-apply trigger=local-actor-create` → `town co-presence local actor
//	    with owner context … global=1` → `town current actor repair … visual_synced=1`
//	    —— 这才是**本地角色外观被重挂**的那条路。
//
// 也就是说：90CN 客户端只在 op2 mode0（当前场景对象自投影）上重挂本地 actor 的外观，
// mode1 只换装备数据 ⇒ mode1 档下必须"重新选角"（重跑 mode0 创建路）才看得到新时装。
// 成本方向也一致：仓库自己的 trace 统计里 op2 mode0 = **4 ms**、op2 mode1 = **24 ms
// （p90 108 ms）**，而 mode1 body 是 **1383..1575 B**（线上 `body_len=` 实测），
// mode0 body 只有 **≈468 B**（同上现场行）⇒ 每次穿脱都发 mode1 正是"装扮栏很卡"的那一半。
//
// 两个开关（都带一键回退；默认取新口径）：
//
//	DNFBRIDGE_EQUIP_MOVE_CONTAINER_REFRESH=suppressed|full
//	    suppressed（默认）= 穿脱后不再重发 5 个整容器（与厂商 9.16 item_slot_refresh_count=0
//	    逐字节同口径，也避免"背包里没有了、身上也没有"）；full = 回退到本批之前的
//	    `PostActionRefreshSelectedItemContainers`（class0/op13 ×5，每次 ≈10.1 KB）。
//	DNFBRIDGE_EQUIP_APPEARANCE_REFRESH=mode0|mode1
//	    mode0（默认，= 家族 Go 服务端与厂商 NOTI 2 的口径）= 每次穿脱**同步**发一次
//	    class0/op2 mode0 当前场景对象自投影（14 条外观行，未穿槽为显式空行），
//	    客户端走 `current-scene-create` → `visual_synced`，模型即时刷新，约 468 B / 客户端 4 ms；
//	    mode1 = 回退到 2026-09-25..09-27 的行为：一次 mode1 穿戴态 + 200 ms 连点合并
//	    （`avatar_appearance_coalesce.go`）。写 mode1 即逐字节回到本批之前，不需要重新编译。
const (
	equipMoveContainerRefreshEnv        = "DNFBRIDGE_EQUIP_MOVE_CONTAINER_REFRESH"
	equipAppearanceRefreshModeEnv       = "DNFBRIDGE_EQUIP_APPEARANCE_REFRESH"
	equipMoveContainerRefreshFull       = "full"
	equipMoveContainerRefreshSuppressed = "suppressed"
	equipAppearanceRefreshMode0         = "mode0"
	equipAppearanceRefreshMode1         = "mode1"
	equipAppearanceSlotGateEnv          = "DNFBRIDGE_EQUIP_APPEARANCE_SLOT_GATE"
	equipAppearanceSlotGateOn           = "on"
	equipAppearanceSlotGateOff          = "off"
)

// equipMoveContainerRefreshFullRequested 回答"穿脱后是否照旧重发整容器"。
// 只有显式写 full 才回退；其余取值（含未设置）都是新口径。
func (s *Service) equipMoveContainerRefreshFullRequested() bool {
	if s == nil {
		return false
	}
	return s.options.equipMoveContainerRefreshFull
}

// equipAppearanceRefreshUsesMode0 回答"穿脱后的外观刷新是否用当前场景对象 mode0"。
// 默认档就是 mode0；只有显式写 DNFBRIDGE_EQUIP_APPEARANCE_REFRESH=mode1 才回退。
func (s *Service) equipAppearanceRefreshUsesMode0() bool {
	if s == nil {
		return true
	}
	return !s.options.equipAppearanceRefreshMode1Rollback
}

// equipMoveContainerRefreshMode 只用于启动日志/诊断。
func (s *Service) equipMoveContainerRefreshMode() string {
	if s.equipMoveContainerRefreshFullRequested() {
		return equipMoveContainerRefreshFull
	}
	return equipMoveContainerRefreshSuppressed
}

// equipAppearanceRefreshMode 只用于启动日志/诊断。
func (s *Service) equipAppearanceRefreshMode() string {
	if s.equipAppearanceRefreshUsesMode0() {
		return equipAppearanceRefreshMode0
	}
	return equipAppearanceRefreshMode1
}

// isWearMoveContainerRefreshAction 判定这条 post action 是否属于"穿脱后的整容器重发"。
// 只有穿脱路径会拿到 PostActionRefreshSelectedWearContainers（见 inventory/handler.go
// 的 ownerAppliedEquipmentMoveResult / ownerAppliedPetEquipmentMoveResult）。
func isWearMoveContainerRefreshAction(action alignedcmd.PostAction) bool {
	return action == alignedcmd.PostActionRefreshSelectedWearContainers
}

func normalizeEquipMoveContainerRefreshMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case equipMoveContainerRefreshFull, "legacy", "containers_full", "on", "1", "true":
		return equipMoveContainerRefreshFull
	default:
		return equipMoveContainerRefreshSuppressed
	}
}

// normalizeEquipAppearanceRefreshMode 只有**显式**写 mode1（及其别名）才回退到 mode1 档；
// 其余取值（含未设置、mode0、legacy、scene_object）都是默认的 mode0 档。
func normalizeEquipAppearanceRefreshMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case equipAppearanceRefreshMode1, "userinfo", "mode_1":
		return equipAppearanceRefreshMode1
	default:
		return equipAppearanceRefreshMode0
	}
}

// normalizeEquipAppearanceSlotGateMode 只有**显式**写 off（及其别名）才关掉外观相关性
// 闸门、回到"每次穿脱都无条件补 PostActionRefreshSelectedActorAppearance"；其余取值
// （含未设置、on、1、true）都是修复档（闸门生效）。见 options.equipAppearanceSlotGateOff。
func normalizeEquipAppearanceSlotGateMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case equipAppearanceSlotGateOff, "0", "false", "no", "legacy", "disable", "disabled", "none":
		return equipAppearanceSlotGateOff
	default:
		return equipAppearanceSlotGateOn
	}
}

// equipAppearanceSlotGateOffRequested 回答"是否回退到每次穿脱都发外观刷新"。
// 只有显式写 DNFBRIDGE_EQUIP_APPEARANCE_SLOT_GATE=off 才为真；nil Service 回落修复档。
func (s *Service) equipAppearanceSlotGateOffRequested() bool {
	if s == nil {
		return false
	}
	return s.options.equipAppearanceSlotGateOff
}

// equipAppearanceSlotGateMode 只用于启动日志/诊断。
func (s *Service) equipAppearanceSlotGateMode() string {
	if s.equipAppearanceSlotGateOffRequested() {
		return equipAppearanceSlotGateOff
	}
	return equipAppearanceSlotGateOn
}

func normalizeUpgradeRoomReplyMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case upgradeRoomReplyEchoFailure, "ack", "failure_ack", "reply":
		return upgradeRoomReplyEchoFailure
	default:
		return upgradeRoomReplyReferenceDrop
	}
}

// 宠物/神器穿戴（op19 落点 list 3 槽 26..29）的"回包后刷新"口径。
//
// 现场（国内服 192.0.2.11，`/opt/dnf90/logs/packet_log.txt`，conn `game-000015`，
// 角色 1，操作者报"穿戴宠物 宠物正确出现 但是背包和已经装备的宠物消失 重新选择角色后出现"）：
// 同一条 op19 上，宠物移动是**唯一还在回放整容器**的搬运路径：
//
//	class1/op19 ACK
//	post_action=refresh_selected_item_containers list_types=[0 1 2 7 38]
//	  list 0 entry_count=64 body_len=7621
//	  list 1 entry_count=97 body_len=12324
//	  list 2 entry_count=12 body_len=1434
//	  list 7 entry_count=21 body_len=2502   ← 宠物容器被整容器替换，装备中的那只不在里面
//	  list 38 entry_count=0 body_len=3
//	msg14 list 3 slot 26（122 B，唯一的 list-3 槽 26 行）→ msg105 → msg102 → op2 mode0 → op413
//
// 即每次穿/脱宠物 ≈ 23.9 KB / 5 个整容器，而**整容器重发里从来没有 list 3**
// （`currentSelectedItemListTypes` = {0,1,2,7,38}，见 current_item_list_types.go:17），
// 落点恰好就是 list 3 槽 26 ⇒ 与 2026-09-25 那次"穿上了装备、身上没有、背包也不存在"
// 的装扮/装备 bug 完全同形（见本文件顶部那段长注与 `PostActionRefreshSelectedWearContainers`
// 的修法）。同一会话里穿普通装备/时装（`DNFBRIDGE_EQUIP_MOVE_CONTAINER_REFRESH=suppressed`）
// 一个整容器都不发，操作者验收"模型会跟着变"。
//
// 厂商口径（本版源码，宠物搬运的权威实现）：
//
//	· `Network\Handlers\Pets\PetInventoryMoveCoordinator.cs:24-33`
//	  `CompleteAsync` = 宠物运行时提交 → `SendCreatureStateRefreshAsync`（0x0069 宠物表
//	  + NOTI 2 外观）→ `SendPetItemRefreshAsync`。**没有任何整容器回放**（既没有
//	  0x000D/op13 的 `SendItemListRefresh`，也没有 list 0/1/2/38）。
//	· `:58-81` `SendPetItemRefreshAsync` 只发两次逐槽 op14：
//	  `SendUpdateItemList(Pet, result.PetCreatureRefreshSlots)`（宠物容器里**真正变动的槽**）
//	  与 `SendUpdateItemList(Equipment, result.EquipmentRefreshSlots)`（list 3 的宠物槽）。
//	  这两个槽集来自 `Network\Handlers\InventoryHandler.Move.cs:184-195` 的
//	  `ApplyPetMoveProjection`：遍历本次提交的 changed slots，按 list 分别收集。
//	· `Network\Handlers\InventoryRefreshSender.cs:377-402` 逐槽刷新走 **class0/0x000E**
//	  （= 我方的 msg 14），body = `u8 listType + u16 count + rows`
//	  （`Network\Builders\Inventory\ItemListUpdateBuilder.cs:30-52`），
//	  **空槽照样发**：`:68-83` 取不到物品就 `WriteEmptyEntry` = `slot + int32(-1)` + 0
//	  （`:133-143`）。这正是我方 `sendSelectedIncrementalItemSlotRefreshes` 里
//	  `entry.patchCore(slot, math.MaxUint32, 0)` 的同形空行
//	  （current_item_list_send.go:208-214，list 0 的空行已在线上跑通：
//	  `game-limited-cube-target-replacement-clear-send list_type=0 slot=102`）。
//	· 穿戴宠物**不属于**宠物容器：`Game\Inventory\Pets\PetInventoryAccessor.cs:19-24`
//	  读的是 `InventoryListType.Equipment` 的 `CreatureEquipSlot`；
//	  `Game\Inventory\Pets\PetInventoryLayout.cs:11-13` 明写旧库那个
//	  `CreatureEquipSlot+216`"协议和在线模型都不把它当作真实宠物栏槽位"。
//	  ⇒ 我方"宠物从 list 7 搬到 list 3 槽 26"的持久化表示**与厂商一致，不需要改**；
//	  要改的只是下发投影。
//
// 已知风险与它为什么被接受：`current_item_list_send.go:222-230` 记录过 list 7 的
// **op14 新建行**会让客户端在库存界面模块 0x23082D3 崩溃（角色 3 领更名卡，7:191，
// 那是"新建一行才会创建对象"的路径）。本条要发的是**宠物刚离开的那个槽**——客户端在
// ACK 那一刻已经用原生 op19 处理把对象搬走了（`native sub_1D2DE80`，见
// inventory/handler.go:1002-1010 的现场注），那个槽在 UI 上本来就有行；
// 客户端补丁的 `ProxyPetItemUpdateDynamicState`（client-patch/90CN.cpp:3650-3671）
// 也证明 list-7 的 op14 行走的是"已存在对象"路径（`esi = 现有 item 对象`）。
// 即便如此，这条是**本轮唯一无法在本机证伪的假设**，所以留了三个档：
//
//	DNFBRIDGE_PET_MOVE_CONTAINER_REFRESH=slots|none|full
//	    slots（默认）= 只发本次真正变动的 list 7 槽（有货就是权威行，空槽就是
//	        `0xFFFFFFFF` 空行）+ 既有的 list 3 槽 26 行 / op105 / op102 / mode0 外观，
//	        与厂商 `PetCreatureRefreshSlots` 逐槽口径同形，**不回放任何整容器**；
//	    none = 连 list 7 那一行也不发，完全依赖客户端原生 op19 搬运
//	        （与 `DNFBRIDGE_EQUIP_MOVE_CONTAINER_REFRESH=suppressed` 的装扮档同口径；
//	        若默认档在实机上出问题，这是最小的一档）；
//	    full = 一键回退到 2026-09-28 第一轮：`PostActionRefreshSelectedPetMoveSlots`
//	        还原成 `PostActionRefreshSelectedItemContainers` 的五个整容器
//	        （list 0/1/2/7/38，每次 ≈23.9 KB），逐字节恢复今天的线上行为。
const (
	petMoveContainerRefreshEnv   = "DNFBRIDGE_PET_MOVE_CONTAINER_REFRESH"
	petMoveContainerRefreshSlots = "slots"
	petMoveContainerRefreshNone  = "none"
	petMoveContainerRefreshFull  = "full"
)

// petMoveContainerRefreshFullRequested 回答"宠物/神器穿戴后是否照旧回放五个整容器"。
// 只有显式写 full 才回退；其余取值（含未设置）都是新口径。
func (s *Service) petMoveContainerRefreshFullRequested() bool {
	if s == nil {
		return false
	}
	return s.options.petMoveContainerRefreshFull
}

// petMoveContainerRefreshSuppressed 回答"是否连 list 7 的逐槽行也不发"。
func (s *Service) petMoveContainerRefreshSuppressed() bool {
	if s == nil {
		return false
	}
	return s.options.petMoveContainerRefreshNone
}

// petMoveContainerRefreshMode 只用于启动日志/诊断。
func (s *Service) petMoveContainerRefreshMode() string {
	switch {
	case s.petMoveContainerRefreshFullRequested():
		return petMoveContainerRefreshFull
	case s.petMoveContainerRefreshSuppressed():
		return petMoveContainerRefreshNone
	default:
		return petMoveContainerRefreshSlots
	}
}

// normalizePetMoveContainerRefreshMode 只有**显式**写 full / none（及其别名）才离开
// 默认的 slots 档；其余取值（含未设置）都是逐槽口径。
func normalizePetMoveContainerRefreshMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case petMoveContainerRefreshFull, "containers", "legacy", "containers_full", "on", "1", "true":
		return petMoveContainerRefreshFull
	case petMoveContainerRefreshNone, "suppressed", "off", "0", "false":
		return petMoveContainerRefreshNone
	default:
		return petMoveContainerRefreshSlots
	}
}

// 时装穿脱（op19 落到 list 1/3 的时装槽）回包后外观刷新的连点合并口径。
//
// 现场（客户端 trace 2026-09-28，pid=3776，操作者"现在穿戴时装模型会跟着变 但是我对
// 穿上脱下的速度不太满意 能优化吗"）：
//
//	· 服务端往返本身已经很快：op19 派发 `phase=before→after` 5–20 ms，
//	  `class0/op2 mode0` 比 ACK 晚 ≈6 ms、它自己的派发 13–22 ms ⇒ 一次穿脱 ≈30–40 ms。
//	· 贵的是客户端那一步：每一次 mode0 都会走
//	  `mode0-owner-compare … branch=current-scene-create` → `local-actor-create`
//	  → `game-options-apply`，即**整题重建本地角色**。连穿 N 件 ⇒ N 次重建。
//
// 所以优化点不是包大小（mode0 已经只有 ≈468 B），而是**重建次数**：
// 窗口内第一次同步发（单件穿脱 = 点一下立刻看到，包数不变），紧随其后的并入一次
// 窗口末尾的尾随刷新 ⇒ N 连点最多 2 个 mode0。
//
//	DNFBRIDGE_EQUIP_APPEARANCE_COALESCE=leading_trailing|off
//	    leading_trailing（默认）= 上述 leading + trailing 合并；
//	    off = 一键逐字节回到 2026-09-28 第一轮：mode0 档每次穿脱都同步发一个 mode0。
//	    （mode1 回退档不受影响，它一直有自己的 200 ms 合并。）
//	DNFBRIDGE_EQUIP_APPEARANCE_COALESCE_MS=<毫秒>
//	    窗口与尾随延迟（同一个值），默认 200；非法/≤0 回落到内置默认。
//	    它只是调参旋钮，不改变档位语义。
const (
	equipAppearanceCoalesceEnv       = "DNFBRIDGE_EQUIP_APPEARANCE_COALESCE"
	equipAppearanceCoalesceWindowEnv = "DNFBRIDGE_EQUIP_APPEARANCE_COALESCE_MS"
	// equipAppearanceTrailingEnv 把"尾随刷新延迟"从"连点窗口"里拆出来。
	// 未配置 ⇒ 逐字节等于改动前（尾随延迟 = 连点窗口）。见
	// avatar_appearance_coalesce.go 的 avatarAppearanceTrailingDelay。
	equipAppearanceTrailingEnv             = "DNFBRIDGE_EQUIP_APPEARANCE_TRAILING_MS"
	equipAppearanceCoalesceLeadingTrailing = "leading_trailing"
	equipAppearanceCoalesceOff             = "off"
	// avatarAppearanceCoalesceWindowMaxMillis 是"连点窗口"的硬上限。
	//
	// 它原来的理由是"连点合并不能长到'点完很久才看到'的程度"（见原注释）—— 那条
	// 理由成立于**窗口 == 尾随延迟**的年代。现在两者已拆开：窗口只回答"两次穿脱隔
	// 多久还算同一次连点"，"多久看到最终外观"由下面
	// avatarAppearanceTrailingMaxMillis 单独守。因此窗口可以放松到能盖住操作者
	// 真实的 0.45–3 s 穿脱间隔（2026-09-28 实测）。超过即回落到默认 200 ms。
	avatarAppearanceCoalesceWindowMaxMillis = 5000
	// avatarAppearanceTrailingMaxMillis 是"尾随刷新延迟"的硬上限：
	// **无论窗口调多大，收手之后最多 1 s 内必须补上最终外观** —— 这才是
	// "点完很久才看到"的真正守卫。
	avatarAppearanceTrailingMaxMillis = 1000
)

// normalizeEquipAppearanceCoalesceMode 只有**显式**写 off（及其别名）才关掉合并；
// 其余取值（含未设置）都是默认的 leading + trailing 档。
func normalizeEquipAppearanceCoalesceMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case equipAppearanceCoalesceOff, "0", "false", "no", "legacy", "disable", "disabled", "none":
		return equipAppearanceCoalesceOff
	default:
		return equipAppearanceCoalesceLeadingTrailing
	}
}

// equipAppearanceCoalesceMode 只用于启动日志/诊断。
func (s *Service) equipAppearanceCoalesceMode() string {
	if s == nil || s.options.avatarAppearanceCoalesceOff {
		return equipAppearanceCoalesceOff
	}
	return equipAppearanceCoalesceLeadingTrailing
}

// normalizeAvatarAppearanceCoalesceWindowMillis 解析毫秒窗口。只有 1..
// avatarAppearanceCoalesceWindowMaxMillis 之间的整数被接受；空值、非数字、
// 0、负数、超上限一律返回 0（= 使用内置默认 200 ms）。
func normalizeAvatarAppearanceCoalesceWindowMillis(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 || parsed > avatarAppearanceCoalesceWindowMaxMillis {
		return 0
	}
	return parsed
}

// normalizeAvatarAppearanceTrailingMillis 解析**尾随刷新延迟**毫秒。
// 只有 1..avatarAppearanceTrailingMaxMillis 之间的整数被接受；空值、非数字、0、
// 负数、超上限一律返回 0（= 沿用连点窗口，也就是改动前口径）。
func normalizeAvatarAppearanceTrailingMillis(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 || parsed > avatarAppearanceTrailingMaxMillis {
		return 0
	}
	return parsed
}

// equipAppearanceCoalesceWindowMillis 只用于启动日志/诊断：回答生效的窗口毫秒数。
func (s *Service) equipAppearanceCoalesceWindowMillis() int64 {
	if s == nil {
		return currentAvatarAppearanceCoalesceWindow.Milliseconds()
	}
	return s.avatarAppearanceCoalesceWindow().Milliseconds()
}

// equipAppearanceTrailingMillis 只用于启动日志/诊断：回答生效的**尾随刷新延迟**毫秒数。
//
// 存在的理由：这个量 2026-09-28 才从"连点窗口"里拆出来（见
// avatar_appearance_coalesce.go）。窗口那一半早就在启动行里回显
// （`equip_appearance_coalesce_ms`），尾随这一半如果不回显，操作者调了
// DNFBRIDGE_EQUIP_APPEARANCE_TRAILING_MS 也无从在启动行确认它到底生效没有 ——
// 而那正是他调参时要看的唯一一处。未配置时按口径回落到窗口值。
func (s *Service) equipAppearanceTrailingMillis() int64 {
	if s == nil {
		return currentAvatarAppearanceCoalesceWindow.Milliseconds()
	}
	return s.avatarAppearanceTrailingDelay().Milliseconds()
}
