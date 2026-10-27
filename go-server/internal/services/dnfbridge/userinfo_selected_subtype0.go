package dnfbridge

import (
	"encoding/binary"
	"sort"
	"strconv"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func (r *csharpLegacyUserInfoReader) buildCSharpUserInfoSubtype0(character dnfrepo.CharacterRecord, hasCharacter bool, charID uint16, charName string) []byte {
	tail := r.one("legacy_character_subtype0_fields", []string{
		"name_tag_item_id", "creature_field1", "creature_field2", "creature_field3", "creature_field4",
		"creature_buffer", "stamina", "fatigue_penalty", "is_event_character", "pc_room_id",
		"is_private_store", "is_premium_pc_room", "server_group_id", "black_count", "guild_level",
		"chaos_point", "disguise_kind", "is_disguised", "expert_job_type", "expert_job_exp",
		"extra46", "extra47", "extra51", "is_hardcore_mode", "is_hardcore_dead", "hardcore_death_count",
		"user_state_bits", "chat_ban_end_time", "fatigue_update", "return_user_flag", "channel_display_mode",
		"channel_type", "channel_id", "is_return_user", "link_slot_enabled", "link_type_a", "link_type_b",
		"emotion_index", "action_byte", "fatigue_display_update", "costume_flag", "aura_flag",
		"pet_display_flag", "title_display_flag", "pvp_stat_a", "pvp_win_streak", "pvp_lose_streak",
		"pvp_rank_point", "trailing_byte",
	})
	// Character selection starts from the lightweight roster row, which does
	// not carry the relational character_stats map. Reload that authoritative
	// map before writing subtype0 so durable flags such as aura_flag survive a
	// relog instead of falling back to a stale legacy zero.
	if r.service != nil && r.characterID != "" {
		if repos, ok := r.service.repositoryGroup(); ok && repos.Character != nil {
			if fullCharacter, found, err := repos.Character.Load(r.ctx, r.characterID); err == nil && found {
				character.Stats = fullCharacter.Stats
			} else if r.session != nil {
				r.service.logGameEvent(r.session, "game-selected-subtype0-character-stats-load-failed",
					"char_id", r.characterID, "found", found, "err", err)
			}
		}
	}
	// PR #239: inject name_tag_item_id from character stats into the tail row
	// so the subtype0 packet carries it for town name decoration display.
	nameTagFromStats := int64(0)
	if character.Stats != nil {
		nameTagFromStats = character.Stats["name_tag_item_id"]
	}
	if nameTagFromStats > 0 {
		if tail == nil {
			tail = make(dnfrepo.LegacyUserInfoRow)
		}
		if rowU32(tail, "name_tag_item_id", 0) == 0 {
			tail["name_tag_item_id"] = strconv.FormatInt(nameTagFromStats, 10)
		}
	}
	addition := r.one("legacy_character_subtype1_fields", []string{"progress1", "progress2", "skill_tree_index"})

	if !hasCharacter {
		character = dnfrepo.CharacterRecord{CharacterID: strconv.Itoa(int(charID)), Name: charName, Level: 1}
	}
	if charName == "" {
		charName = character.Name
	}
	if charName == "" {
		charName = csharpSelectCharacterName(charID)
	}
	level := byte(character.Level)
	if level == 0 {
		level = 1
	}

	var w packetWriter
	w.writeByte(0)
	w.writeUint16(1)
	w.writeUint16(charID)
	writeClientNameDstr(&w, charName)
	w.writeByte(byte(numericCharacterStat(character.Job)))
	w.writeByte(characterGrowTypeByte(character))
	w.writeByte(level)
	w.writeByte(byte(numericCharacterStatValue(character, "pvp_grade")))
	w.writeByte(byte(numericCharacterStatValue(character, "pvp_rating_grade")))
	w.writeByte(byte(numericCharacterStatValue(character, "user_state")))
	appearances := r.currentSubtype0AppearanceEntries()
	w.writeByte(byte(len(appearances)))
	for _, entry := range appearances {
		writeCSharpAppearanceEntry(&w, entry)
	}
	r.writeCSharpUserInfoSubtype0Tail(&w, tail, addition, character)
	return w.bytes()
}

type csharpAppearanceEntry struct {
	slot          byte
	displayItemID uint32
	state         byte
	flag20        byte
	// linkItemID 是厂商 CharacterAppearanceEntry.FromBytes 偏移 +14 的 LinkItemId
	// （外观列表的"关联物品ID"）：克隆装扮显示被克隆件（displayItemID）时，
	// 这里写回克隆件自己。见 clone_avatar.go 的 cloneAvatarRowLinkItemID。
	linkItemID uint32
}

func (r *csharpLegacyUserInfoReader) currentSubtype0AppearanceEntries() []csharpAppearanceEntry {
	if r == nil || r.service == nil || strings.TrimSpace(r.characterID) == "" {
		return nil
	}
	repos, ok := r.service.repositoryGroup()
	if !ok || repos.Equipment == nil {
		return nil
	}
	record, found, err := repos.Equipment.Load(r.ctx, r.characterID)
	if err != nil {
		if r.session != nil {
			r.service.logGameEvent(r.session, "game-upper-selected-userinfo-subtype0-appearance-load-failed", "character_id", r.characterID, "error", err)
		}
		return nil
	}
	if !found || len(record.Entries) == 0 {
		return nil
	}
	entries := make([]dnfrepo.EquipmentEntry, 0, len(record.Entries))
	for _, entry := range record.Entries {
		if entry.SlotIndex < 0 || entry.SlotIndex > 12 || entry.ItemID <= 0 {
			continue
		}
		entries = append(entries, entry)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].SlotIndex == entries[j].SlotIndex {
			return entries[i].ItemID < entries[j].ItemID
		}
		return entries[i].SlotIndex < entries[j].SlotIndex
	})
	if len(entries) > 0xff {
		entries = entries[:0xff]
	}
	out := make([]csharpAppearanceEntry, 0, len(entries))
	for _, entry := range entries {
		displayID := sceneInventoryUint32FromInt64(entry.ItemID)
		linkItemID := uint32(0)
		// 克隆装扮：厂商 ResolveAppearanceDisplayItemId（Noti2InventoryProjectionBuilder.cs:249-257）
		// 显示被克隆件、ResolveAppearanceLinkItemId（:259-272）把克隆件自己写进 LinkItemId。
		// 持久克隆来源优先于下面那条旧 C# 行的 raw[12:16] 兼容读取
		// （旧行的 ClearAvatarId 就在 43 字节摘要的 +12，本仓 0x77 行不是那个形状）。
		if cloneTarget := cloneAvatarAppearanceLinkedItemID(entry); cloneTarget != 0 {
			displayID = cloneTarget
			linkItemID = cloneAvatarRowLinkItemID(entry)
		} else if entry.SlotIndex <= 9 && len(entry.RawEntry) >= 16 {
			if cloneTarget := binary.LittleEndian.Uint32(entry.RawEntry[12:16]); cloneTarget != 0 {
				displayID = cloneTarget
			}
		}
		out = append(out, csharpAppearanceEntry{
			slot:          byte(entry.SlotIndex),
			displayItemID: displayID,
			state:         appearanceStateFromEquipment(entry),
			flag20:        appearanceFlag20FromEquipment(entry),
			linkItemID:    linkItemID,
		})
	}
	return out
}

func writeCSharpAppearanceEntry(w *packetWriter, entry csharpAppearanceEntry) {
	w.writeByte(entry.slot)
	w.writeUint32(entry.displayItemID)
	w.writeUint32(4)
	w.writeZero(4)
	w.writeByte(entry.state)
	// LinkItemId（厂商 CharacterAppearanceEntry.cs:76 的 FromBytes 偏移 +14）：
	// 非克隆行是 0，与改动前逐字节相同。
	w.writeUint32(entry.linkItemID)
	w.writeUint32(0)
	w.writeByte(entry.flag20)
}

func appearanceStateFromEquipment(entry dnfrepo.EquipmentEntry) byte {
	attr := sceneInventoryExtraByte(entry.Extra, "attr", "equipment_attr", "amplification_attr")
	amplify := sceneInventoryExtraByte(entry.Extra, "amplify_type", "amplification_type")
	return attr*2 + nonzeroByte(amplify)
}

func appearanceFlag20FromEquipment(entry dnfrepo.EquipmentEntry) byte {
	if value := sceneInventoryExtraByte(entry.Extra, "enchant_upgrade_count", "enchant_upgrade", "reinforce"); value != 0 {
		return value
	}
	return currentItemListEquipmentExtData(entry)
}

func nonzeroByte(value byte) byte {
	if value == 0 {
		return 0
	}
	return 1
}

func writeCSharpUserInfoSubtype0Tail(w *packetWriter, tail dnfrepo.LegacyUserInfoRow, addition dnfrepo.LegacyUserInfoRow, character dnfrepo.CharacterRecord) {
	(csharpLegacyUserInfoReader{}).writeCSharpUserInfoSubtype0Tail(w, tail, addition, character)
}

func (r csharpLegacyUserInfoReader) writeCSharpUserInfoSubtype0Tail(w *packetWriter, tail dnfrepo.LegacyUserInfoRow, addition dnfrepo.LegacyUserInfoRow, character dnfrepo.CharacterRecord) {
	// PR #239: name_tag_item_id may live in character.Stats (written by the
	// transactional Cera-shop checkout owner) rather than the legacy subtype0 row.
	nameTagItemID := rowU32(tail, "name_tag_item_id", 0)
	if nameTagItemID == 0 && character.Stats != nil {
		nameTagItemID = uint32(character.Stats["name_tag_item_id"])
	}
	w.writeUint32(nameTagItemID)
	w.writeByte(rowU8(tail, "creature_field1", 0))
	w.writeByte(rowU8(tail, "creature_field2", 0))
	w.writeByte(rowU8(tail, "creature_field3", 0))
	w.writeByte(rowU8(tail, "creature_field4", 0))
	writeFixedBytes(w, []byte(rowString(tail, "creature_buffer", "")), 8)
	w.writeByte(rowU8(tail, "stamina", 0))
	w.writeUint32(rowU32(tail, "fatigue_penalty", 0))
	w.writeByte(rowU8(tail, "is_event_character", byte(numericCharacterStatValue(character, "is_event_character"))))
	w.writeUint32(rowStatU32(tail, character, "pc_room_id", 0x00010001))
	w.writeByte(rowU8(tail, "is_private_store", 0))
	w.writeByte(rowU8(tail, "is_premium_pc_room", 0))
	w.writeByte(rowU8(tail, "server_group_id", 0))
	w.writeUint32(rowU32(tail, "black_count", 0))
	w.writeByte(rowU8(tail, "guild_level", 0))
	w.writeUint32(rowU32(tail, "chaos_point", 0))
	w.writeByte(1)
	w.writeByte(rowU8(tail, "disguise_kind", 0))
	w.writeByte(rowU8(tail, "is_disguised", 0))
	w.writeByte(rowU8(tail, "expert_job_type", byte(numericCharacterStatValue(character, "expert_job_type"))))
	w.writeUint32(rowU32(tail, "expert_job_exp", uint32(numericCharacterStatValue(character, "expert_job_exp"))))
	w.writeByte(rowU8(tail, "extra46", 0))
	w.writeUint32(rowU32(tail, "extra47", 0))
	w.writeUint16(rowU16(tail, "extra51", 0))
	w.writeByte(rowU8(tail, "is_hardcore_mode", 0))
	w.writeByte(rowU8(tail, "is_hardcore_dead", 0))
	w.writeUint16(rowU16(tail, "hardcore_death_count", 0))
	w.writeUint32(rowStatU32(addition, character, "progress1", 0))
	w.writeUint32(rowStatU32(addition, character, "progress2", 0))
	w.writeByte(rowStatU8(tail, character, "user_state_bits", 3))
	w.writeUint32(rowU32(tail, "chat_ban_end_time", 0))
	w.writeByte(100)
	w.writeUint16(rowU16(tail, "fatigue_update", 0))
	w.writeByte(rowStatU8(tail, character, "return_user_flag", 1))
	// Current-client channel triple: u16 display mode, u8 channel kind, u16
	// channel id.
	//
	// The display-mode slot and the channel-id slot keep following the live
	// session channel. That is the pinned current-EXE contract of this record
	// (TestBuildCSharpSelectedUserInfoSubtype0UsesSessionChannelContext), and
	// nothing in the 2026-09-24 evidence questions it.
	//
	// The channel-KIND byte must never be filled from the raw served channel
	// kind. That kind is exactly the value the client classifies channels with
	// (etc/channeluiinfo_chn.etc channel families), so republishing a special
	// kind here - for example the ancient-dungeon channel 121 kind 22, or the
	// death-tower channel 508 kind 22 - gives the client a second place to read
	// a special/arena channel family for the character, and it is the only
	// per-character downlink value that differed between the blocked session
	// (dnf:10005 on channel 121, kind 22) and the working sessions (dnf:10001 on
	// channels 10/11, kind 11) on 2026-09-24. Town operations such as the channel
	// move must not depend on it; the persisted row value
	// (dnf_characters.channel_type, schema default 0) is authoritative here.
	channelDisplayMode := rowU16(tail, "channel_display_mode", 0)
	channelType := rowU8(tail, "channel_type", 0)
	channelID := rowStatU16(tail, character, "channel_id", 2)
	if r.session != nil && r.session.channel.ID > 0 {
		channelID = uint16(r.session.channel.ID)
		channelDisplayMode = channelID
	}
	w.writeUint16(channelDisplayMode)
	w.writeByte(channelType)
	w.writeUint16(channelID)
	w.writeByte(r.skillTreeWireIndex(character))
	w.writeByte(rowU8(tail, "is_return_user", 0))
	w.writeByte(rowU8(tail, "link_slot_enabled", byte(numericCharacterStatValue(character, "link_slot_enabled"))))
	w.writeByte(rowU8(tail, "link_type_a", 0))
	w.writeByte(rowU8(tail, "link_type_b", 0))
	w.writeUint16(statU16(character, "emotion_index", rowU16(tail, "emotion_index", 0)))
	w.writeByte(rowU8(tail, "action_byte", 0))
	w.writeUint16(rowU16(tail, "fatigue_display_update", 0))
	w.writeByte(rowU8(tail, "costume_flag", 0))
	// Aura-skin-slot state is owned by the structured character record.  The
	// legacy subtype0 mirror is an import/protocol-evidence table and can keep
	// a stale zero after op863 has already committed aura_flag=1.
	w.writeByte(statU8(character, currentOpenAuraSkinSlotFlagStat, rowU8(tail, "aura_flag", 0)))
	w.writeByte(rowU8(tail, "pet_display_flag", 0))
	w.writeByte(rowU8(tail, "title_display_flag", 0))
	w.writeUint32(rowU32(tail, "pvp_stat_a", 0))
	w.writeByte(rowU8(tail, "pvp_win_streak", 0))
	w.writeByte(rowU8(tail, "pvp_lose_streak", 0))
	w.writeUint32(rowU32(tail, "pvp_rank_point", 0))
	w.writeByte(rowU8(tail, "trailing_byte", 0))
}
