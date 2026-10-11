package onlineevent

import (
	"context"
	"encoding/binary"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	pvf "longheng.io/server/internal/platform/pvf"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSemesterActual369dCatalogAndMail(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("actual PVF not supplied")
	}
	a, e := pvf.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	c, e := LoadSemesterCatalog(nil, a)
	if e != nil {
		t.Fatal(e)
	}
	g, _, cmd := semesterTest(t)
	for i := range c.Rewards {
		cmd.Index = uint32(i)
		if _, e := ClaimSemesterReward(nil, g, c, cmd); e != nil {
			t.Fatal(e)
		}
	}
	box, _, _ := g.Mailbox.Load(nil, "1")
	if len(box.Mails) != 3 {
		t.Fatal(box)
	}
	text, e := a.ReadText(SemesterPVFPath)
	if e != nil {
		t.Fatal(e)
	}
	for _, replacement := range []string{"[control panel]\n2 7 28 `event_1511_attendance_event`\n[/control panel]", "[reward info]\n1 490010219 1\n[/reward info]"} {
		name := "control panel"
		if strings.Contains(replacement, "reward info") {
			name = "reward info"
		}
		start := strings.Index(text, "["+name+"]")
		end := strings.Index(text[start:], "[/"+name+"]") + start + len("[/"+name+"]")
		bad := attendanceCatalogTestSource{SemesterPVFPath: text[:start] + replacement + text[end:]}
		if _, e := LoadSemesterCatalog(nil, bad); e == nil {
			t.Fatal("changed contract accepted")
		}
	}
}

func semesterTest(t *testing.T) (repo.Group, *SemesterCatalog, SemesterClaimCommand) {
	t.Helper()
	g := weeklyTestRepos(t)
	c := epicTestCampaign(t, g)
	now := c.Ends.Add(-time.Hour)
	k := SemesterJournalKey(c)
	ch, _, _ := g.Character.Load(nil, "1")
	ch.Level = 90
	ch.Stats = map[string]int64{k + ":day_bitmap": (1 << 24) - 1, k + ":qualified_days": 24, k + ":version": 1, "unrelated": 123}
	if e := g.Character.Save(nil, ch); e != nil {
		t.Fatal(e)
	}
	cat := &SemesterCatalog{Rewards: [3]AttendancePVFItemReward{{ItemID: 490010219, Count: 1}, {ItemID: 490010217, Count: 1}, {ItemID: 490010215, Count: 1}}}
	cmd := SemesterClaimCommand{AccountID: "a", CharacterID: "1", Campaign: c, Now: now, Validate: func() bool { return true }}
	cmd.Deliver = func(m repo.MailboxRepository, r AttendancePVFItemReward, key string) (string, error) {
		return repo.AppendSystemMail(nil, m, repo.SystemMailDelivery{RecipientCharacterID: cmd.CharacterID, Source: key, CreatedAt: cmd.Now, Attachments: []repo.MailAttachment{{ItemID: r.ItemID, Count: r.Count}}})
	}
	return g, cat, cmd
}
func TestSemesterNativePolicyProjectionBoundaries(t *testing.T) {
	g, _, cmd := semesterTest(t)
	ch, _, _ := g.Character.Load(nil, "1")
	k := SemesterJournalKey(cmd.Campaign)
	for _, n := range []int{0, 7, 8, 16, 24, 28} {
		t.Run(string(rune('A'+n)), func(t *testing.T) {
			ch.Stats[k+":day_bitmap"] = (1 << uint(n)) - 1
			ch.Stats[k+":qualified_days"] = int64(n)
			s, e := SemesterCharacterSnapshot(ch, cmd.Campaign, cmd.Now)
			if e != nil {
				t.Fatal(e)
			}
			b, e := BuildSemesterNotification(s)
			if e != nil || len(b) != 11 || b[0] != byte(n) || b[1] != byte(n) || binary.LittleEndian.Uint32(b[3:]) != s.Eligible {
				t.Fatal(s, b, e)
			}
			if n == 28 && b[2] != 1 {
				t.Fatal("today bit")
			}
		})
	}
	if _, e := BuildSemesterNotification(SemesterSnapshot{Eligible: 7}); e == nil {
		t.Fatal("bad mask")
	}
	for _, f := range []string{"day_bitmap", "qualified_days", "claim_bitmap", "version", "claim_observed"} {
		t.Run(f, func(t *testing.T) {
			x := repo.CloneCharacter(ch)
			x.Stats[k+":"+f] = -1
			if _, e := SemesterCharacterSnapshot(x, cmd.Campaign, cmd.Now); e == nil {
				t.Fatal("bad durable")
			}
		})
	}
	ch.Stats[k+":day_bitmap"] = 1 << 27
	ch.Stats[k+":qualified_days"] = 1
	if _, e := SemesterCharacterSnapshot(ch, cmd.Campaign, cmd.Campaign.CreatedAt); e == nil {
		t.Fatal("future bit")
	}
}
func TestSemesterClaimConcurrentCharacterScope(t *testing.T) {
	g, c, cmd := semesterTest(t)
	var wg sync.WaitGroup
	for i := 0; i < 48; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			x := cmd
			x.Index = uint32(i % 3)
			if _, e := ClaimSemesterReward(nil, g, c, x); e != nil {
				t.Error(e)
			}
		}(i)
	}
	wg.Wait()
	box, _, _ := g.Mailbox.Load(nil, "1")
	if len(box.Mails) != 3 {
		t.Fatal(len(box.Mails))
	}
	ch, _, _ := g.Character.Load(nil, "1")
	k := SemesterJournalKey(cmd.Campaign)
	if ch.Stats[k+":claim_bitmap"] != 7 || ch.Stats["unrelated"] != 123 {
		t.Fatal(ch.Stats)
	}
	ch.CharacterID = "2"
	ch.Stats[k+":claim_bitmap"] = 0
	if e := g.Character.Save(nil, ch); e != nil {
		t.Fatal(e)
	}
	cmd.CharacterID = "2"
	cmd.Deliver = func(m repo.MailboxRepository, r AttendancePVFItemReward, key string) (string, error) {
		return repo.AppendSystemMail(nil, m, repo.SystemMailDelivery{RecipientCharacterID: "2", Source: key, CreatedAt: cmd.Now, Attachments: []repo.MailAttachment{{ItemID: r.ItemID, Count: r.Count}}})
	}
	if _, e := ClaimSemesterReward(nil, g, c, cmd); e != nil {
		t.Fatal(e)
	}
	box, _, _ = g.Mailbox.Load(nil, "2")
	if len(box.Mails) != 1 {
		t.Fatal(box)
	}
}
func TestSemesterClaimRollbackCancelPanicAndStale(t *testing.T) {
	for _, mode := range []string{"error", "panic", "cancel", "stale"} {
		t.Run(mode, func(t *testing.T) {
			g, c, cmd := semesterTest(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			valid := true
			cmd.Validate = func() bool { return valid }
			deliver := cmd.Deliver
			cmd.Deliver = func(m repo.MailboxRepository, r AttendancePVFItemReward, key string) (string, error) {
				id, e := deliver(m, r, key)
				if e != nil {
					return id, e
				}
				switch mode {
				case "error":
					return id, errors.New("after mail")
				case "panic":
					panic("after mail")
				case "cancel":
					cancel()
				case "stale":
					valid = false
				}
				return id, nil
			}
			if _, e := ClaimSemesterReward(ctx, g, c, cmd); e == nil {
				t.Fatal("accepted")
			}
			ch, _, _ := g.Character.Load(nil, "1")
			box, _, _ := g.Mailbox.Load(nil, "1")
			if ch.Stats[SemesterJournalKey(cmd.Campaign)+":claim_bitmap"] != 0 || len(box.Mails) != 0 {
				t.Fatal(ch, box)
			}
		})
	}
}

type semesterUnknownCommit struct {
	repo.MailboxAssetUnitOfWork
	once bool
}

func (u *semesterUnknownCommit) WithinMailboxAssets(ctx context.Context, a, b string, f func(repo.CharacterRepository, repo.InventoryRepository, repo.MailboxRepository) error) error {
	e := u.MailboxAssetUnitOfWork.WithinMailboxAssets(ctx, a, b, f)
	if e == nil && !u.once {
		u.once = true
		return errors.New("unknown commit")
	}
	return e
}
func TestSemesterUnknownCommitRetryAndClearPreservesStats(t *testing.T) {
	g, c, cmd := semesterTest(t)
	u := &semesterUnknownCommit{MailboxAssetUnitOfWork: g.MailboxAssets}
	g.MailboxAssets = u
	if _, e := ClaimSemesterReward(nil, g, c, cmd); e == nil {
		t.Fatal("expected unknown")
	}
	if _, e := ClaimSemesterReward(nil, g, c, cmd); e != nil {
		t.Fatal(e)
	}
	box, _, _ := g.Mailbox.Load(nil, "1")
	if len(box.Mails) != 1 {
		t.Fatal(box)
	}
	g.MailboxAssets = u.MailboxAssetUnitOfWork
	k := SemesterJournalKey(cmd.Campaign)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		x := cmd
		x.Index = 1
		if _, e := ClaimSemesterReward(nil, g, c, x); e != nil {
			t.Error(e)
		}
	}()
	go func() {
		defer wg.Done()
		owner := repo.SemesterClearOwner{AccountID: "a", CharacterID: "1"}
		e := g.SemesterClear.WithinSemesterClear(nil, owner, []repo.SemesterClearOwner{owner}, "test:semester-concurrent", func(tx repo.Group) error {
			ch, _, e := tx.Character.Load(nil, "1")
			if e != nil {
				return e
			}
			ch.Stats[k+":day_bitmap"] = (1 << 25) - 1
			ch.Stats[k+":qualified_days"] = 25
			return repo.SaveCharacterFields(nil, tx.Character, ch, repo.CharacterFieldStats)
		})
		if e != nil {
			t.Error(e)
		}
	}()
	wg.Wait()
	ch, _, _ := g.Character.Load(nil, "1")
	if ch.Stats[k+":qualified_days"] != 25 || ch.Stats[k+":claim_bitmap"] != 3 || ch.Stats["unrelated"] != 123 {
		t.Fatal(ch.Stats)
	}
}
func TestSemesterClaimRejectOwnerAndCatalog(t *testing.T) {
	for _, mode := range []string{"account", "index", "expired", "clock", "catalog", "level"} {
		t.Run(mode, func(t *testing.T) {
			g, c, cmd := semesterTest(t)
			switch mode {
			case "account":
				cmd.AccountID = "foreign"
			case "index":
				cmd.Index = 3
			case "expired":
				cmd.Now = cmd.Campaign.Ends
			case "clock":
				cmd.Now = cmd.Campaign.CreatedAt.Add(-time.Second)
			case "catalog":
				c.Rewards[0].ItemID++
			case "level":
				ch, _, _ := g.Character.Load(nil, "1")
				ch.Level = 16
				g.Character.Save(nil, ch)
			}
			if _, e := ClaimSemesterReward(nil, g, c, cmd); e == nil {
				t.Fatal("accepted")
			}
			box, _, _ := g.Mailbox.Load(nil, "1")
			if len(box.Mails) != 0 {
				t.Fatal(box)
			}
		})
	}
}
