package mysql

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/platform/db"
)

type unreadQueryErrorDB struct {
	*fakeSQLDB
	err error
}

func (d *unreadQueryErrorDB) QueryContext(context.Context, string, ...any) (SQLRows, error) {
	return nil, d.err
}

func TestMailboxUnreadHeadersSQLProjectionAndShard(t *testing.T) {
	const characterID = "mail-unread-character"
	readDBs := []string{"read_a", "read_b"}
	readShard := readDBs[pickDatabase(characterID, len(readDBs))]
	mailsTable := mysqlTable(readShard, "dnf", mysqlMailsTable)
	metadataTable := mysqlTable(readShard, "dnf", mysqlMailMetadataTable)
	created := time.Date(2026, 11, 13, 1, 2, 3, 0, time.UTC)
	expires := created.Add(24 * time.Hour)

	sqlDB := &fakeSQLDB{rowsets: []fakeSQLRows{{rows: []fakeSQLRow{
		{values: []any{"1", false, false, created, expires, nil}},
		{values: []any{"2", true, false, created, expires, "  TRUE  "}},
		{values: []any{"3", false, true, nil, nil, "yes"}},
	}}}}
	store := &mysqlMailboxStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{
		db:          sqlDB,
		readDBs:     readDBs,
		writeDBs:    []string{"write_only"},
		tablePrefix: "dnf",
		now:         time.Now,
	}}}

	headers, err := store.ListUnreadHeaders(context.Background(), characterID)
	if err != nil || len(headers) != 3 {
		t.Fatalf("headers=%+v err=%v", headers, err)
	}
	if headers[0].MailID != "1" || headers[0].Read || headers[0].Deleted ||
		headers[0].MailboxSaved != "" || !headers[0].CreatedAt.Equal(created) || !headers[0].ExpireAt.Equal(expires) {
		t.Fatalf("row0=%+v", headers[0])
	}
	if headers[1].MailID != "2" || !headers[1].Read || headers[1].MailboxSaved != "  TRUE  " {
		t.Fatalf("row1=%+v", headers[1])
	}
	if headers[2].MailID != "3" || !headers[2].Deleted || headers[2].MailboxSaved != "yes" ||
		!headers[2].CreatedAt.IsZero() || !headers[2].ExpireAt.IsZero() {
		t.Fatalf("row2=%+v", headers[2])
	}

	if len(sqlDB.execs) != 1 {
		t.Fatalf("queries=%d want 1", len(sqlDB.execs))
	}
	call := sqlDB.execs[0]
	if len(call.Args) != 1 || call.Args[0] != characterID {
		t.Fatalf("args=%v", call.Args)
	}
	for _, want := range []string{
		"SELECT m.mail_id, m.read_flag, m.deleted_flag, m.created_at, m.expire_at, md.metadata_value",
		"FROM " + mailsTable + " m",
		"LEFT JOIN " + metadataTable + " md",
		"BINARY md.metadata_key = 'mailbox_saved'",
		"WHERE m.character_id = ?",
	} {
		if !strings.Contains(call.Query, want) {
			t.Fatalf("query missing %q: %s", want, call.Query)
		}
	}
	for _, forbidden := range []string{
		"FOR UPDATE", "ORDER BY", "mailboxes", "mail_attachments", "mail_attachment_extra",
		" body", " gold", "claimed_flag", "sender_", "recipient_", " title",
	} {
		if strings.Contains(call.Query, forbidden) {
			t.Fatalf("query leaked %q: %s", forbidden, call.Query)
		}
	}
	if strings.Contains(call.Query, "write_only") {
		t.Fatalf("query did not use the character read shard: %s", call.Query)
	}
}

func TestMailboxUnreadHeadersHonorsRouterReadLocking(t *testing.T) {
	sqlDB := &fakeSQLDB{}
	store := &mysqlMailboxStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{
		db:          sqlDB,
		readDBs:     []string{"read_a"},
		writeDBs:    []string{"write_a"},
		tablePrefix: "dnf",
		lockReads:   true,
		now:         time.Now,
	}}}
	if _, err := store.ListUnreadHeaders(context.Background(), "character-1"); err != nil {
		t.Fatal(err)
	}
	if len(sqlDB.execs) != 1 || !strings.HasSuffix(sqlDB.execs[0].Query, " FOR UPDATE") {
		t.Fatalf("query=%q", sqlDB.execs[0].Query)
	}
}

func TestMailboxUnreadHeadersMissingAndErrors(t *testing.T) {
	ctx := context.Background()
	store := &mysqlMailboxStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{
		db:       &fakeSQLDB{},
		readDBs:  []string{"read_a"},
		writeDBs: []string{"write_a"},
		now:      time.Now,
	}}}
	headers, err := store.ListUnreadHeaders(ctx, "missing-mailbox")
	if err != nil || len(headers) != 0 {
		t.Fatalf("missing mailbox headers=%+v err=%v", headers, err)
	}
	if _, err := store.ListUnreadHeaders(ctx, "  "); !errors.Is(err, db.ErrRecordKeyRequired) {
		t.Fatalf("blank character error=%v", err)
	}

	queryErr := errors.New("mail unread query failed")
	failing := &mysqlMailboxStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{
		db:       &unreadQueryErrorDB{fakeSQLDB: &fakeSQLDB{}, err: queryErr},
		readDBs:  []string{"read_a"},
		writeDBs: []string{"write_a"},
		now:      time.Now,
	}}}
	if _, err := failing.ListUnreadHeaders(ctx, "character-1"); !errors.Is(err, queryErr) {
		t.Fatalf("query error=%v want %v", err, queryErr)
	}

	rowsErr := errors.New("mail unread rows failed")
	rowsFailing := &mysqlMailboxStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{
		db:       &fakeSQLDB{rowsets: []fakeSQLRows{{err: rowsErr}}},
		readDBs:  []string{"read_a"},
		writeDBs: []string{"write_a"},
		now:      time.Now,
	}}}
	if _, err := rowsFailing.ListUnreadHeaders(ctx, "character-1"); !errors.Is(err, rowsErr) {
		t.Fatalf("rows error=%v want %v", err, rowsErr)
	}

	scanFailing := &mysqlMailboxStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{
		db:       &fakeSQLDB{rowsets: []fakeSQLRows{{rows: []fakeSQLRow{{values: []any{"1", false}}}}}},
		readDBs:  []string{"read_a"},
		writeDBs: []string{"write_a"},
		now:      time.Now,
	}}}
	if _, err := scanFailing.ListUnreadHeaders(ctx, "character-1"); err == nil {
		t.Fatal("scan error was not propagated")
	}
}

func TestMailboxUnreadCountCanonicalSavedValueReachesGo(t *testing.T) {
	sqlDB := &fakeSQLDB{rowsets: []fakeSQLRows{{rows: []fakeSQLRow{
		{values: []any{"saved", false, false, nil, nil, " \tTrUe\n "}},
	}}}}
	store := &mysqlMailboxStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{
		db:       sqlDB,
		readDBs:  []string{"read_a"},
		writeDBs: []string{"write_a"},
		now:      time.Now,
	}}}
	headers, err := store.ListUnreadHeaders(context.Background(), "character-1")
	if err != nil || len(headers) != 1 || headers[0].MailboxSaved != " \tTrUe\n " {
		t.Fatalf("headers=%+v err=%v", headers, err)
	}
	if strings.Contains(sqlDB.execs[0].Query, "metadata_value =") ||
		strings.Contains(sqlDB.execs[0].Query, "LOWER(") ||
		strings.Contains(sqlDB.execs[0].Query, "UPPER(") {
		t.Fatalf("saved value compared in SQL instead of Go: %s", sqlDB.execs[0].Query)
	}
}
