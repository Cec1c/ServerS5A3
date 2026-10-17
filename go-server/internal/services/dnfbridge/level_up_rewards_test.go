package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"longheng.io/server/internal/modules/dnf/adventuregroup"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"strings"
	"testing"
)

func levelUpNoticeFixture(t *testing.T) (*Service, *gameSession, *bufferConn, *bufferConn, dnfrepo.Group) {
	t.Helper()
	repos := memory.NewMemoryGroup()
	ctx := context.Background()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "a", Name: "升级勇士", Level: 85, Stats: map[string]int64{dnfrepo.LevelUpEventKey(85): 1792100000}}); err != nil {
		t.Fatal(err)
	}
	conn, observer := &bufferConn{}, &bufferConn{}
	source := &gameSession{conn: conn, selectedCharacterID: 77, accountID: "a", characterGeneration: 1}
	peer := &gameSession{conn: observer, selectedCharacterID: 88, accountID: "b", characterGeneration: 2}
	source.channel.ID = 19
	peer.channel.ID = 20
	service := &Service{options: options{currentClientVersion: "official-24.9", gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain}, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }, gameSessions: map[uint16]*gameSession{77: source, 88: peer}}
	return service, source, conn, observer, repos
}

type failingCapsuleAccount struct{ dnfrepo.AccountRepository }

func (f failingCapsuleAccount) Save(context.Context, dnfrepo.AccountRecord) error {
	return errors.New("injected capsule account save failure")
}

type failingCapsuleSettlement struct {
	dnfrepo.CharacterSettlementUnitOfWork
}

func (f failingCapsuleSettlement) WithinCharacterSettlement(ctx context.Context, id string, apply func(dnfrepo.Group) error) error {
	return f.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, id, func(tx dnfrepo.Group) error { tx.Account = failingCapsuleAccount{tx.Account}; return apply(tx) })
}

func TestRealPVFAdventureCapsuleRecordsLevelEventInConsumptionTransaction(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("未指定真实PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := progression.Load(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	threshold, err := tables.ThresholdToNext(1)
	if err != nil || threshold == 0 {
		t.Fatalf("threshold=%d %v", threshold, err)
	}
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "提交", true: "回滚"}[failed], func(t *testing.T) {
			s, session, repos, q, _, _ := newAdventureShopExpirationFixture(t, adventureShopExpirationSource(""))
			s.initialEquipmentArchive = archive
			s.options.gameUpperHeader = gameUpperHeaderServer16
			s.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
			ctx := context.Background()
			c, _, _ := repos.Character.Load(ctx, "31")
			c.Level = 1
			c.Stats = map[string]int64{"exp": int64(threshold - 1)}
			if err := repos.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			if err := repos.Skill.Save(ctx, dnfrepo.SkillRecord{CharacterID: "31", Points: dnfrepo.SkillPointState{SyncedLevel: 1}}); err != nil {
				t.Fatal(err)
			}
			a, _, _ := repos.Account.Load(ctx, "shop-owner")
			runtime, err := adventuregroup.ParseRuntimeState(a, s.adventureGroupTable.Runtime(), q.now)
			if err != nil {
				t.Fatal(err)
			}
			runtime.GrowthExperience = 1
			encoded, err := runtime.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			a.Metadata[adventuregroup.RuntimeStateMetadataKey] = encoded
			if err := repos.Account.Save(ctx, a); err != nil {
				t.Fatal(err)
			}
			if failed {
				repos.CharacterSettlement = failingCapsuleSettlement{repos.CharacterSettlement}
				s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
			}
			if err := s.handleCurrentAdventureGrowthCapsule(session, nil); err != nil {
				t.Fatal(err)
			}
			c, _, _ = repos.Character.Load(ctx, "31")
			a, _, _ = repos.Account.Load(ctx, "shop-owner")
			runtime, err = adventuregroup.ParseRuntimeState(a, s.adventureGroupTable.Runtime(), q.now)
			if err != nil {
				t.Fatal(err)
			}
			if failed {
				if c.Level != 1 || dnfrepo.HasPendingLevelUp(c) || runtime.GrowthExperience != 1 {
					t.Fatalf("partial capsule commit=%+v %+v", c, runtime)
				}
			} else {
				if c.Level != 2 || c.Stats[dnfrepo.LevelUpEventKey(2)] <= 0 || runtime.GrowthExperience != 0 {
					t.Fatalf("capsule event/consumption not atomic=%+v %+v", c, runtime)
				}
			}
		})
	}
}
func level85Delivery() progression.LevelUpRewardDelivery {
	return progression.LevelUpRewardDelivery{LevelUpReward: progression.LevelUpReward{Level: 85, ItemID: 3330, Count: 500}, CharacterName: "升级勇士", MailID: "1"}
}

func TestCurrentLevelUpRewardCanceledLoadDoesNotCacheResourceError(t *testing.T) {
	s := &Service{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if catalog, err := s.currentLevelUpRewardCatalog(ctx); !errors.Is(err, context.Canceled) || catalog != nil {
		t.Fatalf("canceled load=%v %v", catalog, err)
	}
	if s.levelUpRewards.loaded || s.levelUpRewards.catalog != nil || s.levelUpRewards.catalogErr != nil {
		t.Fatal("请求取消污染了进程级PVF缓存")
	}
}

func TestCurrentLevelUpRewardBroadcastUsesNativeNameOnlyDSTR(t *testing.T) {
	body, ok := buildCurrentLevelUpRewardBroadcast(level85Delivery())
	if !ok {
		t.Fatal("valid committed reward rejected")
	}
	wantName := []byte{0xc9, 0xfd, 0xbc, 0xb6, 0xd3, 0xc2, 0xca, 0xbf}
	if len(body) != 5+len(wantName) || body[0] != 6 || binary.LittleEndian.Uint32(body[1:5]) != uint32(len(wantName)) || !bytes.Equal(body[5:], wantName) {
		t.Fatalf("wire=%x", body)
	}
	for _, mutate := range []func(*progression.LevelUpRewardDelivery){func(d *progression.LevelUpRewardDelivery) { d.Level = 84 }, func(d *progression.LevelUpRewardDelivery) { d.Count = 499 }, func(d *progression.LevelUpRewardDelivery) { d.ItemID = 3331 }, func(d *progression.LevelUpRewardDelivery) { d.CharacterName = "" }, func(d *progression.LevelUpRewardDelivery) { d.CharacterName = "x\x00y" }, func(d *progression.LevelUpRewardDelivery) { d.CharacterName = strings.Repeat("中", 128) }} {
		d := level85Delivery()
		mutate(&d)
		if _, ok := buildCurrentLevelUpRewardBroadcast(d); ok {
			t.Fatalf("invalid native claim=%+v", d)
		}
	}
}

func TestCurrentLevelUpRewardBroadcastReachesOtherChannelAfterMailCommit(t *testing.T) {
	s, source, conn, observer, repos := levelUpNoticeFixture(t)
	if err := repos.Mailbox.Save(context.Background(), dnfrepo.MailboxRecord{CharacterID: "77", Mails: map[string]dnfrepo.MailRecord{"1": {MailID: "1"}}}); err != nil {
		t.Fatal(err)
	}
	identity, _ := s.boundGameSessionCharacterSnapshot(source)
	s.notifyCurrentLevelUpRewards(context.Background(), identity, []progression.LevelUpRewardDelivery{level85Delivery()})
	alarm, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if alarm.Header.MsgID != 99 || alarm.Header.Classification != 0 {
		t.Fatalf("alarm=%+v", alarm)
	}
	native, rest := splitGameServerUpperPacket(t, rest)
	if native.Header.MsgID != 86 || native.Header.Classification != 0 || len(rest) != 0 {
		t.Fatalf("native=%+v rest=%x", native, rest)
	}
	peer, rest := splitGameServerUpperPacket(t, observer.write.Bytes())
	if peer.Header.MsgID != 86 || peer.Header.Classification != 0 || !bytes.Equal(peer.Body, native.Body) || len(rest) != 0 {
		t.Fatalf("observer=%+v rest=%x", peer, rest)
	}
	if len(s.characterAssetReservations) != 0 {
		t.Fatal("notification leaked identity reservation")
	}
}

func TestCurrentLevelUpRewardStaleGenerationDoesNotNotifyReplacementOrWorld(t *testing.T) {
	s, source, conn, observer, _ := levelUpNoticeFixture(t)
	identity, _ := s.boundGameSessionCharacterSnapshot(source)
	s.mu.Lock()
	source.characterGeneration++
	s.mu.Unlock()
	s.notifyCurrentLevelUpRewards(context.Background(), identity, []progression.LevelUpRewardDelivery{level85Delivery()})
	if conn.write.Len() != 0 || observer.write.Len() != 0 {
		t.Fatal("stale source projected a reward")
	}
}

func TestRealPVFUnifiedLevelUpRewardsCommitMailAndNativeBroadcast(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("未指定真实PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s, source, conn, observer, repos := levelUpNoticeFixture(t)
	s.initialEquipmentArchive = archive
	// 此记录是隔离内存仓储中的新升级事件，不查询或操作玩家数据库。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.currentLevelUpRewardCatalog(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled initial load=%v", err)
	}
	s.afterCurrentLevelUpRewardCommand(source)
	mailbox, found, err := repos.Mailbox.Load(context.Background(), "77")
	if err != nil || !found || len(mailbox.Mails) != 1 {
		t.Fatalf("mailbox=%+v found=%v err=%v", mailbox, found, err)
	}
	for _, mail := range mailbox.Mails {
		if len(mail.Attachments) != 1 || mail.Attachments[0].ItemID != 3330 || mail.Attachments[0].Count != 500 {
			t.Fatalf("attachment=%+v", mail)
		}
	}
	first, peerFirst := conn.write.Len(), observer.write.Len()
	if first == 0 || peerFirst == 0 {
		t.Fatalf("native projection missing source=%d peer=%d", first, peerFirst)
	}
	s.afterCurrentLevelUpRewardCommand(source)
	s.recoverCurrentLevelUpRewards(context.Background())
	if conn.write.Len() != first || observer.write.Len() != peerFirst {
		t.Fatal("replay or clock duplicated notification")
	}
	character, _, _ := repos.Character.Load(context.Background(), "77")
	if character.Stats[dnfrepo.LevelUpEventKey(85)] >= 0 || character.Stats["level_up_reward_count_85"] != 500 {
		t.Fatalf("permanent receipt=%+v", character.Stats)
	}
	catalog, err := s.currentLevelUpRewardCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for level := 70; level <= 86; level++ {
		if _, ok := catalog.Reward(level); !ok {
			t.Fatalf("actual PVF missing level%d", level)
		}
	}
	for level := 87; level <= 90; level++ {
		if _, ok := catalog.Reward(level); ok {
			t.Fatalf("invented level%d", level)
		}
	}
}
