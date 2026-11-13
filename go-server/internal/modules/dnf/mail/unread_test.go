package mail

import (
	"context"
	"errors"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

type unreadFallbackMailbox struct {
	record  dnfrepo.MailboxRecord
	found   bool
	loadErr error
	loads   int
	saves   int
}

func (m *unreadFallbackMailbox) Load(context.Context, string) (dnfrepo.MailboxRecord, bool, error) {
	m.loads++
	if m.loadErr != nil {
		return dnfrepo.MailboxRecord{}, false, m.loadErr
	}
	return dnfrepo.CloneMailbox(m.record), m.found, nil
}

func (m *unreadFallbackMailbox) Save(context.Context, dnfrepo.MailboxRecord) error {
	m.saves++
	return nil
}

type unreadHeaderMailbox struct {
	headers []dnfrepo.MailboxUnreadHeader
	err     error
	loads   int
	saves   int
}

func (m *unreadHeaderMailbox) Load(context.Context, string) (dnfrepo.MailboxRecord, bool, error) {
	m.loads++
	return dnfrepo.MailboxRecord{}, false, errors.New("narrow unread reader used full Load")
}

func (m *unreadHeaderMailbox) Save(context.Context, dnfrepo.MailboxRecord) error {
	m.saves++
	return errors.New("unread count attempted Save")
}

func (m *unreadHeaderMailbox) ListUnreadHeaders(context.Context, string) ([]dnfrepo.MailboxUnreadHeader, error) {
	if m.err != nil {
		return nil, m.err
	}
	return append([]dnfrepo.MailboxUnreadHeader(nil), m.headers...), nil
}

func unreadHeaderFromRecord(mail dnfrepo.MailRecord) dnfrepo.MailboxUnreadHeader {
	return dnfrepo.MailboxUnreadHeader{
		MailID:       mail.MailID,
		Read:         mail.Read,
		Deleted:      mail.Deleted,
		CreatedAt:    mail.CreatedAt,
		ExpireAt:     mail.ExpireAt,
		MailboxSaved: mail.Metadata["mailbox_saved"],
	}
}

func TestUnreadCountMatchesOwnerOpenSemantics(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name string
		mail dnfrepo.MailRecord
		now  time.Time
		want int
	}{
		{name: "unread", mail: dnfrepo.MailRecord{MailID: "1", ExpireAt: now.Add(time.Hour)}, now: now, want: 1},
		{name: "claimed_but_unread_still_counts", mail: dnfrepo.MailRecord{MailID: "1", Claimed: true, ExpireAt: now.Add(time.Hour)}, now: now, want: 1},
		{name: "read", mail: dnfrepo.MailRecord{MailID: "1", Read: true, ExpireAt: now.Add(time.Hour)}, now: now, want: 0},
		{name: "deleted", mail: dnfrepo.MailRecord{MailID: "1", Deleted: true, ExpireAt: now.Add(time.Hour)}, now: now, want: 0},
		{name: "expiry_boundary_equal_is_live", mail: dnfrepo.MailRecord{MailID: "1", ExpireAt: now}, now: now, want: 1},
		{name: "expiry_one_nanosecond_past", mail: dnfrepo.MailRecord{MailID: "1", ExpireAt: now.Add(-time.Nanosecond)}, now: now, want: 0},
		{name: "saved_mixed_case_survives_expiry", mail: dnfrepo.MailRecord{
			MailID: "1", ExpireAt: now.Add(-time.Hour), Metadata: map[string]string{"mailbox_saved": " \tTrUe\n "},
		}, now: now, want: 1},
		{name: "saved_non_true_does_not_survive_expiry", mail: dnfrepo.MailRecord{
			MailID: "1", ExpireAt: now.Add(-time.Hour), Metadata: map[string]string{"mailbox_saved": "truthy"},
		}, now: now, want: 0},
		{name: "saved_key_is_exact", mail: dnfrepo.MailRecord{
			MailID: "1", ExpireAt: now.Add(-time.Hour), Metadata: map[string]string{"MailBox_Saved": "true"},
		}, now: now, want: 0},
		{name: "relative_expiry_boundary_equal_is_live", mail: dnfrepo.MailRecord{
			MailID: "1", CreatedAt: now.Add(-mailboxNormalLifetime),
		}, now: now, want: 1},
		{name: "relative_expiry_one_nanosecond_past", mail: dnfrepo.MailRecord{
			MailID: "1", CreatedAt: now.Add(-mailboxNormalLifetime - time.Nanosecond),
		}, now: now, want: 0},
		{name: "zero_now_does_not_evaluate_expiry", mail: dnfrepo.MailRecord{
			MailID: "1", ExpireAt: now.Add(-time.Hour),
		}, now: time.Time{}, want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			group := dnfrepomemory.NewMemoryGroup()
			if err := group.Mailbox.Save(context.Background(), dnfrepo.MailboxRecord{
				CharacterID: "character-1",
				Mails:       map[string]dnfrepo.MailRecord{test.mail.MailID: test.mail},
			}); err != nil {
				t.Fatal(err)
			}
			got, err := UnreadCount(context.Background(), group, "character-1", test.now)
			if err != nil || got != test.want {
				t.Fatalf("fallback count=%d want=%d err=%v", got, test.want, err)
			}

			narrow := &unreadHeaderMailbox{headers: []dnfrepo.MailboxUnreadHeader{unreadHeaderFromRecord(test.mail)}}
			got, err = UnreadCount(context.Background(), dnfrepo.Group{Mailbox: narrow}, "character-1", test.now)
			if err != nil || got != test.want {
				t.Fatalf("narrow count=%d want=%d err=%v", got, test.want, err)
			}
			if narrow.loads != 0 || narrow.saves != 0 {
				t.Fatalf("narrow reader fell back: loads=%d saves=%d", narrow.loads, narrow.saves)
			}
		})
	}
}

func TestUnreadCountMissingMailboxAndErrors(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

	missing := &unreadFallbackMailbox{found: false}
	got, err := UnreadCount(ctx, dnfrepo.Group{Mailbox: missing}, "character-1", now)
	if err != nil || got != 0 || missing.loads != 1 || missing.saves != 0 {
		t.Fatalf("missing mailbox count=%d err=%v loads=%d saves=%d", got, err, missing.loads, missing.saves)
	}

	loadErr := errors.New("mailbox load failed")
	failingLoad := &unreadFallbackMailbox{loadErr: loadErr}
	if _, err := UnreadCount(ctx, dnfrepo.Group{Mailbox: failingLoad}, "character-1", now); !errors.Is(err, loadErr) {
		t.Fatalf("fallback error=%v want %v", err, loadErr)
	}
	if failingLoad.saves != 0 {
		t.Fatalf("fallback error attempted %d saves", failingLoad.saves)
	}

	narrowErr := errors.New("unread headers failed")
	failingNarrow := &unreadHeaderMailbox{err: narrowErr}
	if _, err := UnreadCount(ctx, dnfrepo.Group{Mailbox: failingNarrow}, "character-1", now); !errors.Is(err, narrowErr) {
		t.Fatalf("narrow error=%v want %v", err, narrowErr)
	}
	if failingNarrow.loads != 0 || failingNarrow.saves != 0 {
		t.Fatalf("narrow error fell back: loads=%d saves=%d", failingNarrow.loads, failingNarrow.saves)
	}

	if _, err := UnreadCount(ctx, dnfrepo.Group{}, "character-1", now); !errors.Is(err, ErrMailboxRepoMissing) {
		t.Fatalf("missing repository error=%v", err)
	}
	if _, err := UnreadCount(ctx, dnfrepo.Group{Mailbox: missing}, "  ", now); !errors.Is(err, ErrMailboxCharacterMissing) {
		t.Fatalf("missing character error=%v", err)
	}
}
