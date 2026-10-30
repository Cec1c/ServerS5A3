package mysql

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"longheng.io/server/internal/platform/metrics"
)

const (
	sqlObserveExec = iota
	sqlObserveQuery
	sqlObserveRow
	sqlObservePing
	sqlObserveBegin
	sqlObserveKinds
)

var sqlObserveNames = [sqlObserveKinds]string{"exec", "query", "queryrow", "ping", "begin"}

const SQLObservationName = "dnf_mysql_observation"

var sqlObserveBounds = [...]time.Duration{time.Millisecond, 5 * time.Millisecond, 10 * time.Millisecond, 25 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond, time.Second, 3 * time.Second}

type sqlObservationSeries struct {
	nanos   atomic.Int64
	errors  atomic.Int64
	rows    atomic.Int64
	buckets [len(sqlObserveBounds) + 1]atomic.Int64
}

// No query text, schema, parameter, player identity or dynamic label is retained.
// Query lifetime includes row consumption; QueryRow finishes at Scan.
type SQLObservation struct {
	series [2][sqlObserveKinds]sqlObservationSeries
}

func NewSQLObservation(registry *metrics.Registry, pool *sql.DB) *SQLObservation {
	if registry == nil {
		return nil
	}
	o := &SQLObservation{}
	registry.RegisterObserver(SQLObservationName, func(r *metrics.Registry) {
		for path := range o.series {
			for kind := range o.series[path] {
				s := &o.series[path][kind]
				label := "pool"
				if path == 1 {
					label = "transaction"
				}
				labels := map[string]string{"path": label, "operation": sqlObserveNames[kind]}
				var total int64
				for j := range s.buckets {
					total += s.buckets[j].Load()
					bound := "+Inf"
					if j < len(sqlObserveBounds) {
						bound = strconv.FormatFloat(sqlObserveBounds[j].Seconds(), 'g', -1, 64)
					}
					r.SetHistogramSample("dnf_mysql_operation_seconds_bucket", map[string]string{"path": label, "operation": sqlObserveNames[kind], "le": bound}, total, false, 0)
				}
				r.SetHistogramSample("dnf_mysql_operation_seconds_count", labels, total, false, 0)
				r.SetHistogramSample("dnf_mysql_operation_seconds_sum", labels, 0, true, float64(s.nanos.Load())/1e9)
				r.SetCounter("dnf_mysql_operation_errors_total", labels, s.errors.Load())
				r.SetCounter("dnf_mysql_rows_read_total", labels, s.rows.Load())
			}
		}
		if pool != nil {
			stats := pool.Stats()
			r.SetGauge("dnf_mysql_pool_in_use", nil, int64(stats.InUse))
			r.SetGauge("dnf_mysql_pool_idle", nil, int64(stats.Idle))
			r.SetCounter("dnf_mysql_pool_wait_total", nil, stats.WaitCount)
			r.SetCounter("dnf_mysql_pool_wait_nanoseconds_total", nil, int64(stats.WaitDuration))
		}
	})
	return o
}
func (o *SQLObservation) start() time.Time {
	if o == nil {
		return time.Time{}
	}
	return time.Now()
}
func (o *SQLObservation) finish(path, kind int, start time.Time, rows int64, err error) {
	if o == nil {
		return
	}
	s := &o.series[path][kind]
	d := time.Since(start)
	s.nanos.Add(int64(d))
	s.rows.Add(rows)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.errors.Add(1)
	}
	i := len(sqlObserveBounds)
	for j, b := range sqlObserveBounds {
		if d <= b {
			i = j
			break
		}
	}
	s.buckets[i].Add(1)
}

// ObservedSQLDB forwards transaction creation. newSQLTransactionDB unwraps it
// when retaining prepared statement ownership and carries this observer forward.
type ObservedSQLDB struct {
	SQLDB
	Observation *SQLObservation
}

func (a *ObservedSQLDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	start := a.Observation.start()
	b, ok := a.SQLDB.(sqlTxBeginner)
	if !ok {
		err := errors.New("observed SQL database cannot begin transactions")
		a.Observation.finish(0, sqlObserveBegin, start, 0, err)
		return nil, err
	}
	tx, err := b.BeginTx(ctx, opts)
	a.Observation.finish(0, sqlObserveBegin, start, 0, err)
	return tx, err
}
func (a *ObservedSQLDB) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	start := a.Observation.start()
	result, err := a.SQLDB.ExecContext(ctx, q, args...)
	a.Observation.finish(0, sqlObserveExec, start, 0, err)
	return result, err
}
func (a *ObservedSQLDB) QueryContext(ctx context.Context, q string, args ...any) (SQLRows, error) {
	start := a.Observation.start()
	rows, err := a.SQLDB.QueryContext(ctx, q, args...)
	return observedRows(a.Observation, 0, start, rows, err)
}
func (a *ObservedSQLDB) QueryRowContext(ctx context.Context, q string, args ...any) SQLRow {
	start := a.Observation.start()
	row := a.SQLDB.QueryRowContext(ctx, q, args...)
	return observedRow(a.Observation, 0, start, row)
}
func (a *ObservedSQLDB) PingContext(ctx context.Context) error {
	start := a.Observation.start()
	err := a.SQLDB.PingContext(ctx)
	a.Observation.finish(0, sqlObservePing, start, 0, err)
	return err
}

type observedSQLRow struct {
	SQLRow
	observer *SQLObservation
	path     int
	start    time.Time
	once     sync.Once
}

func observedRow(o *SQLObservation, path int, start time.Time, row SQLRow) SQLRow {
	if o == nil {
		return row
	}
	return &observedSQLRow{SQLRow: row, observer: o, path: path, start: start}
}
func (r *observedSQLRow) Scan(dest ...any) error {
	err := r.SQLRow.Scan(dest...)
	r.once.Do(func() {
		var count int64
		if err == nil {
			count = 1
		}
		r.observer.finish(r.path, sqlObserveRow, r.start, count, err)
	})
	return err
}

type observedSQLRows struct {
	SQLRows
	observer *SQLObservation
	path     int
	start    time.Time
	once     sync.Once
	rows     int64
	scanErr  error
}

func observedRows(o *SQLObservation, path int, start time.Time, rows SQLRows, err error) (SQLRows, error) {
	if o == nil {
		return rows, err
	}
	if err != nil {
		o.finish(path, sqlObserveQuery, start, 0, err)
		return rows, err
	}
	return &observedSQLRows{SQLRows: rows, observer: o, path: path, start: start}, nil
}
func (r *observedSQLRows) finish(err error) {
	r.once.Do(func() { r.observer.finish(r.path, sqlObserveQuery, r.start, r.rows, errors.Join(r.scanErr, err)) })
}
func (r *observedSQLRows) Next() bool {
	ok := r.SQLRows.Next()
	if ok {
		r.rows++
	} else {
		r.finish(r.SQLRows.Err())
	}
	return ok
}
func (r *observedSQLRows) Scan(dest ...any) error {
	err := r.SQLRows.Scan(dest...)
	if err != nil {
		r.scanErr = err
	}
	return err
}
func (r *observedSQLRows) Close() error {
	err := r.SQLRows.Close()
	r.finish(errors.Join(r.SQLRows.Err(), err))
	return err
}
