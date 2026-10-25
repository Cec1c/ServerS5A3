package guild

import (
	"context"
	"errors"
	"math"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemexpiration"
	r "longheng.io/server/internal/modules/dnf/repository"
)

const essenceFixtureText = "[daily max ap]\n5000\n[ap reward]\n10 15 15 30 0\n[/ap reward]\n[gp reward]\n30 15 40 15\n[/gp reward]"

func essenceTestCatalog(t *testing.T) *EssenceCatalog {
	t.Helper()
	c, err := ParseEssenceCatalog(essenceFixtureText)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestEssenceCatalogMatchedPVFAndMalformed(t *testing.T) {
	for _, text := range []string{"", strings.Replace(essenceFixtureText, "5000", "0", 1), strings.Replace(essenceFixtureText, "10 15 15 30 0", "10 15 15", 1), strings.Replace(essenceFixtureText, "10 15 15 30 0", "-1 15 15 30 0", 1), essenceFixtureText + "\n[daily max ap]\n5000"} {
		if _, err := ParseEssenceCatalog(text); err == nil {
			t.Fatalf("accepted malformed %q", text)
		}
	}
	if path := os.Getenv("GUILD_ESSENCE_EXTRACTED_ETC"); path != "" {
		text, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		c, err := ParseEssenceCatalog(string(text))
		if err != nil || c.dailyMax != 5000 || c.ap != [4]uint32{10, 15, 15, 30} {
			t.Fatal(c, err)
		}
	} else {
		t.Log("matched PVF not configured; structural parser cases only")
	}
}

func essenceTestGroup(t *testing.T) (r.Group, r.GuildOwner, time.Time) {
	t.Helper()
	repos := guildTestGroup(t, 2)
	reg, g, owner, _ := textRegistry(t)
	g.Essence.GP = 17
	now := time.Date(2026, 10, 25, 21, 59, 59, 0, time.UTC)
	if err := Save(context.Background(), repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	return repos, owner, now
}

func TestEssenceLoginConcurrentRestartDayAndCharacterScope(t *testing.T) {
	repos, owner, now := essenceTestGroup(t)
	ctx := context.Background()
	c := essenceTestCatalog(t)
	var wg sync.WaitGroup
	out := make(chan EssenceIncome, 12)
	errs := make(chan error, 12)
	for range 12 {
		wg.Go(func() {
			result, err := GrantEssenceLogin(ctx, repos.Guild, owner, c, now, func() bool { return true })
			out <- result
			errs <- err
		})
	}
	wg.Wait()
	close(out)
	close(errs)
	var total uint32
	for x := range out {
		total += x.AP
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if total != 10 {
		t.Fatal(total)
	}
	reg, _ := Load(ctx, repos.Settings)
	if reg.Guilds[1].Essence != (EssenceBalance{AP: 10, GP: 17}) {
		t.Fatal(reg.Guilds[1].Essence)
	}
	// Independent character in the same account has an independent daily grant.
	ch, _, _ := repos.Character.Load(ctx, "2")
	ch.AccountID = owner.AccountID
	repos.Character.Save(ctx, ch)
	m := reg.Guilds[1].Members["2"]
	m.AccountID = owner.AccountID
	reg.Guilds[1].Members["2"] = m
	Save(ctx, repos.Settings, reg, now)
	res, err := GrantEssenceLogin(ctx, repos.Guild, r.GuildOwner{AccountID: owner.AccountID, CharacterID: "2"}, c, now, func() bool { return true })
	if err != nil || res.AP != 10 {
		t.Fatal(res, err)
	}
	res, err = GrantEssenceLogin(ctx, repos.Guild, owner, c, now.Add(time.Second), func() bool { return true })
	if err != nil || res.AP != 10 {
		t.Fatal("06:00", res, err)
	}
	if _, err = GrantEssenceLogin(ctx, repos.Guild, owner, c, now, func() bool { return true }); !errors.Is(err, ErrEssenceState) {
		t.Fatal("clock rollback", err)
	}
}

func TestEssenceCapPartialZeroMembershipAndOverflow(t *testing.T) {
	for _, tc := range []struct {
		name    string
		earned  int64
		balance uint32
		event   EssenceEvent
		want    uint32
		fail    bool
	}{
		{"partial", 4995, 10, EssenceGuildClear, 5, false}, {"cap", 5000, 10, EssencePartyGuildClear, 0, false}, {"party", 0, 10, EssencePartyGuildClear, 30, false}, {"recommended", 0, 10, EssenceRecommendedClear, 15, false}, {"overflow", 0, math.MaxUint32, EssenceLogin, 0, true}, {"unknown", 0, 10, 4, 0, true}, {"corrupt", -1, 10, EssenceLogin, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repos, owner, now := essenceTestGroup(t)
			ctx := context.Background()
			reg, _ := Load(ctx, repos.Settings)
			reg.Guilds[1].Essence.AP = tc.balance
			Save(ctx, repos.Settings, reg, now)
			ch, _, _ := repos.Character.Load(ctx, owner.CharacterID)
			ch.Stats[essenceDayEnd] = itemexpiration.NextDailyDeleteAt(now).Unix()
			ch.Stats[essenceDayAP] = tc.earned
			before := r.CloneCharacter(ch)
			res, err := ApplyEssenceIncome(ctx, repos.Settings, &ch, essenceTestCatalog(t), tc.event, now)
			if (err != nil) != tc.fail || res.AP != tc.want {
				t.Fatal(res, err)
			}
			if tc.fail && !reflect.DeepEqual(ch, before) {
				t.Fatal("failed operation changed caller")
			}
			loaded, _ := Load(ctx, repos.Settings)
			if loaded.Guilds[1].Essence.AP != tc.balance+tc.want || loaded.Guilds[1].Essence.GP != 17 {
				t.Fatal("balance")
			}
		})
	}
}

func TestEssenceDailySurvivesGuildChangeAndLateSessionRollback(t *testing.T) {
	repos, owner, now := essenceTestGroup(t)
	ctx := context.Background()
	c := essenceTestCatalog(t)
	before, _, _ := repos.Settings.Load(ctx, RegistryScope)
	beforeChar, _, _ := repos.Character.Load(ctx, owner.CharacterID)
	calls := 0
	_, err := GrantEssenceLogin(ctx, repos.Guild, owner, c, now, func() bool { calls++; return calls < 2 })
	after, _, _ := repos.Settings.Load(ctx, RegistryScope)
	afterChar, _, _ := repos.Character.Load(ctx, owner.CharacterID)
	if !errors.Is(err, ErrCharacterNotFound) || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeChar, afterChar) {
		t.Fatal("late rollback", err)
	}
	if _, err = GrantEssenceLogin(ctx, repos.Guild, owner, c, now, func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	reg, _ := Load(ctx, repos.Settings)
	g := reg.Guilds[1]
	delete(reg.Guilds, 1)
	g.ID = 2
	reg.Guilds[2] = g
	g.Essence = EssenceBalance{}
	if err = Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	res, err := GrantEssenceLogin(ctx, repos.Guild, owner, c, now, func() bool { return true })
	if err != nil || res.AP != 0 || res.GuildID != 2 {
		t.Fatal("switch", res, err)
	}
	reg, _ = Load(ctx, repos.Settings)
	if reg.Guilds[2].Essence.AP != 0 {
		t.Fatal("switch regrant")
	}
	delete(reg.Guilds, 2)
	Save(ctx, repos.Settings, reg, now)
	res, err = GrantEssenceLogin(ctx, repos.Guild, owner, c, now.Add(24*time.Hour), func() bool { return true })
	if err != nil || res != (EssenceIncome{}) {
		t.Fatal("nonmember", res, err)
	}
}
