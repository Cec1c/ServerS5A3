package onlineevent

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func marbleTestCatalog() *MarbleCatalog {
	c := &MarbleCatalog{ClearRewards: [3][2]int64{{4, 2}, {8, 2}, {12, 2}}, PostalDays: 15,
		StageRewards: []MarbleStageReward{{4, 104, 1}, {8, 108, 1}, {12, 112, 1}}}
	for i := range c.Slots {
		c.Slots[i] = MarbleSlot{Index: i, Type: 1, ItemID: 100, ItemCount: 1}
	}
	return c
}

func TestMarbleLoginClearsResetAndLegacyProgress(t *testing.T) {
	c := marbleTestCatalog()
	now := time.Date(2026, 9, 12, 5, 59, 59, 0, ChinaCalendar)
	character := dnfrepo.CharacterRecord{}
	for i := 0; i <= 15; i++ {
		if i > 0 {
			if err := RecordMarbleClear(&character, c, now); err != nil {
				t.Fatal(err)
			}
		}
		s, err := ReadMarbleState(character, c, now)
		if err != nil || s.Turns != 2+int64(min(i, 12)/4)*2 || s.Clears != int64(min(i, 12)) || s.CalendarDay != 20260911 {
			t.Fatalf("clear %d: %+v %v", i, s, err)
		}
	}
	character.Stats["attendance_marble_dice_used"] = 8
	character.Stats["attendance_marble_position"] = 27
	character.Stats["attendance_marble_round"] = 4
	character.Stats["attendance_marble_reward_4"] = 1
	character.Stats["attendance_marble_buff_30"] = 1
	if err := RecordMarbleClear(&character, c, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	s, err := ReadMarbleState(character, c, now.Add(time.Second))
	if err != nil || s.Remaining() != 2 || s.Clears != 1 || s.Position != 27 || s.Round != 4 || s.Reward4 != 1 || character.Stats["attendance_marble_buff_30"] != 1 {
		t.Fatalf("06:00 reset: %+v %v", s, err)
	}
	character.Stats["attendance_marble_dice_used"] = 3
	s, err = ReadMarbleState(character, c, now.Add(time.Second))
	if err != nil || s.Remaining() != 0 {
		t.Fatalf("legacy spent: %+v %v", s, err)
	}
}

func TestMarbleOneDungeonBuffsAndRarity(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "1", AccountID: "a", Stats: map[string]int64{"attendance_marble_buff_29": 2, "attendance_marble_buff_30": 1, "attendance_marble_buff_32": 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ConsumeMarbleDungeonBuffs(ctx, repos, "other", "1"); !errors.Is(err, ErrMarbleState) {
		t.Fatal("foreign account consumed buffs")
	}
	mask, err := ConsumeMarbleDungeonBuffs(ctx, repos, "a", "1")
	if err != nil || mask != 11 {
		t.Fatalf("first run buffs %d %v", mask, err)
	}
	mask, err = ConsumeMarbleDungeonBuffs(ctx, repos, "a", "1")
	if err != nil || mask != 1 {
		t.Fatalf("second run reused buffs %d %v", mask, err)
	}
	mask, err = ConsumeMarbleDungeonBuffs(ctx, repos, "a", "1")
	if err != nil || mask != 0 {
		t.Fatalf("spent buff used again %d %v", mask, err)
	}
	base := [7]int64{700000, 964900, 990000, 1000000, 1000001, 1000002, 1000003}
	got := MarbleEquipmentRarityThresholds(base)
	if got != [7]int64{688000, 952900, 978000, 1000000, 1000001, 1000002, 1000003} {
		t.Fatalf("unique thresholds %v", got)
	}
}

func TestMarbleTransactionalRewardsDoublesAndFailures(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	c := marbleTestCatalog()
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, ChinaCalendar)
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "1", AccountID: "a", Stats: map[string]int64{"gold": 10000}}); err != nil {
		t.Fatal(err)
	}
	cmd := MarbleCommand{AccountID: "a", CharacterID: "1", Now: now}
	failMail := false
	cmd.Deliver = func(m dnfrepo.MailboxRepository, r AttendancePVFItemReward, key string) (string, error) {
		id, err := dnfrepo.AppendSystemMail(ctx, m, dnfrepo.SystemMailDelivery{RecipientCharacterID: "1", Source: "test", CompletionKey: key, CreatedAt: now, Attachments: []dnfrepo.MailAttachment{{ItemID: r.ItemID, Count: r.Count}}})
		if failMail {
			return "", errors.New("mail transaction failed after write")
		}
		return id, err
	}
	settle := func() MarbleState {
		t.Helper()
		s, err := SettleMarble(ctx, repos, c, cmd)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := settle()
	if s.Remaining() != 2 {
		t.Fatalf("fresh %+v", s)
	}
	cmd.Dice = &[2]byte{1, 1}
	for i := 0; i < 6; i++ {
		s = settle()
		if s.DiceUsed != 1 || s.Position != int64((i+1)*2) || s.DoubleChain != int64(i) || (s.Double == 1) != (i < 5) {
			t.Fatalf("double %d: %+v", i, s)
		}
	}
	cmd.Dice = &[2]byte{1, 2}
	s = settle()
	if s.Remaining() != 0 || s.Double != 0 {
		t.Fatalf("last paid %+v", s)
	}
	before, _, _ := repos.Character.Load(ctx, "1")
	if _, err := SettleMarble(ctx, repos, c, cmd); !errors.Is(err, ErrMarbleSpent) {
		t.Fatalf("exhausted %v", err)
	}
	after, _, _ := repos.Character.Load(ctx, "1")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("exhausted roll changed state")
	}
	cmd.AccountID = "other"
	if _, err := SettleMarble(ctx, repos, c, cmd); !errors.Is(err, ErrMarbleState) {
		t.Fatalf("ownership %v", err)
	}
	cmd.AccountID = "a"
	cmd.Now = now.Add(24 * time.Hour)
	cmd.Dice = &[2]byte{2, 2}
	s = settle()
	cmd.Dice = nil
	cmd.CloseDouble = true
	s = settle()
	if s.Double != 0 || s.DoubleChain != 0 || s.Remaining() != 1 {
		t.Fatalf("close %+v", s)
	}
	cmd.CloseDouble = false
	// A teleport reaches the paid slot; all gold, mail and progress changes share the transaction.
	c.Slots[22] = MarbleSlot{Index: 22, Type: 3, Goto: 14}
	c.Slots[14] = MarbleSlot{Index: 14, Type: 5, Gold: 10000, ItemID: 105, ItemCount: 5}
	cmd.Dice = &[2]byte{1, 2}
	before, _, _ = repos.Character.Load(ctx, "1")
	mailBefore, _, _ := repos.Mailbox.Load(ctx, "1")
	failMail = true
	if _, err := SettleMarble(ctx, repos, c, cmd); err == nil {
		t.Fatal("mail failure accepted")
	}
	after, _, _ = repos.Character.Load(ctx, "1")
	mailAfter, _, _ := repos.Mailbox.Load(ctx, "1")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(mailBefore, mailAfter) {
		t.Fatal("mail failure did not roll back assets")
	}
	failMail = false
	s = settle()
	if s.Position != 14 || s.GoldAfter != 0 || !s.GoldChanged || len(s.MailIDs) != 1 {
		t.Fatalf("teleport paid slot %+v", s)
	}
	// Cross each cumulative threshold once; reopening and subsequent rolls do not repeat it.
	for _, lap := range []int64{4, 8, 12} {
		ch, _, _ := repos.Character.Load(ctx, "1")
		ch.Stats["attendance_marble_position"] = 27
		ch.Stats["attendance_marble_round"] = lap - 1
		ch.Stats["attendance_marble_dice_used"] = 0
		if err := repos.Character.Save(ctx, ch); err != nil {
			t.Fatal(err)
		}
		s = settle()
		if s.Round != lap || s.Position != 2 || len(s.MailIDs) != 2 {
			t.Fatalf("lap %+v", s)
		}
		s = settle()
		if len(s.MailIDs) != 1 {
			t.Fatal("cumulative reward repeated")
		}
	}
	ch, _, _ := repos.Character.Load(ctx, "1")
	ch.Stats["attendance_marble_position"] = 11
	ch.Stats["attendance_marble_dice_used"] = 0
	if err := repos.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	if _, err := SettleMarble(ctx, repos, c, cmd); !errors.Is(err, ErrMarbleGold) {
		t.Fatalf("gold check %v", err)
	}
}

func TestMarbleConcurrentRollsCannotOverspend(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	c := marbleTestCatalog()
	for i := range c.Slots {
		c.Slots[i] = MarbleSlot{Index: i, Type: 7, Buff: 30}
	}
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "1", AccountID: "a"}); err != nil {
		t.Fatal(err)
	}
	cmd := MarbleCommand{AccountID: "a", CharacterID: "1", Now: time.Now(), Dice: &[2]byte{1, 2}, Deliver: func(dnfrepo.MailboxRepository, AttendancePVFItemReward, string) (string, error) {
		t.Error("buff unexpectedly mailed")
		return "", ErrMarbleState
	}}
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			_, err := SettleMarble(ctx, repos, c, cmd)
			if err != nil && !errors.Is(err, ErrMarbleSpent) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	ch, _, _ := repos.Character.Load(ctx, "1")
	s, err := ReadMarbleState(ch, c, cmd.Now)
	if err != nil || s.DiceUsed != 2 || s.Position != 6 || ch.Stats["attendance_marble_buff_30"] != 2 {
		t.Fatalf("concurrent %+v %v", s, err)
	}
}
