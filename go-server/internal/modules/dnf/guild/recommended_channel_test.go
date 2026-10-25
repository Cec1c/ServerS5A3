package guild

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestGuildRecommendedChannelPersistsPermissionAndReplay(t *testing.T) {
	reg, g, master, member := textRegistry(t)
	now := g.UpdatedAt.Add(time.Hour)
	if _, err := reg.SetRecommendedChannel(master, 0, 19, now); err != nil {
		t.Fatal(err)
	}
	if g.RecommendedRegion != 0 || g.RecommendedChannel != 19 || !g.UpdatedAt.Equal(now) {
		t.Fatal("recommended channel did not persist native fields")
	}
	before, _ := json.Marshal(reg)
	if _, err := reg.SetRecommendedChannel(master, 0, 19, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(reg)
	if string(before) != string(after) || !g.UpdatedAt.Equal(now) {
		t.Fatal("replay changed state or UpdatedAt")
	}
	if _, err := reg.SetRecommendedChannel(member, 7, 11, now); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("default member error=%v", err)
	}
	g.GradePermissions = map[byte]uint32{1: 0, 4: GradePermissionMask}
	before, _ = json.Marshal(reg)
	if _, err := reg.SetRecommendedChannel(member, 0, 19, now); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("permission mask must not grant master authority: %v", err)
	}
	after, _ = json.Marshal(reg)
	if string(before) != string(after) {
		t.Fatal("non-master save mutated state")
	}
	if _, err := reg.SetRecommendedChannel(master, 249, math.MaxUint32, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if g.RecommendedRegion != 249 || g.RecommendedChannel != math.MaxUint32 {
		t.Fatal("master boundary selection not saved")
	}
	m := g.Members[master.CharacterID]
	m.Grade = 4
	g.Members[master.CharacterID] = m
	if _, err := reg.SetRecommendedChannel(master, 0, 19, now); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("invalid master grade: %v", err)
	}
	m = g.Members[member.CharacterID]
	m.Grade = 1
	g.Members[member.CharacterID] = m
	if _, err := reg.SetRecommendedChannel(member, 0, 19, now); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("forged master grade: %v", err)
	}

}

func TestGuildRecommendedChannelRejectsInvalidOwnershipAndBounds(t *testing.T) {
	reg, g, master, _ := textRegistry(t)
	before, _ := json.Marshal(reg)
	for _, tc := range []struct {
		name            string
		owner           dnfrepo.GuildOwner
		region, channel uint32
		want            error
	}{
		{"channel_zero", master, 0, 0, ErrInvalidRecommendedChannel},
		{"region_250", master, 250, 19, ErrInvalidRecommendedChannel},
		{"foreign", dnfrepo.GuildOwner{CharacterID: "99", AccountID: "a99"}, 0, 19, ErrNotMember},
		{"wrong_account", dnfrepo.GuildOwner{CharacterID: master.CharacterID, AccountID: "old"}, 0, 19, ErrNotMember},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := reg.SetRecommendedChannel(tc.owner, tc.region, tc.channel, time.Now()); !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
		})
	}
	after, _ := json.Marshal(reg)
	if string(before) != string(after) || g.RecommendedChannel != 0 {
		t.Fatal("invalid selection mutated registry")
	}
}

func TestGuildRecommendedChannelRollsBackOnSaveFailure(t *testing.T) {
	ctx := context.Background()
	repos := guildTestGroup(t, 2)
	reg, g, master, _ := textRegistry(t)
	now := time.Unix(1700000000, 0)
	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("recommended channel save failed")
	err := Update(ctx, lateFailGuildTx{base: repos.Guild, fail: boom}, []dnfrepo.GuildOwner{master}, now, func(_ dnfrepo.Group, current *Registry) error {
		_, err := current.SetRecommendedChannel(master, 0, 19, now.Add(time.Hour))
		return err
	})
	if !errors.Is(err, boom) {
		t.Fatalf("update error=%v", err)
	}
	loaded, err := Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.Guilds[g.ID]
	if got.RecommendedRegion != 0 || got.RecommendedChannel != 0 || !got.UpdatedAt.Equal(now) {
		t.Fatal("failed save retained recommended channel")
	}
}
