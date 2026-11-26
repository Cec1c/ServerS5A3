package mysql

import (
	"context"
	"database/sql"
	"strconv"

	"longheng.io/server/internal/modules/dnf/repository"
)

var _ repository.CharacterWeaponSkinStateReader = (*mysqlCharStore)(nil)

const (
	mysqlWeaponSkinSampleID     = 101609901
	mysqlWeaponSkinSelectedStat = "weapon_skin_demo_selected"
	mysqlWeaponSkinSampleOwned  = "weapon_skin_demo_beiyun_owned"
	mysqlWeaponSkinOwnedPrefix  = "weapon_skin_owned_"
)

// LoadCharacterWeaponSkinState performs at most two narrow primary-key lookups
// (one when no weapon skin is selected, two when a weapon skin is active) to
// load Job + selected/ownership weapon-skin stats without hydrating full stats.
func (s *mysqlCharStore) LoadCharacterWeaponSkinState(ctx context.Context, id string) (repository.CharacterRecord, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return repository.CharacterRecord{}, false, err
	}
	if s.router.lockReads || s.trackStatChanges || !s.router.environment().mirrorStatsAuthoritative {
		return s.Load(ctx, id)
	}
	table, err := s.router.readTable(mysqlCharTable, id)
	if err != nil {
		return repository.CharacterRecord{}, false, err
	}
	statsTable, err := s.router.readTable(mysqlCharacterStatsTable, id)
	if err != nil {
		return repository.CharacterRecord{}, false, err
	}
	query := "SELECT c.job, x.stat_value FROM " + table + " c LEFT JOIN " + statsTable +
		" x ON x.character_id = ? AND x.stat_key = ? AND BINARY x.stat_key = BINARY ? WHERE c.character_id = ?"
	var job string
	var selected sql.NullInt64
	if err := s.router.db.QueryRowContext(ctx, query, id, mysqlWeaponSkinSelectedStat, mysqlWeaponSkinSelectedStat, id).Scan(&job, &selected); err != nil {
		found, scanErr := scanErr(err)
		return repository.CharacterRecord{}, found, scanErr
	}
	record := repository.CharacterRecord{
		CharacterID: id,
		Job:         job,
	}
	if !selected.Valid || selected.Int64 <= 0 {
		return record, true, nil
	}
	ownedKey := mysqlWeaponSkinSampleOwned
	if selected.Int64 != mysqlWeaponSkinSampleID {
		ownedKey = mysqlWeaponSkinOwnedPrefix + strconv.FormatInt(selected.Int64, 10)
	}
	ownedQuery := "SELECT stat_value FROM " + statsTable + " WHERE character_id = ? AND stat_key = ? AND BINARY stat_key = BINARY ?"
	var owned sql.NullInt64
	if err := s.router.db.QueryRowContext(ctx, ownedQuery, id, ownedKey, ownedKey).Scan(&owned); err != nil {
		found, scanErr := scanErr(err)
		if scanErr != nil {
			return repository.CharacterRecord{}, false, scanErr
		}
		if !found {
			owned.Valid = false
		}
	}
	record.Stats = map[string]int64{
		mysqlWeaponSkinSelectedStat: selected.Int64,
	}
	if owned.Valid {
		record.Stats[ownedKey] = owned.Int64
	}
	return record, true, nil
}
