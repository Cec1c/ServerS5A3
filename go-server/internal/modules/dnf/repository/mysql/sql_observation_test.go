package mysql

import (
	"context"
	"database/sql"
	"errors"
	"longheng.io/server/internal/platform/metrics"
	"testing"
	"time"
)

func observationCount(s *sqlObservationSeries) int64 {
	var n int64
	for i := range s.buckets {
		n += s.buckets[i].Load()
	}
	return n
}
func TestSQLObservationLazyRowsAndErrors(t *testing.T) {
	o := NewSQLObservation(metrics.New(), nil)
	r := observedRow(o, 0, time.Now().Add(-time.Second), fakeSQLRow{values: []any{"value"}})
	if observationCount(&o.series[0][sqlObserveRow]) != 0 {
		t.Fatal("recorded before Scan")
	}
	var value string
	if err := r.Scan(&value); err != nil || value != "value" {
		t.Fatal(value, err)
	}
	if observationCount(&o.series[0][sqlObserveRow]) != 1 || o.series[0][sqlObserveRow].rows.Load() != 1 || o.series[0][sqlObserveRow].nanos.Load() < int64(time.Second) {
		t.Fatal("scan not observed")
	}
	_ = r.Scan(&value)
	if observationCount(&o.series[0][sqlObserveRow]) != 1 {
		t.Fatal("double scan duplicated observation")
	}
	noRow := observedRow(o, 0, time.Now(), fakeSQLRow{err: sql.ErrNoRows})
	if !errors.Is(noRow.Scan(), sql.ErrNoRows) || o.series[0][sqlObserveRow].errors.Load() != 0 {
		t.Fatal("no rows is not an operational failure")
	}
	sentinel := errors.New("scan failed")
	rows, err := observedRows(o, 1, time.Now(), &fakeSQLRows{rows: []fakeSQLRow{{values: []any{"first"}}, {err: sentinel}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if observationCount(&o.series[1][sqlObserveQuery]) != 0 {
		t.Fatal("query recorded before consumption")
	}
	if !rows.Next() || rows.Scan(&value) != nil || !rows.Next() || !errors.Is(rows.Scan(&value), sentinel) {
		t.Fatal("row behavior changed")
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	s := &o.series[1][sqlObserveQuery]
	if observationCount(s) != 1 || s.rows.Load() != 2 || s.errors.Load() != 1 {
		t.Fatalf("count=%d rows=%d errors=%d", observationCount(s), s.rows.Load(), s.errors.Load())
	}
	rows, err = observedRows(o, 0, time.Now(), nil, sentinel)
	if rows != nil || err != sentinel || o.series[0][sqlObserveQuery].errors.Load() != 1 {
		t.Fatal("issue error lost")
	}
}
func TestSQLObservationPreservesPreparedTransaction(t *testing.T) {
	ctx := context.Background()
	db, state := openPreparedSQLFixture(t)
	prepared := NewPreparedSQLDB(db, 4)
	defer prepared.Close()
	registry := metrics.New()
	o := NewSQLObservation(registry, db)
	adapter := &ObservedSQLDB{SQLDB: prepared, Observation: o}
	const query = "UPDATE observation_fixture SET n=?"
	if _, err := adapter.ExecContext(ctx, query, 1); err != nil {
		t.Fatal(err)
	}
	tx, err := adapter.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	scoped := newSQLTransactionDB(tx, adapter)
	if scoped.prepared != prepared || scoped.observation != o {
		t.Fatal("wrapper lost transaction capabilities")
	}
	for i := 0; i < 3; i++ {
		if _, err := scoped.ExecContext(ctx, query, i); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := state.snapshot().prepareCalls; got != 1 {
		t.Fatalf("prepared handle reuse lost: %d", got)
	}
	if observationCount(&o.series[0][sqlObserveExec]) != 1 || observationCount(&o.series[1][sqlObserveExec]) != 3 || observationCount(&o.series[0][sqlObserveBegin]) != 1 {
		t.Fatal("pool/transaction counts wrong")
	}
	registry.RunObservers()
	for _, sample := range registry.Snapshot() {
		for key := range sample.Labels {
			if key != "path" && key != "operation" && key != "le" {
				t.Fatalf("unbounded label %s", key)
			}
		}
	}
}
func TestSQLObservationNilPreservesObjects(t *testing.T) {
	var o *SQLObservation
	row := &fakeSQLRow{}
	raw := &fakeSQLRows{}
	if observedRow(o, 0, o.start(), row) != row {
		t.Fatal("nil row observer changed value")
	}
	rows, err := observedRows(o, 0, o.start(), raw, nil)
	if rows != raw || err != nil {
		t.Fatal("nil rows observer changed value")
	}
	o.finish(0, sqlObserveExec, time.Time{}, 0, nil)
}
