package mysql

import (
	"context"
	"database/sql"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
)

// mergeCharacterExtraStat 合并一条 `dnf_character_stats` 行到记录里。
//
// mirrorAuthoritative=true（默认）时，mirror 键（grow_type / exp / level 等住在
// `dnf_characters` 列里的键）**不合并**：它们的权威值已经由 mergeCharacterMirrorStats
// 从列上写入，补充表里的同名影子行不得覆盖。返回是否真的写入了。
func mergeCharacterExtraStat(
	record *repository.CharacterRecord,
	key string,
	value int64,
	mirrorAuthoritative bool,
) bool {
	if mirrorAuthoritative && isMySQLCharacterMirrorStatKey(key) {
		return false
	}
	if record.Stats == nil {
		record.Stats = make(map[string]int64)
	}
	record.Stats[key] = value
	return true
}

func loadCharacterSupplement(
	ctx context.Context,
	router mysqlRouter,
	characterID string,
	record *repository.CharacterRecord,
	shadowStats ...*bool,
) error {
	statsTable, err := router.readTable(mysqlCharacterStatsTable, characterID)
	if err != nil {
		return err
	}
	locationTable, err := router.readTable(mysqlCharacterLocationTable, characterID)
	if err != nil {
		return err
	}
	rosterTable, err := router.readTable(mysqlCharacterRosterTable, characterID)
	if err != nil {
		return err
	}
	equipTable, err := router.readTable(mysqlCharacterRosterEquipTable, characterID)
	if err != nil {
		return err
	}
	listTable, err := router.readTable(mysqlCharacterRosterListTable, characterID)
	if err != nil {
		return err
	}

	rows, err := router.db.QueryContext(
		ctx,
		router.selectQuery("SELECT stat_key, stat_value FROM "+statsTable+" WHERE character_id = ? ORDER BY stat_key"),
		characterID,
	)
	if err != nil {
		return err
	}
	// 权威列优先：mirror 键（grow_type 等）的权威值已经由
	// mergeCharacterMirrorStats 从 `dnf_characters` 的列写进 Stats，而
	// `dnf_character_stats` 按设计只保存非 mirror 的"额外"键
	// （replaceCharacterExtraStats 主动跳过 mirror 键，所以线上该表永远没有
	// grow_type 行）。历史 stats_json 迁移或手工 SQL 仍可能留下同名影子行，
	// 旧实现让它在 mirror 之后覆盖权威列 —— 对 grow_type 而言那会把一个已转职
	// 角色静默改回未转职，分支技能随即被判"该方向不可学"。默认跳过影子行。
	mirrorAuthoritative := characterMirrorStatsAuthoritative()
	for rows.Next() {
		var key string
		var value int64
		if err := rows.Scan(&key, &value); err != nil {
			rows.Close()
			return err
		}
		if isMySQLCharacterMirrorStatKey(key) && len(shadowStats) == 1 && shadowStats[0] != nil {
			*shadowStats[0] = true
		}
		mergeCharacterExtraStat(record, key, value, mirrorAuthoritative)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	err = router.db.QueryRowContext(
		ctx,
		router.selectQuery("SELECT channel_id, town_id, dungeon_id, room_id FROM "+locationTable+" WHERE character_id = ?"),
		characterID,
	).Scan(
		&record.Location.ChannelID,
		&record.Location.TownID,
		&record.Location.DungeonID,
		&record.Location.RoomID,
	)
	if err != nil && !errorsIsNoRows(err) {
		return err
	}

	if err := loadCharacterRoster(ctx, router, rosterTable, equipTable, listTable, characterID, &record.Roster); err != nil {
		return err
	}
	return nil
}

func errorsIsNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

func saveCharacterSupplement(
	ctx context.Context,
	database SQLDB,
	router mysqlRouter,
	characterID string,
	record repository.CharacterRecord,
	fields map[repository.CharacterField]struct{},
	previousStats ...map[string]int64,
) error {
	if _, ok := fields[repository.CharacterFieldStats]; ok {
		statsTable, err := router.writeTable(mysqlCharacterStatsTable, characterID)
		if err != nil {
			return err
		}
		var saveErr error
		if len(previousStats) == 1 {
			saveErr = saveChangedCharacterStats(ctx, database, statsTable, characterID, previousStats[0], record.Stats)
		} else {
			saveErr = replaceCharacterExtraStats(ctx, database, statsTable, characterID, record.Stats)
		}
		if saveErr != nil {
			return saveErr
		}
	}
	if _, ok := fields[repository.CharacterFieldLocation]; ok {
		locationTable, err := router.writeTable(mysqlCharacterLocationTable, characterID)
		if err != nil {
			return err
		}
		columns := []string{"character_id", "channel_id", "town_id", "dungeon_id", "room_id"}
		updates := []string{
			updateValue("channel_id"),
			updateValue("town_id"),
			updateValue("dungeon_id"),
			updateValue("room_id"),
		}
		if _, err := database.ExecContext(
			ctx,
			buildUpsert(locationTable, columns, updates),
			characterID,
			record.Location.ChannelID,
			record.Location.TownID,
			record.Location.DungeonID,
			record.Location.RoomID,
		); err != nil {
			return err
		}
	}
	if _, ok := fields[repository.CharacterFieldRoster]; ok {
		rosterTable, err := router.writeTable(mysqlCharacterRosterTable, characterID)
		if err != nil {
			return err
		}
		equipTable, err := router.writeTable(mysqlCharacterRosterEquipTable, characterID)
		if err != nil {
			return err
		}
		listTable, err := router.writeTable(mysqlCharacterRosterListTable, characterID)
		if err != nil {
			return err
		}
		if err := replaceCharacterRoster(ctx, database, rosterTable, equipTable, listTable, characterID, record.Roster); err != nil {
			return err
		}
	}
	return nil
}

func replaceCharacterExtraStats(
	ctx context.Context,
	database SQLDB,
	table, characterID string,
	stats map[string]int64,
) error {
	// 升级事件和领取审计是永久事实，不能随普通数值整包替换被旧快照删除。
	if _, err := database.ExecContext(ctx, "DELETE FROM "+table+" WHERE character_id = ? AND LEFT(stat_key, ?) <> ? AND LEFT(stat_key, ?) <> ?", characterID,
		len(repository.LevelUpEventStatPrefix), repository.LevelUpEventStatPrefix,
		len(repository.LevelUpRewardStatPrefix), repository.LevelUpRewardStatPrefix); err != nil {
		return err
	}
	known := make(map[string]struct{}, len(mysqlCharacterStatColumns))
	for _, stat := range mysqlCharacterStatColumns {
		known[stat.key] = struct{}{}
	}
	query := "INSERT INTO " + table + " (character_id, stat_key, stat_value) VALUES (?, ?, ?)"
	for _, key := range sortedStringKeys(stats) {
		if _, ok := known[key]; ok {
			continue
		}
		keyQuery := query
		if _, event := repository.LevelUpEventLevel(key); event {
			keyQuery += " ON DUPLICATE KEY UPDATE stat_value = IF(stat_value = 0, VALUES(stat_value), IF(stat_value < 0 OR VALUES(stat_value) >= 0, stat_value, VALUES(stat_value)))"
		} else if strings.HasPrefix(key, repository.LevelUpRewardStatPrefix) {
			keyQuery += " ON DUPLICATE KEY UPDATE stat_value = IF(stat_value = 0, VALUES(stat_value), stat_value)"
		}
		if _, err := database.ExecContext(ctx, keyQuery, characterID, key, stats[key]); err != nil {
			return err
		}
	}
	return nil
}

func characterRosterColumns() []string {
	return []string{
		"character_id",
		"header_unk_a",
		"header_unk_b",
		"header_total_or_slot_limit",
		"header_used_or_remain",
		"header_selected_or_page",
		"header_roster_state",
		"header_page_count",
		"header_roster_flag",
		"header_roster_value_a",
		"header_roster_value_b",
		"entry_byte_a",
		"entry_packed_job_grow",
		"entry_byte_c",
		"entry_field_2cc",
		"entry_state0",
		"entry_time_a",
		"entry_time_b",
		"entry_value0",
		"entry_value1",
		"entry_value2",
		"entry_reserved_a",
		"entry_reserved_b",
		"entry_value3",
		"entry_object_id",
		"entry_flag0_eq1",
		"entry_special_status_flag",
		"entry_value5",
		"entry_display_flags",
		"entry_reserved_c",
		"entry_reserved_d",
		"entry_value6",
		"entry_flag1_nonzero",
		"entry_bool_a_eq1",
		"entry_bool_b_eq1",
		"entry_bool_c_eq1",
		"entry_flag2_nonzero",
		"entry_flag3_nonzero",
		"entry_flag4_nonzero",
		"entry_flag5_nonzero",
		"entry_value7",
		"entry_flag6_eq1",
	}
}

func characterRosterArgs(characterID string, roster repository.CharacterRoster) []any {
	header := roster.Header
	entry := roster.Entry
	return []any{
		characterID,
		header.UnkA,
		header.UnkB,
		header.TotalOrSlotLimit,
		header.UsedOrRemain,
		header.SelectedOrPage,
		header.RosterState,
		header.PageCount,
		header.RosterFlag,
		header.RosterValueA,
		header.RosterValueB,
		entry.ByteA,
		entry.PackedJobGrow,
		entry.ByteC,
		entry.Field2CC,
		entry.State0,
		entry.TimeA,
		entry.TimeB,
		entry.Value0,
		entry.Value1,
		entry.Value2,
		entry.ReservedA,
		entry.ReservedB,
		entry.Value3,
		entry.ObjectID,
		entry.Flag0Eq1,
		entry.SpecialStatusFlag,
		entry.Value5,
		entry.DisplayFlags,
		entry.ReservedC,
		entry.ReservedD,
		entry.Value6,
		entry.Flag1Nonzero,
		entry.BoolAEq1,
		entry.BoolBEq1,
		entry.BoolCEq1,
		entry.Flag2Nonzero,
		entry.Flag3Nonzero,
		entry.Flag4Nonzero,
		entry.Flag5Nonzero,
		entry.Value7,
		entry.Flag6Eq1,
	}
}

func characterRosterScanDest(roster *repository.CharacterRoster) []any {
	header := &roster.Header
	entry := &roster.Entry
	return []any{
		&header.UnkA,
		&header.UnkB,
		&header.TotalOrSlotLimit,
		&header.UsedOrRemain,
		&header.SelectedOrPage,
		&header.RosterState,
		&header.PageCount,
		&header.RosterFlag,
		&header.RosterValueA,
		&header.RosterValueB,
		&entry.ByteA,
		&entry.PackedJobGrow,
		&entry.ByteC,
		&entry.Field2CC,
		&entry.State0,
		&entry.TimeA,
		&entry.TimeB,
		&entry.Value0,
		&entry.Value1,
		&entry.Value2,
		&entry.ReservedA,
		&entry.ReservedB,
		&entry.Value3,
		&entry.ObjectID,
		&entry.Flag0Eq1,
		&entry.SpecialStatusFlag,
		&entry.Value5,
		&entry.DisplayFlags,
		&entry.ReservedC,
		&entry.ReservedD,
		&entry.Value6,
		&entry.Flag1Nonzero,
		&entry.BoolAEq1,
		&entry.BoolBEq1,
		&entry.BoolCEq1,
		&entry.Flag2Nonzero,
		&entry.Flag3Nonzero,
		&entry.Flag4Nonzero,
		&entry.Flag5Nonzero,
		&entry.Value7,
		&entry.Flag6Eq1,
	}
}

func loadCharacterRoster(
	ctx context.Context,
	router mysqlRouter,
	rosterTable, equipTable, listTable, characterID string,
	roster *repository.CharacterRoster,
) error {
	columns := characterRosterColumns()[1:]
	query := router.selectQuery("SELECT " + quotedColumns(columns) + " FROM " + rosterTable + " WHERE character_id = ?")
	if err := router.db.QueryRowContext(ctx, query, characterID).Scan(characterRosterScanDest(roster)...); err != nil {
		if errorsIsNoRows(err) {
			return nil
		}
		return err
	}
	equipmentRows, err := router.db.QueryContext(
		ctx,
		router.selectQuery("SELECT slot, item_id_or_icon, raw_entry, packed_flags, optional_id_or_expire, aux_value, aux_flag FROM "+equipTable+" WHERE character_id = ? ORDER BY ordinal"),
		characterID,
	)
	if err != nil {
		return err
	}
	for equipmentRows.Next() {
		var row repository.CharacterRosterEquipSummary
		var rawEntry []byte
		if err := equipmentRows.Scan(
			&row.Slot,
			&row.ItemIDOrIcon,
			&rawEntry,
			&row.PackedFlags,
			&row.OptionalIDOrExpire,
			&row.AuxValue,
			&row.AuxFlag,
		); err != nil {
			equipmentRows.Close()
			return err
		}
		row.RawEntry = append([]byte(nil), rawEntry...)
		roster.Entry.EquipSummary = append(roster.Entry.EquipSummary, row)
	}
	if err := equipmentRows.Err(); err != nil {
		equipmentRows.Close()
		return err
	}
	if err := equipmentRows.Close(); err != nil {
		return err
	}
	listRows, err := router.db.QueryContext(
		ctx,
		router.selectQuery("SELECT list_name, int_value FROM "+listTable+" WHERE character_id = ? ORDER BY list_name, ordinal"),
		characterID,
	)
	if err != nil {
		return err
	}
	defer listRows.Close()
	for listRows.Next() {
		var listName string
		var value int64
		if err := listRows.Scan(&listName, &value); err != nil {
			return err
		}
		switch listName {
		case "flags":
			roster.Entry.Flags = append(roster.Entry.Flags, value)
		case "linked_ids":
			roster.Entry.LinkedIDBlock = append(roster.Entry.LinkedIDBlock, value)
		}
	}
	if err := listRows.Err(); err != nil {
		return err
	}
	return nil
}

func replaceCharacterRoster(
	ctx context.Context,
	database SQLDB,
	rosterTable, equipTable, listTable, characterID string,
	roster repository.CharacterRoster,
) error {
	if _, err := database.ExecContext(ctx, "DELETE FROM "+equipTable+" WHERE character_id = ?", characterID); err != nil {
		return err
	}
	if _, err := database.ExecContext(ctx, "DELETE FROM "+listTable+" WHERE character_id = ?", characterID); err != nil {
		return err
	}
	if isZeroCharacterRoster(roster) {
		_, err := database.ExecContext(ctx, "DELETE FROM "+rosterTable+" WHERE character_id = ?", characterID)
		return err
	}
	columns := characterRosterColumns()
	updates := make([]string, 0, len(columns)-1)
	for _, column := range columns[1:] {
		updates = append(updates, updateValue(column))
	}
	if _, err := database.ExecContext(ctx, buildUpsert(rosterTable, columns, updates), characterRosterArgs(characterID, roster)...); err != nil {
		return err
	}
	equipmentQuery := "INSERT INTO " + equipTable + " (character_id, ordinal, slot, item_id_or_icon, raw_entry, packed_flags, optional_id_or_expire, aux_value, aux_flag) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)"
	for index, row := range roster.Entry.EquipSummary {
		if _, err := database.ExecContext(
			ctx,
			equipmentQuery,
			characterID,
			index,
			row.Slot,
			row.ItemIDOrIcon,
			row.RawEntry,
			row.PackedFlags,
			row.OptionalIDOrExpire,
			row.AuxValue,
			row.AuxFlag,
		); err != nil {
			return err
		}
	}
	listQuery := "INSERT INTO " + listTable + " (character_id, list_name, ordinal, int_value) VALUES (?, ?, ?, ?)"
	for index, value := range roster.Entry.LinkedIDBlock {
		if _, err := database.ExecContext(ctx, listQuery, characterID, "linked_ids", index, value); err != nil {
			return err
		}
	}
	for index, value := range roster.Entry.Flags {
		if _, err := database.ExecContext(ctx, listQuery, characterID, "flags", index, value); err != nil {
			return err
		}
	}
	return nil
}
