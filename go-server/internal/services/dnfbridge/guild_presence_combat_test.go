package dnfbridge

import (
	"bytes"
	"context"
	"sync"
	"testing"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestGuildPresenceBackgroundSnapshotDefersDuringDungeon(t *testing.T) {
	for _, enterAfterQueue := range []bool{false, true} {
		name := "already-in-dungeon"
		if enterAfterQueue {
			name = "entered-after-queued"
		}
		t.Run(name, func(t *testing.T) {
			s, peers, repos, _ := guildPresenceFixture(t)
			p := peers[0]
			p.guild.memberListRequested = true
			conn := p.conn.(*bufferConn)
			conn.write.Reset()
			identities := &guildRosterCountCharacter{CharacterRepository: repos.Character}
			narrow := &guildRosterDisplayCharacters{}
			repos.Character = identities
			repos.Guild = &guildRosterReadUOW{GuildUnitOfWork: repos.Guild, GuildReadUnitOfWork: repos.Guild.(dnfrepo.GuildReadUnitOfWork), characters: narrow}
			s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p.ctx = ctx
			if err := s.startGameSessionEvents(ctx, p); err != nil {
				t.Fatal(err)
			}
			defer stopTestGameSessionEvents(t, s, p)
			if !enterAfterQueue {
				p.dungeon.runtime = &runtimeDungeonState{}
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			if err := s.tryPostGameSessionCharacterEvent(p, "test-enter-dungeon", p.selectedCharacterID, p.characterGeneration, func() error {
				close(entered)
				<-release
				if enterAfterQueue {
					p.dungeon.mu.Lock()
					p.dungeon.runtime = &runtimeDungeonState{}
					p.dungeon.mu.Unlock()
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			waitTestSignal(t, entered, "dungeon transition gate")
			peer, _ := s.boundGameSessionCharacterSnapshot(p)
			for i := 0; i < 20; i++ {
				s.requestCurrentGuildPresenceRefresh(peer, 1)
			}
			once.Do(func() { close(release) })
			if err := s.callGameSession(ctx, p, "ordinary-after-presence", func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			if n := conn.write.Len(); n != 0 {
				t.Errorf("background presence wrote %d bytes during dungeon combat", n)
			}
			if identities.identities.Load() != 0 || identities.loads.Load() != 0 || narrow.queries != 0 {
				t.Errorf("deferred presence still read character/roster: identity=%d full=%d roster=%d", identities.identities.Load(), identities.loads.Load(), narrow.queries)
			}
			p.guild.mu.Lock()
			pending := p.guild.presenceRefresh
			p.guild.mu.Unlock()
			if pending != nil || !p.events.active.Load() {
				t.Fatal("deferred presence retained a retry or stopped ordinary events")
			}
			// Explicit requests must still work, even inside the dungeon.
			if err := s.callGameSession(ctx, p, "explicit-guild-window", func() error {
				conn.write.Reset()
				guildDispatch(t, s, p, 140, nil)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if body := guildReply(t, p, 140); len(body) == 0 || body[0] != 1 {
				t.Fatal("explicit in-dungeon roster request was suppressed")
			}
			registry, err := dnfguild.Load(ctx, repos.Settings)
			if err != nil {
				t.Fatal(err)
			}
			member := registry.Guilds[1].Members["2"]
			member.Memo = "after-combat"
			registry.Guilds[1].Members["2"] = member
			if err = dnfguild.Save(ctx, repos.Settings, registry, s.gameplayNow()); err != nil {
				t.Fatal(err)
			}
			// Town refresh reads the latest committed view, not a saved combat snapshot.
			if err = s.callGameSession(ctx, p, "return-to-town", func() error {
				p.dungeon.mu.Lock()
				p.dungeon.runtime = nil
				p.dungeon.mu.Unlock()
				conn.write.Reset()
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			s.requestCurrentGuildPresenceRefresh(peer, 1)
			if err = s.callGameSession(ctx, p, "town-refresh-drain", func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(guildReply(t, p, 140), rosterNameBytes("after-combat")) {
				t.Fatal("town presence replayed stale roster or failed to resume")
			}
		})
	}
}
