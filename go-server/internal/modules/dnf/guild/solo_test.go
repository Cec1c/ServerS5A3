package guild

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestSoloGuildAtomicReplayAndPolicyIsolation(t *testing.T) {
	ctx := context.Background()
	g := guildTestGroup(t, 2)
	req := guildRequest(1, 2, "单人公会", 21)
	req.Founder = r.GuildOwner{}
	req.Promotion = ""
	var wg sync.WaitGroup
	results := make(chan CreateResult, 8)
	errorsOut := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := CreateSolo(ctx, g.Guild, req, time.Now())
			results <- got
			errorsOut <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errorsOut)
	for err := range errorsOut {
		if err != nil {
			t.Fatal(err)
		}
	}
	fresh := 0
	for got := range results {
		if !got.Replay {
			fresh++
		}
		if len(got.Guild.Members) != 1 || got.GoldAfter != 600000 {
			t.Fatalf("result=%+v", got)
		}
	}
	if fresh != 1 {
		t.Fatalf("commits=%d", fresh)
	}
	other, _, _ := g.Character.Load(ctx, "2")
	if other.Stats["gold"] != 900000 {
		t.Fatal("unrelated character changed")
	}
	if _, err := CreateWithFoundingMember(ctx, g.Guild, req, time.Now()); err == nil {
		t.Fatal("solo receipt accepted by dual policy")
	}
	req.Founder = r.GuildOwner{AccountID: "a2", CharacterID: "2"}
	if _, err := CreateSolo(ctx, g.Guild, req, time.Now()); err == nil {
		t.Fatal("solo accepted a fabricated founder")
	}
}

func TestSoloGuildRejectsInvalidStateAndRollsBack(t *testing.T) {
	for _, scenario := range []string{"gold", "owner", "generation", "late-save", "duplicate", "already-member"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			g := guildTestGroup(t, 2)
			req := guildRequest(1, 2, "单人公会", 22)
			req.Founder = r.GuildOwner{}
			uow := g.Guild
			switch scenario {
			case "gold":
				c, _, _ := g.Character.Load(ctx, "1")
				c.Stats["gold"] = 299999
				_ = g.Character.Save(ctx, c)
			case "owner":
				req.Creator.AccountID = "a2"
			case "generation":
				req.Revalidate = func() bool { return false }
			case "late-save":
				uow = lateFailGuildTx{g.Guild, errors.New("late save failure")}
			case "duplicate", "already-member":
				old := req
				old.OperationID = "12345678901234567890123456789012"
				if scenario == "duplicate" {
					old.Creator = r.GuildOwner{AccountID: "a2", CharacterID: "2"}
				} else {
					old.Name = "已有公会"
				}
				if _, err := CreateSolo(ctx, g.Guild, old, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			before, _, _ := g.Character.Load(ctx, "1")
			regBefore, _ := Load(ctx, g.Settings)
			if _, err := CreateSolo(ctx, uow, req, time.Now()); err == nil {
				t.Fatal("invalid creation accepted")
			}
			after, _, _ := g.Character.Load(ctx, "1")
			regAfter, _ := Load(ctx, g.Settings)
			if after.Stats["gold"] != before.Stats["gold"] || len(regAfter.Guilds) != len(regBefore.Guilds) || len(regAfter.Creations) != len(regBefore.Creations) {
				t.Fatal("failed creation changed assets")
			}
		})
	}
}
