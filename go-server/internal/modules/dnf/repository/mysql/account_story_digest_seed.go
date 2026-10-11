// 本文件实现"账号级剧情回顾记录"的一次性播种迁移。
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
)

const (
	// mysqlAccountStoryDigestSeedScope/mysqlAccountStoryDigestSeedKey 是这次
	// 一次性播种的完成标记。它落在既有 settings/entry 表(dnf_setting_values)
	// 里，不新建表、不新建一套存储；标记存在即认为"升级前的账号都已播种过"，
	// 之后启动不再重复执行。
	mysqlAccountStoryDigestSeedScope = "dnf.repository.migrations"
	mysqlAccountStoryDigestSeedKey   = "account_story_digest_viewed_level_seed"
	mysqlAccountStoryDigestSeedDone  = "1"
)

// SeedAccountStoryDigestViewedLevel 为升级前就存在的账号播种账号级
// story_digest_viewed_level = 该账号下角色的最高等级。
//
// 目的：新口径生效前已经存在的账号（dnf:10001/10005/10006/10011/10012 等）
// 不应该再被强制播放一次剧情回顾。播种后账号级记录已经覆盖其所有角色的当前
// 等级，选角路径会直接下发角色当前等级（客户端判定"同一段"⇒ 不播）。
//
// 只在写库上执行，且只在标记缺失时执行一次；播种 SQL 自身也用
// GREATEST(旧值, 新值) 写账号元数据，所以即使被手工重跑（删掉标记行）也只会
// 抬升、不会回退任何账号的记录。新账号/新角色不在播种范围内，仍然按
// "第一次看过就记一次"的自然流程走。
//
// 记忆点（运维用）：想再跑一次播种，删掉写库里的标记行即可，例如
// DELETE FROM dnf_setting_values WHERE scope='dnf.repository.migrations'
//
//	AND entry_key='account_story_digest_viewed_level_seed';
func SeedAccountStoryDigestViewedLevel(ctx context.Context, database SQLDB, options SchemaOptions) error {
	if !options.AutoCreate {
		return nil
	}
	if database == nil {
		return ErrMySQLDBRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tablePrefix, err := normTablePrefix(options.TablePrefix)
	if err != nil {
		return err
	}
	databases, err := repository.ValidateDatabases(options.DatabasePlan.SchemaDatabases())
	if err != nil {
		return err
	}
	for _, databaseName := range databases {
		done, err := accountStoryDigestSeedApplied(ctx, database, databaseName, tablePrefix)
		if err != nil {
			return fmt.Errorf("read account story digest seed marker in %s: %w", databaseName, err)
		}
		if done {
			continue
		}
		if err := seedAccountStoryDigestViewedLevel(ctx, database, databaseName, tablePrefix); err != nil {
			return fmt.Errorf("seed account story digest viewed level in %s: %w", databaseName, err)
		}
		if err := markAccountStoryDigestSeedApplied(ctx, database, databaseName, tablePrefix); err != nil {
			return fmt.Errorf("mark account story digest seed in %s: %w", databaseName, err)
		}
	}
	return nil
}

func accountStoryDigestSeedApplied(
	ctx context.Context,
	database SQLDB,
	databaseName, tablePrefix string,
) (bool, error) {
	table := mysqlTable(databaseName, tablePrefix, mysqlSettingValuesTable)
	query := "SELECT " + quoteSQLIdentifier("entry_value") +
		" FROM " + table +
		" WHERE " + quoteSQLIdentifier("scope") + " = ? AND " + quoteSQLIdentifier("entry_key") + " = ?"
	var value string
	err := database.QueryRowContext(ctx, query, mysqlAccountStoryDigestSeedScope, mysqlAccountStoryDigestSeedKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(value) == mysqlAccountStoryDigestSeedDone, nil
}

func seedAccountStoryDigestViewedLevel(
	ctx context.Context,
	database SQLDB,
	databaseName, tablePrefix string,
) error {
	characters := mysqlTable(databaseName, tablePrefix, mysqlCharTable)
	metadata := mysqlTable(databaseName, tablePrefix, mysqlAccountMetadataTable)
	quotedValue := quoteSQLIdentifier("entry_value")
	// 每个账号取自己角色的最高等级，只算未软删的角色（与角色列表
	// `delete_flag = 0` 的权威过滤一致）。HAVING 过滤掉"只有 0 级角色"的账号，
	// 避免给它们写一条无意义的 0。ON DUPLICATE KEY 侧仍是 GREATEST，重跑安全。
	query := "INSERT INTO " + metadata +
		" (" + quoteSQLIdentifier("account_id") + ", " + quoteSQLIdentifier("entry_key") + ", " + quotedValue + ")" +
		" SELECT c." + quoteSQLIdentifier("account_id") + ", ?, CAST(MAX(c." + quoteSQLIdentifier("level") + ") AS CHAR)" +
		" FROM " + characters + " c" +
		" WHERE c." + quoteSQLIdentifier("account_id") + " <> ''" +
		" AND c." + quoteSQLIdentifier("delete_flag") + " = 0" +
		" GROUP BY c." + quoteSQLIdentifier("account_id") +
		" HAVING MAX(c." + quoteSQLIdentifier("level") + ") > 0" +
		" ON DUPLICATE KEY UPDATE " + quotedValue + " = GREATEST(CAST(" + quotedValue + " AS UNSIGNED), CAST(VALUES(" + quotedValue + ") AS UNSIGNED))"
	_, err := database.ExecContext(ctx, query, repository.AccountStoryDigestViewedLevelMetadataKey)
	return err
}

func markAccountStoryDigestSeedApplied(
	ctx context.Context,
	database SQLDB,
	databaseName, tablePrefix string,
) error {
	table := mysqlTable(databaseName, tablePrefix, mysqlSettingValuesTable)
	quotedValue := quoteSQLIdentifier("entry_value")
	query := "INSERT INTO " + table +
		" (" + quoteSQLIdentifier("scope") + ", " + quoteSQLIdentifier("entry_key") + ", " + quotedValue + ")" +
		" VALUES (?, ?, ?)" +
		" ON DUPLICATE KEY UPDATE " + quotedValue + " = VALUES(" + quotedValue + ")"
	_, err := database.ExecContext(
		ctx,
		query,
		mysqlAccountStoryDigestSeedScope,
		mysqlAccountStoryDigestSeedKey,
		mysqlAccountStoryDigestSeedDone,
	)
	return err
}
