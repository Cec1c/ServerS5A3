package mysql

import (
	"context"
	"database/sql"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

func TestWave3WeaponSkinStateNarrowRead(t *testing.T) {
	db := &fakeSQLDB{rows: []fakeSQLRow{
		// 1. No weapon skin selected -> single query.
		{values: []any{"swordman", nil}},
		// 2. Sample weapon skin selected -> two narrow queries.
		{values: []any{"swordman", int64(mysqlWeaponSkinSampleID)}},
		{values: []any{int64(mysqlWeaponSkinSampleID)}},
		// 3. Catalog weapon skin selected -> two narrow queries.
		{values: []any{"fighter", int64(10002456)}},
		{values: []any{int64(10002456)}},
		// 4. Missing character.
		{err: sql.ErrNoRows},
	}}
	store := newCharacterSelectionIdentityStore(t, db)
	reader, ok := any(store).(repository.CharacterWeaponSkinStateReader)
	if !ok {
		t.Fatal("missing CharacterWeaponSkinStateReader")
	}

	none, found, err := reader.LoadCharacterWeaponSkinState(context.Background(), "19")
	if err != nil || !found || none.Job != "swordman" || len(none.Stats) != 0 {
		t.Fatalf("none=%+v found=%v err=%v", none, found, err)
	}
	if len(db.execs) != 1 {
		t.Fatalf("expected 1 query for unselected weapon skin, got %d", len(db.execs))
	}

	sample, found, err := reader.LoadCharacterWeaponSkinState(context.Background(), "19")
	if err != nil || !found || sample.Job != "swordman" ||
		sample.Stats[mysqlWeaponSkinSelectedStat] != mysqlWeaponSkinSampleID ||
		sample.Stats[mysqlWeaponSkinSampleOwned] != mysqlWeaponSkinSampleID {
		t.Fatalf("sample=%+v found=%v err=%v", sample, found, err)
	}
	if len(db.execs) != 3 {
		t.Fatalf("expected 3 total queries after sample skin, got %d", len(db.execs))
	}

	cat, found, err := reader.LoadCharacterWeaponSkinState(context.Background(), "20")
	if err != nil || !found || cat.Job != "fighter" ||
		cat.Stats[mysqlWeaponSkinSelectedStat] != 10002456 ||
		cat.Stats["weapon_skin_owned_10002456"] != 10002456 {
		t.Fatalf("cat=%+v found=%v err=%v", cat, found, err)
	}
	if len(db.execs) != 5 {
		t.Fatalf("expected 5 total queries after catalog skin, got %d", len(db.execs))
	}

	if _, found, err := reader.LoadCharacterWeaponSkinState(context.Background(), "missing"); found || err != nil {
		t.Fatalf("missing: found=%v err=%v", found, err)
	}
}
