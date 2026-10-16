package progression

import (
	"context"
	"errors"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"
)

func testLevelRewards(t *testing.T) *LevelUpRewardCatalog {
	t.Helper()
	c, err := ParseLevelUpRewards("[level up reward]\n85 1110 1111 3330 500\n[/level up reward]\n[level up reward]\n86 1161 1162 3330 80\n[/level up reward]")
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func levelRewardSeed(t *testing.T) dnfrepo.Group {
	t.Helper()
	r := memory.NewMemoryGroup()
	ctx := context.Background()
	if err := r.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "dnf:1", Name: "升级勇士", Level: 84, Stats: map[string]int64{"exp": 8400}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Skill.Save(ctx, dnfrepo.SkillRecord{CharacterID: "77", Points: dnfrepo.SkillPointState{SyncedLevel: 84}}); err != nil {
		t.Fatal(err)
	}
	return r
}
func commitRewardLevels(t *testing.T, r dnfrepo.Group, level int) {
	t.Helper()
	ctx := context.Background()
	if err := r.CharacterProgression.WithinCharacterProgression(ctx, "77", func(c dnfrepo.CharacterRepository, s dnfrepo.SkillRepository) error {
		character, _, err := c.Load(ctx, "77")
		if err != nil {
			return err
		}
		skill, _, err := s.Load(ctx, "77")
		if err != nil {
			return err
		}
		if err := ApplyCharacterExperience(&character, &skill, level, uint32(level*100), dnfrepo.SkillPointState{SyncedLevel: level}, time.Unix(1792100000, 0)); err != nil {
			return err
		}
		if err := dnfrepo.SaveCharacterFields(ctx, c, character, dnfrepo.CharacterFieldBase, dnfrepo.CharacterFieldStats); err != nil {
			return err
		}
		return dnfrepo.SaveSkillFields(ctx, s, skill, dnfrepo.SkillFieldPoints)
	}); err != nil {
		t.Fatal(err)
	}
}
func rewardCommand() LevelUpRewardCommand {
	return LevelUpRewardCommand{CharacterID: "77", Now: time.Unix(1792100001, 0), ResolveAttachment: func(r LevelUpReward) (dnfrepo.MailAttachment, error) {
		return dnfrepo.MailAttachment{ItemID: r.ItemID, Count: r.Count}, nil
	}}
}

func TestLevelUpRewardsCrossLevelsConcurrentReplayAndDeletedMail(t *testing.T) {
	ctx := context.Background()
	repos := levelRewardSeed(t)
	catalog := testLevelRewards(t)
	commitRewardLevels(t, repos, 87)
	reader := repos.Character.(dnfrepo.PendingLevelUpReader)
	if ids, err := reader.ListPendingLevelUpCharacters(ctx, "", 128); err != nil || !reflect.DeepEqual(ids, []string{"77"}) {
		t.Fatalf("pending=%v err=%v", ids, err)
	}
	var wg sync.WaitGroup
	results := make(chan []LevelUpRewardDelivery, 16)
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, e := DeliverLevelUpRewards(ctx, repos, catalog, rewardCommand())
			results <- d
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var delivered []LevelUpRewardDelivery
	for d := range results {
		delivered = append(delivered, d...)
	}
	if len(delivered) != 2 || delivered[0].Level != 85 || delivered[0].Count != 500 || delivered[1].Level != 86 || delivered[1].Count != 80 {
		t.Fatalf("deliveries=%+v", delivered)
	}
	mailbox, _, _ := repos.Mailbox.Load(ctx, "77")
	if len(mailbox.Mails) != 2 {
		t.Fatalf("mails=%+v", mailbox)
	}
	character, _, _ := repos.Character.Load(ctx, "77")
	for level := 85; level <= 87; level++ {
		if character.Stats[dnfrepo.LevelUpEventKey(level)] >= 0 {
			t.Fatalf("event%d not consumed", level)
		}
	}
	if character.Stats["level_up_reward_item_85"] != 3330 || character.Stats["level_up_reward_count_85"] != 500 || character.Stats["level_up_reward_mail_85"] <= 0 {
		t.Fatalf("audit=%v", character.Stats)
	}
	// 物理删除/过期邮件后，冷重建消费者仍以角色永久记录防重。
	mailbox.Mails = nil
	if err := repos.Mailbox.Save(ctx, mailbox); err != nil {
		t.Fatal(err)
	}
	if got, err := DeliverLevelUpRewards(ctx, repos, testLevelRewards(t), rewardCommand()); err != nil || len(got) != 0 {
		t.Fatalf("cold replay=%+v %v", got, err)
	}
	mailbox, _, _ = repos.Mailbox.Load(ctx, "77")
	if len(mailbox.Mails) != 0 {
		t.Fatal("deleted mail recreated")
	}
	if ids, err := reader.ListPendingLevelUpCharacters(ctx, "", 128); err != nil || len(ids) != 0 {
		t.Fatalf("consumed pending=%v %v", ids, err)
	}
}

type failingRewardCharacters struct{ dnfrepo.CharacterRepository }

func (f failingRewardCharacters) SaveFields(context.Context, dnfrepo.CharacterRecord, ...dnfrepo.CharacterField) error {
	return errors.New("injected event consumption failure")
}

type failingRewardUnit struct{ dnfrepo.MailboxAssetUnitOfWork }

func (f failingRewardUnit) WithinMailboxAssets(ctx context.Context, a, b string, apply func(dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.MailboxRepository) error) error {
	return f.MailboxAssetUnitOfWork.WithinMailboxAssets(ctx, a, b, func(c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository, m dnfrepo.MailboxRepository) error {
		return apply(failingRewardCharacters{c}, i, m)
	})
}
func TestLevelUpRewardsFailedMailTransactionAndInvalidItemRemainRetryable(t *testing.T) {
	ctx := context.Background()
	repos := levelRewardSeed(t)
	commitRewardLevels(t, repos, 86)
	catalog := testLevelRewards(t)
	failed := repos
	failed.MailboxAssets = failingRewardUnit{repos.MailboxAssets}
	if d, e := DeliverLevelUpRewards(ctx, failed, catalog, rewardCommand()); e == nil || len(d) != 0 {
		t.Fatalf("failure=%v %v", d, e)
	}
	mailbox, _, _ := repos.Mailbox.Load(ctx, "77")
	if len(mailbox.Mails) != 0 {
		t.Fatal("uncommitted mail leaked")
	}
	character, _, _ := repos.Character.Load(ctx, "77")
	if character.Stats[dnfrepo.LevelUpEventKey(85)] <= 0 {
		t.Fatal("failed transaction consumed event")
	}
	bad := rewardCommand()
	bad.ResolveAttachment = func(LevelUpReward) (dnfrepo.MailAttachment, error) {
		return dnfrepo.MailAttachment{}, errors.New("PVF item missing")
	}
	if d, e := DeliverLevelUpRewards(ctx, repos, catalog, bad); e == nil || len(d) != 0 {
		t.Fatalf("invalid item=%v %v", d, e)
	}
	if d, e := DeliverLevelUpRewards(ctx, repos, catalog, rewardCommand()); e != nil || len(d) != 2 {
		t.Fatalf("retry=%v %v", d, e)
	}
}

func TestLevelUpRewardsNoHistoricalBackfillSameLevelOrGMEdit(t *testing.T) {
	ctx := context.Background()
	repos := levelRewardSeed(t)
	catalog := testLevelRewards(t)
	character, _, _ := repos.Character.Load(ctx, "77")
	character.Level = 85
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if d, e := DeliverLevelUpRewards(ctx, repos, catalog, rewardCommand()); e != nil || len(d) != 0 {
		t.Fatalf("GM edit delivered=%v %v", d, e)
	}
	commitRewardLevels(t, repos, 85)
	character, _, _ = repos.Character.Load(ctx, "77")
	if dnfrepo.HasPendingLevelUp(character) {
		t.Fatal("same level created historical event")
	}
}

func TestLevelUpRewardsMalformedPVF(t *testing.T) {
	for _, text := range []string{"[level up reward]\n85 1 2 3330 500", "[level up reward]\n85 1 2 3330\n[/level up reward]", "[level up reward]\n85 1 2 3330 0\n[/level up reward]", "[level up reward]\n85 1 2 3330 500\n[/level up reward]\n[level up reward]\n85 1 2 3330 500\n[/level up reward]", "[level up reward]\n85 1 `x` 3330 500\n[/level up reward]"} {
		if _, err := ParseLevelUpRewards(text); err == nil {
			t.Fatalf("accepted malformed PVF %q", text)
		}
	}
	if c, e := ParseLevelUpRewards("[other]\n1\n[/other]"); e != nil {
		t.Fatal(e)
	} else if _, ok := c.Reward(85); ok {
		t.Fatal("invented reward")
	}
}

func TestLevelUpRewardsUsePVFLevelsItemsAndCountsWithoutFallback(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want []LevelUpReward
	}{
		{
			name: "修改等级道具数量",
			text: "[level up reward]\n83 10 20 9001 7\n[/level up reward]\n[level up reward]\n85 30 40 3330 499\n[/level up reward]",
			want: []LevelUpReward{{Level: 83, ItemID: 9001, Count: 7}, {Level: 85, ItemID: 3330, Count: 499}},
		},
		{
			name: "删除85级配置",
			text: "[level up reward]\n86 1 2 9002 3\n[/level up reward]",
			want: []LevelUpReward{{Level: 86, ItemID: 9002, Count: 3}},
		},
		{name: "无奖励配置", text: "[other]\n1\n[/other]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			catalog, err := LoadLevelUpRewards(ctx, progressionMemorySource{LevelUpRewardPVFPath: tc.text})
			if err != nil {
				t.Fatal(err)
			}
			repos := levelRewardSeed(t)
			character, _, _ := repos.Character.Load(ctx, "77")
			character.Level = 82
			if err := repos.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			if err := repos.Skill.Save(ctx, dnfrepo.SkillRecord{CharacterID: "77", Points: dnfrepo.SkillPointState{SyncedLevel: 82}}); err != nil {
				t.Fatal(err)
			}
			commitRewardLevels(t, repos, 86)
			var resolved []LevelUpReward
			cmd := rewardCommand()
			cmd.ResolveAttachment = func(reward LevelUpReward) (dnfrepo.MailAttachment, error) {
				resolved = append(resolved, reward)
				return dnfrepo.MailAttachment{ItemID: reward.ItemID, Count: reward.Count}, nil
			}
			deliveries, err := DeliverLevelUpRewards(ctx, repos, catalog, cmd)
			if err != nil || len(deliveries) != len(tc.want) || !reflect.DeepEqual(resolved, tc.want) {
				t.Fatalf("deliveries=%+v resolved=%+v want=%+v err=%v", deliveries, resolved, tc.want, err)
			}
			mailbox, _, err := repos.Mailbox.Load(ctx, "77")
			if err != nil || len(mailbox.Mails) != len(tc.want) {
				t.Fatalf("mailbox=%+v err=%v", mailbox, err)
			}
			character, _, _ = repos.Character.Load(ctx, "77")
			for i, want := range tc.want {
				if deliveries[i].LevelUpReward != want {
					t.Fatalf("delivery=%+v want=%+v", deliveries[i], want)
				}
				mail := mailbox.Mails[deliveries[i].MailID]
				if len(mail.Attachments) != 1 || mail.Attachments[0].ItemID != want.ItemID || mail.Attachments[0].Count != want.Count {
					t.Fatalf("attachment=%+v want=%+v", mail.Attachments, want)
				}
				suffix := strconv.Itoa(want.Level)
				if character.Stats["level_up_reward_item_"+suffix] != want.ItemID || character.Stats["level_up_reward_count_"+suffix] != want.Count {
					t.Fatalf("奖励回执未采用PVF: %+v", character.Stats)
				}
			}
			for level := 83; level <= 86; level++ {
				if character.Stats[dnfrepo.LevelUpEventKey(level)] >= 0 {
					t.Fatalf("event%d not consumed", level)
				}
				if _, configured := catalog.Reward(level); !configured && character.Stats["level_up_reward_mail_"+strconv.Itoa(level)] != 0 {
					t.Fatalf("未配置的%d级被默认补发", level)
				}
			}
		})
	}
}

func TestLevelUpEventsFollowAtomicCommitAndRollback(t *testing.T) {
	ctx := context.Background()
	repos := seedProgressionCommitRepositories(t, ctx)
	plan := validProgressionCommitPlan()
	if _, err := Commit(ctx, repos.CharacterProgression, plan); err != nil {
		t.Fatal(err)
	}
	c, _, _ := repos.Character.Load(ctx, plan.CharacterID)
	for level := plan.Expected.Level + 1; level <= plan.Next.Level; level++ {
		if c.Stats[dnfrepo.LevelUpEventKey(level)] <= 0 {
			t.Fatalf("missing level%d event", level)
		}
	}
	before := dnfrepo.CloneCharacter(c)
	if result, err := Commit(ctx, repos.CharacterProgression, plan); err != nil || !result.Idempotent {
		t.Fatalf("replay=%+v %v", result, err)
	}
	c, _, _ = repos.Character.Load(ctx, plan.CharacterID)
	if !reflect.DeepEqual(before, c) {
		t.Fatal("replay changed event or character")
	}
	repos = seedProgressionCommitRepositories(t, ctx)
	err := repos.CharacterProgression.WithinCharacterProgression(ctx, plan.CharacterID, func(characters dnfrepo.CharacterRepository, skills dnfrepo.SkillRepository) error {
		c, _, _ := characters.Load(ctx, plan.CharacterID)
		sk, _, _ := skills.Load(ctx, plan.CharacterID)
		if err := ApplyCharacterExperience(&c, &sk, plan.Next.Level, plan.Next.Experience, plan.Next.SkillPoints, time.Now()); err != nil {
			return err
		}
		if err := characters.Save(ctx, c); err != nil {
			return err
		}
		return errors.New("injected skill write failure")
	})
	if err == nil {
		t.Fatal("missing failure")
	}
	c, _, _ = repos.Character.Load(ctx, plan.CharacterID)
	if c.Level != plan.Expected.Level || dnfrepo.HasPendingLevelUp(c) {
		t.Fatalf("rolled-back event leaked=%+v", c)
	}
}
