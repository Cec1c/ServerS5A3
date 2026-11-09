package mysql

import (
	"context"
	"reflect"
	"strings"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestMySQLWeddingNamespaceHasIndependentWriteAuthority(t *testing.T) {
	ctx := context.Background()
	stats := map[string]int64{"gold": 7, "custom": 8, "wedding_v1_partner": 2, "wedding_v1_love_points": 9, "wedding_v1_room_points": 10}
	for _, explicit := range []bool{false, true} {
		db := &fakeSQLDB{}
		g := newTestMySQLGroup(t, db)
		fields := r.AllCharacterFields()
		if explicit {
			fields = []r.CharacterField{r.CharacterFieldWedding}
		}
		if err := r.SaveCharacterFields(ctx, g.Character, r.CharacterRecord{CharacterID: "1", Stats: stats}, fields...); err != nil {
			t.Fatal(err)
		}
		written := map[string]int64{}
		deletedNamespace := false
		for _, call := range db.execs {
			if strings.HasPrefix(call.Query, "DELETE FROM") && strings.Contains(call.Query, "character_stats") {
				if explicit {
					if len(call.Args) != 3 || call.Args[1] != len(r.CharacterWeddingStatPrefix) || call.Args[2] != r.CharacterWeddingStatPrefix || !strings.Contains(call.Query, "LEFT(stat_key, ?) = ?") {
						t.Fatalf("wedding delete escaped namespace: %+v", call)
					}
					deletedNamespace = true
				} else if len(call.Args) < 7 || call.Args[6] != r.CharacterWeddingStatPrefix || !strings.Contains(call.Query, "LEFT(stat_key, ?) <> ?") {
					t.Fatalf("ordinary replacement does not protect wedding rows: %+v", call)
				}
			}
			if strings.HasPrefix(call.Query, "INSERT INTO") && strings.Contains(call.Query, "character_stats") && len(call.Args) == 3 {
				key := call.Args[1].(string)
				if explicit != r.IsCharacterWeddingStat(key) {
					t.Fatalf("explicit=%v wrote unowned key %s", explicit, key)
				}
				written[key] = call.Args[2].(int64)
			}
		}
		if explicit && (!deletedNamespace || len(written) != 3) {
			t.Fatalf("incomplete wedding replacement: %+v", written)
		}
		if !explicit && !reflect.DeepEqual(written, map[string]int64{"custom": 8}) {
			t.Fatalf("ordinary supplemental stats: %+v", written)
		}
	}
}

func TestMySQLWeddingNamespaceExcludedFromStatsDelta(t *testing.T) {
	db := &fakeSQLDB{}
	before := map[string]int64{"wedding_v1_partner": 2, "wedding_v1_love_points": 80, "wedding_v1_room_points": 500, "ordinary": 1}
	after := map[string]int64{"wedding_v1_love_points": 999, "ordinary": 2}
	if err := saveChangedCharacterStats(context.Background(), db, "stats", "1", before, after); err != nil {
		t.Fatal(err)
	}
	if len(db.execs) != 1 || len(db.execs[0].Args) != 3 || db.execs[0].Args[1] != "ordinary" {
		t.Fatalf("delta overwrote wedding namespace: %+v", db.execs)
	}
}
