package mysql

import (
	"context"
	"fmt"
	r "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"testing"
)

func TestChangedCharacterStatsUseConfiguredBatchWithoutLosingPermanentRules(t *testing.T) {
	t.Setenv(currentMysqlInsertBatchEnv, "200")
	stats := map[string]int64{}
	for i := 0; i < 501; i++ {
		stats[fmt.Sprintf("delta_%04d", i)] = int64(i)
	}
	stats[r.LevelUpEventStatPrefix+"90"] = 1
	stats[r.LevelUpRewardStatPrefix+"90"] = 1
	db := &fakeSQLDB{}
	if err := saveChangedCharacterStats(context.Background(), db, "stats", "owner", nil, stats); err != nil {
		t.Fatal(err)
	}
	if len(db.execs) != 5 {
		t.Fatalf("501 normal + two permanent groups writes=%d", len(db.execs))
	}
	var rows int
	var eventRule, rewardRule bool
	for _, call := range db.execs {
		rows += len(call.Args) / 3
		if len(call.Args) > 600 {
			t.Fatal("batch parameter overflow")
		}
		eventRule = eventRule || strings.Contains(call.Query, "stat_value < 0")
		rewardRule = rewardRule || strings.Contains(call.Query, "VALUES(stat_value), stat_value)")
	}
	if rows != 503 || !eventRule || !rewardRule {
		t.Fatalf("rows=%d event=%v reward=%v", rows, eventRule, rewardRule)
	}
}

func TestSQLFamilyObservationCountsConsumptionAndFixedFamilies(t *testing.T) {
	o := &SQLObservation{}
	db := &ObservedSQLDB{SQLDB: &fakeSQLDB{rows: []fakeSQLRow{{values: []any{"x"}}}}, Observation: o}
	var value string
	if err := db.QueryRowContext(context.Background(), "SELECT value FROM `db`.`dnf_character_stats` WHERE character_id=?", "private-id").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if o.families[2][sqlObserveRow].count.Load() != 1 || o.families[2][sqlObserveRow].rows.Load() != 1 {
		t.Fatal("family did not finish at Scan")
	}
	if sqlFamily("SELECT arbitrary_player_payload").family != 0 {
		t.Fatal("dynamic label admitted")
	}
}

func TestSQLFamilyUsesTableInsteadOfSharedCharacterColumns(t *testing.T) {
	for _, test := range []struct {
		query string
		want  int
	}{
		{"SELECT character_id FROM `character_schema`.`dnf_inventory_items` WHERE character_id=?", 3},
		{"INSERT INTO `db`.`dnf_equipment_entries` (character_id, entry_key) VALUES (?, ?)", 4},
		{"DELETE FROM `db`.`dnf_pet_entries` WHERE character_id=?", 6},
		{"UPDATE `db`.`dnf_quest_states` SET character_id=? WHERE character_id=?", 5},
		{"SELECT c.character_id FROM `db`.`dnf_characters` c LEFT JOIN `db`.`dnf_equipment_entries` e ON c.character_id=e.character_id", 2},
		{"SELECT account_id FROM `db`.`dnf_setting_values` WHERE scope=?", 8},
	} {
		if got := sqlFamily(test.query).family; got != test.want {
			t.Errorf("table family=%d want=%d query=%s", got, test.want, test.query)
		}
	}
}
