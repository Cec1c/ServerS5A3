package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/premium"
	r "longheng.io/server/internal/modules/dnf/repository"
)

type guildBuffWire struct {
	op             uint16
	index, seconds uint32
}

func guildBuffWires(t *testing.T, session *gameSession) []guildBuffWire {
	t.Helper()
	var rows []guildBuffWire
	for wire := session.conn.(*bufferConn).write.Bytes(); len(wire) > 0; {
		p, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		wire = rest
		if p.Header.MsgID != 487 && p.Header.MsgID != 488 {
			continue
		}
		want := 5
		if p.Header.MsgID == 487 {
			want = 17
		}
		if len(p.Body) != want || p.Body[0] != 1 {
			t.Fatalf("invalid buff packet %d:%x", p.Header.MsgID, p.Body)
		}
		row := guildBuffWire{op: p.Header.MsgID, index: binary.LittleEndian.Uint32(p.Body[1:])}
		if want == 17 {
			if binary.LittleEndian.Uint32(p.Body[5:]) != 0 || binary.LittleEndian.Uint32(p.Body[13:]) != 0 {
				t.Fatal("unknown buff parameters")
			}
			row.seconds = binary.LittleEndian.Uint32(p.Body[9:])
		}
		rows = append(rows, row)
	}
	return rows
}

func guildBenefitsFixture(t *testing.T) (*Service, []*gameSession, r.Group, *fakeCurrentDungeonDeathTimerQueue) {
	t.Helper()
	s, p, repos := guildPurchaseFixture(t)
	q := newFakeCurrentDungeonDeathTimerQueue()
	s.gameplayTimers = q
	source := cargoQuerySource()
	source["etc/(r)guild.etc"] = "[guild contents list]\n1 5 169 -1 0 10150602 0 36000 `permanent`\n2 3 167 1 0 10149667 0 200 `stats1`\n3 1 10 1 0 10149666 0 200 `exp1`\n5 3 167 7 0 10149667 0 1400 `stats7`\n[/guild contents list]\n[additional guild contents list]\n[/additional guild contents list]\n[guild cargo size limit]\n56"
	cat, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = cat
	reg, _ := guild.Load(context.Background(), repos.Settings)
	reg.Guilds[1].Mileage = 100000
	if err := guild.Save(context.Background(), repos.Settings, reg, q.Now()); err != nil {
		t.Fatal(err)
	}
	return s, p, repos, q
}

func TestGuildBenefitNativePurchaseRefreshAndRenewal(t *testing.T) {
	s, p, repos, q := guildBenefitsFixture(t)
	for i, id := range []uint32{1, 2, 3, 5} {
		for _, peer := range p {
			peer.conn.(*bufferConn).write.Reset()
		}
		sendGuildPurchase(t, s, p[0], id, uint16(30+i))
		if !bytes.Equal(guildReply(t, p[0], 761), []byte{1}) {
			t.Fatal("purchase rejected")
		}
		if p[2].conn.(*bufferConn).write.Len() != 0 {
			t.Fatal("other guild received benefits")
		}
		reg, _ := guild.Load(context.Background(), repos.Settings)
		for _, peer := range p[:2] {
			if !bytes.Equal(guildReply(t, peer, 70), buildCurrentGuildManagementBody(reg.Guilds[1], 2, q.Now(), reg.Guilds[1].Members[peer.accountID[1:]].Grade)) {
				t.Fatal("benefit projection stale")
			}
		}
	}
	want := []guildBuffWire{{488, 167, 0}, {488, 169, 0}, {487, 167, 8 * 86400}, {487, 169, 0}}
	if got := guildBuffWires(t, p[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("renewal=%+v want=%+v", got, want)
	}
	reg, _ := guild.Load(context.Background(), repos.Settings)
	if reg.Guilds[1].Mileage != 62200 || len(reg.BenefitPurchases) != 4 {
		t.Fatal("incorrect benefit charge")
	}
	p[0].conn.(*bufferConn).write.Reset()
	sendGuildPurchase(t, s, p[0], 5, 33)
	if len(guildBuffWires(t, p[0])) != 0 {
		t.Fatal("receipt replay reset buff tick")
	}
	reg, _ = guild.Load(context.Background(), repos.Settings)
	if reg.Guilds[1].Mileage != 62200 {
		t.Fatal("replay charged again")
	}
}

func TestGuildBenefitExpiryRestorationAndOwnerChange(t *testing.T) {
	s, p, repos, q := guildBenefitsFixture(t)
	ctx := context.Background()
	reg, _ := guild.Load(ctx, repos.Settings)
	g := reg.Guilds[1]
	g.Benefits = map[byte]guild.Benefit{3: {Type: 3, Value: 167, ExpiresAt: q.Now().Add(30 * 24 * time.Hour)}, 5: {Type: 5, Value: 169}}
	if err := guild.Save(ctx, repos.Settings, reg, q.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.sendCurrentGuildManagementState(p[0], g); err != nil {
		t.Fatal(err)
	}
	if got := guildBuffWires(t, p[0]); !reflect.DeepEqual(got, []guildBuffWire{{487, 167, 30 * 86400}, {487, 169, 0}}) {
		t.Fatal("initial", got)
	}
	p[0].conn.(*bufferConn).write.Reset()
	if err := s.restoreCurrentGuildBuffProjection(p[0]); err != nil {
		t.Fatal(err)
	}
	if got := guildBuffWires(t, p[0]); !reflect.DeepEqual(got, []guildBuffWire{{488, 167, 0}, {488, 169, 0}, {487, 167, 30 * 86400}, {487, 169, 0}}) {
		t.Fatal("actor restore", got)
	}
	p[0].conn.(*bufferConn).write.Reset()
	q.mu.Lock()
	q.now = q.now.Add(30 * 24 * time.Hour)
	q.mu.Unlock()
	peer, _ := s.boundGameSessionCharacterSnapshot(p[0])
	q.mu.Lock()
	timer := q.active[guildBenefitTimerName(p[0])]
	q.mu.Unlock()
	if timer == nil {
		t.Fatal("no expiry timer")
	}
	q.fire(timer, false)
	if got := guildBuffWires(t, p[0]); !reflect.DeepEqual(got, []guildBuffWire{{488, 167, 0}, {488, 169, 0}, {487, 169, 0}}) {
		t.Fatal("expiration", got)
	}
	p[0].conn.(*bufferConn).write.Reset()
	p[0].characterGeneration++
	s.expireCurrentGuildBenefit(peer)
	if p[0].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("stale timer sent to replaced owner")
	}
	if err := s.writeCurrentGuildBuffProjection(p[0], nil, false); err != nil {
		t.Fatal(err)
	}
	if got := guildBuffWires(t, p[0]); !reflect.DeepEqual(got, []guildBuffWire{{488, 169, 0}}) {
		t.Fatal("departure did not remove effect", got)
	}
}

func TestGuildBenefitMonsterExperienceActuallyCommits(t *testing.T) {
	for _, mode := range []string{"active", "expired", "foreign_owner", "growth_contract"} {
		t.Run(mode, func(t *testing.T) {
			s, run, repos := prepareCurrentDungeonMonsterExperienceTest(t)
			ctx := context.Background()
			reg := guild.NewRegistry()
			c, _, _ := repos.Character.Load(ctx, "99")
			c.Name = "经验会员"
			if err := repos.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			g, err := reg.Create(c, "经验公会", s.gameplayNow())
			if err != nil {
				t.Fatal(err)
			}
			expires := s.gameplayNow().Add(time.Hour)
			if mode == "expired" {
				expires = s.gameplayNow().Add(-time.Hour)
			}
			g.Benefits = map[byte]guild.Benefit{1: {Type: 1, Value: 10, ExpiresAt: expires}}
			if mode == "foreign_owner" {
				m := g.Members["99"]
				m.AccountID = "other"
				g.Members["99"] = m
			}
			if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
				t.Fatal(err)
			}
			want := int64(220)
			if mode == "expired" || mode == "foreign_owner" {
				want = 200
			}
			if mode == "growth_contract" {
				s.premiumCatalog = &currentPremiumCatalog{effectsByType: map[int64]currentPremiumEffectInfo{premium.TypeBonusExp: {BonusExperiencePercent: 20}}}
				account, _, _ := repos.Account.Load(ctx, "account-1")
				if account.AccountID == "" {
					account.AccountID = "account-1"
				}
				account.Metadata = map[string]string{"premium_expire_84": "2147483647"}
				if err := repos.Account.Save(ctx, account); err != nil {
					t.Fatal(err)
				}
				want = 260
			}
			session := &gameSession{selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: run}}
			got, err := s.awardCurrentDungeonMonsterExperience(ctx, session, run, run.Room.Snapshot().Monsters[0])
			if err != nil || got.Character.Stats["exp"] != want {
				t.Fatalf("got=%+v err=%v want=%d", got, err, want)
			}
			stored, _, _ := repos.Character.Load(ctx, "99")
			if stored.Stats["exp"] != want {
				t.Fatal("bonus not persisted")
			}
			// The same stale runtime cannot commit the death again.
			if _, err := s.awardCurrentDungeonMonsterExperience(ctx, session, run, run.Room.Snapshot().Monsters[0]); err == nil {
				t.Fatal("stale death repeated")
			}
		})
	}
}

func TestGuildBenefitExpiryRetriesSceneTransitionAndCancelsOnClose(t *testing.T) {
	s, p, _, q := guildBenefitsFixture(t)
	sendGuildPurchase(t, s, p[0], 2, 30)
	q.mu.Lock()
	task := q.active[guildBenefitTimerName(p[0])]
	q.now = q.now.Add(24 * time.Hour)
	q.mu.Unlock()
	if task == nil {
		t.Fatal("missing expiry timer")
	}
	p[0].conn.(*bufferConn).write.Reset()
	p[0].townMu.Lock()
	q.fire(task, false)
	p[0].townMu.Unlock()
	q.mu.Lock()
	retry := q.active[guildBenefitTimerName(p[0])]
	q.mu.Unlock()
	if retry == nil || retry.delay != time.Second || p[0].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("busy scene expiry not deferred")
	}
	q.fire(retry, false)
	if !reflect.DeepEqual(guildBuffWires(t, p[0]), []guildBuffWire{{488, 167, 0}}) {
		t.Fatal("deferred expiry did not clear effect")
	}
	sendGuildPurchase(t, s, p[0], 2, 31)
	s.closeCurrentGuildCreateSession(p[0])
	q.mu.Lock()
	remaining := q.active[guildBenefitTimerName(p[0])]
	q.mu.Unlock()
	if remaining != nil {
		t.Fatal("closed session retained expiry timer")
	}
}
