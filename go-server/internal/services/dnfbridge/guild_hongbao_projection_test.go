package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestGuildHongbaoPointsProjectsCurrentCharacterWithoutSaving(t *testing.T) {
	s, peers, repos := guildTextFixture(t)
	ctx := context.Background()
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	m := reg.Guilds[1].Members["1"]
	m.Level, m.Name, m.ContributionPoints, m.Mileage = 20, "旧角色名", 74, 999
	reg.Guilds[1].Members["1"] = m
	if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	actor, _, err := repos.Character.Load(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	actor.Level, actor.Name = 90, "当前角色名"
	if err := repos.Character.Save(ctx, actor); err != nil {
		t.Fatal(err)
	}
	owner := r.GuildOwner{AccountID: actor.AccountID, CharacterID: actor.CharacterID}
	receipt, err := json.Marshal(map[string]any{"Version": 1, "Owner": owner, "GuildDungeon": guild.DungeonClearResult{GuildID: 1, Contribution: 60}})
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Settings.Save(ctx, r.SettingsRecord{Scope: r.GuildSettlementReceiptScope(owner, "projection-clear"), Values: map[string]string{"receipt": string(receipt)}, UpdatedAt: s.gameplayNow()}); err != nil {
		t.Fatal(err)
	}
	before, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
	for range 2 {
		guildDispatch(t, s, peers[0], 1301, nil)
		packet := guildReply(t, peers[0], 1301)
		if len(packet) != 85 || packet[0] != 1 {
			t.Fatalf("packet=%x", packet)
		}
		row := packet[5:45]
		if row[4] != 90 || !bytes.Equal(bytes.TrimRight(row[5:35], "\x00"), rosterNameBytes(actor.Name)) || binary.LittleEndian.Uint32(row[35:39]) != 14 {
			t.Fatalf("stale/incorrect hongbao projection: %x", row)
		}
	}
	after, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
	current, _, _ := repos.Character.Load(ctx, "1")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(actor, current) {
		t.Fatal("query changed persisted state")
	}
}

func TestGuildHongbaoPointsDoesNotProjectReownedOrDeletedCharacter(t *testing.T) {
	for _, mode := range []string{"reowned", "deleted"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, repos := guildTextFixture(t)
			ctx := context.Background()
			actor, _, _ := repos.Character.Load(ctx, "2")
			if mode == "reowned" {
				actor.AccountID = "new-owner"
			} else {
				actor.Stats["delete_flag"] = 1
			}
			actor.Name = "不得投影"
			if err := repos.Character.Save(ctx, actor); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			guildDispatch(t, s, peers[0], 1301, nil)
			packet := guildReply(t, peers[0], 1301)
			if len(packet) != 45 || packet[0] != 1 || binary.LittleEndian.Uint32(packet[1:5]) != 1 || binary.LittleEndian.Uint32(packet[5:9]) != 1 {
				t.Fatalf("invalid member in roster: %x", packet)
			}
			after, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("query changed membership")
			}
		})
	}
}
