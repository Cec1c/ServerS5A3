package mysql

import (
	"context"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

func TestMySQLCharacterCrystalSelectionPreservesNoneAcrossSaveAndReload(t *testing.T) {
	ctx := context.Background()
	statIndex := requireMySQLCharacterStatIndex(t, mysqlCharacterCrystalSelectionStatColumn)
	for _, fullSave := range []bool{false, true} {
		for selection := int64(-1); selection < 6; selection++ {
			t.Run(fmt.Sprintf("full_save=%t/selection=%d", fullSave, selection), func(t *testing.T) {
				database := &fakeSQLDB{}
				repositories := newTestMySQLGroup(t, database)
				record := repository.CharacterRecord{
					CharacterID: "crystal-char-1", AccountID: "acc-1", Job: "0", Level: 1,
					Stats: map[string]int64{mysqlCharacterCrystalSelectionStatColumn: selection, "gold": 42},
				}
				var err error
				if fullSave {
					err = repositories.Character.Save(ctx, record)
				} else {
					err = repository.SaveCharacterFields(ctx, repositories.Character, record, repository.CharacterFieldStats)
				}
				if err != nil {
					t.Fatal(err)
				}
				call := requireExecContaining(t, database, "`dnf_s1_w1`.`dnf_characters`")
				// 从实际 SQL 入参建立重读行，避免用内存仓储绕过 mirror 列的写入转换。
				persisted := call.Args[8+statIndex]
				if persisted != selection {
					t.Fatalf("SQL crystal selection = %v, want %d", persisted, selection)
				}
				values := []any{record.CharacterID, record.AccountID, record.Slot, record.Name, record.Job, record.Level}
				values = append(values, call.Args[8:8+len(mysqlCharacterStatColumns)]...)
				values = append(values, nil, nil)
				reloaded := newTestMySQLGroup(t, &fakeSQLDB{rows: []fakeSQLRow{{values: values}}})
				loaded, found, err := reloaded.Character.Load(ctx, record.CharacterID)
				if err != nil || !found {
					t.Fatalf("reload found=%t err=%v", found, err)
				}
				if got := loaded.Stats[mysqlCharacterCrystalSelectionStatColumn]; got != selection {
					t.Fatalf("reloaded selection = %d, want %d", got, selection)
				}
				if loaded.Stats["gold"] != 42 {
					t.Fatalf("unrelated gold = %d, want 42", loaded.Stats["gold"])
				}
			})
		}
	}
}

func TestMySQLCharacterCrystalSelectionKeepsExistingStatBounds(t *testing.T) {
	for _, test := range []struct {
		key                   string
		value, fallback, want int64
	}{
		{mysqlCharacterCrystalSelectionStatColumn, -1, -1, -1},
		{mysqlCharacterCrystalSelectionStatColumn, -2, -1, 0},
		{"skill_tree_index", -1, 0, -1},
		{"gold", -1, 0, 0},
		{"grow_type", -1, 0, 0},
	} {
		t.Run(fmt.Sprintf("%s/%d", test.key, test.value), func(t *testing.T) {
			record := repository.CharacterRecord{Stats: map[string]int64{test.key: test.value}}
			if got := characterStatMirrorValue(record, test.key, test.fallback); got != test.want {
				t.Fatalf("mirror value = %d, want %d", got, test.want)
			}
		})
	}
	if got := characterStatMirrorValue(repository.CharacterRecord{}, mysqlCharacterCrystalSelectionStatColumn, -1); got != -1 {
		t.Fatalf("missing selection = %d, want none", got)
	}
}
