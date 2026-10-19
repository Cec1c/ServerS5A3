package dnfbridge

import (
	"fmt"
	"math"

	"longheng.io/server/internal/modules/dnf/onlineevent"
)

const (
	// The legacy command enum names both values from the opposite packet
	// direction. Keep current-EXE notification semantics local to this owner.
	currentSpendTimeEventInfoMsgID uint16 = 108
	currentSpendTimeProgressMsgID  uint16 = 1206
	currentSpendTimeEventID        uint32 = 2347
	currentJoustEventID            uint32 = 2365

	// Current EXE 2E62BBE checks active event 181 before
	// opening the Anton channel selector. Without it, the local NPC gate
	// reports dstr 32045 (使用日期不准确) before sending a channel request.
	// This is an availability flag, independent of raid entry cost and runs.
	currentAntonChannelEventID uint16 = 181
	// Actual current 0133101A and 0141784C check this activity before
	// admitting/displaying the Luke channel selector.
	currentLukeChannelEventID uint16 = 282
	// Actual 01A55120 checks 2426 before selecting the original mode chooser
	// (window 127 / RaidSelectSingleModeWindow.xui), instead of window 123.
	currentAntonModeSelectionEventID uint16 = 2426

	// currentTownNpcVisibleEventID 是「红尾乔纳森」（`npc/476_jonathan.npc`，NPC id
	// 476，站在**中央公园 3 区 地铁中心** `map/town/centralpark/centralpark_metrocenter.map`
	// (873,200)）文档里唯一一条可见性闸门 `[open visible event] 2395` 的事件号（操作者
	// 清单第 56 条）。
	//
	// 取证（真档 Script.pvf，sha256 cc517a6e…）：
	//   - `npc/476_jonathan.npc` 全文 61 行，第 60-61 行是 `[open visible event]` / `2395`；
	//     文档里**没有** `[event open]` 段（与 NPC 20449 那种"自带时间窗"的形式不同），
	//     所以这个事件"开没开"只能由客户端从服务端拿到的事件表决定。
	//   - 全档 1,057,625 个条目按 UTF-8 解码后搜字面量 `[open visible event]` 共 203 处命中，
	//     **全部落在 `.npc` 文档里**；其中取值为 2395 的只有 `npc/476_jonathan.npc` 一个。
	//   - 中央公园同一族邻居（469 凯蒂 / 470 祈求者比比 / 471 尼巫 / 472 派伊 / 473 装备商人 /
	//     474 祈求者比比 / 475 红色魔女）都**没有**任何 `[open visible event]`，只有 476 有。
	//   - 同一机制的在库先例：NPC 20449（艾莉亚）的 `[open visible event] 2365` 被删掉后
	//     客户端现场就不再认这只 NPC，恢复后才恢复；见 changlog 2026-08-04「preserve
	//     native joust NPC event gates」——「All-day availability remains owned by the
	//     server's active event-2365 op108 catalog」。
	currentTownNpcVisibleEventID uint32 = 2395

	currentSpendTimeDescriptorParamCount = 12
	currentSpendTimeMaxRewardItems       = 10
	currentSpendTimeProgressRawSize      = 0x20
	currentSpendTimeUnusedParam          = math.MaxUint32
	currentSpendTimeBaseCatalogValue     = 1
	currentSpendTimeEventInfoCodec       = "current_op108_spend_time_and_all_day_joust_catalog_zlib"
	currentSpendTimeEventRoute           = "spend time event/0/0"
)

// buildCurrentSpendTimeEventInfoBody retains the established op108 grammar.
// The distributed v76 EXE consumes its base rows at 0x2302760..0x2302d68;
// older NoPack evidence called the corresponding reader sub_20457F0:
//
//	u16 base_catalog_count,
//	base_catalog_count * { u16 event_id, u32 value, dstr title,
//	                          dstr description, u8 has_extended, ... },
//	u8 active_count, active_count * { u16 event_id, 12 * u32 param }
//
// The older sub_2043290 reads a retired list only when the installed base table
// contains special key 9. This catalog contains events 2347 and 2365, neither
// of which is that special key, so a retired-count byte must not be inserted
// before active_count.
//
// The base row is required even though event 2347 owns a fixed client UI.
// Current 0x2302040 installs the active table, 0x22ff610 tests activity presence,
// and 0x1136d10 shows the HUD only when the activity exists in game state 1.
// Event 2347 also retains its exact extended action route. Current action
// registration 0x22f7084 maps "spend time event" to 0x6B4, not the older
// EXE's 0x6AD. Current 0x22ffbf0 stores that action at row+0x140, the two
// route arguments at +0x164/+0x168, and the clickable flag at +0x13c.
// Actual-code route/search tests cover the published /0/0 route. UI factories,
// action dispatch and physical hover/rendering remain separate evidence; a
// successful route parse must not be reported as a restored reward window.
//
// Event 2347 owns params 0..N-1 as reward item IDs, param 10 as N, and param
// 11 as the total duration of all four stages. Current 0x1137ba0 treats -1,
// not zero, as the unused reward-slot sentinel.
//
// Event 2365 has no schedule extension, so whenever it is present the joust
// activity is available for the full server uptime instead of depending on a
// historical date window. That is exactly why its two rows are no longer
// unconditional: the client opens the Korean BLUE/RED joust window from this
// catalog row alone, even when every op1240/1241/1242/1243 push is already
// suppressed by DNFBRIDGE_JOUST_EVENT_ENABLED. includeJoustEvent is therefore an
// explicit argument owned by the caller (Service.currentSpendTimeCatalogIncludesJoustEvent)
// so this builder stays a pure function:
//
//	true  - emit the 2365 base row and the 2365 active row (byte-for-byte the
//	        pre-switch body, kept for the day the joust event is re-enabled);
//	false - emit neither row, and keep base_catalog_count/active_count
//	        consistent with the rows that are actually written (1 and 1).
//
// Event 2347's own bytes are identical in both modes.
// The remaining indirect sub_20457F0 finishers consume no bytes for this
// minimum body.
// buildCurrentSpendTimeEventInfoBody 是只带（可选）马战 2365 两行的历史形状；保留
// 原签名与逐字节行为，供既有钉死用例继续钉住「今天的行为」。
func buildCurrentSpendTimeEventInfoBody(
	rewardItemIDs []uint32,
	totalStageSeconds uint32,
	includeJoustEvent bool,
) ([]byte, error) {
	return buildCurrentSpendTimeEventInfoBodyWithTownNpcEvent(rewardItemIDs, totalStageSeconds, includeJoustEvent, false)
}

// buildCurrentSpendTimeEventInfoBodyWithTownNpcEvent 在上一版之上多一个**默认关闭**的
// 追加项：把「红尾乔纳森」可见性闸门用到的事件 `currentTownNpcVisibleEventID`（2395）
// 作为一条通用 base 行 + 一条通用 active 行写进同一个 op108 目录。
//
// 行形状与 2365 **完全同构**（`has_extended=0`、无时间窗 ⇒ 目录在就有、不存在就无），
// 因为那是本仓已经在生产里跑通的「通用活动行」形状：
//
//	base  行： u16 event_id, u32 value(=1), dstr title(nil), dstr description(nil), u8 has_extended(0)
//	active行： u16 event_id, 12 * u32 param(全 0)
//
// includeTownNpcEvent=false 时**逐字节等于**改动前的 op108 体（含 2365 的两档都对得上），
// 所以默认档不需要任何新的期望值。
//
// 只覆盖 2395；2347（在线时长/签到）与 2365（竞技大赛）的字节在任何取值下都不变。
func buildCurrentSpendTimeEventInfoBodyWithTownNpcEvent(
	rewardItemIDs []uint32,
	totalStageSeconds uint32,
	includeJoustEvent bool,
	includeTownNpcEvent bool,
) ([]byte, error) {
	return buildCurrentSpendTimeEventInfoBodyWithAntonChannelEvent(
		rewardItemIDs, totalStageSeconds, includeJoustEvent, includeTownNpcEvent, false,
	)
}

// buildCurrentSpendTimeEventInfoBodyWithAntonChannelEvent retains the
// process-first op108 grammar and publishes only the verified Anton flag.
// Its generic base/active pair has no schedule or click action: the server
// advertises availability, while the native NPC still checks prerequisites.
// No Luke (282), Fiendwar (2449), entry-count, or inventory state is invented.
func buildCurrentSpendTimeEventInfoBodyWithAntonChannelEvent(
	rewardItemIDs []uint32,
	totalStageSeconds uint32,
	includeJoustEvent bool,
	includeTownNpcEvent bool,
	includeAntonChannelEvent bool,
) ([]byte, error) {
	return buildCurrentSpendTimeEventInfoBodyWithAntonModeSelection(rewardItemIDs, totalStageSeconds, includeJoustEvent, includeTownNpcEvent, includeAntonChannelEvent, false)
}

func buildCurrentSpendTimeEventInfoBodyWithAntonModeSelection(
	rewardItemIDs []uint32,
	totalStageSeconds uint32,
	includeJoustEvent, includeTownNpcEvent, includeAntonChannelEvent, includeAntonModeSelection bool,
) ([]byte, error) {
	return buildCurrentActivityEventInfoBody(rewardItemIDs, totalStageSeconds, includeJoustEvent, includeTownNpcEvent, includeAntonChannelEvent, includeAntonModeSelection, true)
}

func buildCurrentActivityEventInfoBody(
	rewardItemIDs []uint32,
	totalStageSeconds uint32,
	includeJoustEvent, includeTownNpcEvent, includeAntonChannelEvent, includeAntonModeSelection, includeSpendTime bool,
) ([]byte, error) {
	return buildCurrentActivityEventInfoBodyWithDayDungeon(rewardItemIDs, totalStageSeconds, includeJoustEvent, includeTownNpcEvent, includeAntonChannelEvent, includeAntonModeSelection, includeSpendTime, 0)
}

// A complete catalog replaces the original native map on every update. Only
// the verified current weekday flag is added; old weekday flags are omitted.
func buildCurrentActivityEventInfoBodyWithDayDungeon(
	rewardItemIDs []uint32,
	totalStageSeconds uint32,
	includeJoustEvent, includeTownNpcEvent, includeAntonChannelEvent, includeAntonModeSelection, includeSpendTime bool,
	dayDungeonEvent uint16,
) ([]byte, error) {
	return buildCurrentActivityEventInfoBodyWithLuke(rewardItemIDs, totalStageSeconds, includeJoustEvent, includeTownNpcEvent, includeAntonChannelEvent, includeAntonModeSelection, includeSpendTime, dayDungeonEvent, false)
}

func buildCurrentActivityEventInfoBodyWithLuke(
	rewardItemIDs []uint32,
	totalStageSeconds uint32,
	includeJoustEvent, includeTownNpcEvent, includeAntonChannelEvent, includeAntonModeSelection, includeSpendTime bool,
	dayDungeonEvent uint16,
	includeLuke bool,
) ([]byte, error) {
	return buildCurrentActivityEventInfoBodyWithMonthly(rewardItemIDs, totalStageSeconds, includeJoustEvent, includeTownNpcEvent, includeAntonChannelEvent, includeAntonModeSelection, includeSpendTime, dayDungeonEvent, includeLuke, false)
}

func buildCurrentActivityEventInfoBodyWithMonthly(
	rewardItemIDs []uint32, totalStageSeconds uint32,
	includeJoustEvent, includeTownNpcEvent, includeAntonChannelEvent, includeAntonModeSelection, includeSpendTime bool,
	dayDungeonEvent uint16, includeLuke, includeMonthly bool,
) ([]byte, error) {
	return buildCurrentActivityEventInfoBodyWithWeekly(rewardItemIDs, totalStageSeconds, includeJoustEvent, includeTownNpcEvent, includeAntonChannelEvent, includeAntonModeSelection, includeSpendTime, dayDungeonEvent, includeLuke, includeMonthly, false)
}
func buildCurrentActivityEventInfoBodyWithWeekly(rewardItemIDs []uint32, totalStageSeconds uint32, includeJoustEvent, includeTownNpcEvent, includeAntonChannelEvent, includeAntonModeSelection, includeSpendTime bool, dayDungeonEvent uint16, includeLuke, includeMonthly, includeWeekly bool) ([]byte, error) {
	return buildCurrentActivityEventInfoBodyWithOrdinary(rewardItemIDs, totalStageSeconds, includeJoustEvent, includeTownNpcEvent, includeAntonChannelEvent, includeAntonModeSelection, includeSpendTime, dayDungeonEvent, includeLuke, includeMonthly, includeWeekly, false)
}

func buildCurrentActivityEventInfoBodyWithOrdinary(rewardItemIDs []uint32, totalStageSeconds uint32, includeJoustEvent, includeTownNpcEvent, includeAntonChannelEvent, includeAntonModeSelection, includeSpendTime bool, dayDungeonEvent uint16, includeLuke, includeMonthly, includeWeekly, includeOrdinary bool) ([]byte, error) {
	return buildCurrentActivityEventInfoBodyWithEpicRoad(rewardItemIDs, totalStageSeconds, includeJoustEvent, includeTownNpcEvent, includeAntonChannelEvent, includeAntonModeSelection, includeSpendTime, dayDungeonEvent, includeLuke, includeMonthly, includeWeekly, includeOrdinary, false)
}

func buildCurrentActivityEventInfoBodyWithEpicRoad(rewardItemIDs []uint32, totalStageSeconds uint32, includeJoustEvent, includeTownNpcEvent, includeAntonChannelEvent, includeAntonModeSelection, includeSpendTime bool, dayDungeonEvent uint16, includeLuke, includeMonthly, includeWeekly, includeOrdinary, includeEpicRoad bool) ([]byte, error) {
	return buildCurrentActivityEventInfoBodyWithOnlinePanel(rewardItemIDs, totalStageSeconds, includeJoustEvent, includeTownNpcEvent, includeAntonChannelEvent, includeAntonModeSelection, includeSpendTime, dayDungeonEvent, includeLuke, includeMonthly, includeWeekly, includeOrdinary, includeEpicRoad, false)
}

// 原生四加四奖励面板使用228；保留其它活动及历史2347目录的字节。
func buildCurrentActivityEventInfoBodyWithOnlinePanel(rewardItemIDs []uint32, totalStageSeconds uint32, includeJoustEvent, includeTownNpcEvent, includeAntonChannelEvent, includeAntonModeSelection, includeSpendTime bool, dayDungeonEvent uint16, includeLuke, includeMonthly, includeWeekly, includeOrdinary, includeEpicRoad, nativeOnlinePanel bool) ([]byte, error) {
	if dayDungeonEvent != 0 && (dayDungeonEvent < 2418 || dayDungeonEvent > 2424) {
		return nil, fmt.Errorf("unverified day dungeon event %d", dayDungeonEvent)
	}
	if includeAntonModeSelection && !includeAntonChannelEvent {
		return nil, fmt.Errorf("Anton mode selection requires Anton channel availability")
	}
	if len(rewardItemIDs) == 0 || len(rewardItemIDs) > currentSpendTimeMaxRewardItems {
		return nil, fmt.Errorf("current spend-time reward count=%d is outside 1..%d", len(rewardItemIDs), currentSpendTimeMaxRewardItems)
	}
	if totalStageSeconds == 0 {
		return nil, fmt.Errorf("current spend-time total stage seconds is zero")
	}
	params := [currentSpendTimeDescriptorParamCount]uint32{}
	for index := 0; index < currentSpendTimeMaxRewardItems; index++ {
		params[index] = currentSpendTimeUnusedParam
	}
	for index, itemID := range rewardItemIDs {
		if itemID == 0 {
			return nil, fmt.Errorf("current spend-time reward[%d] item id is zero", index)
		}
		params[index] = itemID
	}
	params[10] = uint32(len(rewardItemIDs))
	params[11] = totalStageSeconds

	// The two counts are written from the same decision that writes the rows, so
	// a catalog without 2365 (or without the optional 2395 town-NPC row) can
	// never advertise a row count the client would then try to read past the end
	// of the body for.
	baseCatalogCount := uint16(0)
	activeCount := byte(0)
	if includeSpendTime {
		baseCatalogCount++
		activeCount++
	}
	if includeJoustEvent {
		baseCatalogCount++
		activeCount++
	}
	if includeTownNpcEvent {
		baseCatalogCount++
		activeCount++
	}
	if includeAntonChannelEvent {
		baseCatalogCount++
		activeCount++
	}
	if includeAntonModeSelection {
		baseCatalogCount++
		activeCount++
	}
	if dayDungeonEvent != 0 {
		baseCatalogCount++
		activeCount++
	}
	if includeLuke {
		baseCatalogCount++
		activeCount++
	}
	if includeMonthly {
		baseCatalogCount++
		activeCount++
	}

	if includeWeekly {
		baseCatalogCount++
		activeCount++
	}
	if includeEpicRoad {
		baseCatalogCount++
		activeCount++
	}
	if includeOrdinary {
		baseCatalogCount++
		activeCount++
	}
	var writer packetWriter
	writer.writeUint16(baseCatalogCount) // base_catalog_count
	if includeSpendTime && nativeOnlinePanel {
		writeCurrentPCRoomTimePointActivityBase(&writer)
	} else if includeSpendTime {
		writer.writeUint16(uint16(currentSpendTimeEventID))
		writer.writeUint32(currentSpendTimeBaseCatalogValue)
		writer.writeRawDstr(nil) // generic title; the fixed event UI owns visible text
		writer.writeRawDstr(nil) // generic description
		writer.writeByte(1)      // has_extended: installs the native click action below
		writer.writeByte(0)      // generic category flags
		writer.writeByte(0)      // generic presentation flags
		writer.writeRawDstr(nil) // optional title override
		writer.writeRawDstr(nil) // optional subtitle override
		writer.writeRawDstr(nil) // optional description override
		writer.writeUint32(0)    // optional start timestamp
		writer.writeUint32(0)    // optional end timestamp
		writer.writeAsciiDstr(currentSpendTimeEventRoute)
		writer.writeRawDstr(nil) // optional status text
		writer.writeByte(0)      // optional presentation tail
	}
	if includeJoustEvent {
		writer.writeUint16(uint16(currentJoustEventID))
		writer.writeUint32(currentSpendTimeBaseCatalogValue)
		writer.writeRawDstr(nil) // generic title; the fixed joust UI owns visible text
		writer.writeRawDstr(nil) // generic description
		writer.writeByte(0)      // no schedule/action extension: permanently active below
	}
	if includeTownNpcEvent {
		// 与 2365 同构的通用行：不装点击动作、无时间窗，只让客户端把 2395 判成"已开"，
		// 从而满足 `npc/476_jonathan.npc` 的 `[open visible event] 2395`。
		writer.writeUint16(uint16(currentTownNpcVisibleEventID))
		writer.writeUint32(currentSpendTimeBaseCatalogValue)
		writer.writeRawDstr(nil) // generic title; the NPC owns all visible text
		writer.writeRawDstr(nil) // generic description
		writer.writeByte(0)      // no schedule/action extension: permanently active below
	}
	if includeAntonChannelEvent {
		writer.writeUint16(currentAntonChannelEventID)
		writer.writeUint32(currentSpendTimeBaseCatalogValue)
		writer.writeRawDstr(nil)
		writer.writeRawDstr(nil)
		writer.writeByte(0) // no historical schedule or unrelated click action
	}
	if includeAntonModeSelection {
		writer.writeUint16(currentAntonModeSelectionEventID)
		writer.writeUint32(currentSpendTimeBaseCatalogValue)
		writer.writeRawDstr(nil)
		writer.writeRawDstr(nil)
		writer.writeByte(0)
	}
	if dayDungeonEvent != 0 {
		writer.writeUint16(dayDungeonEvent)
		writer.writeUint32(currentSpendTimeBaseCatalogValue)
		writer.writeRawDstr(nil)
		writer.writeRawDstr(nil)
		writer.writeByte(0)
	}
	if includeLuke {
		writer.writeUint16(currentLukeChannelEventID)
		writer.writeUint32(currentSpendTimeBaseCatalogValue)
		writer.writeRawDstr(nil)
		writer.writeRawDstr(nil)
		writer.writeByte(0)
	}
	if includeMonthly {
		writer.writeUint16(currentMonthlyAttendanceEventID)
		writer.writeUint32(currentSpendTimeBaseCatalogValue)
		writer.writeRawDstr(nil)
		writer.writeRawDstr(nil)
		writer.writeByte(0)
	}
	if includeWeekly {
		writer.writeUint16(2208)
		writer.writeUint32(currentSpendTimeBaseCatalogValue)
		writer.writeRawDstr(nil)
		writer.writeRawDstr(nil)
		writer.writeByte(0)
	}
	if includeOrdinary {
		writer.writeUint16(331)
		writer.writeUint32(currentSpendTimeBaseCatalogValue)
		writer.writeRawDstr(nil) // generic name: extended title below owns this menu row
		writeClientNameDstr(&writer, "每天登录选择角色后登记签到，领取对应日期奖励。")
		writer.writeByte(1)                     // has the verified native menu extension
		writer.writeByte(0)                     // category unchanged
		writer.writeByte(4)                     // regular presentation: actual GetStr(38499+4) is [一般]
		writeClientNameDstr(&writer, "每日签到得好礼") // descriptor title -> calendar and window21
		writer.writeRawDstr(nil)
		writer.writeRawDstr(nil)
		writer.writeUint32(0)
		writer.writeUint32(0)
		writer.writeAsciiDstr("attendance daily event/0/0")
		writer.writeRawDstr(nil)
		// Native op108 writes this byte to descriptor+B4. EventListWindow
		// permits that owned descriptor when no PVF menu template exists.
		// This branch is only enabled for the verified ordinary331 catalog.
		writer.writeByte(1) // allow the ordinary331 server menu entry
	}
	if includeEpicRoad {
		writer.writeUint16(currentEpicRoadEventID)
		writer.writeUint32(1)
		writer.writeRawDstr(nil)
		writer.writeRawDstr(nil)
		writer.writeByte(0)
	}
	writer.writeByte(activeCount) // active_count
	if includeSpendTime && nativeOnlinePanel {
		writer.writeUint16(currentPCRoomTimePointEventID)
		for i := 0; i < currentSpendTimeDescriptorParamCount; i++ {
			writer.writeUint32(0)
		}
	} else if includeSpendTime {
		writer.writeUint16(uint16(currentSpendTimeEventID))
		for _, value := range params {
			writer.writeUint32(value)
		}
	}
	if includeJoustEvent {
		writer.writeUint16(uint16(currentJoustEventID))
		for index := 0; index < currentSpendTimeDescriptorParamCount; index++ {
			writer.writeUint32(0)
		}
	}
	if includeTownNpcEvent {
		writer.writeUint16(uint16(currentTownNpcVisibleEventID))
		for index := 0; index < currentSpendTimeDescriptorParamCount; index++ {
			writer.writeUint32(0)
		}
	}
	if includeAntonChannelEvent {
		writer.writeUint16(currentAntonChannelEventID)
		for index := 0; index < currentSpendTimeDescriptorParamCount; index++ {
			writer.writeUint32(0)
		}
	}
	if includeAntonModeSelection {
		writer.writeUint16(currentAntonModeSelectionEventID)
		for index := 0; index < currentSpendTimeDescriptorParamCount; index++ {
			writer.writeUint32(0)
		}
	}
	if dayDungeonEvent != 0 {
		writer.writeUint16(dayDungeonEvent)
		for index := 0; index < currentSpendTimeDescriptorParamCount; index++ {
			writer.writeUint32(0)
		}
	}
	if includeLuke {
		writer.writeUint16(currentLukeChannelEventID)
		for index := 0; index < currentSpendTimeDescriptorParamCount; index++ {
			writer.writeUint32(0)
		}
	}
	if includeMonthly {
		writer.writeUint16(currentMonthlyAttendanceEventID)
		writer.writeUint32(onlineevent.MonthlyRequiredClears) // Actual02301534/01113250 stores required clears.
		for i := 1; i < currentSpendTimeDescriptorParamCount; i++ {
			writer.writeUint32(0)
		}
	}
	if includeWeekly {
		writer.writeUint16(2208)
		for i := 0; i < currentSpendTimeDescriptorParamCount; i++ {
			writer.writeUint32(0)
		}
	}
	if includeEpicRoad {
		writer.writeUint16(currentEpicRoadEventID)
		for i := 0; i < currentSpendTimeDescriptorParamCount; i++ {
			writer.writeUint32(0)
		}
	}
	if includeOrdinary {
		writer.writeUint16(331)
		for i := 0; i < currentSpendTimeDescriptorParamCount; i++ {
			writer.writeUint32(0)
		}
	}
	return writer.bytes(), nil
}

// buildCurrentSpendTimeProgressBody matches current NoPack op1206 dispatch:
//
//	u32 event_id, u8 wide_flag, raw[wide_flag ? 0xA0 : 0x20]
//
// Event 2347 uses the narrow record. Its first two u32 values are authoritative
// elapsed seconds and the number of automatically completed reward stages.
func buildCurrentSpendTimeProgressBody(elapsedSeconds uint64, completedStages uint32) ([]byte, error) {
	if elapsedSeconds > math.MaxUint32 {
		return nil, fmt.Errorf("current spend-time elapsed seconds=%d overflows u32", elapsedSeconds)
	}
	var writer packetWriter
	writer.writeUint32(currentSpendTimeEventID)
	writer.writeByte(0) // narrow 0x20-byte record
	writer.writeUint32(uint32(elapsedSeconds))
	writer.writeUint32(completedStages)
	writer.writeBytes(make([]byte, currentSpendTimeProgressRawSize-8))
	return writer.bytes(), nil
}
