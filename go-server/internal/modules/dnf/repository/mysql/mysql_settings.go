// 本文件实现 DNF 设置仓储的 MySQL 字段化读写。
package mysql

import (
	"context"
	"database/sql"
	"sync"

	"longheng.io/server/internal/modules/dnf/repository"
)

const mysqlSettingsTable = "settings"

type cachedSettingsRecord struct {
	record repository.SettingsRecord
	found  bool
}

type mysqlSettingsStore struct {
	mysqlStoreBase
	cacheLockedReads     bool
	lockedCacheMu        sync.Mutex
	lockedCacheGen       uint64
	lockedWritesInFlight int
	lockedCache          map[string]cachedSettingsRecord
}

// Load 按 scope 从 MySQL 读取 DNF 设置记录。
func (s *mysqlSettingsStore) Load(ctx context.Context, scope string) (repository.SettingsRecord, bool, error) {
	var loadGen uint64
	var allowCacheFill bool
	if s.cacheLockedReads && s.router.lockReads {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return repository.SettingsRecord{}, false, err
			}
		}
		s.lockedCacheMu.Lock()
		if s.lockedWritesInFlight == 0 {
			if entry, ok := s.lockedCache[scope]; ok {
				s.lockedCacheMu.Unlock()
				return repository.CloneSettings(entry.record), entry.found, nil
			}
			loadGen = s.lockedCacheGen
			allowCacheFill = true
		}
		s.lockedCacheMu.Unlock()
	}
	table, err := s.router.readTable(mysqlSettingsTable, scope)
	if err != nil {
		return repository.SettingsRecord{}, false, err
	}
	valuesTable, err := s.router.readTable(mysqlSettingValuesTable, scope)
	if err != nil {
		return repository.SettingsRecord{}, false, err
	}
	query := s.router.selectQuery("SELECT scope, updated_at FROM " + table + " WHERE scope = ?")
	var record repository.SettingsRecord
	var updatedAt sql.NullTime
	err = s.router.db.QueryRowContext(ctx, query, scope).Scan(
		&record.Scope,
		&updatedAt,
	)
	if err != nil {
		ok, scanErr := scanErr(err)
		if scanErr == nil && !ok && allowCacheFill {
			s.lockedCacheMu.Lock()
			if s.lockedWritesInFlight == 0 && s.lockedCacheGen == loadGen {
				if s.lockedCache == nil {
					s.lockedCache = make(map[string]cachedSettingsRecord, 2)
				}
				s.lockedCache[scope] = cachedSettingsRecord{found: false}
			}
			s.lockedCacheMu.Unlock()
		}
		return repository.SettingsRecord{}, ok, scanErr
	}
	record.Values, err = loadStringMap(
		ctx,
		s.router.db,
		s.router.selectQuery("SELECT entry_key, entry_value FROM "+valuesTable+" WHERE scope = ? ORDER BY entry_key"),
		scope,
	)
	if err != nil {
		return repository.SettingsRecord{}, false, err
	}
	record.UpdatedAt = scanTime(updatedAt)
	cloned := repository.CloneSettings(record)
	if allowCacheFill {
		s.lockedCacheMu.Lock()
		if s.lockedWritesInFlight == 0 && s.lockedCacheGen == loadGen {
			if s.lockedCache == nil {
				s.lockedCache = make(map[string]cachedSettingsRecord, 2)
			}
			s.lockedCache[scope] = cachedSettingsRecord{
				record: repository.CloneSettings(cloned),
				found:  true,
			}
		}
		s.lockedCacheMu.Unlock()
	}
	return cloned, true, nil
}

// Save 保存完整 DNF 设置记录到 MySQL。
func (s *mysqlSettingsStore) Save(ctx context.Context, record repository.SettingsRecord) error {
	return s.SaveFields(ctx, record, repository.AllSettingsFields()...)
}

// SaveFields 保存 DNF 设置指定字段到 MySQL。
// 当前只有 Values 字段，保留字段化接口是为了和其他玩家仓储统一。
func (s *mysqlSettingsStore) SaveFields(ctx context.Context, record repository.SettingsRecord, fields ...repository.SettingsField) error {
	scope, err := requireRecordKey(repository.SettingsKey, record, "settings")
	if err != nil {
		return err
	}
	fields = repository.SettingsFields.Normalize(fields)
	if len(fields) == 0 {
		return nil
	}
	if s.cacheLockedReads {
		s.lockedCacheMu.Lock()
		s.lockedCacheGen++
		s.lockedWritesInFlight++
		delete(s.lockedCache, scope)
		s.lockedCacheMu.Unlock()
		defer func() {
			s.lockedCacheMu.Lock()
			s.lockedCacheGen++
			s.lockedWritesInFlight--
			delete(s.lockedCache, scope)
			s.lockedCacheMu.Unlock()
		}()
	}
	table, err := s.router.writeTable(mysqlSettingsTable, scope)
	if err != nil {
		return err
	}
	valuesTable, err := s.router.writeTable(mysqlSettingValuesTable, scope)
	if err != nil {
		return err
	}
	columns := []string{"scope", "updated_at"}
	args := []any{scope, timeOrNow(record.UpdatedAt, s.router.now)}
	updates := []string{updateValue("updated_at")}
	return withMySQLWriteExecutor(ctx, s.router.db, func(database SQLDB) error {
		if _, execErr := database.ExecContext(ctx, buildUpsert(table, columns, updates), args...); execErr != nil {
			return execErr
		}
		return replaceStringMap(ctx, database, valuesTable, "scope", scope, record.Values)
	})
}
