package dnfbridge

import (
	"encoding/binary"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemexpiration"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const currentPVFWrongExpirationSource = "runtime_pvf"

const (
	currentJoustEventItemFirst uint32 = 490005585
	currentJoustEventItemLast  uint32 = 490005593
)

// currentPVFRetiredExpirationRenewalDate 是操作者定下的"退役道具统一续期"策略：
//
//	PVF 里声明了**绝对** [expiration date]、而那个日期已经过去的道具，
//	一律续期到 2028-01-01 00:00:00。
//
// 口径与 PVF 自己完全一致：解析 [expiration date] 用的是 currentPVFExpirationDateLocation
// = CST(UTC+8)（见 dungeon_drop_catalog.go 的 parseCurrentPVFExpirationDate），所以这里
// 也用同一个时区声明，客户端显示的正是"2028-01-01 00:00:00"；换成 UTC 表示就是
// 2027-12-31T16:00:00Z（unix 1830268800），在 u32 到期线上完全放得下。
//
// 为什么需要这条策略：当前客户端把"到期时间已过"（以及到期时间为 0）直接渲染成
// "已超过使用期限"。真实运行时 PVF（runtime/data/dnf/Script.pvf，195651 个道具）里
// 有 16001 个道具的绝对 [expiration date] 停在 2009~2017 年（闪亮/灿烂/光辉的主线任务
// 完成券 490005938/490005971/490005972 = `[expiration date] 2017-11-02 06:00:00`、
// 退役活动礼包/爆竹/时装/宠物/称号……），于是它们在本地服里全部变成"过期"：邮件附件行
// （mail/body.go 的 0x38）、背包/仓库行（0x38 + 0x0B）、领取后的堆栈、掉落与礼包产出
// 全都指向同一个过去的时间点。续期后这些路径读的是同一个常量，口径必然一致。
//
// 边界（有意为之，不要放宽）：
//   - 只动 PVF **自己声明的绝对日期**。"本就无期限"的道具（ExpirationDate 为零）一个字节
//     都不写；只声明相对 [usable period] 的道具仍按发放时刻 + N 天计算，行为不变。
//   - 还没到期的绝对日期原样保留（例如运行时 PVF 里已经修到 2028-08-16 06:00 的马战/
//     爆竹那一批），不做"提前续期"，避免把仍在运行的官方活动时间线一起改掉。
//   - 以实例创建/修补时刻判定"已经过去"，续期结果是一个**常量**而不是 now+N，
//     所以重复登录、重复领取、重复修补都不会把期限越推越远（幂等）。
var currentPVFRetiredExpirationRenewalDate = time.Date(
	2028, time.January, 1, 0, 0, 0, 0, currentPVFExpirationDateLocation,
)

// currentPVFRenewedRetiredExpirationDate 按上面的策略钳制一个绝对到期日：
// 已经过去（now 时刻起不再有效）→ 续期地平线；未声明或还没到期 → 原样返回。
func currentPVFRenewedRetiredExpirationDate(expirationDate time.Time, now time.Time) time.Time {
	if expirationDate.IsZero() {
		return expirationDate
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if expirationDate.After(now) {
		return expirationDate
	}
	return currentPVFRetiredExpirationRenewalDate
}

// currentPVFInstanceExpirationSupersededByRenewal 报告堆栈上留着的"实例到期时间"
// 是不是那条**已经退役的 PVF 绝对日期**抄下来的。
//
// 判据（三条同时成立）：
//  1. 实例时间戳非零；
//  2. 策略确实把 PVF 的绝对日期往后挪了（renewed > raw）；
//  3. 实例时间不晚于 PVF 原来那个日期 —— 一个活的按实例倒计时（租用 24 小时、
//     相对 [usable period] 的发放期）不可能比它自己的声明日期还早，只有"当年照抄
//     退役日期"的旧实例会满足这一条。
//
// 第 3 条同时保护了租用：租用道具的实例到期时间（now+24h）晚于 2017 那种退役日期，
// 所以不会被这里续期，租用照旧到期。马战那 9 件（currentJoustEventHasRetiredInstanceExpiration）
// 是另一套：它们的 PVF 日期是活的 2028-08-16，实例里却留着 2017，由那条规则单独负责。
func currentPVFInstanceExpirationSupersededByRenewal(
	instanceExpire uint32,
	rawExpiration time.Time,
	renewedExpiration time.Time,
) bool {
	if instanceExpire == 0 {
		return false
	}
	rawUnix := currentPVFExpirationUnix(rawExpiration)
	renewedUnix := currentPVFExpirationUnix(renewedExpiration)
	if rawUnix == 0 || renewedUnix == 0 || renewedUnix <= rawUnix {
		return false
	}
	return instanceExpire <= rawUnix
}

// currentJoustEventHasRetiredInstanceExpiration identifies the nine items
// that form the 2017 horse-joust shop/reward chain.  Their original PVF date
// was in 2017, so clients that bought one before the local all-day repair
// retain that stale deadline in the item instance even after the current PVF
// definition is moved forward.  These are static event items, not rentals or
// duration grants: a smaller per-instance deadline can only be the retired
// event date and must not win over the repaired PVF definition.
func currentJoustEventHasRetiredInstanceExpiration(
	itemID uint32,
	instanceExpire uint32,
	definitionExpire time.Time,
) bool {
	if itemID < currentJoustEventItemFirst || itemID > currentJoustEventItemLast ||
		instanceExpire == 0 || definitionExpire.IsZero() {
		return false
	}
	definitionUnix := currentPVFExpirationUnix(definitionExpire)
	return definitionUnix != 0 && instanceExpire < definitionUnix
}

func currentPVFWrongExpirationUnix(stack dnfrepo.ItemStack) uint32 {
	for _, key := range []string{"expire_time", "expire_unix"} {
		raw := strings.TrimSpace(stack.Extra[key])
		value, err := strconv.ParseUint(raw, 10, 32)
		if err == nil && value != 0 {
			return uint32(value)
		}
	}
	if !stack.ExpireAt.IsZero() && stack.ExpireAt.Unix() > 0 && stack.ExpireAt.Unix() <= math.MaxUint32 {
		return uint32(stack.ExpireAt.Unix())
	}
	return 0
}

// cleanupCurrentPVFWrongExpirationProjection removes only the synthetic state
// written by the superseded +0x6E implementation. A genuine rental expiration
// has no runtime_pvf marker and is preserved. The row tail is cleared only when
// it exactly equals the synthetic timestamp, so an unrelated real price value
// is never erased merely because the item also has a PVF expiration date.
func cleanupCurrentPVFWrongExpirationProjection(stack dnfrepo.ItemStack) (dnfrepo.ItemStack, bool) {
	if !strings.EqualFold(strings.TrimSpace(stack.Extra["expiration_source"]), currentPVFWrongExpirationSource) {
		return stack, false
	}
	wrongUnix := currentPVFWrongExpirationUnix(stack)
	changed := false
	if !stack.ExpireAt.IsZero() {
		stack.ExpireAt = time.Time{}
		changed = true
	}
	if stack.Extra != nil {
		cloned := make(map[string]string, len(stack.Extra))
		for key, value := range stack.Extra {
			cloned[key] = value
		}
		for _, key := range []string{"expire_time", "expire_unix", "expiration_source"} {
			if _, exists := cloned[key]; exists {
				delete(cloned, key)
				changed = true
			}
		}
		stack.Extra = cloned
	}
	if wrongUnix != 0 && len(stack.RawEntry) == currentItemListEntryWireSize &&
		binary.LittleEndian.Uint32(stack.RawEntry[0x6E:0x72]) == wrongUnix {
		stack.RawEntry = append([]byte(nil), stack.RawEntry...)
		binary.LittleEndian.PutUint32(stack.RawEntry[0x6E:0x72], 0)
		changed = true
	}
	return stack, changed
}

func applyCurrentPVFStackableUsePeriodAt(stack dnfrepo.ItemStack, expirationDate time.Time, now time.Time) (dnfrepo.ItemStack, bool) {
	stack, changed := cleanupCurrentPVFWrongExpirationProjection(stack)
	patchedExpire, expireChanged := applyCurrentItemListExpireTimeRaw(stack, expirationDate)
	stack = patchedExpire
	changed = changed || expireChanged
	usePeriod := currentPVFStackableUsePeriodSeconds(expirationDate, now)
	if len(stack.RawEntry) == 0 {
		stack.RawEntry = make([]byte, currentItemListEntryWireSize)
		changed = true
	}
	if len(stack.RawEntry) == currentItemListEntryWireSize && binary.LittleEndian.Uint16(stack.RawEntry[0x0B:0x0D]) != usePeriod {
		stack.RawEntry = append([]byte(nil), stack.RawEntry...)
		binary.LittleEndian.PutUint16(stack.RawEntry[0x0B:0x0D], usePeriod)
		changed = true
	}
	return stack, changed
}

func applyCurrentItemListExpireTimeRaw(stack dnfrepo.ItemStack, expirationDate time.Time) (dnfrepo.ItemStack, bool) {
	if expirationDate.IsZero() || expirationDate.Unix() <= 0 {
		return stack, false
	}
	changed := false
	expireUnix := sceneInventoryUint32FromInt64(expirationDate.Unix())
	if len(stack.RawEntry) == 0 {
		stack.RawEntry = make([]byte, currentItemListEntryWireSize)
		changed = true
	}
	if len(stack.RawEntry) == currentItemListEntryWireSize && (binary.LittleEndian.Uint32(stack.RawEntry[currentItemListExpireTimeOffset:currentItemListExpireTimeOffset+4]) != expireUnix ||
		binary.LittleEndian.Uint32(stack.RawEntry[legacyWrongCurrentItemListExpireTimeOffset:legacyWrongCurrentItemListExpireTimeOffset+4]) == expireUnix) {
		stack.RawEntry = append([]byte(nil), stack.RawEntry...)
		if binary.LittleEndian.Uint32(stack.RawEntry[legacyWrongCurrentItemListExpireTimeOffset:legacyWrongCurrentItemListExpireTimeOffset+4]) == expireUnix {
			binary.LittleEndian.PutUint32(stack.RawEntry[legacyWrongCurrentItemListExpireTimeOffset:legacyWrongCurrentItemListExpireTimeOffset+4], 0)
		}
		binary.LittleEndian.PutUint32(stack.RawEntry[currentItemListExpireTimeOffset:currentItemListExpireTimeOffset+4], expireUnix)
		changed = true
	}
	return stack, changed
}

func applyCurrentPVFStackableUsePeriod(stack dnfrepo.ItemStack, expirationDate time.Time) (dnfrepo.ItemStack, bool) {
	return applyCurrentPVFStackableUsePeriodAt(stack, expirationDate, time.Now().UTC())
}

func applyCurrentStackableExpirationAt(stack dnfrepo.ItemStack, expirationDate time.Time, now time.Time) (dnfrepo.ItemStack, bool) {
	stack, changed := cleanupCurrentPVFWrongExpirationProjection(stack)
	if expirationDate.IsZero() {
		return stack, changed
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if stack.Extra == nil {
		stack.Extra = make(map[string]string, 4)
		changed = true
	} else {
		cloned := make(map[string]string, len(stack.Extra)+4)
		for key, value := range stack.Extra {
			cloned[key] = value
		}
		stack.Extra = cloned
	}
	expireUnix := strconv.FormatInt(expirationDate.Unix(), 10)
	for _, key := range []string{"expire_time", "expire_unix"} {
		if stack.Extra[key] != expireUnix {
			stack.Extra[key] = expireUnix
			changed = true
		}
	}
	if stack.Extra["item_kind"] == "" {
		stack.Extra["item_kind"] = string(dungeonDropItemStackable)
		changed = true
	}
	if !stack.ExpireAt.Equal(expirationDate) {
		stack.ExpireAt = expirationDate
		changed = true
	}
	patched, patchedChanged := applyCurrentPVFStackableUsePeriodAt(stack, expirationDate, now)
	return patched, changed || patchedChanged
}

func applyCurrentStackableExpiration(stack dnfrepo.ItemStack, expirationDate time.Time) (dnfrepo.ItemStack, bool) {
	return applyCurrentStackableExpirationAt(stack, expirationDate, time.Now().UTC())
}

func applyCurrentPVFItemExpirationAt(stack dnfrepo.ItemStack, definition dungeonDropItemDefinition, now time.Time) (dnfrepo.ItemStack, bool) {
	if definition.DailyDeleteItem {
		stack, cleanupChanged := cleanupCurrentPVFWrongExpirationProjection(stack)
		if now.IsZero() {
			now = time.Now().UTC()
		}
		patched, changed, expired, err := itemexpiration.ReconcileDailyDelete(stack, now)
		if err != nil || expired {
			// Removal is owned by the atomic reconciliation, never by a row
			// builder. An overdue instance must not receive another deadline.
			return stack, false
		}
		patched, rawChanged := applyCurrentItemListExpireTimeRaw(patched, patched.ExpireAt)
		patched, periodChanged := applyCurrentPVFStackableUsePeriodAt(patched, patched.ExpireAt, now)
		return patched, cleanupChanged || changed || rawChanged || periodChanged
	}
	stack, changed := cleanupCurrentPVFWrongExpirationProjection(stack)
	// P7b：GM 面板发放时写下的到期覆盖（expiration_source=gm_override）优先于 PVF。
	// 这一段对应 UP item_expiration_projection.go:180-192，本仓此前没有（P7 报告 §5.1），
	// 于是"GM 发的 30 天"会在重登时被 PVF 的绝对 [expiration date] 覆盖回去。
	// handled=false（没标记 / 开关 off / 标记在但期限不可用）时下面一行都不改变行为。
	if override, overrideChanged, handled := applyCurrentGMExpirationOverrideProjectionAt(stack, now); handled {
		return override, changed || overrideChanged
	}
	if definition.ExpirationDate.IsZero() {
		// 本就无期限的道具：一个字节都不写（过期投影只负责"有到期日"的道具）。
		return stack, changed
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	// 策略：PVF 的绝对 [expiration date] 已经过去时不再原样投影成"过期"，
	// 而是统一续期到 2028-01-01（见 currentPVFRetiredExpirationRenewalDate）。
	// 这条钳制在邮件/背包/仓库/装备/产出所有堆栈投影上共用同一个 helper，
	// 所以同一道具在各路径上得到的到期时间必然相同。
	rawExpiration := definition.ExpirationDate
	definition.ExpirationDate = currentPVFRenewedRetiredExpirationDate(rawExpiration, now)
	expirationDate := definition.ExpirationDate
	if instanceExpire := currentItemListStackExpire(stack); instanceExpire != 0 &&
		!currentJoustEventHasRetiredInstanceExpiration(
			sceneInventoryUint32FromInt64(stack.ItemID), instanceExpire, definition.ExpirationDate) &&
		!currentPVFInstanceExpirationSupersededByRenewal(
			instanceExpire, rawExpiration, definition.ExpirationDate) {
		expirationDate = time.Unix(int64(instanceExpire), 0).UTC()
	}
	if stack.Extra == nil {
		stack.Extra = make(map[string]string, 5)
		changed = true
	} else {
		cloned := make(map[string]string, len(stack.Extra)+5)
		for key, value := range stack.Extra {
			cloned[key] = value
		}
		stack.Extra = cloned
	}
	expireUnix := strconv.FormatInt(expirationDate.Unix(), 10)
	for _, key := range []string{"expire_time", "expire_unix"} {
		if stack.Extra[key] != expireUnix {
			stack.Extra[key] = expireUnix
			changed = true
		}
	}
	if definition.Kind != "" && stack.Extra["item_kind"] == "" {
		stack.Extra["item_kind"] = string(definition.Kind)
		changed = true
	}
	if definition.PVFPath != "" && stack.Extra["pvf_path"] == "" {
		stack.Extra["pvf_path"] = definition.PVFPath
		changed = true
	}
	if definition.StackableType != "" && stack.Extra["stackable_type"] == "" {
		stack.Extra["stackable_type"] = definition.StackableType
		changed = true
	}
	if definition.StackLimit > 0 && stack.Extra["stack_limit"] == "" {
		stack.Extra["stack_limit"] = strconv.FormatInt(definition.StackLimit, 10)
		changed = true
	}
	if definition.UsablePeriodDays > 0 {
		usablePeriod := strconv.FormatInt(definition.UsablePeriodDays, 10)
		if stack.Extra["usable_period_days"] != usablePeriod {
			stack.Extra["usable_period_days"] = usablePeriod
			changed = true
		}
		if stack.Extra["expiration_source"] != "runtime_pvf_usable_period_grant" {
			stack.Extra["expiration_source"] = "runtime_pvf_usable_period_grant"
			changed = true
		}
	}
	if !stack.ExpireAt.Equal(expirationDate) {
		stack.ExpireAt = expirationDate
		changed = true
	}
	patchedExpire, expireChanged := applyCurrentItemListExpireTimeRaw(stack, expirationDate)
	stack = patchedExpire
	changed = changed || expireChanged
	if definition.Kind == dungeonDropItemStackable {
		patched, patchedChanged := applyCurrentPVFStackableUsePeriodAt(stack, expirationDate, now)
		stack = patched
		changed = changed || patchedChanged
	}
	return stack, changed
}

func applyCurrentPVFItemExpiration(stack dnfrepo.ItemStack, definition dungeonDropItemDefinition) (dnfrepo.ItemStack, bool) {
	return applyCurrentPVFItemExpirationAt(stack, definition, time.Now().UTC())
}

// ---------------------------------------------------------------------------
// GM 发放期限覆盖的消费端（P7b）
// ---------------------------------------------------------------------------

const (
	// currentGMExpirationOverrideEnv 是"发放与保持共用一个开关"的开关名：与写入端
	// gmtool/api_items_grant_options.go:56 的 gmMailOptionsEnv 逐字相同
	// （DNFBRIDGE_GM_MAIL_OPTIONS）。**空 = off，只有显式 `on` 才算开**
	// （`true`/`1`/`yes` 一律不算）。
	//
	// 为什么复用发放开关（P7b 任务书候选 a）而不是新开一个专用开关：
	//  1. 操作者只需要记住一个开关 —— 打开它，"发放时写 GM 期限"与"重登时保住 GM
	//     期限"同时成立；关掉它两条一起不生效，不存在"发了却保不住"的半开状态
	//     （两个开关的 4 种组合里有 2 种是这种半开状态）。
	//  2. 代价（如实记账，不要当成缺陷）：把开关从 on 改回空/off 之后，**已经发出去的**
	//     GM 期限不再被消费，下次登录会被 PVF 期限覆盖回去。标记本身仍留在
	//     Extra["expiration_source"] / ["gm_expire_days"] 里，重新开 on 即恢复。
	currentGMExpirationOverrideEnv = "DNFBRIDGE_GM_MAIL_OPTIONS"

	// currentGMExpirationOverrideSource 是 GM 面板发放时写下的来源标记值，必须逐字
	// 等于写入端 gmtool/api_items_grant_options.go:343 的 gmExpirationSource
	// （上游是 UP item_expiration_projection.go:15 的 currentGMExpirationSource）。
	//
	// 这是一条**跨包契约**：写入端只把它写进**邮件附件 Extra**
	// （gmtool/api_items_grant_options.go:387-390 → gmtool/api_items.go:401 →
	// :421 的 MailAttachment.Extra），领取时由 mail/owner.go:820 cloneStringMap →
	// :861 原样带进**背包堆栈 Extra**，本文件读的就是同一个键。两层是不是真的接上了
	// 由 TestGMExpirationOverrideMarkerMatchesGrantWriter（从写入端源码取字面量比对）
	// 与 TestGMExpirationOverrideSurvivesMailRepairClaimAndRelogin（端到端）钉死。
	currentGMExpirationOverrideSource = "gm_override"

	// currentGMExpirationOverrideInvalidEvent 是"标记在、但期限或天数记账读不出来"的
	// 具名事件（不静默：见 reportCurrentGMExpirationOverrideInvalid）。
	currentGMExpirationOverrideInvalidEvent = "game-item-expiration-gm-override-invalid"
)

// currentGMExpirationOverrideInvalidEventSink 是上面那条具名事件的唯一出口。
// 生产实现写 slog 默认日志（与 platform/adminops 的 slog.Default().Error 同形）；
// 测试替换它来捕获事件，从而不必改动进程级 logger。
var currentGMExpirationOverrideInvalidEventSink = func(kind string, fields ...any) {
	slog.Default().Warn(kind, fields...)
}

// currentGMExpirationOverrideEnabled 解析 DNFBRIDGE_GM_MAIL_OPTIONS：两端裁剪 +
// 大小写不敏感 + 逐字节等于 `on`，与写入端 GMMailOptionsFromEnv
// （gmtool/api_items_grant_options.go:61-63）同一口径。
//
// 直接 os.Getenv 自读（本仓先例 force_town_exit_trigger.go:75、
// character_option_projection.go:82），**不改 options.go**（本批纪律）。
func currentGMExpirationOverrideEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(currentGMExpirationOverrideEnv)), "on")
}

// currentGMExpirationOverrideMarked 报告这条实例（邮件附件 / 背包堆栈 / 装备条目）
// 是不是带 GM 期限标记。判据与上游逐字一致：`expiration_source` 两端裁剪后大小写
// 不敏感等于 "gm_override"（UP item_expiration_projection.go:180）。
func currentGMExpirationOverrideMarked(extra map[string]string) bool {
	if extra == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(extra["expiration_source"]), currentGMExpirationOverrideSource)
}

// currentGMExpirationOverrideInstanceExpire 是"这条实例带在身上的到期时间"，
// 口径与 currentItemListStackExpire（current_item_list_entry.go:77-85）完全一致：
// Extra 的 expire_time/expire_unix 优先，其次 ExpireAt。邮件附件没有前两个键、
// 只有 ExpireAt，所以两条路都必须看。
func currentGMExpirationOverrideInstanceExpire(extra map[string]string, expireAt time.Time) uint32 {
	return currentItemListStackExpire(dnfrepo.ItemStack{ExpireAt: expireAt, Extra: extra})
}

// reportCurrentGMExpirationOverrideInvalid 记一条具名事件。**绝不 panic，绝不改别的
// 字段**；事件里给出确切去向（result），不留"到底保留还是回退"的疑问：
//
//	reason=missing_deadline     标记在、但没有任何可用到期时间
//	                            -> result=pvf_fallback（**回退到今天的行为**：继续走 PVF 投影）
//	reason=missing_expire_days  标记与期限都在、只是缺 gm_expire_days 记账键
//	reason=invalid_expire_days  gm_expire_days 存在但解析失败或 <=0
//	                            上面两条 -> result=gm_deadline_kept（**保留 GM 期限**：
//	                            到期时间的真源是 expire_time/expire_unix/ExpireAt，
//	                            gm_expire_days 只是天数记账；非法就不刷新它，
//	                            与 UP:297-303 的 `expireDays > 0` 判据同形）
func reportCurrentGMExpirationOverrideInvalid(reason string, itemID int64, extra map[string]string, expireAt time.Time) {
	expire := currentGMExpirationOverrideInstanceExpire(extra, expireAt)
	result := "gm_deadline_kept"
	if expire == 0 {
		result = "pvf_fallback"
	}
	currentGMExpirationOverrideInvalidEventSink(
		currentGMExpirationOverrideInvalidEvent,
		"reason", reason,
		"item_id", itemID,
		"gm_expire_days", strings.TrimSpace(extra["gm_expire_days"]),
		"expire_unix", expire,
		"item_kind", strings.TrimSpace(extra["item_kind"]),
		"result", result,
	)
}

// applyCurrentGMExpirationOverrideProjectionAt 是 UP item_expiration_projection.go:180-192
// 的消费端移植：带 GM 标记的实例，到期时间听 GM 的，不按 PVF 重算。
//
// 返回 handled=true 表示"这条实例的到期时间归 GM 管"，调用方**必须**原样返回，
// 不得再走 PVF 投影；false 表示走今天那条 PVF 投影（一个字节都不改）。
//
// 与上游逐段对拍（UP:180-192，配合本文件下面的 UP:268-317 移植）：
//
//	:180      判据 expiration_source == "gm_override"（EqualFold + TrimSpace）
//	:181-182  期限 = currentItemListStackExpire(stack)（expire_time/expire_unix 优先，
//	          其次 ExpireAt）；==0 时上游**不接管**，继续往下走 PVF 投影
//	:183-190  只把 gm_expire_days 当天数记账（Atoi 失败按 0 处理），再交
//	          applyCurrentGMExpirationOverrideAt 把 Extra/ExpireAt/行字节对齐到这个期限
//
// 本仓相对上游的两点差异（都在本批测试里钉死）：
//  1. 整个分支由 DNFBRIDGE_GM_MAIL_OPTIONS 把关：off 档连标记都不看，投影结果与
//     P7b 之前逐字节相同（P7b 任务书 §2 的硬要求）。
//  2. 不静默：上面三种 reason 各记一条具名事件（上游 `expireDays, _ := strconv.Atoi`
//     是静默按 0 处理）。
func applyCurrentGMExpirationOverrideProjectionAt(stack dnfrepo.ItemStack, now time.Time) (dnfrepo.ItemStack, bool, bool) {
	if !currentGMExpirationOverrideEnabled() || !currentGMExpirationOverrideMarked(stack.Extra) {
		return stack, false, false
	}
	expireUnix := currentGMExpirationOverrideInstanceExpire(stack.Extra, stack.ExpireAt)
	if expireUnix == 0 {
		reportCurrentGMExpirationOverrideInvalid("missing_deadline", stack.ItemID, stack.Extra, stack.ExpireAt)
		return stack, false, false
	}
	// Atoi 在溢出时返回被钳制的最大值 + err，所以解析失败必须显式归零，
	// 否则一个越界的 gm_expire_days 会被当成合法天数写回去。
	expireDays, parseErr := strconv.Atoi(strings.TrimSpace(stack.Extra["gm_expire_days"]))
	if parseErr != nil || expireDays <= 0 {
		reason := "invalid_expire_days"
		if strings.TrimSpace(stack.Extra["gm_expire_days"]) == "" {
			reason = "missing_expire_days"
		}
		reportCurrentGMExpirationOverrideInvalid(reason, stack.ItemID, stack.Extra, stack.ExpireAt)
		expireDays = 0
	}
	patched, overrideChanged := applyCurrentGMExpirationOverrideAt(
		stack,
		time.Unix(int64(expireUnix), 0).UTC(),
		expireDays,
		now,
	)
	return patched, overrideChanged, true
}

// applyCurrentGMExpirationOverrideAt 是 UP item_expiration_projection.go:268-317 的
// 移植（消费端版本）：把实例的到期时间钉在 expirationDate 上，并把 Extra 与 0x77 行
// 一起对齐。它与 gmtool 发放端的同名函数（gmtool/api_items_grant_options.go:369-403）
// 是同一份上游语义的两个半边 —— 发放端只有纯日期（不写行字节，见该函数注释），
// 消费端在背包/仓库/装备条目上必须把持久化行也写对，否则客户端按行 0x38 渲染。
//
// 逐段对拍（UP:268-317）：
//
//	:274-276 到期时间零值 / Unix<=0 -> 原样返回、changed=false（唯一提前返回）
//	:277-279 now 零值时取墙钟（上游同形；调用方在大多数路径已经给了 now）
//	:281-285 复制 Extra（绝不改调用方的 map）
//	:286-292 expire_time / expire_unix = 到期 Unix 字符串
//	:293-296 expiration_source = "gm_override"
//	:297-303 expireDays > 0 才写 gm_expire_days（<=0 时一个字节都不动）
//	:304-307 stack.ExpireAt = 到期时间
//	:308-310 applyCurrentItemListExpireTimeRaw —— 持久化行的 0x38 也写这个期限
//	:311-315 堆叠品的使用期限秒数（行 0x0B）按同一期限重算
//
// 幂等：同样的 (期限, 天数) 重复调用不改任何字节（第二次 changed=false）。
func applyCurrentGMExpirationOverrideAt(
	stack dnfrepo.ItemStack,
	expirationDate time.Time,
	expireDays int,
	now time.Time,
) (dnfrepo.ItemStack, bool) {
	if expirationDate.IsZero() || expirationDate.Unix() <= 0 {
		return stack, false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	changed := false
	cloned := make(map[string]string, len(stack.Extra)+4)
	for key, value := range stack.Extra {
		cloned[key] = value
	}
	stack.Extra = cloned
	expireUnix := strconv.FormatInt(expirationDate.Unix(), 10)
	for _, key := range []string{"expire_time", "expire_unix"} {
		if stack.Extra[key] != expireUnix {
			stack.Extra[key] = expireUnix
			changed = true
		}
	}
	if stack.Extra["expiration_source"] != currentGMExpirationOverrideSource {
		stack.Extra["expiration_source"] = currentGMExpirationOverrideSource
		changed = true
	}
	if expireDays > 0 {
		days := strconv.Itoa(expireDays)
		if stack.Extra["gm_expire_days"] != days {
			stack.Extra["gm_expire_days"] = days
			changed = true
		}
	}
	if !stack.ExpireAt.Equal(expirationDate) {
		stack.ExpireAt = expirationDate
		changed = true
	}
	patched, rawChanged := applyCurrentItemListExpireTimeRaw(stack, expirationDate)
	stack = patched
	changed = changed || rawChanged
	if strings.EqualFold(strings.TrimSpace(stack.Extra["item_kind"]), string(dungeonDropItemStackable)) {
		patched, periodChanged := applyCurrentPVFStackableUsePeriodAt(stack, expirationDate, now)
		stack = patched
		changed = changed || periodChanged
	}
	return stack, changed
}

func normalizeCurrentPVFItemStack(stack dnfrepo.ItemStack, definition dungeonDropItemDefinition, now time.Time) (dnfrepo.ItemStack, bool) {
	return applyCurrentPVFItemExpirationAt(stack, definition, now)
}

func normalizeCurrentPVFEquipmentEntry(entry dnfrepo.EquipmentEntry, definition dungeonDropItemDefinition, now time.Time) (dnfrepo.EquipmentEntry, bool) {
	stack, changed := applyCurrentPVFItemExpirationAt(dnfrepo.ItemStack{
		ItemID:   entry.ItemID,
		Bind:     entry.Bind,
		ExpireAt: entry.ExpireAt,
		RawEntry: entry.RawEntry,
		Extra:    entry.Extra,
	}, definition, now)
	entry.ExpireAt = stack.ExpireAt
	entry.RawEntry = stack.RawEntry
	entry.Extra = stack.Extra
	return entry, changed
}
