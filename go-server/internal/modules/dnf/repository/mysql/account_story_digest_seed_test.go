// 本文件覆盖账号级"剧情回顾已看过"播种迁移的幂等性与零副作用边界。
package mysql

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

func accountStoryDigestSeedOptions() SchemaOptions {
	return SchemaOptions{
		AutoCreate:  true,
		TablePrefix: "dnf",
		DatabasePlan: repository.DatabasePlan{
			WriteDatabases: []string{"dnf_s1_w1"},
			ReadDatabases:  []string{"dnf_s1_w1"},
		},
	}
}

func TestMySQLAccountStoryDigestSeedWritesAccountsAndMarkerOnce(t *testing.T) {
	sqlDB := &fakeSQLDB{} // 没有标记行：QueryRow 返回 sql.ErrNoRows
	if err := SeedAccountStoryDigestViewedLevel(context.Background(), sqlDB, accountStoryDigestSeedOptions()); err != nil {
		t.Fatal(err)
	}
	if len(sqlDB.execs) != 3 {
		// fakeSQLDB 把 QueryRowContext 也记进 execs：标记探测 + 播种 + 写标记。
		t.Fatalf("exec count = %d, want marker probe + seed + marker: %v", len(sqlDB.execs), execQueries(sqlDB))
	}
	seed := requireExecContaining(t, sqlDB, "INSERT INTO `dnf_s1_w1`.`dnf_account_metadata`")
	assertContains(t, seed.Query, "SELECT c.`account_id`, ?, CAST(MAX(c.`level`) AS CHAR)")
	assertContains(t, seed.Query, "FROM `dnf_s1_w1`.`dnf_characters` c")
	assertContains(t, seed.Query, "AND c.`delete_flag` = 0")
	assertContains(t, seed.Query, "GROUP BY c.`account_id`")
	assertContains(t, seed.Query, "HAVING MAX(c.`level`) > 0")
	// 播种自身也单调：被手工重跑（删掉标记行）时只抬升、不回退。
	assertContains(
		t,
		seed.Query,
		"`entry_value` = GREATEST(CAST(`entry_value` AS UNSIGNED), CAST(VALUES(`entry_value`) AS UNSIGNED))",
	)
	if len(seed.Args) != 1 || seed.Args[0] != repository.AccountStoryDigestViewedLevelMetadataKey {
		t.Fatalf("seed args=%#v", seed.Args)
	}
	for _, call := range sqlDB.execs {
		assertNotContains(t, call.Query, "DELETE FROM")
	}

	marker := requireExecContaining(t, sqlDB, "INSERT INTO `dnf_s1_w1`.`dnf_setting_values`")
	if len(marker.Args) != 3 ||
		marker.Args[0] != mysqlAccountStoryDigestSeedScope ||
		marker.Args[1] != mysqlAccountStoryDigestSeedKey ||
		marker.Args[2] != mysqlAccountStoryDigestSeedDone {
		t.Fatalf("marker args=%#v", marker.Args)
	}
}

func TestMySQLAccountStoryDigestSeedSkipsEveryWriteWhenMarkerExists(t *testing.T) {
	sqlDB := &fakeSQLDB{rows: []fakeSQLRow{{values: []any{mysqlAccountStoryDigestSeedDone}}}}
	if err := SeedAccountStoryDigestViewedLevel(context.Background(), sqlDB, accountStoryDigestSeedOptions()); err != nil {
		t.Fatal(err)
	}
	if len(sqlDB.execs) != 1 {
		t.Fatalf("marked migration wrote %v, want only the marker probe", execQueries(sqlDB))
	}
	assertContains(t, sqlDB.execs[0].Query, "SELECT `entry_value` FROM `dnf_s1_w1`.`dnf_setting_values`")
}

func TestMySQLAccountStoryDigestSeedRerunsWhenMarkerIsNotDone(t *testing.T) {
	// 标记行存在但不是 "1"（例如运维手工改成 0 表示"再跑一次"）⇒ 继续播种。
	sqlDB := &fakeSQLDB{rows: []fakeSQLRow{{values: []any{"0"}}}}
	if err := SeedAccountStoryDigestViewedLevel(context.Background(), sqlDB, accountStoryDigestSeedOptions()); err != nil {
		t.Fatal(err)
	}
	requireExecContaining(t, sqlDB, "INSERT INTO `dnf_s1_w1`.`dnf_account_metadata`")
	requireExecContaining(t, sqlDB, "INSERT INTO `dnf_s1_w1`.`dnf_setting_values`")
}

func TestMySQLAccountStoryDigestSeedIsNoOpWithoutAutoCreate(t *testing.T) {
	sqlDB := &fakeSQLDB{}
	options := accountStoryDigestSeedOptions()
	options.AutoCreate = false
	if err := SeedAccountStoryDigestViewedLevel(context.Background(), sqlDB, options); err != nil {
		t.Fatal(err)
	}
	if len(sqlDB.execs) != 0 {
		t.Fatalf("AutoCreate=false wrote SQL: %v", execQueries(sqlDB))
	}
	if err := SeedAccountStoryDigestViewedLevel(context.Background(), nil, accountStoryDigestSeedOptions()); err != ErrMySQLDBRequired {
		t.Fatalf("nil database error = %v, want %v", err, ErrMySQLDBRequired)
	}
}

func TestComponentStartSeedsAccountStoryDigestAfterSchema(t *testing.T) {
	sqlDB := &fakeSQLDB{}
	component := NewComponent(sqlDB, ComponentOptions{
		AutoCreateSchema: true,
		TablePrefix:      "dnf",
		DatabasePlan: repository.DatabasePlan{
			WriteDatabases: []string{"dnf_s1_w1"},
			ReadDatabases:  []string{"dnf_s1_w1"},
		},
	})
	if err := component.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	requireExecContaining(t, sqlDB, "INSERT INTO `dnf_s1_w1`.`dnf_account_metadata`")
	requireExecContaining(t, sqlDB, "INSERT INTO `dnf_s1_w1`.`dnf_setting_values`")
}
