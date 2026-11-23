package dnfbridge

// This file deliberately references only symbols present at baseline
// 0a1fd587. Copy it into a clean baseline worktree and run the same command to
// compare the old per-member encoder and un-deduplicated cache:
//
//   go test ./internal/services/dnfbridge -run '^$' +//     -bench 'BenchmarkGuild(MemberListProjection|PresenceSharedRoster)' +//     -benchmem -count=5

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
)

func benchmarkGuildRoster(members int) *dnfguild.Guild {
	g := &dnfguild.Guild{
		ID:          42,
		Name:        "基准公会",
		Level:       10,
		MemberLimit: 5000,
		Members:     make(map[string]dnfguild.Member, members),
	}
	for i := 1; i <= members; i++ {
		id := fmt.Sprint(i)
		g.Members[id] = dnfguild.Member{
			CharacterID: id,
			AccountID:   "acc:" + id,
			Name:        fmt.Sprintf("成员%d", i),
			Memo:        "备注",
			Level:       90,
			Job:         "1",
			GrowType:    "0",
			Grade:       4,
		}
	}
	return g
}

func BenchmarkGuildMemberListProjection(b *testing.B) {
	for _, members := range []int{190, 939, 5000} {
		b.Run(fmt.Sprintf("members=%d", members), func(b *testing.B) {
			g := benchmarkGuildRoster(members)
			online := map[string]byte{"1": 11}
			now := time.Unix(1_700_000_000, 0).UTC()
			b.ReportAllocs()
			b.SetBytes(int64(members))
			for b.Loop() {
				chunks := buildCurrentGuildMemberListChunks(g, uint16(dnfenum.CmdPacketGuildAllMemberList), true, true, online, now, 1)
				if len(chunks) == 0 {
					b.Fatal("empty projection")
				}
			}
		})
	}
}

func BenchmarkGuildPresenceSharedRoster(b *testing.B) {
	g := benchmarkGuildRoster(939)
	online := map[string]byte{"1": 11}
	now := time.Unix(1_700_000_000, 0).UTC()
	opcode := uint16(dnfenum.CmdPacketGuildAllMemberList)

	b.Run("cold", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			shared := &guildPresenceReadCache{g: g}
			if chunks := shared.sharedRosterChunks(nil, g, opcode, true, true, online, now, 1); len(chunks) != 4 {
				b.Fatalf("chunks=%d want 4", len(chunks))
			}
		}
	})

	b.Run("hit", func(b *testing.B) {
		shared := &guildPresenceReadCache{g: g}
		shared.sharedRosterChunks(nil, g, opcode, true, true, online, now, 1)
		b.ReportAllocs()
		for b.Loop() {
			if chunks := shared.sharedRosterChunks(nil, g, opcode, true, true, online, now, 1); len(chunks) != 4 {
				b.Fatalf("chunks=%d want 4", len(chunks))
			}
		}
	})

	for _, viewers := range []int{4, 8, 16} {
		b.Run(fmt.Sprintf("concurrent=%d", viewers), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				shared := &guildPresenceReadCache{g: g}
				var wg sync.WaitGroup
				for range viewers {
					wg.Add(1)
					go func() {
						defer wg.Done()
						_ = shared.sharedRosterChunks(nil, g, opcode, true, true, online, now, 1)
					}()
				}
				wg.Wait()
			}
		})
	}
}
