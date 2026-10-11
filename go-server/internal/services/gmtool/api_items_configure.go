package gmtool

// B：已有物品的"在线配置"（品级 / 强化-增幅 / 期限）。
//
//	GET  /api/characters/{id}/items/config-options?listType={listType}&slot={slot}
//	POST /api/characters/{id}/items/configure   {"listType":N,"slot":N,"options":{...}}
//
// # 前端契约（wwwroot/js/inventory.js:261-386，逐字段对齐，不许改名）
//
//   - 查询参数名就是 **listType** 与 **slot**（inventory.js:265）。
//   - configure 请求体：`{ listType: item.listType, slot: item.slot, options }`
//     （inventory.js:375-379）。`options` 里前端会放的字段（inventory.js:350-369）：
//     `qualityMode`(int) / `upgradeLevel`(int) / `amplifyType`(int) / `forgingLevel`(int)
//     / `avatarOptionValue`(int) / `expirationDays`(int)。
//   - 前端读的 capability 字段（inventory.js:287-321）：
//     `type`（'avatar' | 'equipment'，其它值走"只渲染期限"分支）、
//     `avatar.{part,options,currentOptionValue}`、
//     `equipment.{qualityOptions,currentQualityMode,canUpgrade,canAmplify,maxUpgradeLevel,
//     currentUpgradeLevel,amplifyTypes,currentAmplifyType,canForge,maxForgingLevel,
//     currentForgingLevel}`、
//     `expiration.{canOverride,durations,defaultDays,currentRemainingDays,maxDays}`。
//     `qualityOptions` / `amplifyTypes` 的每项是 `{value,label}`（give.js:454-459 optionHtml）。
//
// # 移植来源（只读参考，C#；行号见每段注释）
//
//	vendor Services/GmService.InventoryConfiguration.cs
//	  :14-24     GetInventoryItemConfigOptions：角色 → 槽位记录 → 能力块（failWhenUnsupported=true）
//	  :26-116    ConfigureInventoryItem：请求 → 目标槽位 → 期限解析 → 装扮分支 → 装备分支
//	  :80-98     装备分支的逐条边界与写入顺序（品级种子 → 强化 → 增幅 → 锻造 → 期限）
//	  :118-245   BuildInventoryItemConfigOptions：装扮 → 装备 → 只读期限 的分支顺序
//	  :276-315   期限能力（当前值/剩余天数/最大天数/默认天数）
//	  :317-403   期限覆盖解析（受限 + 可覆盖才允许改）
//	  :484-519   IsInventoryConfigurableEquipment：只有主背包/穿戴容器里的装备才算
//	vendor ServerCore/Game/Inventory/ItemGrantOptions.cs:42-69   EquipmentGrantPolicy.Describe
//	  （canUpgrade = 可强化类型；canAmplify 另需 55 级 + 稀有度 2；canForge = 武器）
//	vendor ServerCore/Game/Inventory/ItemGrantOptions.cs:167-177 GetAmplifyTypeLabel（1体力…4智力）
//	vendor ServerCore/Game/Inventory/InventoryModels.cs:12-36   ItemQualityMode{Random=0,Top=1} 与 TopQualitySeed
//	vendor ServerCore/Game/Inventory/AmplifyInitialValueResolver.cs:18-27
//	  红字初始值 = [option data] 的 base × [rarity weight] 的权重
//
// 90CN 侧（本文件真正落库的那一套，全部是既有实现，不新造语义）：
//
//	modules/dnf/itemquality/quality.go:41-53   Apply：quality_seed + 0x77 行 0x06..0x09（唯一实现）
//	modules/dnf/inventory/owner.go:2580-2622   upgradeLevelOf / packedUpgradeAttrOf / setUpgradeLevel
//	                                           （游戏自己的强化等级读写器；本文件逐字移植写入器）
//	modules/dnf/inventory/owner_amplify.go:2653-2670 upgradeAmplifyState（红字类型/数值的读法）
//	modules/dnf/inventory/owner_amplify.go:288-309   setAmplifyState（红字类型/数值的写器；逐字移植）
//	modules/dnf/itemgrade/owner.go:141-160     品级调整 owner 的写侧口径（itemquality.Apply +
//	                                           item_kind 归一 + last_* 审计键）
//	services/dnfbridge/amplify_item_pvf.go:105-159 / :240-253   etc/amplifyitem.etc 的解析与
//	                                           红字初值公式（与游戏内净化/注入完全同一份数据）
//	services/gmtool/api_items_grant_options.go:146-158 支持强化/增幅的装备类型表（唯一一份）
//	services/gmtool/api_items_durability.go:105-126   按物品索引打开 PVF 文档的方式
//
// # 锻造落点（2026-09-28 重新取证；上一版"没有锻造字段"的结论是**错的**）
//
// 结论：**锻造等级 = 0x77 物品行的 0x57 字节**，Extra 键 `byte_57` / `value_57`；
// 上限 = PVF `etc/upgrade_separate.etc` 的 `[separate upgrade max]`（真档 8）。
//
// 证据（全部本地可复核）：
//
//	E1 厂商 S4A21 的线格式写入器 ServerS4A21-master/.../ItemListProtocolWriter.cs:145-170
//	   （WriteEntry84）按序写：slot(2)/itemId(4)/value(4)/Attr(1)/durability(2)/SealFlag(1)/
//	   附魔块(8)/marker16(4)/编年史块(17)/ExpireTime(4)/徽章块(11)/随机选项块(16)/
//	   WriteTailBlock(9)。WriteTailBlock（:404-415）的**第 1 字节就是 core.GenuineUpgrade**
//	   —— 也就是锻造；ItemCore.Encode（:588-596）的字段顺序与之一致
//	   （GenuineUpgrade / EmancipateEquipmentLevel / TradeRestriction / u16 / 3 字节 /
//	   RemainUseCount / SortLockFlag）。
//	E2 A21 该行的宽度 = 84（Entry84Size），其"锻造所在字节"= 84-10 = 0x4A：
//	   MakeEquipListCodec.cs:154 `Forging = raw[raw.Length - 10]`、:388 `tail10[0] = f.Forging`；
//	   86JP GM 工具同族实现 ItemExtraView.cs:299-302 `Forging => _tailData2F[27]`
//	   （tailData2F = 0x2F 起 37 字节 ⇒ 0x2F+27 = 0x4A）。
//	E3 **90CN 客户端不是 A21 客户端**：把 E1 的字段宽度套到 90CN 反汇编取证的偏移上，
//	   逐项相差固定 +13 字节：
//	     A21 ExpireTime 0x2B  ↔ 90CN 0x38（current_item_list_types.go:7）
//	     A21 徽章数      0x2F  ↔ 90CN 0x3C（socket_emblem_protocol.go:39）
//	     A21 效果符文    0x38  ↔ 90CN 0x45（equipment_effect_rune.go:28 = 0x3C+9）
//	     A21 随机选项块  0x3A  ↔ 90CN 0x47（userinfo_current_equipment_wire.go:153-167：
//	                            count=0x47、三条记录在 0x48/0x4B/0x4E）
//	   ⇒ 90CN 的 9 字节尾块 = 0x57..0x5F，锻造 = **0x57**、解封装备等级 = 0x58、
//	     交易限制 = 0x59。
//	E4 90CN 自己的代码已经在**匿名往返**这三个字节：byte_57/byte_58/byte_59
//	   （modules/dnf/inventory/body.go:461、dnfbridge/current_item_list_stack_projection.go:88、
//	   current_item_list_equipment_projection.go:35、equipped_creature_item_refresh.go:107-109）。
//	   三个连续字节、顺序与 A21 尾块前三个字段一一对应 —— 这是"那三格在 90CN 客户端里
//	   承载有意义的状态"的现成证据。
//	E5 上限来自 PVF：ServerS4A21-master/.../SeparateUpgradeTableProvider.cs:11 读
//	   `etc/upgrade_separate.etc`，SeparateUpgradeTable.cs:42-44 取 `[separate upgrade max]`。
//	   真档 dump（命令见下）里是 8，且同文件 `[level]` 也是 8、`[table]` 有 10 档
//	   （4 个数一档：SuccessWeight 在第 3 位、MaterialWeight 在第 4 位，与 :29-41 一致）。
//	E6 反例排除：同族 86JP 工具把锻造读在 0x4A，但 90CN 的 0x4A 是 E3 里那张"索引状态
//	   记录数组"的第 3 个字节（0x48+index, index=2）。写 0x4A 会让客户端按 count 读到一条
//	   伪造记录（userinfo_current_equipment_wire.go:158-172 会因为记录数与 count 不符把整行
//	   判成 mismatch）—— 所以**不用** 0x4A，也不用 tail_data_2f[27]。
//
// 取证命令（本机可复跑）：
//
//	go run ./cmd/scratch/pvfdump -pvf '<90CN客户端>/Script.pvf' -path 'etc/upgrade_separate.etc'
//	  → [separate upgrade max] 8 / [level] 8 / [table] 40 个数（10 档）
//	对 A21 侧：直接读 ServerS4A21-master/Server/DfoServer/Network/Builders/Inventory/
//	  ItemListProtocolWriter.cs（WriteEntry84 + WriteTailBlock）与
//	  Game/Inventory/Models/ItemCore.cs:560-603（Encode 顺序）。
//
// 自检（操作者 30 秒可验）：面板里给一把武器设"锻造 8"保存 → 客户端重开背包看 tooltip
// 是否显示锻造 +8；再设回 0 应立即消失。若某客户端版本显示不对，先怀疑偏移而不是数据：
// 用 `GET /api/characters/{id}/items/config-options` 看 forgingPVFPath 与
// maxForgingLevel，再用响应/事件里的 forging_row_offset 核对写入位置。
//
// # 本仓与上游的有意差异（每一项都写清理由）
//
//	ITEMCFG-D1 每个 option 字段都是**可选的**（指针）：没出现的字段不介入。
//	           上游 DTO 的零值就是"写成 0"（例如 count/ExpireDays 那一类陷阱），
//	           而 `{"options":{"expirationDays":7}}` 这种只改期限的请求在上游会被
//	           读成"顺手把强化清零"。前端在装备分支每次都发全四个字段，所以
//	           面板行为不变；只有"少发字段"的调用会变安全。
//	ITEMCFG-D2 红字**类型没变**时沿用已存的红字数值（amplify_value），不按稀有度重算。
//	           上游 :92 每次都写 `AmplifyInitialValueResolver.Resolve(rarity)`，会把
//	           玩家已经洗上去的数值抹回初值。类型真的换了（或原先没有）才写 PVF 初值，
//	           且初值仍然只来自 etc/amplifyitem.etc（算不出来就明确拒绝）。
//	ITEMCFG-D3 **锻造（forgingLevel）= 0x77 行的 0x57 字节**，上限取 PVF。证据链见下面
//	           "锻造落点"一节；面板的 canForge/currentForgingLevel/maxForgingLevel 全部
//	           由真实数据算出，configure 走同一个写入点落库。
//	ITEMCFG-D4 装扮（avatar）属性**不写**：本仓 gmtool 没有可复用的
//	           [avatar select ability] 读侧解析（解析器在 dnfbridge/avatar_compound_pvf.go
//	           里是包内私有；modules/dnf/avataroptionchange 只导出写侧 owner，不含
//	           "该模板对本职业有哪些合法选项"）。所以装扮的能力块只报期限
//	           （与上游 :129-147 的 failWhenUnsupported 分支同形），
//	           收到 avatarOptionValue 时具名拒绝，绝不发明选项表。
//	ITEMCFG-D5 只支持"住在角色自己记录里"的容器：listType 0/1/7/38 → Slots，
//	           listType 2 → Warehouse（repository/item_normalization_audit.go:163-175
//	           的不变量）。穿戴装备（listType 3）属于装备聚合、账号金库（12）属于
//	           账号共享背包，两者都具名拒绝并指出该去哪里改。
//	           账号共享槽 0:354..0:365（晶块/灵魂）同样拒绝 —— 它们的持久归属是账号
//	           （account_inventory_repository.go:19-46），不该被角色侧接口改写。
//	ITEMCFG-D6 装备的强化/增幅是**单件实例属性**：槽位数量 != 1 时拒绝
//	           （与发放侧 api_items_grant_options.go:162-171 的"装备一封只能发 1 件"同口径）。
//	ITEMCFG-D7 写库边界用 `Group.CharacterItems.WithinCharacterItems` +
//	           `dnfrepo.SaveInventoryFields`（与 api_items_delete.go:582-611 同一条），
//	           **不自己拼 SQL**。改造的 Extra 键与游戏自己的写入器逐字相同，
//	           并在 0x77 行存在时把对应偏移一起改掉（免得缓存行与权威 Extra 打架）。
//	ITEMCFG-D8 在线守卫用既有的 requireOffline（online_guard.go:117-142）：默认档
//	           （DNFBRIDGE_GM_REQUIRE_OFFLINE 未开）不检查 —— 与 P2/P3/P4 全部新写路由
//	           同一约定。注意本仓对"直写背包"的默认立场是拒绝
//	           （api_items.go:334-337），items/delete 那条路由就是这个立场的条件注册；
//	           本接口是操作者显式点"配置"才触发的单行属性写入，按任务要求实现，
//	           但把开关建议写在响应 hint 与交付报告里。

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemquality"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	// itemcfgItemListEntrySize 是 0x77 物品行的宽度
	// （modules/dnf/inventory/body.go:28 `currentItemListEntrySize = 0x77`）。
	// 只有长度**正好**等于它才改行内偏移 —— 与游戏自己的写入器同判据
	// （inventory/owner.go:2618、owner_amplify.go:303）。
	itemcfgItemListEntrySize = 0x77

	// 与 vendor EquipmentGrantPolicy（ItemGrantOptions.cs:44）同界；
	// 31 也是 0x0A 打包字节里等级位的宽度（inventory/owner.go:2613 `level & 0x1F`）。
	// **锻造上限不在这里**：它来自 PVF etc/upgrade_separate.etc 的 [separate upgrade max]
	// （vendor ItemGrantOptions.cs:45 的常量 8 只是它自己那份 PVF 的值，不许写死）。
	itemcfgMaxUpgradeLevel = 31

	// 红字类型 1..4（体力/精神/力量/智力），与 vendor GetAmplifyTypeLabel 一致；
	// 上游 gm.go:1812 的 amplify_type 边界同样是 0..4。
	itemcfgMaxAmplifyType = 4

	// itemcfgMinAmplifyRarity 与 vendor ItemGrantOptions.cs:57（Rarity >= 2）、
	// 以及 90CN 自己增幅 owner 的 `resolution.TargetRarity < 2` 门槛
	// （modules/dnf/inventory/owner_amplify.go:134）完全一致。
	itemcfgMinAmplifyRarity = 2

	// 期限上界与发放侧同界（api_items_grant_options.go:122 的 gmMailExpireDaysMax / 上游 :1809）。
	itemcfgMaxExpireDays = 3650

	// 品级模式：0=随机，1=100% 最上级（vendor InventoryModels.cs:32-36 的
	// ItemQualityMode{Random=0,Top=1}；本仓发放侧的 qualityOptions 用的也是 0/1，
	// 见 api_items.go:254-257 与 api_items_grant_options.go:411-416）。
	itemcfgQualityRandom = 0
	itemcfgQualityTop    = 1

	// itemcfgAmplifyItemPVFPath 是红字数值表的唯一路径
	// （dnfbridge/amplify_item_pvf.go:14 的 currentAmplifyItemPVFPath，逐字相同）。
	itemcfgAmplifyItemPVFPath = "etc/amplifyitem.etc"

	// itemcfgSeparateUpgradePVFPath 是**锻造**（别升级 / separate upgrade）表的唯一路径。
	// 厂商依据：ServerS4A21-master 的 SeparateUpgradeTableProvider.cs:11 读的就是
	// `etc/upgrade_separate.etc`，其 [separate upgrade max] 段就是锻造上限
	// （SeparateUpgradeTable.cs:42-44）；PVF 里同文件还有 [level] 与之同值（真档 8/8）。
	itemcfgSeparateUpgradePVFPath = "etc/upgrade_separate.etc"

	// itemcfgForgingRowOffset 是锻造等级在 0x77 物品行里的**唯一字节**：0x57。
	// 证据链（逐段可复核，见文件头"锻造落点"一节）：
	//  1. A21 的线格式写入器 ItemListProtocolWriter.WriteEntry84（:145-170）按序写
	//     slot/itemId/value/Attr/durability/SealFlag/附魔块/marker16/编年史块(17)/
	//     ExpireTime/徽章块(11)/随机选项块(16)/WriteTailBlock(9)，而 WriteTailBlock
	//     （:404-415）的**第 1 个字节就是 core.GenuineUpgrade = 锻造**，其后是
	//     EmancipateEquipmentLevel、TradeRestriction、u16、3 字节、RemainUseCount；
	//     ItemCore.Encode（:588-596）的顺序与之一致。
	//  2. 90CN 的线格式写入器（inventory/body.go:448-465、dnfbridge 的
	//     current_item_list_*_projection.go）与 A21 的字段宽度逐项相同，但 90CN 客户端
	//     的偏移整体比 A21 多 **13 字节**（90CN 侧由反汇编取证：到期时间 0x38 而不是
	//     A21 的 0x2B；徽章数 0x3C 而不是 0x2F；效果符文 0x45 而不是 0x38；16 字节的
	//     随机选项/索引状态块 0x47..0x56 而不是 0x3A..0x49）。
	//  3. ⇒ 90CN 客户端的 9 字节尾块落在 0x57..0x5F，锻造 = 0x57、解封装备等级 = 0x58、
	//     交易限制 = 0x59。**90CN 自己的代码已经在匿名地往返这三字节**
	//     （byte_57/byte_58/byte_59：inventory/body.go:461、current_item_list_stack_projection.go:88、
	//     current_item_list_equipment_projection.go:35、equipped_creature_item_refresh.go:107-109）
	//     —— 三个连续字节、顺序与 A21 尾块前三个字段一一对应。
	//  4. 反例排除：同族 86JP 工具把锻造读在 tailData2F[27]（=行 0x4A），但 0x4A 在
	//     90CN 客户端里是索引状态记录数组的第 3 个字节（userinfo_current_equipment_wire.go:153-167
	//     读 count=0x47、三条记录在 0x48/0x4B/0x4E），写进去会污染客户端读到的记录表，
	//     因此**不用** 0x4A。
	itemcfgForgingRowOffset = 0x57

	// itemcfgExpirationRemainingFallbackDays 对应 vendor :288-292 的兜底：
	// 当前到期时间已经过去（或没有实例到期时间）时，期限输入框默认 30 天。
	itemcfgExpirationRemainingFallbackDays = 30

	// itemcfgAmplifyLevelConstFallback 只在"读不到 etc/amplifyitem.etc"时用于拒绝文案，
	// 取值与 vendor ItemGrantOptions.cs:20 的默认值一致（真档 [equip level const] 就是 55）。
	itemcfgAmplifyLevelConstFallback = 55
)

// itemcfgForgingExtraKeys 是锻造等级在 Extra 里的键（与既有投影读的两个别名一致，
// 读序也是这两个：sceneInventoryExtraByte(extra, "byte_57", "value_57")）。
var itemcfgForgingExtraKeys = []string{"byte_57", "value_57"}

// itemcfgOptionKeys 是 options 对象唯一接受的字段名（严格解码用，
// 与 api_items_grant_options.go:88 的 gmMailGrantOptionKeys 同一形态）。
var itemcfgOptionKeys = []string{
	"qualityMode", "upgradeLevel", "amplifyType", "forgingLevel",
	"avatarOptionValue", "expirationDays",
}

var (
	itemcfgErrOptionsMissing            = errors.New("缺少 options 对象")
	itemcfgErrNoOptions                 = errors.New("options 里没有任何要改的字段")
	itemcfgErrSlotEmpty                 = errors.New("目标槽位没有可配置物品")
	itemcfgErrInventoryMissing          = errors.New("角色没有背包记录")
	itemcfgErrNoConfigurableFields      = errors.New("该物品没有可配置项（既不是可配置装备，模板也没有可覆盖的期限）")
	itemcfgErrAvatarOptionsUnavailable  = errors.New("该装扮没有可由本面板解析的可选属性（90CN 面板没有 [avatar select ability] 的读侧解析），且它没有可覆盖的期限")
	itemcfgErrAvatarOptionUnsupported   = errors.New("装扮属性配置未移植到 90CN 面板：本接口不写时装属性（ext_data0），只支持装备品级/强化/增幅/锻造与期限")
	itemcfgErrForgingTableUnavailable   = errors.New("读取锻造表 etc/upgrade_separate.etc 失败（没有 [separate upgrade max] 就无法确定上限）")
	itemcfgErrForgingNotWeapon          = errors.New("只有武器可以锻造")
	itemcfgErrForgingLevelOutOfRange    = errors.New("锻造等级超出 PVF 上限")
	itemcfgErrEquipmentNotConfigurable  = errors.New("该物品不是可配置装备：强化/增幅/品级只写给支持这些属性的装备类型")
	itemcfgErrEquipmentStack            = errors.New("该槽位是装备但数量不是 1：装备的强化/增幅/品级是单件实例属性，无法表达一堆同款")
	itemcfgErrExpirationUnsupported     = errors.New("该物品不支持自定义期限（PVF 模板没有期限，实例也没有到期时间）")
	itemcfgErrTransactionUnavailable    = errors.New("角色背包事务能力未就绪")
	itemcfgErrItemIndexBuilding         = errors.New("物品索引还在构建中, 稍等几秒再试")
	itemcfgErrWalletSlot                = errors.New("槽位 0:0..0:2 是钱包行（金币/复活币/技能点），没有物品实例可配置")
	itemcfgErrAccountSharedSlot         = errors.New("槽位 0:354..0:365 是账号共享槽（晶块/灵魂），持久归属是账号；请在账号侧（账号晶块接口）读写")
	itemcfgErrWornEquipment             = errors.New("listType=3（身上穿戴）属于装备聚合 dnf_equipment_*，不在角色背包行里，本接口不写")
	itemcfgErrAccountCargoSlot          = errors.New("listType=12（账号金库）的持久归属是账号共享背包，请用账号金库接口")
	itemcfgErrContainerUnsupported      = errors.New("不支持的容器")
	itemcfgErrListTypeMissing           = errors.New("缺少 listType 查询参数")
	itemcfgErrSlotMissing               = errors.New("缺少 slot 查询参数")
	itemcfgErrAmplifyTableUnavailable   = errors.New("读取红字数值表 etc/amplifyitem.etc 失败")
	itemcfgErrAmplifyValueUnresolvable  = errors.New("无法从 PVF 计算红字初始值")
	itemcfgErrQualityWriteFailed        = errors.New("写入品级种子失败")
	itemcfgErrQualityModeInvalid        = errors.New("装备品级选项无效")
	itemcfgErrUpgradeUnsupported        = errors.New("该装备不支持强化")
	itemcfgErrAmplifyUnsupported        = errors.New("该装备不支持增幅")
	itemcfgErrExpirationDaysInvalid     = errors.New("期限天数非法")
	itemcfgErrListTypeOutOfRange        = errors.New("listType 超出范围")
	itemcfgErrSlotOutOfRange            = errors.New("slot 超出范围")
	itemcfgErrUpgradeLevelOutOfRange    = errors.New("强化/增幅等级超出范围")
	itemcfgErrAmplifyTypeOutOfRange     = errors.New("红字属性类型超出范围")
	itemcfgErrItemIDInvalid             = errors.New("物品 ID 超出 uint32")
	itemcfgErrItemIndexFailed           = errors.New("物品索引构建失败")
	itemcfgErrItemUnknown               = errors.New("物品在 PVF 中不存在")
	itemcfgErrAvatarStackFieldsRejected = errors.New("装扮不支持装备字段")
)

// ---------------------------------------------------------------------------
// 请求 / 目标
// ---------------------------------------------------------------------------

// itemcfgOptionsRequest 是 `options` 对象。**全部字段用指针**：nil = 本次不改这一项
// （见 ITEMCFG-D1）。前端的装备分支每次都发全四个整数，所以"前端行为"不变。
type itemcfgOptionsRequest struct {
	QualityMode       *int `json:"qualityMode"`
	UpgradeLevel      *int `json:"upgradeLevel"`
	AmplifyType       *int `json:"amplifyType"`
	ForgingLevel      *int `json:"forgingLevel"`
	AvatarOptionValue *int `json:"avatarOptionValue"`
	ExpirationDays    *int `json:"expirationDays"`
}

type itemcfgConfigureRequest struct {
	ListType *int                   `json:"listType"`
	Slot     *int                   `json:"slot"`
	Options  *itemcfgOptionsRequest `json:"options"`
}

// itemcfgTarget 是一次请求归一化后的落点。
type itemcfgTarget struct {
	ListType int
	Slot     int
	Key      string
	Field    dnfrepo.InventoryField
}

// itemcfgRejection 是"拒绝、零写入"的载体：reason 进具名日志，err 进响应体。
// 形态与 api_items_delete.go:221-240 的 itemRemoveRejection、
// api_quest_mutations.go 的 questMutationRejection 一致。
type itemcfgRejection struct {
	reason string
	err    error
}

func (e *itemcfgRejection) Error() string {
	message := strings.TrimSpace(e.err.Error())
	if strings.HasSuffix(message, "。") || strings.HasSuffix(message, "）") {
		return message
	}
	return message + "（未做任何写入）。"
}

func (e *itemcfgRejection) Unwrap() error { return e.err }

func itemcfgReject(reason string, err error) error {
	return &itemcfgRejection{reason: reason, err: err}
}

// itemcfgReadErrorMessage 是**只读**路由（GET config-options）的文案出口：
// 拒绝文案不带"（未做任何写入）"——那条注解是写给写接口的，读接口本来就不写。
// reason 与日志口径完全不变（logItemcfgRejection 仍按 *itemcfgRejection 归类）。
func itemcfgReadErrorMessage(err error) string {
	var rejection *itemcfgRejection
	if errors.As(err, &rejection) {
		return rejection.err.Error()
	}
	return err.Error()
}

// itemcfgDecodeConfigureBody 严格解码 configure 请求体（未知字段一律拒绝，
// 与上游 DTO 只认这几个字段的事实一致；旧页面缓存发别的字段名时，点名报错比
// "静默只改了一部分"好）。
func itemcfgDecodeConfigureBody(w http.ResponseWriter, r *http.Request) (itemcfgConfigureRequest, bool) {
	var payload itemcfgConfigureRequest
	body := http.MaxBytesReader(w, r.Body, maxBodyBytes)
	defer func() { _ = r.Body.Close() }()
	raw, err := io.ReadAll(body)
	if err != nil {
		writeFailure(w, "请求读取失败")
		return payload, false
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return payload, true
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		writeFailure(w, fmt.Sprintf(
			"请求格式不正确（本接口只接受 listType / slot / options，options 只接受 %s）：%s",
			strings.Join(itemcfgOptionKeys, " / "), err.Error()))
		return payload, false
	}
	return payload, true
}

// itemcfgResolveTarget 把 (listType, slot) 归一成槽位键与要保存的字段。
// 拒绝都发生在触碰仓储之前。
func itemcfgResolveTarget(listTypeValue, slotValue int) (itemcfgTarget, error) {
	if listTypeValue < 0 || listTypeValue > 255 {
		return itemcfgTarget{}, itemcfgReject("list_type_out_of_range",
			fmt.Errorf("%w: listType=%d 必须在 0..255", itemcfgErrListTypeOutOfRange, listTypeValue))
	}
	if slotValue < 0 || slotValue > math.MaxInt16 {
		return itemcfgTarget{}, itemcfgReject("slot_out_of_range",
			fmt.Errorf("%w: slot=%d 必须在 0..%d", itemcfgErrSlotOutOfRange, slotValue, math.MaxInt16))
	}
	listType := byte(listTypeValue)
	slot := int16(slotValue)

	field := dnfrepo.InventoryFieldSlots
	switch listType {
	case listTypeMain, listTypeAvatar, listTypePet, listTypeGuildMedal:
		field = dnfrepo.InventoryFieldSlots
	case listTypePersonalCargo:
		field = dnfrepo.InventoryFieldWarehouse
	case listTypeEquipment:
		return itemcfgTarget{}, itemcfgReject("worn_equipment", itemcfgErrWornEquipment)
	case listTypeAccountCargo:
		return itemcfgTarget{}, itemcfgReject("account_cargo_container", itemcfgErrAccountCargoSlot)
	default:
		return itemcfgTarget{}, itemcfgReject("container_unsupported",
			fmt.Errorf("%w: listType=%d（本接口只支持 0/1/2/7/38）", itemcfgErrContainerUnsupported, listTypeValue))
	}
	if listType == listTypeMain {
		if slot >= 0 && slot <= 2 {
			return itemcfgTarget{}, itemcfgReject("wallet_slot", itemcfgErrWalletSlot)
		}
		if isAccountSharedItemSlot(listType, slot) {
			return itemcfgTarget{}, itemcfgReject("account_shared_slot", itemcfgErrAccountSharedSlot)
		}
	}
	return itemcfgTarget{
		ListType: int(listType),
		Slot:     int(slot),
		Key:      itemSlotKey(listType, slot),
		Field:    field,
	}, nil
}

// itemcfgEntriesOf 按 target 选出记录里真正承载该容器的 map（不新建 map：
// 只有确认槽位存在之后才会写入）。
func itemcfgEntriesOf(record dnfrepo.InventoryRecord, target itemcfgTarget) map[string]dnfrepo.ItemStack {
	if target.Field == dnfrepo.InventoryFieldWarehouse {
		return record.Warehouse
	}
	return record.Slots
}

// itemcfgEntry 按物品 id 取 PVF 索引条目；索引没建好/查不到都明确失败
// （**绝不**凭空造一个条目出来）。
func (s *Server) itemcfgEntry(itemID int64) (itemEntry, error) {
	if itemID <= 0 || itemID > math.MaxUint32 {
		return itemEntry{}, itemcfgReject("item_id_invalid",
			fmt.Errorf("%w: %d", itemcfgErrItemIDInvalid, itemID))
	}
	if err := s.items.failure(); err != nil {
		return itemEntry{}, itemcfgReject("item_index_failed",
			fmt.Errorf("%w: %s", itemcfgErrItemIndexFailed, err.Error()))
	}
	if !s.items.isReady() {
		return itemEntry{}, itemcfgReject("item_index_building", itemcfgErrItemIndexBuilding)
	}
	entry, found := s.items.lookup(uint32(itemID))
	if !found {
		return itemEntry{}, itemcfgReject("item_unknown",
			fmt.Errorf("%w: 物品 ID %d 在 PVF 中不存在(装备/堆叠表都没有)", itemcfgErrItemUnknown, itemID))
	}
	return entry, nil
}

// itemcfgIsAvatar 与 api_items.go:609-619 的读面同一判据：索引里装扮的
// kind 是 equipment，靠 tag 里的 avatar 分叉（firstItemTag 已归一化成小写 token）。
func itemcfgIsAvatar(entry itemEntry) bool {
	if strings.EqualFold(strings.TrimSpace(entry.Kind), "avatar") {
		return true
	}
	return strings.Contains(strings.ToLower(entry.Tag), "avatar")
}

// ---------------------------------------------------------------------------
// GET /api/characters/{id}/items/config-options
// ---------------------------------------------------------------------------

func (s *Server) handleItemConfigOptions(w http.ResponseWriter, r *http.Request) {
	ctx, group, panelID, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	listTypeValue := queryInt(r, "listType", -1)
	if listTypeValue < 0 {
		writeFailure(w, itemcfgErrListTypeMissing.Error()+
			"：本接口要 ?listType={listType}&slot={slot}，且 listType 必须是 0..255 的十进制整数。")
		return
	}
	slotValue := queryInt(r, "slot", -1)
	if slotValue < 0 {
		writeFailure(w, itemcfgErrSlotMissing.Error()+
			"：本接口要 ?listType={listType}&slot={slot}，且 slot 必须是 0..32767 的十进制整数。")
		return
	}
	target, err := itemcfgResolveTarget(listTypeValue, slotValue)
	if err != nil {
		s.logItemcfgRejection(panelID, character, "config-options", itemcfgTarget{
			ListType: listTypeValue, Slot: slotValue,
		}, 0, err)
		writeFailure(w, itemcfgReadErrorMessage(err))
		return
	}
	if group.Inventory == nil {
		writeFailure(w, itemcfgErrInventoryMissing.Error()+"：背包仓储未就绪。")
		return
	}
	record, found, loadErr := group.Inventory.Load(ctx, character.CharacterID)
	if loadErr != nil {
		writeFailure(w, "读取角色背包失败: "+loadErr.Error())
		return
	}
	if !found {
		writeFailure(w, itemcfgErrInventoryMissing.Error()+"。")
		return
	}
	stack, exists := itemcfgEntriesOf(record, target)[target.Key]
	if !exists || stack.ItemID <= 0 || stack.Count <= 0 {
		s.logItemcfgRejection(panelID, character, "config-options", target, 0,
			itemcfgReject("slot_not_found", itemcfgErrSlotEmpty))
		writeFailure(w, fmt.Sprintf("%s：%s。", itemcfgErrSlotEmpty.Error(), target.Key))
		return
	}
	entry, entryErr := s.itemcfgEntry(stack.ItemID)
	if entryErr != nil {
		s.logItemcfgRejection(panelID, character, "config-options", target, stack.ItemID, entryErr)
		writeFailure(w, itemcfgReadErrorMessage(entryErr))
		return
	}
	now := time.Now().UTC()
	capability, capErr := s.itemcfgBuildCapability(entry, stack, now)
	if capErr != nil {
		s.logItemcfgRejection(panelID, character, "config-options", target, stack.ItemID, capErr)
		writeFailure(w, itemcfgReadErrorMessage(capErr))
		return
	}
	payload := map[string]any{
		"characterId":    panelIDOfCharacter(character),
		"itemTemplateId": entry.ID,
		"name":           entry.Name,
		"kind":           entry.Kind,
		"listType":       target.ListType,
		"slot":           target.Slot,
		"key":            target.Key,
		"equipment":      nil,
		"avatar":         nil,
		"expiration":     nil,
	}
	for key, value := range capability {
		payload[key] = value
	}
	writeOK(w, payload)
}

// ---------------------------------------------------------------------------
// POST /api/characters/{id}/items/configure
// ---------------------------------------------------------------------------

// itemcfgApplied 是一次配置真正落地的结果（进响应、事件与测试断言）。
type itemcfgApplied struct {
	Stack        dnfrepo.ItemStack
	Kind         string
	Applied      []string
	QualitySeed  uint32
	UpgradeLevel int
	AmplifyType  int
	AmplifyValue int
	ForgingLevel int
	ExpireTime   int64
	CanUpgrade   bool
	CanAmplify   bool
	CanForge     bool
}

func (s *Server) handleItemConfigure(w http.ResponseWriter, r *http.Request) {
	ctx, group, panelID, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	body, ok := itemcfgDecodeConfigureBody(w, r)
	if !ok {
		return
	}
	if body.ListType == nil {
		s.logItemcfgRejection(panelID, character, "configure", itemcfgTarget{}, 0,
			itemcfgReject("missing_list_type", itemcfgErrListTypeMissing))
		writeFailure(w, itemcfgErrListTypeMissing.Error()+"（未做任何写入）。")
		return
	}
	if body.Slot == nil {
		s.logItemcfgRejection(panelID, character, "configure", itemcfgTarget{}, 0,
			itemcfgReject("missing_slot", itemcfgErrSlotMissing))
		writeFailure(w, itemcfgErrSlotMissing.Error()+"（未做任何写入）。")
		return
	}
	target, err := itemcfgResolveTarget(*body.ListType, *body.Slot)
	if err != nil {
		s.logItemcfgRejection(panelID, character, "configure", itemcfgTarget{
			ListType: *body.ListType, Slot: *body.Slot,
		}, 0, err)
		writeFailure(w, err.Error())
		return
	}
	// 顺序与 P2/P3/P4 的新写路由一致：解析角色 → 校验请求体 → 守卫 → 事务。
	if !s.requireOffline(w, "item-configure", panelID) {
		return
	}
	if group.CharacterItems == nil {
		s.logItemcfgRejection(panelID, character, "configure", target, 0,
			itemcfgReject("character_items_transaction_unavailable", itemcfgErrTransactionUnavailable))
		writeFailure(w, itemcfgErrTransactionUnavailable.Error())
		return
	}

	var applied itemcfgApplied
	var entry itemEntry
	err = group.CharacterItems.WithinCharacterItems(ctx, character.CharacterID, func(
		inventories dnfrepo.InventoryRepository,
		_ dnfrepo.EquipmentRepository,
	) error {
		record, exists, loadErr := inventories.Load(ctx, character.CharacterID)
		if loadErr != nil {
			return loadErr
		}
		if !exists {
			return itemcfgReject("inventory_record_missing", itemcfgErrInventoryMissing)
		}
		record = dnfrepo.CloneInventory(record)
		record.CharacterID = character.CharacterID
		entries := itemcfgEntriesOf(record, target)
		stack, stackFound := entries[target.Key]
		if !stackFound || stack.ItemID <= 0 || stack.Count <= 0 {
			return itemcfgReject("slot_not_found",
				fmt.Errorf("%w：%s", itemcfgErrSlotEmpty, target.Key))
		}
		resolved, entryErr := s.itemcfgEntry(stack.ItemID)
		if entryErr != nil {
			return entryErr
		}
		entry = resolved
		applied, entryErr = s.itemcfgApplyOptions(target, resolved, stack, body.Options, time.Now().UTC())
		if entryErr != nil {
			return entryErr
		}
		entries[target.Key] = applied.Stack
		record.UpdatedAt = time.Now().UTC()
		// 单一写入点：只保存承载该容器的那一个字段（与 api_items_delete.go:609-610 同款）。
		return dnfrepo.SaveInventoryFields(ctx, inventories, record, target.Field)
	})
	if err != nil {
		var rejection *itemcfgRejection
		if errors.As(err, &rejection) {
			s.logItemcfgRejection(panelID, character, "configure", target, int64(entry.ID), rejection)
			writeFailure(w, rejection.Error())
			return
		}
		s.logEvent("gm-item-configure-failed",
			"panel_id", panelID,
			"character_id", character.CharacterID,
			"account_id", character.AccountID,
			"list_type", target.ListType,
			"slot", target.Slot,
			"key", target.Key,
			"reason", "write_failed",
			"error", err.Error(),
			"result", "failed")
		writeFailure(w, "写入物品配置失败: "+err.Error())
		return
	}

	s.logEvent("gm-item-configure",
		"panel_id", panelID,
		"character_id", character.CharacterID,
		"account_id", character.AccountID,
		"list_type", target.ListType,
		"slot", target.Slot,
		"key", target.Key,
		"item_id", entry.ID,
		"name", entry.Name,
		"type", applied.Kind,
		"applied", strings.Join(applied.Applied, ","),
		"quality_seed", applied.QualitySeed,
		"upgrade_level", applied.UpgradeLevel,
		"amplify_type", applied.AmplifyType,
		"amplify_value", applied.AmplifyValue,
		"forging_level", applied.ForgingLevel,
		"forging_row_offset", itemcfgForgingRowOffset,
		"expire_time", applied.ExpireTime,
		"result", "applied")
	writeOK(w, map[string]any{
		"characterId":      panelIDOfCharacter(character),
		"listType":         target.ListType,
		"slot":             target.Slot,
		"key":              target.Key,
		"container":        containerLabel(target.ListType),
		"itemTemplateId":   entry.ID,
		"name":             entry.Name,
		"kind":             entry.Kind,
		"type":             applied.Kind,
		"applied":          applied.Applied,
		"qualitySeed":      applied.QualitySeed,
		"upgradeLevel":     applied.UpgradeLevel,
		"amplifyType":      applied.AmplifyType,
		"amplifyValue":     applied.AmplifyValue,
		"forgingLevel":     applied.ForgingLevel,
		"forgingRowOffset": itemcfgForgingRowOffset,
		"expireTime":       applied.ExpireTime,
		"canUpgrade":       applied.CanUpgrade,
		"canAmplify":       applied.CanAmplify,
		"canForge":         applied.CanForge,
		"notification":     "inventory_row_updated",
		"hint": "配置已直接写入该角色背包行（与游戏同一批 Extra 键）。" +
			"若角色正在线上，客户端仍显示旧行；建议在面板开关 DNFBRIDGE_GM_REQUIRE_OFFLINE=on 下操作，或让玩家重开背包/重新登录。",
	})
}

// itemcfgApplyOptions 是"改哪些字段"的唯一实现：先算能力块，再按 options 逐项校验并写入。
// 返回的错误一律是 *itemcfgRejection（拒绝 ⇒ 零写入）。
func (s *Server) itemcfgApplyOptions(
	target itemcfgTarget,
	entry itemEntry,
	stack dnfrepo.ItemStack,
	options *itemcfgOptionsRequest,
	now time.Time,
) (itemcfgApplied, error) {
	if options == nil {
		return itemcfgApplied{}, itemcfgReject("missing_options", itemcfgErrOptionsMissing)
	}
	result := itemcfgApplied{Stack: stack, Kind: "expiration"}

	// 装扮属性：见 ITEMCFG-D4。
	if options.AvatarOptionValue != nil {
		return itemcfgApplied{}, itemcfgReject("avatar_option_unsupported", itemcfgErrAvatarOptionUnsupported)
	}

	// 期限（上游 :43-50 的 wantsExpiration，对任意道具都成立，位于装备分支之前）。
	if options.ExpirationDays != nil {
		days := *options.ExpirationDays
		if days < 1 || days > itemcfgMaxExpireDays {
			return itemcfgApplied{}, itemcfgReject("expiration_days_out_of_range", fmt.Errorf(
				"%w: expirationDays=%d 必须在 1..%d", itemcfgErrExpirationDaysInvalid, days, itemcfgMaxExpireDays))
		}
		if s.itemcfgExpirationCapability(entry, stack, now) == nil {
			return itemcfgApplied{}, itemcfgReject("expiration_unsupported", itemcfgErrExpirationUnsupported)
		}
		// 与发放侧同一个"GM 期限覆写"实现（api_items_grant_options.go:369-403），
		// 绝不在这里另写一套到期时间/记账键。
		updated, _ := applyCurrentGMExpirationOverrideAt(
			stack, now.Add(time.Duration(days)*24*time.Hour), days, now)
		stack = updated
		result.Stack = stack
		result.ExpireTime = unixOrZero(stack.ExpireAt)
		result.Applied = append(result.Applied, "expirationDays")
	}

	wantsEquipmentFields := options.QualityMode != nil || options.UpgradeLevel != nil ||
		options.AmplifyType != nil || options.ForgingLevel != nil
	if !wantsEquipmentFields {
		if len(result.Applied) == 0 {
			return itemcfgApplied{}, itemcfgReject("no_options", itemcfgErrNoOptions)
		}
		result.Stack = itemcfgStampAuditKey(stack, now)
		result.ExpireTime = unixOrZero(result.Stack.ExpireAt)
		return result, nil
	}

	// 锻造：见 ITEMCFG-D3。
	if itemcfgIsAvatar(entry) {
		return itemcfgApplied{}, itemcfgReject("avatar_equipment_fields", itemcfgErrAvatarStackFieldsRejected)
	}
	capability, capErr := s.itemcfgEquipmentCapability(entry, stack)
	if capErr != nil {
		return itemcfgApplied{}, capErr
	}
	if capability == nil {
		return itemcfgApplied{}, itemcfgReject("equipment_not_configurable", itemcfgErrEquipmentNotConfigurable)
	}
	if entry.Kind == grantItemKindEquipment && stack.Count != 1 {
		return itemcfgApplied{}, itemcfgReject("equipment_stack_not_single", fmt.Errorf(
			"%w（当前 %d）", itemcfgErrEquipmentStack, stack.Count))
	}
	result.Kind = "equipment"
	result.CanUpgrade = itemcfgBool(capability["canUpgrade"])
	result.CanAmplify = itemcfgBool(capability["canAmplify"])
	result.CanForge = itemcfgBool(capability["canForge"])
	result.UpgradeLevel = itemcfgUpgradeLevelOf(stack)
	result.AmplifyType = itemcfgAmplifyTypeOf(stack)
	result.AmplifyValue = itemcfgAmplifyValueOf(stack)
	result.ForgingLevel = itemcfgForgingLevelOf(stack)

	if options.QualityMode != nil {
		mode := *options.QualityMode
		var seed uint32
		switch mode {
		case itemcfgQualityRandom:
			generated, genErr := itemquality.NewRandomSeed()
			if genErr != nil {
				return itemcfgApplied{}, itemcfgReject("quality_seed_generation_failed",
					fmt.Errorf("%w: %s", itemcfgErrQualityWriteFailed, genErr.Error()))
			}
			seed = generated
		case itemcfgQualityTop:
			seed = itemquality.TopSeed
		default:
			return itemcfgApplied{}, itemcfgReject("quality_mode_invalid", fmt.Errorf(
				"%w: qualityMode=%d（只接受 0=随机品级 / 1=100%% 最上级）", itemcfgErrQualityModeInvalid, mode))
		}
		// 品级种子的唯一实现（modules/dnf/itemquality），与游戏内品级调整箱同一条。
		if err := itemquality.Apply(&stack, seed); err != nil {
			return itemcfgApplied{}, itemcfgReject("quality_write_failed",
				fmt.Errorf("%w: %s", itemcfgErrQualityWriteFailed, err.Error()))
		}
		result.QualitySeed = seed
		result.Applied = append(result.Applied, "qualityMode")
	}

	if options.UpgradeLevel != nil {
		level := *options.UpgradeLevel
		if level < 0 || level > itemcfgMaxUpgradeLevel {
			return itemcfgApplied{}, itemcfgReject("upgrade_level_out_of_range", fmt.Errorf(
				"%w: upgradeLevel=%d 必须在 0..%d", itemcfgErrUpgradeLevelOutOfRange, level, itemcfgMaxUpgradeLevel))
		}
		if level > 0 && !result.CanUpgrade {
			return itemcfgApplied{}, itemcfgReject("upgrade_unsupported",
				fmt.Errorf("%w（物品 %d，类型 %s）", itemcfgErrUpgradeUnsupported, entry.ID, entry.Tag))
		}
		// 逐字移植游戏自己的写入器（inventory/owner.go:2606-2622 setUpgradeLevel）。
		itemcfgSetUpgradeLevel(&stack, byte(level))
		result.UpgradeLevel = level
		result.Applied = append(result.Applied, "upgradeLevel")
	}

	if options.AmplifyType != nil {
		amplifyType := *options.AmplifyType
		if amplifyType < 0 || amplifyType > itemcfgMaxAmplifyType {
			return itemcfgApplied{}, itemcfgReject("amplify_type_out_of_range", fmt.Errorf(
				"%w: amplifyType=%d 必须在 0..%d", itemcfgErrAmplifyTypeOutOfRange, amplifyType, itemcfgMaxAmplifyType))
		}
		if amplifyType > 0 && !result.CanAmplify {
			return itemcfgApplied{}, itemcfgReject("amplify_unsupported", fmt.Errorf(
				"%w（需要 %d 级及以上、稀有度 %d 及以上的可强化装备；本件 %d 级 / 稀有度 %d）",
				itemcfgErrAmplifyUnsupported, s.itemcfgAmplifyEquipLevelConst(), itemcfgMinAmplifyRarity,
				entry.MinLevel, entry.Rarity))
		}
		value := 0
		if amplifyType > 0 {
			table, tableErr := s.itemcfgAmplifyTable()
			if tableErr != nil {
				return itemcfgApplied{}, itemcfgReject("amplify_table_unavailable",
					fmt.Errorf("%w: %s", itemcfgErrAmplifyTableUnavailable, tableErr.Error()))
			}
			resolved, valueErr := table.initialValue(entry.Rarity, amplifyType)
			if valueErr != nil {
				return itemcfgApplied{}, itemcfgReject("amplify_value_unresolved",
					fmt.Errorf("%w: %s", itemcfgErrAmplifyValueUnresolvable, valueErr.Error()))
			}
			value = int(resolved)
			// ITEMCFG-D2：类型没变时沿用已经存在的红字数值（不把玩家洗好的数值抹回初值）。
			if amplifyType == result.AmplifyType && result.AmplifyValue > 0 {
				value = result.AmplifyValue
			}
		}
		// 逐字移植游戏自己的写入器（inventory/owner_amplify.go:288-309 setAmplifyState）。
		itemcfgSetAmplifyState(&stack, byte(amplifyType), uint16(value))
		result.AmplifyType = amplifyType
		result.AmplifyValue = value
		result.Applied = append(result.Applied, "amplifyType")
	}

	if options.ForgingLevel != nil {
		level := *options.ForgingLevel
		// 前端在装备分支**每次都发 forgingLevel**（不可锻造时那个输入框根本不渲染，
		// parseInt(undefined || '0') = 0），所以"本来就等于当前值"时什么都不做：
		// 不改 Extra、不碰行内字节，也不把 forgingLevel 记成一次改动。
		if level != result.ForgingLevel {
			// 上限来自 PVF（etc/upgrade_separate.etc 的 [separate upgrade max]），
			// 读不到就拒绝 —— 绝不拿一个写死的数字当上限。
			forgingTable, forgingErr := s.itemcfgSeparateUpgradeTable()
			if forgingErr != nil {
				return itemcfgApplied{}, itemcfgReject("forging_table_unavailable",
					fmt.Errorf("%w: %s", itemcfgErrForgingTableUnavailable, forgingErr.Error()))
			}
			if level < 0 || level > forgingTable.maxLevel {
				return itemcfgApplied{}, itemcfgReject("forging_level_out_of_range", fmt.Errorf(
					"%w: forgingLevel=%d 必须在 0..%d（%s 的 [separate upgrade max]）",
					itemcfgErrForgingLevelOutOfRange, level, forgingTable.maxLevel, itemcfgSeparateUpgradePVFPath))
			}
			if level > 0 && !itemcfgIsForgeableWeapon(entry) {
				return itemcfgApplied{}, itemcfgReject("forging_not_weapon", fmt.Errorf(
					"%w（物品 %d 的类型是 %s）", itemcfgErrForgingNotWeapon, entry.ID, entry.Tag))
			}
			// 唯一写点：Extra 两个别名 + 0x77 行的 0x57（见 itemcfgForgingRowOffset 的证据链）。
			itemcfgSetForgingLevel(&stack, byte(level))
			result.ForgingLevel = level
			result.Applied = append(result.Applied, "forgingLevel")
		}
	}

	if len(result.Applied) == 0 {
		return itemcfgApplied{}, itemcfgReject("no_options", itemcfgErrNoOptions)
	}
	result.Stack = itemcfgStampAuditKey(stack, now)
	// 响应里的当前值一律**从最终那一行读回来**（用游戏自己的读器），而不是回显
	// "我打算写什么"：没被本次改动的字段也如实回报，操作者看到的就是库里真实的样子。
	result.QualitySeed = itemcfgQualitySeed(result.Stack, entry)
	result.UpgradeLevel = itemcfgUpgradeLevelOf(result.Stack)
	result.AmplifyType = itemcfgAmplifyTypeOf(result.Stack)
	result.AmplifyValue = itemcfgAmplifyValueOf(result.Stack)
	result.ForgingLevel = itemcfgForgingLevelOf(result.Stack)
	result.ExpireTime = unixOrZero(result.Stack.ExpireAt)
	return result, nil
}

// itemcfgStampAuditKey 记一个"这次是 GM 改的"的审计键（Inert：不在
// repository.ItemStackIdentityExtraKeys 里，不参与堆叠合并判定；读侧没有任何代码
// 按它分叉）。与 modules/dnf/itemgrade/owner.go:154 的 last_grade_adjust 同一手法。
func itemcfgStampAuditKey(stack dnfrepo.ItemStack, now time.Time) dnfrepo.ItemStack {
	if stack.Extra == nil {
		stack.Extra = make(map[string]string, 2)
	}
	stack.Extra["last_gm_configure"] = now.Format(time.RFC3339)
	return stack
}

// ---------------------------------------------------------------------------
// 能力块
// ---------------------------------------------------------------------------

// itemcfgBuildCapability 对应 vendor BuildInventoryItemConfigOptions（:118-245）的分支顺序：
// 装扮 → 装备 → 只读期限。返回的 map 直接并进响应。
func (s *Server) itemcfgBuildCapability(entry itemEntry, stack dnfrepo.ItemStack, now time.Time) (map[string]any, error) {
	expiration := s.itemcfgExpirationCapability(entry, stack, now)
	if itemcfgIsAvatar(entry) {
		if expiration == nil {
			return nil, itemcfgReject("avatar_options_unavailable", itemcfgErrAvatarOptionsUnavailable)
		}
		return map[string]any{
			"type":       "expiration",
			"avatar":     nil,
			"equipment":  nil,
			"expiration": expiration,
		}, nil
	}
	equipment, capErr := s.itemcfgEquipmentCapability(entry, stack)
	if capErr != nil {
		return nil, capErr
	}
	if equipment == nil {
		if expiration == nil {
			return nil, itemcfgReject("no_configurable_fields", itemcfgErrNoConfigurableFields)
		}
		return map[string]any{
			"type":       "expiration",
			"avatar":     nil,
			"equipment":  nil,
			"expiration": expiration,
		}, nil
	}
	return map[string]any{
		"type":       "equipment",
		"avatar":     nil,
		"equipment":  equipment,
		"expiration": expiration,
	}, nil
}

// itemcfgEquipmentCapability 是 vendor EquipmentGrantPolicy.Describe（ItemGrantOptions.cs:42-69）
// 在本仓数据上的落地：
//
//	canUpgrade = 索引条目是装备 + 类型在 api_items_grant_options.go:146-158 的同一张支持表里
//	canAmplify = canUpgrade + 等级 ≥ [equip level const] + 稀有度 ≥ 2 + 该稀有度在
//	             etc/amplifyitem.etc 里有正权重（否则连初值都算不出来，必须如实报 false）
//	canForge   = 武器（vendor InventorySeparateUpgradeService.cs:70-74 的 IsWeapon 门禁）
//	             + PVF etc/upgrade_separate.etc 可读；maxForgingLevel 取它的
//	             [separate upgrade max]（真档 8），读不到就 canForge=false + 上限 0
//
// 返回 nil（而不是错误）表示"这不是可配置装备"，由调用方决定是退到期限还是拒绝。
func (s *Server) itemcfgEquipmentCapability(entry itemEntry, stack dnfrepo.ItemStack) (map[string]any, error) {
	if entry.Kind != grantItemKindEquipment || !gmEntrySupportsEquipmentAttributes(entry.Tag) {
		return nil, nil
	}
	canAmplify := false
	table, tableErr := s.itemcfgAmplifyTable()
	if tableErr != nil {
		// 读不到红字表 ⇒ 我们既算不出初值，也说不清等级门槛。如实报 canAmplify=false
		// （前端于是不渲染红字下拉），并记一条具名日志（不阻断整个能力块）。
		s.logEvent("gm-item-configure-capability-degraded",
			"item_id", entry.ID, "path", itemcfgAmplifyItemPVFPath,
			"reason", "amplify_table_unavailable", "error", tableErr.Error())
	} else {
		canAmplify = int64(entry.MinLevel) >= table.equipLevelConst &&
			entry.Rarity >= itemcfgMinAmplifyRarity &&
			table.hasInitialValues(entry.Rarity)
	}
	// 锻造（separate upgrade）：只有武器可锻；上限取 PVF 的 [separate upgrade max]。
	// 读不到表 ⇒ 如实报 canForge=false + maxForgingLevel=0（前端于是不渲染锻造输入框），
	// 并记一条降级日志；绝不写一个我们说不出上限的字段。
	canForge := false
	maxForging := 0
	if forgingTable, forgingErr := s.itemcfgSeparateUpgradeTable(); forgingErr != nil {
		s.logEvent("gm-item-configure-capability-degraded",
			"item_id", entry.ID, "path", itemcfgSeparateUpgradePVFPath,
			"reason", "forging_table_unavailable", "error", forgingErr.Error())
	} else if itemcfgIsForgeableWeapon(entry) {
		canForge = true
		maxForging = forgingTable.maxLevel
	}
	seed := itemcfgQualitySeed(stack, entry)
	return map[string]any{
		"type":                entry.Tag,
		"rarity":              entry.Rarity,
		"minimumLevel":        entry.MinLevel,
		"canUpgrade":          true,
		"canAmplify":          canAmplify,
		"canForge":            canForge,
		"maxUpgradeLevel":     itemcfgMaxUpgradeLevel,
		"maxForgingLevel":     maxForging,
		"currentQualityMode":  itemcfgQualityMode(seed),
		"currentQualitySeed":  seed,
		"currentUpgradeLevel": itemcfgUpgradeLevelOf(stack),
		"currentAmplifyType":  itemcfgAmplifyTypeOf(stack),
		"currentForgingLevel": itemcfgForgingLevelOf(stack),
		"forgingPVFPath":      itemcfgSeparateUpgradePVFPath,
		"qualityOptions":      itemcfgQualityOptions(),
		"amplifyTypes":        itemcfgAmplifyTypeOptions(),
		"expirationDaysMax":   itemcfgMaxExpireDays,
	}, nil
}

// itemcfgExpirationCapability 对应 vendor BuildInventoryExpirationConfig（:276-315）：
// 只有"受限"的物品才给期限控件；返回 nil 表示没有可配置的期限。
//
// 本仓的受限判据（与 api_items.go:646-652 的读面同一份数据）：
// 实例已到期时间（stack.ExpireAt）> 0，或 PVF 模板声明了相对期限 / 固定截止。
// `dailyDeleteItem`（每日 06:00 删除）在本仓索引里恒为 false
// （api_items.go:650 的既有行为），所以这里不复制上游那条分支 ——
// 没有数据就不假装有。
func (s *Server) itemcfgExpirationCapability(entry itemEntry, stack dnfrepo.ItemStack, now time.Time) map[string]any {
	instance := unixOrZero(stack.ExpireAt)
	if instance <= 0 && entry.UsablePeriodDays <= 0 && entry.AbsoluteExpireUnix <= 0 {
		return nil
	}
	remaining := int64(itemcfgExpirationRemainingFallbackDays)
	if instance > 0 && instance > now.Unix() {
		remaining = int64(math.Ceil(float64(instance-now.Unix()) / 86400.0))
	}
	if remaining < 1 {
		remaining = 1
	}
	defaultDays := remaining
	if defaultDays > itemcfgMaxExpireDays {
		defaultDays = itemcfgMaxExpireDays
	}
	return map[string]any{
		"canOverride":          true,
		"currentExpireTime":    instance,
		"currentRemainingDays": remaining,
		"maxDays":              itemcfgMaxExpireDays,
		"defaultDays":          defaultDays,
		// 90CN 没有"装扮期限档位表"（vendor 的 AvatarDurationResolver 是 PVF 侧的独立解析器），
		// 所以 durations 恒为 null：前端于是走"自定义天数"输入框那条分支
		// （inventory.js:316-320），而不是一个我们编出来的档位下拉。
		"durations": nil,
	}
}

// ---------------------------------------------------------------------------
// 物品行的读（与游戏自己的读器同一批键）
// ---------------------------------------------------------------------------

// itemcfgQualitySeed 读品级种子：先 Extra["quality_seed"]（itemquality.Apply 写的键），
// 装备再回落到 0x77 行的 0x06..0x09（Apply 的第二写点）。堆叠行的 0x06..0x09 是数量，
// 所以那里的回落按 entry.Kind 关掉 —— 绝不把数量当品级。
func itemcfgQualitySeed(stack dnfrepo.ItemStack, entry itemEntry) uint32 {
	if seed := itemcfgExtraUint32(stack.Extra, "quality_seed"); seed > 0 {
		return seed
	}
	if entry.Kind == grantItemKindEquipment && len(stack.RawEntry) >= 10 {
		return binary.LittleEndian.Uint32(stack.RawEntry[6:10])
	}
	return 0
}

func itemcfgQualityMode(seed uint32) int {
	if seed == itemquality.TopSeed {
		return itemcfgQualityTop
	}
	return itemcfgQualityRandom
}

// itemcfgUpgradeLevelOf 逐字移植 inventory/owner.go:2580-2592 upgradeLevelOf：
// 打包字节的低 5 位是强化等级（0x1F），其次是 reinforce/upgrade_level 等 Extra 键，
// 最后才是 0x77 行的 0x0A。面板显示的等级因此与游戏读到的是同一个数。
func itemcfgUpgradeLevelOf(stack dnfrepo.ItemStack) int {
	if packed := itemcfgPackedUpgradeAttrOf(stack); packed != 0 {
		return int(packed & 0x1F)
	}
	for _, key := range []string{"reinforce", "upgrade_level", "enchant_upgrade", "enchant_upgrade_count"} {
		if value, ok := itemcfgExtraInt(stack.Extra, key); ok && value != 0 {
			return int(byte(value) & 0x1F)
		}
	}
	if len(stack.RawEntry) > 0x0A {
		return int(stack.RawEntry[0x0A] & 0x1F)
	}
	return 0
}

// itemcfgPackedUpgradeAttrOf 逐字移植 inventory/owner.go:2594-2604 packedUpgradeAttrOf。
func itemcfgPackedUpgradeAttrOf(stack dnfrepo.ItemStack) byte {
	if stack.Extra != nil {
		for _, key := range []string{"ext_data0", "ext0", "packed_flag_byte", "packed_flag", "packed"} {
			if value, ok := itemcfgExtraInt(stack.Extra, key); ok && value != 0 {
				return byte(value)
			}
		}
	}
	if len(stack.RawEntry) > 0x0A {
		return stack.RawEntry[0x0A]
	}
	return 0
}

// itemcfgSetUpgradeLevel 逐字移植 inventory/owner.go:2606-2622 setUpgradeLevel
// （打包字节高 3 位保留，写 Extra 四键 + 0x77 行的 0x0A）。
func itemcfgSetUpgradeLevel(stack *dnfrepo.ItemStack, level byte) {
	if stack == nil {
		return
	}
	if stack.Extra == nil {
		stack.Extra = make(map[string]string, 4)
	}
	packed := (itemcfgPackedUpgradeAttrOf(*stack) & 0xE0) | (level & 0x1F)
	stack.Extra["ext_data0"] = strconv.Itoa(int(packed))
	stack.Extra["packed_flag_byte"] = strconv.Itoa(int(packed))
	stack.Extra["reinforce"] = strconv.Itoa(int(level))
	stack.Extra["upgrade_level"] = strconv.Itoa(int(level))
	if len(stack.RawEntry) == itemcfgItemListEntrySize {
		stack.RawEntry = append([]byte(nil), stack.RawEntry...)
		stack.RawEntry[0x0A] = packed
	}
}

// itemcfgAmplifyTypeOf 是 inventory/owner.go:2653-2670 upgradeAmplifyState 的"类型"一半：
// Extra 五键 → 0x77 行 0x13。超过 4 的值按 vendor :200-202 的口径报 0（前端只认 0..4）。
//
// **有意未移植**同一函数末尾的 prefix_data_0e[5] 回落：那是历史行的十六进制前缀块，
// 本接口不读也不写它（见 ITEMCFG-D2 的说明）。
func itemcfgAmplifyTypeOf(stack dnfrepo.ItemStack) int {
	for _, key := range []string{"amplify_type", "amplification_type", "amplify_attr", "amplification_attr", "byte_13", "value_13", "value_c"} {
		if value, ok := itemcfgExtraInt(stack.Extra, key); ok {
			if value < 0 || value > itemcfgMaxAmplifyType {
				return 0
			}
			return int(value)
		}
	}
	if len(stack.RawEntry) > 0x13 {
		if value := stack.RawEntry[0x13]; value <= itemcfgMaxAmplifyType {
			return int(value)
		}
	}
	return 0
}

// itemcfgAmplifyValueOf 是同一个读器的"数值"一半（Extra 五键 → 0x77 行 0x14..0x15）。
func itemcfgAmplifyValueOf(stack dnfrepo.ItemStack) int {
	for _, key := range []string{"amplify_value", "amplification_value", "amplify_bonus", "amplification_bonus", "marker_16", "marker16", "value_d"} {
		if value, ok := itemcfgExtraInt(stack.Extra, key); ok && value > 0 {
			return int(value)
		}
	}
	if len(stack.RawEntry) > 0x15 {
		return int(stack.RawEntry[0x14]) | int(stack.RawEntry[0x15])<<8
	}
	return 0
}

// itemcfgIsForgeableWeapon 是"能不能锻造"的判据：只有武器。
// 依据：vendor InventorySeparateUpgradeService.cs:70-74（`!EquipmentTypeInfo.IsWeapon(...)`
// → ErrorNotWeapon）与 EquipmentType.cs 的 `IsWeapon(type) => type == EquipmentType.Weapon`
// （[weapon] → Weapon=12）。90CN 的索引把物品文档 [equipment type] 的第一个 `[tag]`
// 归一化成小写 token（items_index.go:321/326-332），武器的 token 就是 "weapon"。
func itemcfgIsForgeableWeapon(entry itemEntry) bool {
	return entry.Kind == grantItemKindEquipment && strings.EqualFold(strings.TrimSpace(entry.Tag), "weapon")
}

// itemcfgForgingLevelOf 读锻造等级：Extra 的两个别名优先（投影读的就是它们），
// 再回落到 0x77 行的 0x57。读不到就是 0（**不是**"未知"，客户端那一格本来就是 0）。
func itemcfgForgingLevelOf(stack dnfrepo.ItemStack) int {
	for _, key := range itemcfgForgingExtraKeys {
		if value, ok := itemcfgExtraInt(stack.Extra, key); ok && value >= 0 && value <= 255 {
			return int(value)
		}
	}
	if len(stack.RawEntry) > itemcfgForgingRowOffset {
		return int(stack.RawEntry[itemcfgForgingRowOffset])
	}
	return 0
}

// itemcfgSetForgingLevel 写锻造等级：Extra 两个别名 + 0x77 行的 0x57（写前复制 RawEntry，
// 与游戏自己的写入器同款 copy-on-write）。**只碰这一个字节**：0x58/0x59 是 A21 尾块里
// 另外两个字段（解封装备等级 / 交易限制），本接口无权替它们赋值。
func itemcfgSetForgingLevel(stack *dnfrepo.ItemStack, level byte) {
	if stack == nil {
		return
	}
	if stack.Extra == nil {
		stack.Extra = make(map[string]string, 2)
	}
	text := strconv.Itoa(int(level))
	for _, key := range itemcfgForgingExtraKeys {
		stack.Extra[key] = text
	}
	if len(stack.RawEntry) == itemcfgItemListEntrySize {
		stack.RawEntry = append([]byte(nil), stack.RawEntry...)
		stack.RawEntry[itemcfgForgingRowOffset] = level
	}
}

// itemcfgSeparateUpgradeTable 是 etc/upgrade_separate.etc 里本文件用到的两段。
type itemcfgSeparateUpgradeTable struct {
	// maxLevel 是 [separate upgrade max]（真档 8）。锻造上限的唯一来源。
	maxLevel int
	// levelCount 是 [table] 段里"4 个数一档"的档数（真档 10），用于交叉校验
	// maxLevel 不超过表长 —— 与 vendor SeparateUpgradeTableProvider.cs:12-25
	// 的 `Levels.Count < MaxLevel` 同一必要条件。
	levelCount int
}

// itemcfgSeparateUpgradeTable 读运行时 PVF 的 etc/upgrade_separate.etc。
//
// 为什么每次读而不缓存：与 itemcfgAmplifyTable 同一取舍（pvfCache 是既有文件、
// 不许改；包级缓存会让测试假 PVF 泄漏）。该文件只有 2.6 KB，且只在操作者手动触发的
// 两条路由、或武器能力块上读一次。
func (s *Server) itemcfgSeparateUpgradeTable() (itemcfgSeparateUpgradeTable, error) {
	if s == nil {
		return itemcfgSeparateUpgradeTable{}, errors.New("gm tool server is nil")
	}
	source, err := s.source()
	if err != nil {
		return itemcfgSeparateUpgradeTable{}, err
	}
	if source == nil {
		return itemcfgSeparateUpgradeTable{}, errors.New("runtime PVF source is unavailable")
	}
	text, err := source.ReadText(itemcfgSeparateUpgradePVFPath)
	if err != nil {
		return itemcfgSeparateUpgradeTable{}, fmt.Errorf("读取 %s 失败: %w", itemcfgSeparateUpgradePVFPath, err)
	}
	document, err := dnfpvf.Parse(itemcfgSeparateUpgradePVFPath, text)
	if err != nil {
		return itemcfgSeparateUpgradeTable{}, fmt.Errorf("解析 %s 失败: %w", itemcfgSeparateUpgradePVFPath, err)
	}
	return itemcfgParseSeparateUpgradeTable(document)
}

// itemcfgParseSeparateUpgradeTable 是本文件的纯解析部分（可单测）。
//
// 逐段对照 vendor SeparateUpgradeTable.cs:24-67：
//
//	"table"                  4 个数一档（TargetLevel = 序号+1、SuccessWeight = 第 3 个、
//	                         MaterialWeight = 第 4 个）；本文件只数档数
//	"separate upgrade max"   第 1 个数 = 锻造上限
//	"item weights by grade"  3 个数一组（档位、材料物品 id、基础数量）—— 与"设一个等级"
//	                         无关，不解析（那是锻造**消耗**的材料表）
//
// 校验（对应 vendor :12-25 的必要条件）：上限必须落在 1..255；若 [table] 可解析，
// 档数必须 ≥ 上限。任何一条不满足都明确失败 —— 调用方据此把 canForge 报 false。
func itemcfgParseSeparateUpgradeTable(document *dnfpvf.Document) (itemcfgSeparateUpgradeTable, error) {
	table := itemcfgSeparateUpgradeTable{}
	if document == nil {
		return table, errors.New("separate upgrade document is nil")
	}
	for _, section := range document.Sections {
		tokens := documentSectionTokens(document, section)
		switch itemcfgSectionKey(section.Name) {
		case "separate upgrade max":
			if len(tokens) > 0 {
				if value, ok := itemcfgTokenInteger(tokens[0]); ok && value > 0 {
					table.maxLevel = int(value)
				}
			}
		case "table":
			numbers := 0
			for _, token := range tokens {
				if _, ok := itemcfgTokenNumber(token); ok {
					numbers++
				}
			}
			if numbers > 0 && numbers%4 == 0 {
				table.levelCount = numbers / 4
			}
		}
	}
	if table.maxLevel <= 0 || table.maxLevel > 255 {
		return table, fmt.Errorf("%s 的 [separate upgrade max] 不是 1..255 的正整数（读到 %d）",
			itemcfgSeparateUpgradePVFPath, table.maxLevel)
	}
	if table.levelCount > 0 && table.levelCount < table.maxLevel {
		return table, fmt.Errorf("%s 的 [table] 只有 %d 档，小于 [separate upgrade max]=%d",
			itemcfgSeparateUpgradePVFPath, table.levelCount, table.maxLevel)
	}
	return table, nil
}

// itemcfgSetAmplifyState 逐字移植 inventory/owner_amplify.go:288-309 setAmplifyState。
func itemcfgSetAmplifyState(stack *dnfrepo.ItemStack, amplifyType byte, amplifyValue uint16) {
	if stack == nil {
		return
	}
	if stack.Extra == nil {
		stack.Extra = make(map[string]string, 10)
	}
	typeText := strconv.Itoa(int(amplifyType))
	valueText := strconv.Itoa(int(amplifyValue))
	for _, key := range []string{"amplify_type", "amplification_type", "byte_13", "value_13", "value_c"} {
		stack.Extra[key] = typeText
	}
	for _, key := range []string{"amplify_value", "amplification_value", "marker_16", "marker16", "value_d"} {
		stack.Extra[key] = valueText
	}
	if len(stack.RawEntry) == itemcfgItemListEntrySize {
		stack.RawEntry = append([]byte(nil), stack.RawEntry...)
		stack.RawEntry[0x13] = amplifyType
		stack.RawEntry[0x14] = byte(amplifyValue)
		stack.RawEntry[0x15] = byte(amplifyValue >> 8)
	}
}

// itemcfgExtraInt 与 inventory/body.go:553-565 firstExtraInt 同口径：
// 跳过空串、用 ParseInt(raw, 0, 64)（于是 0x.. 也认），返回第一个可解析的值。
func itemcfgExtraInt(extra map[string]string, key string) (int64, bool) {
	raw := strings.TrimSpace(extra[key])
	if raw == "" {
		return 0, false
	}
	value, err := strconv.ParseInt(raw, 0, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

// itemcfgExtraUint32 与 dnfbridge/scene_inventory.go:544-550 的 sceneInventoryExtraUint32
// 同口径：解析失败 / 越界都按 0。
func itemcfgExtraUint32(extra map[string]string, key string) uint32 {
	raw := strings.TrimSpace(extra[key])
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseUint(raw, 0, 32)
	if err != nil {
		return 0
	}
	return uint32(value)
}

func itemcfgBool(value any) bool {
	result, _ := value.(bool)
	return result
}

// ---------------------------------------------------------------------------
// etc/amplifyitem.etc（红字数值表）
// ---------------------------------------------------------------------------

// itemcfgAmplifyTable 是 etc/amplifyitem.etc 里本文件真正用到的三段。
type itemcfgAmplifyTable struct {
	equipLevelConst int64
	optionBase      map[int64]float64
	rarityWeight    map[int64]float64
}

// itemcfgAmplifyEquipLevelConst 只服务于"拒绝文案里报出门槛"：优先报运行时 PVF 的
// [equip level const]，读不到才回落到 vendor 的默认值 55。
func (s *Server) itemcfgAmplifyEquipLevelConst() int64 {
	if s == nil {
		return itemcfgAmplifyLevelConstFallback
	}
	table, err := s.itemcfgAmplifyTable()
	if err != nil || table.equipLevelConst <= 0 {
		return itemcfgAmplifyLevelConstFallback
	}
	return table.equipLevelConst
}

// itemcfgAmplifyTable 读运行时 PVF 的 etc/amplifyitem.etc。
//
// 为什么每次都读而不缓存：pvfCache（deps.go:32-63）是既有文件、本批不许改，而包级
// 全局缓存会让测试用的假 PVF 泄漏到别的用例里。该文件只有 4.4 KB、且只有操作者
// 手动触发的两条路由会读它，代价可以接受（也因此**不**在任何热路径上调用）。
func (s *Server) itemcfgAmplifyTable() (itemcfgAmplifyTable, error) {
	if s == nil {
		return itemcfgAmplifyTable{}, errors.New("gm tool server is nil")
	}
	source, err := s.source()
	if err != nil {
		return itemcfgAmplifyTable{}, err
	}
	return s.itemcfgAmplifyTableFromSource(source)
}

func (s *Server) itemcfgAmplifyTableFromSource(source dnfpvf.Source) (itemcfgAmplifyTable, error) {
	if source == nil {
		return itemcfgAmplifyTable{}, errors.New("runtime PVF source is unavailable")
	}
	text, err := source.ReadText(itemcfgAmplifyItemPVFPath)
	if err != nil {
		return itemcfgAmplifyTable{}, fmt.Errorf("读取 %s 失败: %w", itemcfgAmplifyItemPVFPath, err)
	}
	document, err := dnfpvf.Parse(itemcfgAmplifyItemPVFPath, text)
	if err != nil {
		return itemcfgAmplifyTable{}, fmt.Errorf("解析 %s 失败: %w", itemcfgAmplifyItemPVFPath, err)
	}
	return itemcfgParseAmplifyTable(document)
}

// itemcfgParseAmplifyTable 逐段移植 dnfbridge/amplify_item_pvf.go:78-159
// （parseCurrentAmplifyItemConfig）里本文件需要的四段：
//
//	[option mapping table]  选项名 → 编号 1..5
//	[option data]           每行 (选项名, 权重, 基础值)，基础值按**映射后的编号**记账
//	[rarity weight]         稀有度名 → 权重
//	[equip level const]     增幅等级门槛（真档 55）
//
// 校验条件与上游 :142-154 同一组必要条件（缺一个就明确失败，绝不填默认值）。
func itemcfgParseAmplifyTable(document *dnfpvf.Document) (itemcfgAmplifyTable, error) {
	table := itemcfgAmplifyTable{
		optionBase:   make(map[int64]float64, 6),
		rarityWeight: make(map[int64]float64, 7),
	}
	if document == nil {
		return table, errors.New("amplify item document is nil")
	}
	mapping := make(map[string]int64, 6)
	for _, section := range document.Sections {
		tokens := documentSectionTokens(document, section)
		switch itemcfgSectionKey(section.Name) {
		case "option mapping table":
			for index := 0; index+1 < len(tokens); index += 2 {
				value, ok := itemcfgTokenInteger(tokens[index+1])
				if !ok || value < 0 || value > 5 {
					continue
				}
				mapping[itemcfgNormalizeOptionName(tokens[index].Value)] = value
			}
		case "option data":
			for index := 0; index+2 < len(tokens); index += 3 {
				option := mapping[itemcfgNormalizeOptionName(tokens[index].Value)]
				base, ok := itemcfgTokenNumber(tokens[index+2])
				if option >= 1 && option <= 5 && ok && base > 0 {
					table.optionBase[option] = base
				}
			}
		case "rarity weight":
			for index := 0; index+1 < len(tokens); index += 2 {
				weight, ok := itemcfgTokenNumber(tokens[index+1])
				rarity, known := itemcfgRarityIndex(itemcfgNormalizeOptionName(tokens[index].Value))
				if known && ok && weight > 0 {
					table.rarityWeight[rarity] = weight
				}
			}
		case "equip level const":
			if len(tokens) > 0 {
				if value, ok := itemcfgTokenInteger(tokens[0]); ok && value > 0 {
					table.equipLevelConst = value
				}
			}
		}
	}
	if table.equipLevelConst <= 0 {
		return table, errors.New("缺少正的 [equip level const]")
	}
	for option := int64(1); option <= 4; option++ {
		if table.optionBase[option] <= 0 {
			return table, fmt.Errorf("缺少选项 %d 的 [option data] 基础值", option)
		}
	}
	return table, nil
}

// hasInitialValues 判断该稀有度能不能为 1..4 四个红字类型都算出初值。
func (t itemcfgAmplifyTable) hasInitialValues(rarity int) bool {
	if t.rarityWeight[int64(rarity)] <= 0 {
		return false
	}
	for amplifyType := 1; amplifyType <= 4; amplifyType++ {
		if t.optionBase[int64(amplifyType)] <= 0 {
			return false
		}
	}
	return true
}

// initialValue 逐字移植 dnfbridge/amplify_item_pvf.go:240-253 currentAmplifyInitialValues：
// 初值 = int64(rarityWeight × optionBase[type])，必须在 1..MaxUint16 之间，否则失败。
// 类型编号与 PVF 选项编号是同一套（1 体力/物防、2 精神/魔防、3 力量/物攻、4 智力/魔攻）。
func (t itemcfgAmplifyTable) initialValue(rarity int, amplifyType int) (uint16, error) {
	weight := t.rarityWeight[int64(rarity)]
	if weight <= 0 {
		return 0, fmt.Errorf("%s 的 [rarity weight] 里没有稀有度 %d 的正权重", itemcfgAmplifyItemPVFPath, rarity)
	}
	base := t.optionBase[int64(amplifyType)]
	if base <= 0 {
		return 0, fmt.Errorf("%s 的 [option data] 里没有红字类型 %d 的基础值", itemcfgAmplifyItemPVFPath, amplifyType)
	}
	value := int64(weight * base)
	if value <= 0 || value > math.MaxUint16 {
		return 0, fmt.Errorf("红字类型 %d / 稀有度 %d 的初值 %d 越界", amplifyType, rarity, value)
	}
	return uint16(value), nil
}

// itemcfgSectionKey 与 dnfbridge/amplify_item_pvf.go:310-312 的 sectionKey648 逐字相同：
// 小写、裁剪、去掉前导 "/"（闭合段 `[/option data]` 与开段归一到同一个 key，于是
// 闭合段的空 token 区间不会重复记账）。
func itemcfgSectionKey(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(value, "/")))
}

// itemcfgNormalizeOptionName 与 dnfbridge/amplify_item_pvf.go:306-308 逐字相同。
func itemcfgNormalizeOptionName(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.Trim(value, "`")))
}

// itemcfgRarityIndex 与 dnfbridge/amplify_item_pvf.go:314-333 逐字相同。
func itemcfgRarityIndex(value string) (int64, bool) {
	switch value {
	case "common":
		return 0, true
	case "uncommon":
		return 1, true
	case "rare":
		return 2, true
	case "unique":
		return 3, true
	case "epic":
		return 4, true
	case "chronicle":
		return 5, true
	case "legendary":
		return 6, true
	default:
		return 0, false
	}
}

// itemcfgTokenInteger 与 dnfbridge 的 currentAmplifyValue.integer() 同口径：只认整数 token。
func itemcfgTokenInteger(token dnfpvf.Token) (int64, bool) {
	if token.Kind != dnfpvf.TokenInt {
		return 0, false
	}
	return token.Int, true
}

// itemcfgTokenNumber 与 dnfbridge 的 currentAmplifyValue.number() 同口径：整数与浮点都认。
func itemcfgTokenNumber(token dnfpvf.Token) (float64, bool) {
	switch token.Kind {
	case dnfpvf.TokenInt:
		return float64(token.Int), true
	case dnfpvf.TokenFloat:
		return token.Float, true
	default:
		return 0, false
	}
}

// ---------------------------------------------------------------------------
// 选项词表 / 审计
// ---------------------------------------------------------------------------

// itemcfgQualityOptions 与 api_items.go:254-257（既有 grant-options 响应）逐个字段相同。
func itemcfgQualityOptions() []map[string]any {
	return []map[string]any{
		{"value": itemcfgQualityRandom, "label": "随机品级"},
		{"value": itemcfgQualityTop, "label": "100% 最上级"},
	}
}

// itemcfgAmplifyTypeOptions 与 api_items.go:258-264 相同；标签来自 vendor
// EquipmentGrantPolicy.GetAmplifyTypeLabel（ItemGrantOptions.cs:167-177）。
func itemcfgAmplifyTypeOptions() []map[string]any {
	return []map[string]any{
		{"value": 0, "label": "无红字（强化）"},
		{"value": 1, "label": "体力"},
		{"value": 2, "label": "精神"},
		{"value": 3, "label": "力量"},
		{"value": 4, "label": "智力"},
	}
}

// logItemcfgRejection 记一条具名拒绝事件（字段与成功事件同形，便于按 reason 聚合）。
func (s *Server) logItemcfgRejection(
	panelID int,
	character dnfrepo.CharacterRecord,
	action string,
	target itemcfgTarget,
	itemID int64,
	err error,
) {
	if err == nil {
		return
	}
	reason := itemcfgReasonFromError(err)
	s.logEvent("gm-item-configure-rejected",
		"action", action,
		"panel_id", panelID,
		"character_id", character.CharacterID,
		"account_id", character.AccountID,
		"list_type", target.ListType,
		"slot", target.Slot,
		"key", target.Key,
		"item_id", itemID,
		"reason", reason,
		"error", err.Error(),
		"result", "rejected")
}

// itemcfgReasonFromError 把拒绝归类成稳定的 reason 串（日志聚合不依赖中文文案）。
func itemcfgReasonFromError(err error) string {
	var rejection *itemcfgRejection
	if errors.As(err, &rejection) && rejection.reason != "" {
		return rejection.reason
	}
	switch {
	case err == nil:
		return "rejected"
	case errors.Is(err, itemcfgErrListTypeMissing):
		return "missing_list_type"
	case errors.Is(err, itemcfgErrSlotMissing):
		return "missing_slot"
	case errors.Is(err, itemcfgErrListTypeOutOfRange):
		return "list_type_out_of_range"
	case errors.Is(err, itemcfgErrSlotOutOfRange):
		return "slot_out_of_range"
	case errors.Is(err, itemcfgErrWalletSlot):
		return "wallet_slot"
	case errors.Is(err, itemcfgErrAccountSharedSlot):
		return "account_shared_slot"
	case errors.Is(err, itemcfgErrWornEquipment):
		return "worn_equipment"
	case errors.Is(err, itemcfgErrAccountCargoSlot):
		return "account_cargo_container"
	case errors.Is(err, itemcfgErrContainerUnsupported):
		return "container_unsupported"
	case errors.Is(err, itemcfgErrSlotEmpty):
		return "slot_not_found"
	case errors.Is(err, itemcfgErrInventoryMissing):
		return "inventory_record_missing"
	case errors.Is(err, itemcfgErrItemUnknown):
		return "item_unknown"
	case errors.Is(err, itemcfgErrItemIndexBuilding):
		return "item_index_building"
	case errors.Is(err, itemcfgErrItemIndexFailed):
		return "item_index_failed"
	case errors.Is(err, itemcfgErrNoConfigurableFields):
		return "no_configurable_fields"
	case errors.Is(err, itemcfgErrAvatarOptionsUnavailable):
		return "avatar_options_unavailable"
	case errors.Is(err, itemcfgErrAvatarOptionUnsupported):
		return "avatar_option_unsupported"
	case errors.Is(err, itemcfgErrForgingTableUnavailable):
		return "forging_table_unavailable"
	case errors.Is(err, itemcfgErrForgingNotWeapon):
		return "forging_not_weapon"
	case errors.Is(err, itemcfgErrForgingLevelOutOfRange):
		return "forging_level_out_of_range"
	case errors.Is(err, itemcfgErrEquipmentNotConfigurable):
		return "equipment_not_configurable"
	case errors.Is(err, itemcfgErrEquipmentStack):
		return "equipment_stack_not_single"
	case errors.Is(err, itemcfgErrExpirationUnsupported):
		return "expiration_unsupported"
	case errors.Is(err, itemcfgErrQualityModeInvalid):
		return "quality_mode_invalid"
	case errors.Is(err, itemcfgErrUpgradeUnsupported):
		return "upgrade_unsupported"
	case errors.Is(err, itemcfgErrAmplifyUnsupported):
		return "amplify_unsupported"
	case errors.Is(err, itemcfgErrAmplifyTableUnavailable):
		return "amplify_table_unavailable"
	case errors.Is(err, itemcfgErrAmplifyValueUnresolvable):
		return "amplify_value_unresolved"
	case errors.Is(err, itemcfgErrTransactionUnavailable):
		return "character_items_transaction_unavailable"
	default:
		return "rejected"
	}
}
