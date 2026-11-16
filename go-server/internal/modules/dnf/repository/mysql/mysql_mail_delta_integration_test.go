//go:build mysql_integration

package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	r "longheng.io/server/internal/modules/dnf/repository"
)

type mailDeltaFixture struct {
	db                                           *sql.DB
	ctx                                          context.Context
	router                                       mysqlRouter
	store                                        *mysqlMailboxStore
	parent, mails, metadata, attachments, extras string
	seed                                         r.MailboxRecord
}

func newMailDeltaFixture(t *testing.T, interpolate bool) *mailDeltaFixture {
	t.Helper()
	if os.Getenv("DNF_RUNTIME_DISPOSABLE_DB") != "1" {
		t.Fatal("requires disposable database")
	}
	cfg, err := driver.ParseDSN(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if err != nil || cfg.Net != "tcp" || cfg.DBName != "" {
		t.Fatal("requires loopback DSN without schema")
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("requires loopback")
	}
	cfg.ParseTime, cfg.InterpolateParams = true, interpolate
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	schema := fmt.Sprintf("mail_delta_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		if _, e := db.Exec("DROP DATABASE IF EXISTS `" + schema + "`"); e != nil {
			t.Error(e)
		}
	})
	plan := r.DatabasePlan{ReadDatabases: []string{schema}, WriteDatabases: []string{schema}}
	if err := EnsureMySQLSchema(ctx, db, SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	g, err := NewMySQLGroupFromDB(db, MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	s := g.Mailbox.(*mysqlMailboxStore)
	f := &mailDeltaFixture{db: db, ctx: ctx, router: s.router, store: s}
	f.parent, _ = s.router.writeTable(mysqlMailboxTable, "owner")
	f.mails, _ = s.router.writeTable(mysqlMailsTable, "owner")
	f.metadata, _ = s.router.writeTable(mysqlMailMetadataTable, "owner")
	f.attachments, _ = s.router.writeTable(mysqlMailAttachmentsTable, "owner")
	f.extras, _ = s.router.writeTable(mysqlMailAttachmentExtraTable, "owner")
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	f.seed = r.MailboxRecord{CharacterID: "owner", UpdatedAt: at, Mails: map[string]r.MailRecord{}}
	for i := 0; i < 80; i++ {
		id := fmt.Sprintf("%03d", i)
		f.seed.Mails[id] = r.MailRecord{MailID: id, SenderCharacterID: "sender", SenderName: "寄件者", RecipientCharacterID: "owner", RecipientName: "收件者", Title: "subject", Body: "body\x00中文", Gold: int64(i), CreatedAt: at, ExpireAt: at.Add(time.Hour), Metadata: map[string]string{"receipt": "durable", "saved": "false"}, Attachments: []r.MailAttachment{
			{ItemID: 1001, Count: 2, RawEntry: []byte{0, 39, 255}, Extra: map[string]string{"durability": "17", "receipt": "keep"}},
			{ItemID: 1002, Count: 1, Bind: true, ExpireAt: at.Add(time.Hour), Extra: map[string]string{"origin": "fixture"}},
		}}
	}
	f.reset(t)
	return f
}

func (f *mailDeltaFixture) oldSave(db SQLDB, value r.MailboxRecord) error {
	return withMySQLWriteExecutor(f.ctx, db, func(tx SQLDB) error {
		if _, err := tx.ExecContext(f.ctx, buildUpsert(f.parent, []string{"character_id", "updated_at"}, []string{updateValue("updated_at")}), value.CharacterID, value.UpdatedAt); err != nil {
			return err
		}
		return replaceMails(f.ctx, tx, f.mails, f.metadata, f.attachments, f.extras, value.CharacterID, value.Mails)
	})
}
func (f *mailDeltaFixture) reset(t *testing.T) {
	t.Helper()
	if err := f.oldSave(SQLDBAdapter{DB: f.db}, f.seed); err != nil {
		t.Fatal(err)
	}
}

func (f *mailDeltaFixture) raw(t *testing.T, owner string) string {
	t.Helper()
	var out []string
	for _, table := range []string{f.parent, f.mails, f.metadata, f.attachments, f.extras} {
		rows, err := f.db.QueryContext(f.ctx, "SELECT * FROM "+table+" WHERE character_id = ?", owner)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var entries []string
		for rows.Next() {
			values := make([]any, len(columns))
			args := make([]any, len(columns))
			for i := range values {
				args[i] = &values[i]
			}
			if err := rows.Scan(args...); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			entries = append(entries, string(data))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		sort.Strings(entries)
		data, _ := json.Marshal(entries)
		out = append(out, string(data))
	}
	data, _ := json.Marshal(out)
	return string(data)
}

func mailObservationCount(o *SQLObservation, kind int) int64 {
	var n int64
	for path := range o.series {
		for i := range o.series[path][kind].buckets {
			n += o.series[path][kind].buckets[i].Load()
		}
	}
	return n
}

func TestMailboxRowDeltaDisposableMariaDB(t *testing.T) {
	for _, interpolate := range []bool{false, true} {
		t.Run(fmt.Sprint(interpolate), func(t *testing.T) {
			f := newMailDeltaFixture(t, interpolate)
			other := r.CloneMailbox(f.seed)
			other.CharacterID = "other"
			if err := f.oldSave(SQLDBAdapter{DB: f.db}, other); err != nil {
				t.Fatal(err)
			}
			otherRaw := f.raw(t, "other")
			cases := []struct {
				name   string
				change func(*r.MailboxRecord)
			}{
				{"unchanged", func(*r.MailboxRecord) {}},
				{"read", func(m *r.MailboxRecord) { x := m.Mails["001"]; x.Read = true; m.Mails["001"] = x }},
				{"header", func(m *r.MailboxRecord) {
					x := m.Mails["001"]
					x.SenderCharacterID = "new-sender"
					x.SenderName = "new-name"
					x.RecipientCharacterID = "new-recipient"
					x.RecipientName = "new-recipient-name"
					x.Title = "new-title"
					x.Body = "new-body"
					x.Gold = 99
					x.CreatedAt = x.CreatedAt.Add(time.Second)
					x.ExpireAt = time.Time{}
					m.Mails["001"] = x
				}},
				{"claim", func(m *r.MailboxRecord) {
					x := m.Mails["001"]
					x.Read, x.Claimed, x.Deleted = true, true, true
					x.Gold = 0
					x.Attachments = nil
					m.Mails["001"] = x
				}},
				{"metadata", func(m *r.MailboxRecord) {
					x := m.Mails["001"]
					delete(x.Metadata, "saved")
					x.Metadata["empty"] = ""
					x.Metadata["receipt"] = "new"
					m.Mails["001"] = x
				}},
				{"attachment", func(m *r.MailboxRecord) {
					x := m.Mails["001"]
					x.Attachments[0].Count = 9
					x.Attachments[0].RawEntry = []byte{1, 0, 42}
					delete(x.Attachments[0].Extra, "receipt")
					x.Attachments[0].Extra["durability"] = "19"
					x.Attachments[0].Extra["new"] = ""
					x.Attachments = append(x.Attachments, r.MailAttachment{ItemID: 1003, Count: 1, Extra: map[string]string{"added": "yes"}})
					m.Mails["001"] = x
				}},
				{"delete", func(m *r.MailboxRecord) { delete(m.Mails, "001") }},
				{"append", func(m *r.MailboxRecord) {
					m.Mails["new"] = r.MailRecord{MailID: "ignored-map-value-id", Title: "new", Metadata: map[string]string{"source": "new"}, Attachments: []r.MailAttachment{{ItemID: 1004, Count: 3}}}
				}},
				{"clear", func(m *r.MailboxRecord) { m.Mails = nil }},
				{"empty-raw", func(m *r.MailboxRecord) {
					x := m.Mails["001"]
					x.Attachments[1].RawEntry = []byte{}
					m.Mails["001"] = x
				}},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					after := r.CloneMailbox(f.seed)
					tc.change(&after)
					after.UpdatedAt = after.UpdatedAt.Add(time.Second)
					var baseline string
					var writes [2]int64
					var queries [2]int64
					var elapsed [2]time.Duration
					for variant := 0; variant < 2; variant++ {
						f.reset(t)
						obs := &SQLObservation{}
						db := &ObservedSQLDB{SQLDB: SQLDBAdapter{DB: f.db}, Observation: obs}
						start := time.Now()
						var err error
						if variant == 0 {
							err = f.oldSave(db, after)
						} else {
							router := f.router
							router.db = db
							router.readDBs = []string{"nonexistent_read_replica"}
							err = (&mysqlMailboxStore{mysqlStoreBase: mysqlStoreBase{router: router}}).Save(f.ctx, after)
						}
						elapsed[variant] = time.Since(start)
						if err != nil {
							t.Fatal(err)
						}
						writes[variant], queries[variant] = mailObservationCount(obs, sqlObserveExec), mailObservationCount(obs, sqlObserveQuery)+mailObservationCount(obs, sqlObserveRow)
						if variant == 0 {
							baseline = f.raw(t, "owner")
						} else if f.raw(t, "owner") != baseline {
							t.Fatal("delta differs from full replacement including raw child rows")
						}
						if f.raw(t, "other") != otherRaw {
							t.Fatal("foreign owner changed")
						}
					}
					if tc.name == "unchanged" && writes[1] != 1 {
						t.Fatal("unchanged mail rows rewritten", writes)
					}
					if tc.name == "read" && writes[1] != 2 {
						t.Fatal("read flag must update exactly parent and one mail", writes)
					}
					if tc.name != "clear" && writes[1] >= writes[0] {
						t.Fatal("writes not reduced", writes)
					}
					t.Logf("mail rows case=%s fullWrites=%d deltaWrites=%d fullQueries=%d deltaQueries=%d fullElapsed=%s deltaElapsed=%s", tc.name, writes[0], writes[1], queries[0], queries[1], elapsed[0], elapsed[1])
				})
			}
			t.Run("orphan-and-sparse-repair", func(t *testing.T) {
				for _, corrupt := range []string{
					"INSERT INTO " + f.metadata + " VALUES ('owner','orphan','key','value')",
					"INSERT INTO " + f.attachments + " (character_id,mail_id,attachment_index,item_id,item_count,bind_flag) VALUES ('owner','orphan',0,99,1,0)",
					"INSERT INTO " + f.extras + " VALUES ('owner','001',99,'key','value')",
					"DELETE FROM " + f.attachments + " WHERE character_id='owner' AND mail_id='001' AND attachment_index=0",
					"UPDATE " + f.attachments + " SET raw_entry=X'' WHERE character_id='owner' AND mail_id='001' AND attachment_index=1",
				} {
					f.reset(t)
					baseline := f.raw(t, "owner")
					if _, err := f.db.ExecContext(f.ctx, corrupt); err != nil {
						t.Fatal(err)
					}
					if err := f.store.Save(f.ctx, f.seed); err != nil {
						t.Fatal(err)
					}
					if f.raw(t, "owner") != baseline {
						t.Fatal("repair left hidden rows")
					}
				}
			})
			t.Run("collation-conflicts-remain-errors", func(t *testing.T) {
				for _, child := range []string{"metadata", "extra"} {
					f.reset(t)
					baseline := f.raw(t, "owner")
					after := r.CloneMailbox(f.seed)
					m := after.Mails["001"]
					if child == "metadata" {
						m.Metadata["Receipt"] = "conflict"
					} else {
						m.Attachments[0].Extra["Receipt"] = "conflict"
					}
					after.Mails["001"] = m
					if err := f.oldSave(SQLDBAdapter{DB: f.db}, after); err == nil {
						t.Fatal("fixture did not conflict", child)
					}
					if err := f.store.Save(f.ctx, after); err == nil {
						t.Fatal("delta swallowed unique-key conflict", child)
					}
					if f.raw(t, "owner") != baseline {
						t.Fatal("conflict changed mailbox", child)
					}
				}
			})
		})
	}
}

type mailDeltaFaultTx struct {
	*sql.Tx
	writes, failAt      int
	queries, failReadAt int
}

var errMailDeltaFault = errors.New("injected mail SQL failure")

func (tx *mailDeltaFaultTx) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	tx.writes++
	if tx.writes == tx.failAt {
		return nil, errMailDeltaFault
	}
	return tx.Tx.ExecContext(ctx, q, args...)
}
func (tx *mailDeltaFaultTx) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	tx.queries++
	if tx.queries == tx.failReadAt {
		return nil, errMailDeltaFault
	}
	return tx.Tx.QueryContext(ctx, q, args...)
}

func TestMailboxRowDeltaRollbackAndRepeatedSavesDisposableMariaDB(t *testing.T) {
	f := newMailDeltaFixture(t, true)
	before := f.raw(t, "owner")
	after := r.CloneMailbox(f.seed)
	delete(after.Mails, "002")
	x := after.Mails["001"]
	x.Read, x.Claimed = true, true
	x.Attachments = nil
	x.Metadata["receipt"] = "changed"
	after.Mails["001"] = x
	after.Mails["new"] = r.MailRecord{Title: "new", Attachments: []r.MailAttachment{{ItemID: 42, Count: 1, Extra: map[string]string{"id": "new"}}}}
	run := func(fail, failRead int) (int, error) {
		tx, err := f.db.BeginTx(f.ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		fault := &mailDeltaFaultTx{Tx: tx, failAt: fail, failReadAt: failRead}
		router := f.router
		router.db = newSQLTransactionDB(fault, SQLDBAdapter{DB: f.db})
		router.lockReads = true
		err = (&mysqlMailboxStore{mysqlStoreBase: mysqlStoreBase{router: router}}).Save(f.ctx, after)
		return fault.writes, err
	}
	n, err := run(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n < 10 {
		t.Fatal("fault fixture did not cover all mail children", n)
	}
	for fail := 1; fail <= n; fail++ {
		if _, err := run(fail, 0); !errors.Is(err, errMailDeltaFault) {
			t.Fatal(fail, err)
		}
		if f.raw(t, "owner") != before {
			t.Fatal("failed write escaped rollback", fail)
		}
	}
	for fail := 1; fail <= 4; fail++ {
		if _, err := run(0, fail); !errors.Is(err, errMailDeltaFault) {
			t.Fatal(fail, err)
		}
		if f.raw(t, "owner") != before {
			t.Fatal("failed read committed parent update", fail)
		}
	}
	t.Logf("actual transaction rollback at every %d write and four read boundaries", n)
	// Two different store instances and owners share one transaction. No cached
	// before-image may survive an intervening legacy write or another Save.
	tx, err := f.db.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	router := f.router
	router.db = newSQLTransactionDB(tx, SQLDBAdapter{DB: f.db})
	router.lockReads = true
	a := &mysqlMailboxStore{mysqlStoreBase: mysqlStoreBase{router: router}}
	b := &mysqlMailboxStore{mysqlStoreBase: mysqlStoreBase{router: router}}
	if err := a.Save(f.ctx, after); err != nil {
		t.Fatal(err)
	}
	legacy := r.CloneMailbox(after)
	m := legacy.Mails["001"]
	m.Body = "legacy-update"
	legacy.Mails["001"] = m
	if err := f.oldSave(router.db, legacy); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(f.ctx, after); err != nil {
		t.Fatal(err)
	}
	other := r.CloneMailbox(after)
	other.CharacterID = "other"
	if err := a.Save(f.ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := a.Save(f.ctx, after); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got, found, err := f.store.Load(f.ctx, "owner")
	if err != nil || !found {
		t.Fatal(found, err)
	}
	after.Mails["new"] = func() r.MailRecord { m := after.Mails["new"]; m.MailID = "new"; return m }()
	if !reflect.DeepEqual(got, after) {
		t.Fatal("repeated saves differ", got, after)
	}
	// A committed operation with a lost reply is reconciled by a fresh read and
	// repeat save; it does not append another mail or resurrect attachments.
	committed := f.raw(t, "owner")
	if err := f.store.Save(f.ctx, after); err != nil {
		t.Fatal(err)
	}
	if f.raw(t, "owner") != committed {
		t.Fatal("repeat save changed committed state")
	}
}
