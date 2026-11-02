package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

type forbiddenGuildFullCharacter struct{ r.CharacterRepository }

func (forbiddenGuildFullCharacter) Load(context.Context, string) (r.CharacterRecord, bool, error) {
	return r.CharacterRecord{}, false, errors.New("display attempted full character load")
}

type displayOnlyGuildUOW struct {
	r.GuildUnitOfWork
	r.GuildReadUnitOfWork
	reads     int
	afterRead func()
}

func (*displayOnlyGuildUOW) WithinGuild(context.Context, []r.GuildOwner, func(r.Group) error) error {
	return errors.New("display attempted guild writer lock")
}
func (u *displayOnlyGuildUOW) WithinGuildRead(ctx context.Context, fn func(r.GuildReadView) error) error {
	u.reads++
	err := u.GuildReadUnitOfWork.WithinGuildRead(ctx, fn)
	if u.afterRead != nil {
		u.afterRead()
	}
	return err
}

// This regression fails on the original handlers even with the new read
// repository available: they still call Character.Load / WithinGuild.
func TestGuildReadDisplayAvoidsFullCharactersAndWriterLock(t *testing.T) {
	for _, count := range []int{191, 1000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s, peers, repos := guildTextFixture(t)
			ctx := context.Background()
			reg, err := guild.Load(ctx, repos.Settings)
			if err != nil {
				t.Fatal(err)
			}
			g := reg.Guilds[1]
			g.MemberLimit = 1000
			seed, _, _ := repos.Character.Load(ctx, "2")
			for i := 4; len(g.Members) < count; i++ {
				c := r.CloneCharacter(seed)
				c.CharacterID, c.AccountID, c.Name = fmt.Sprint(i), fmt.Sprint("a", i), fmt.Sprint("Member", i)
				if err := repos.Character.Save(ctx, c); err != nil {
					t.Fatal(err)
				}
				if _, err := reg.JoinDirect(g.ID, c, s.gameplayNow()); err != nil {
					t.Fatal(err)
				}
			}
			if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
				t.Fatal(err)
			}
			u := &displayOnlyGuildUOW{GuildUnitOfWork: repos.Guild, GuildReadUnitOfWork: repos.Guild.(r.GuildReadUnitOfWork)}
			repos.Guild, repos.Character = u, forbiddenGuildFullCharacter{repos.Character}
			s.repositoryProvider = func() (r.Group, bool) { return repos, true }
			for _, opcode := range []uint16{67, 139, 140, 141, 1300, 1301} {
				t.Run(fmt.Sprint(opcode), func(t *testing.T) {
					peers[0].conn.(*bufferConn).write.Reset()
					guildDispatch(t, s, peers[0], opcode, nil)
					body := guildReply(t, peers[0], opcode)
					if len(body) < 3 || body[0] != 1 {
						t.Fatalf("op%d failed: %x", opcode, body)
					}
					if opcode == 1301 && binary.LittleEndian.Uint32(body[1:5]) != uint32(count) {
						t.Fatal("lost members")
					}
				})
			}
			if u.reads != 6 {
				t.Fatal("unexpected snapshot count", u.reads)
			}
		})
	}
}

func TestGuildReadDisplayDropsRetiredGeneration(t *testing.T) {
	for _, opcode := range []uint16{67, 1301} {
		t.Run(fmt.Sprint(opcode), func(t *testing.T) {
			s, peers, repos := guildTextFixture(t)
			u := &displayOnlyGuildUOW{GuildUnitOfWork: repos.Guild, GuildReadUnitOfWork: repos.Guild.(r.GuildReadUnitOfWork)}
			u.afterRead = func() { s.mu.Lock(); peers[0].characterGeneration++; s.mu.Unlock() }
			repos.Guild = u
			s.repositoryProvider = func() (r.Group, bool) { return repos, true }
			guildDispatch(t, s, peers[0], opcode, nil)
			if peers[0].conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("reply sent to retired character generation")
			}
		})
	}
}
