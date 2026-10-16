package dnfbridge

import (
	"context"
	"errors"
	"fmt"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
)

var (
	errDungeonContextListCount     = errors.New("dnf dungeon-context list count exceeds current EXE range")
	errDungeonContextGroupCount    = errors.New("dnf dungeon-context group count exceeds current EXE range")
	errDungeonContextOptionalCount = errors.New("dnf dungeon-context optional-table count exceeds current EXE range")
	errDungeonContextPairCount     = errors.New("dnf dungeon-context pair count exceeds current EXE range")
	errDungeonContextBoundedMode   = errors.New("dnf dungeon-context bounded mode exceeds current EXE range")
	errDungeonContextCappedValue   = errors.New("dnf dungeon-context capped value exceeds current EXE range")
)

const currentDungeonContextMsgID = uint16(dnfenum.CmdPacketUseLotteryItem)

type currentDungeonContextGroupRow struct {
	ValueA uint32
	ValueB uint32
	ValueC uint32
}

type currentDungeonContextGroup struct {
	ObjectOrActorKey uint16
	Rows             []currentDungeonContextGroupRow
}

type currentDungeonContextOptionalRow struct {
	Key    uint32
	Value0 uint16
	Value1 uint16
}

type currentDungeonContextPair struct {
	Key   uint32
	Value uint32
}

type currentDungeonContext struct {
	Value0               uint32
	Value1               uint32
	List0                []uint16
	List1                []uint16
	Groups               []currentDungeonContextGroup
	ContextValue0        uint32
	ContextValue1        uint16
	ContextValue2        uint16
	ContextValue3        uint32
	OptionalTablePresent bool
	OptionalRows         []currentDungeonContextOptionalRow
	BooleanLikeValue     byte
	LargeContextValue0   uint32
	LargeContextValue1   uint32
	BoundedMode          byte
	CappedValue          uint16
	Pairs                []currentDungeonContextPair
}

func (packet currentDungeonContext) Build() ([]byte, error) {
	if len(packet.List0) > 8 || len(packet.List1) > 8 {
		return nil, fmt.Errorf("%w: list0=%d list1=%d", errDungeonContextListCount, len(packet.List0), len(packet.List1))
	}
	if len(packet.Groups) > int(^byte(0)) {
		return nil, fmt.Errorf("%w: count=%d", errDungeonContextGroupCount, len(packet.Groups))
	}
	for groupIndex, group := range packet.Groups {
		if len(group.Rows) > int(^byte(0)) {
			return nil, fmt.Errorf("%w: group=%d rows=%d", errDungeonContextGroupCount, groupIndex, len(group.Rows))
		}
	}
	if len(packet.OptionalRows) > int(^byte(0)) {
		return nil, fmt.Errorf("%w: count=%d", errDungeonContextOptionalCount, len(packet.OptionalRows))
	}
	if !packet.OptionalTablePresent && len(packet.OptionalRows) != 0 {
		return nil, fmt.Errorf("%w: rows=%d without table-present flag", errDungeonContextOptionalCount, len(packet.OptionalRows))
	}
	if len(packet.Pairs) > int(^byte(0)) {
		return nil, fmt.Errorf("%w: count=%d", errDungeonContextPairCount, len(packet.Pairs))
	}
	if packet.BoundedMode >= 8 {
		return nil, fmt.Errorf("%w: value=%d", errDungeonContextBoundedMode, packet.BoundedMode)
	}
	if packet.CappedValue > 100 {
		return nil, fmt.Errorf("%w: value=%d", errDungeonContextCappedValue, packet.CappedValue)
	}

	var writer packetWriter
	writer.writeUint32(packet.Value0)
	writer.writeUint32(packet.Value1)
	writer.writeByte(byte(len(packet.List0)))
	for _, value := range packet.List0 {
		writer.writeUint16(value)
	}
	writer.writeByte(byte(len(packet.List1)))
	for _, value := range packet.List1 {
		writer.writeUint16(value)
	}
	writer.writeByte(byte(len(packet.Groups)))
	for _, group := range packet.Groups {
		writer.writeUint16(group.ObjectOrActorKey)
		writer.writeByte(byte(len(group.Rows)))
		for _, row := range group.Rows {
			writer.writeUint32(row.ValueA)
			writer.writeUint32(row.ValueB)
			writer.writeUint32(row.ValueC)
		}
	}
	writer.writeUint32(packet.ContextValue0)
	writer.writeUint16(packet.ContextValue1)
	writer.writeUint16(packet.ContextValue2)
	writer.writeUint32(packet.ContextValue3)
	if packet.OptionalTablePresent {
		writer.writeByte(1)
		writer.writeByte(byte(len(packet.OptionalRows)))
		for _, row := range packet.OptionalRows {
			writer.writeUint32(row.Key)
			writer.writeUint16(row.Value0)
			writer.writeUint16(row.Value1)
		}
	} else {
		writer.writeByte(0)
	}
	writer.writeByte(packet.BooleanLikeValue)
	writer.writeUint32(packet.LargeContextValue0)
	writer.writeUint32(packet.LargeContextValue1)
	writer.writeByte(packet.BoundedMode)
	writer.writeUint16(packet.CappedValue)
	writer.writeByte(byte(len(packet.Pairs)))
	for _, pair := range packet.Pairs {
		writer.writeUint32(pair.Key)
		writer.writeUint32(pair.Value)
	}
	return writer.bytes(), nil
}

func (s *Service) sendCurrentDungeonContextOp27(session *gameSession, source string) error {
	packet := currentDungeonContext{}
	// Current EXE 0x203a520 reads this u16 and 0x1c55230 stores it in
	// the despair tower controller (getter 0x36252d0), clamped to 1..100.
	// Read the committed character, including after reconnect/return.
	if session != nil && session.selectedCharacterID != 0 {
		ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
		character, err := s.dungeonCharacter(ctx, session.selectedCharacterID, session)
		cancel()
		if err != nil {
			return err
		}
		floor, err := dnfdungeon.DespairNextFloor(character.Stats)
		if err != nil {
			return err
		}
		// Zero already displays floor one through the client's 1..100 clamp.
		// Preserve the existing empty-context bytes until a clear is committed.
		if floor > 1 {
			packet.ContextValue1 = uint16(floor)
		}
	}
	// 深渊派对准入的 **blocked 槽位**就写在这条下发体的 `List0` 上：本仓
	// `currentDungeonContext` 的 `List0`（`u8 条数 + N×u16`）与厂商
	// `EnterSelectDungeonStateBuilder.BuildA21EnterSelectDungeon` 的
	// `blockedCount + blockedCount×u16 slot` 在**同一偏移（body[8]）**、同一形状。
	// 默认档（`DNFBRIDGE_HELL_BLOCKED_SLOTS` 未设置）恒为 nil ⇒ 下发体逐字节等于今天
	// 那 37 字节全零体。见 dungeon_hell_blocked_slots.go。
	blockedSlots := s.hellBlockedSlotNumbers(session)
	packet.List0 = blockedSlots
	body, err := packet.Build()
	if err != nil {
		return err
	}
	s.logGameEvent(session, "game-upper-current-dungeon-context-op27-send",
		"source", source,
		"char_id", session.selectedCharacterID,
		"msg_id", currentDungeonContextMsgID,
		"classification", 0,
		"body_len", len(body),
		"list0_count", len(blockedSlots),
		"list1_count", 0,
		"group_count", 0,
		"optional_table_present", false,
		"pair_count", 0,
		"hell_blocked_slots", formatHellBlockedSlotNumbers(blockedSlots),
		"hell_blocked_slots_env", hellBlockedSlotsEnv,
		"hell_blocked_slots_mode", s.hellBlockedSlotsGate().String(),
		"hell_blocked_pvf_status", session.hellBlocked.PvfStatus,
		"hell_blocked_area", session.hellBlocked.AreaID,
		"body_source", "current_exe_sub_1D868B0_typed_minimum",
		"despair_next_floor", packet.ContextValue1,
		"context_source", "persisted_character_despair_highest_cleared")
	if err := s.sendGameUpperRawClass(session, currentDungeonContextMsgID, body, 0); err != nil {
		return err
	}
	if len(blockedSlots) > 0 {
		// 只在这一条**真的带上了** blocked 槽位时才记账：下一次进图/开页不会重复下发。
		markHellBlockedSlotsDelivered(session)
	}
	return nil
}
