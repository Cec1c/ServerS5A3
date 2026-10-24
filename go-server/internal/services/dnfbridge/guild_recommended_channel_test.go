package dnfbridge

import (
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func guildRecommendedChannelFixture(t *testing.T) (*Service, []*gameSession, dnfrepo.Group, *channelcatalog.Catalog) {
	t.Helper()
	s, peers, repos := guildTextFixture(t)
	s.options.channelInfoFile = filepath.Join("testdata", "channel_info.etc")
	s.options.channelServerID = 1
	s.options.channelAdvertiseID = 0
	catalog, script, err := s.loadChannelAssets()
	if err != nil {
		t.Fatal(err)
	}
	s.catalog, s.channelScript = catalog, script
	channel, found := catalog.Channel(19)
	if !found {
		t.Fatal("fixture is missing ordinary channel 19")
	}
	peers[0].channel = channel
	peers[1].channel = channel
	return s, peers, repos, catalog
}

func assertGuildRecommendedManagement(t *testing.T, s *Service, session *gameSession, guild *dnfguild.Guild) {
	t.Helper()
	grade := guild.Members[strconv.Itoa(int(session.selectedCharacterID))].Grade
	want := buildCurrentGuildManagementBody(
		guild,
		len(s.currentGuildOnlineChannels(guild, session)),
		s.gameplayNow(),
		grade,
	)
	if got := guildReply(t, session, 70); !bytes.Equal(got, want) {
		t.Fatalf("NOTI70=%x want=%x", got, want)
	}
}

func TestGuildRecommendedChannelNativeDirectoryFlow(t *testing.T) {
	s, peers, repos, catalog := guildRecommendedChannelFixture(t)
	registry, err := dnfguild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if registry.Guilds[1].RecommendedChannel != 0 {
		t.Fatal("fixture starts configured")
	}

	handled, err := currentGameplayModules.DispatchUpper(
		s,
		peers[0],
		uint16(dnfenum.CmdPacketSetGuildRecommandChannel),
		1,
		nil,
	)
	if !handled {
		t.Fatal("current client op830 is not handled")
	}
	if err != nil {
		t.Fatal(err)
	}
	first, _ := splitCurrentGameServerUpperPacketAuto(t, peers[0].conn.(*bufferConn).write.Bytes())
	if first.Header.MsgID != uint16(dnfenum.CmdPacketSetGuildRecommandChannel) {
		t.Fatalf("first reply op=%d want=830", first.Header.MsgID)
	}
	if got := guildReply(t, peers[0], uint16(dnfenum.CmdPacketSetGuildRecommandChannel)); !bytes.Equal(got, []byte{1}) {
		t.Fatalf("op830 ACK=%x", got)
	}
	registry, err = dnfguild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	guild := registry.Guilds[1]
	if guild.RecommendedRegion != 0 || guild.RecommendedChannel != 19 {
		t.Fatalf("saved region/channel=%d/%d want advertised 0/19", guild.RecommendedRegion, guild.RecommendedChannel)
	}
	assertGuildRecommendedManagement(t, s, peers[0], guild)
	assertGuildRecommendedManagement(t, s, peers[1], guild)
	if peers[2].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("recommended channel refresh crossed guilds")
	}

	firstUpdatedAt := guild.UpdatedAt
	for _, peer := range peers {
		peer.conn.(*bufferConn).write.Reset()
	}
	guildDispatch(t, s, peers[0], uint16(dnfenum.CmdPacketSetGuildRecommandChannel), nil)
	if got := guildReply(t, peers[0], uint16(dnfenum.CmdPacketSetGuildRecommandChannel)); !bytes.Equal(got, []byte{1}) {
		t.Fatalf("replay ACK=%x", got)
	}
	registry, err = dnfguild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	guild = registry.Guilds[1]
	if !guild.UpdatedAt.Equal(firstUpdatedAt) {
		t.Fatal("replay changed guild UpdatedAt")
	}
	assertGuildRecommendedManagement(t, s, peers[0], guild)
	assertGuildRecommendedManagement(t, s, peers[1], guild)
	if peers[2].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("replay refresh crossed guilds")
	}

	fresh := &Service{
		options:            s.options,
		repositoryProvider: s.repositoryProvider,
		gameSessions:       map[uint16]*gameSession{},
		catalog:            s.currentCatalog(),
		channelScript:      s.currentChannelScript(),
	}
	channel, _ := catalog.Channel(19)
	relog := &gameSession{
		conn:                                &bufferConn{},
		accountID:                           "a2",
		selectedCharacterID:                 2,
		characterGeneration:                 91,
		channel:                             channel,
		initialTownLegacySceneReadyAccepted: true,
		townActorOwnerChannel:               currentSceneObjectContext,
		townSceneReadyCharacterID:           2,
	}
	fresh.gameSessions[2] = relog
	if err := fresh.sendCurrentInitialTownGuildStateProjectionLocked(relog, "recommended-channel-relog"); err != nil {
		t.Fatal(err)
	}
	assertGuildRecommendedManagement(t, fresh, relog, guild)
}

func TestGuildRecommendedChannelRequiresActualMaster(t *testing.T) {
	for _, mode := range []string{"ordinary_member", "custom_permission"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, repos, _ := guildRecommendedChannelFixture(t)
			ctx := context.Background()
			registry, err := dnfguild.Load(ctx, repos.Settings)
			if err != nil {
				t.Fatal(err)
			}
			g := registry.Guilds[1]
			peer := peers[1]
			switch mode {
			case "custom_permission":
				g.GradePermissions = map[byte]uint32{4: dnfguild.GradePermissionMask}
			}
			if err := dnfguild.Save(ctx, repos.Settings, registry, s.gameplayNow()); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			guildDispatch(t, s, peer, uint16(dnfenum.CmdPacketSetGuildRecommandChannel), nil)
			if got := guildReply(t, peer, uint16(dnfenum.CmdPacketSetGuildRecommandChannel)); !bytes.Equal(got, []byte{0, 19}) {
				t.Fatalf("master-only ACK=%x", got)
			}
			after, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected request changed state")
			}
			for _, other := range peers {
				if other != peer && other.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("rejected request refreshed peer")
				}
			}
		})
	}
}

func TestGuildRecommendedChannelIgnoresCharacterValueClass(t *testing.T) {
	s, peers, repos, _ := guildRecommendedChannelFixture(t)
	before, _, _ := repos.Settings.Load(context.Background(), dnfguild.RegistryScope)
	handled, err := currentGameplayModules.DispatchUpper(
		s,
		peers[0],
		uint16(dnfenum.CmdPacketSetGuildRecommandChannel),
		0,
		nil,
	)
	if !handled || err != nil {
		t.Fatalf("class0 handled=%v err=%v", handled, err)
	}
	if peers[0].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("class0 character value update produced a guild save reply")
	}
	after, _, _ := repos.Settings.Load(context.Background(), dnfguild.RegistryScope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("class0 character value update changed guild recommendation")
	}
}

func TestGuildRecommendedChannelRejectsInvalidRequestAndDirectory(t *testing.T) {
	for _, mode := range []string{"non_empty", "no_catalog", "missing_channel", "special_channel", "advertise_250", "wrong_account", "unbound", "save_failure"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, repos, catalog := guildRecommendedChannelFixture(t)
			ctx := context.Background()
			body := []byte(nil)
			want := byte(7)
			switch mode {
			case "non_empty":
				body, want = []byte{0, 19, 0, 0}, currentGuildGenericError
			case "no_catalog":
				s.catalog = nil
			case "missing_channel":
				peers[0].channel.ID = 65535
			case "special_channel":
				channel, found := firstNonResidentGuildChannel(catalog)
				if !found {
					t.Fatal("fixture has no special channel")
				}
				peers[0].channel = channel
			case "advertise_250":
				s.options.channelAdvertiseID = 250
			case "wrong_account":
				peers[0].accountID, want = "wrong-owner", currentGuildGenericError
			case "unbound":
				delete(s.gameSessions, 1)
				want = currentGuildGenericError
			case "save_failure":
				broken := repos
				broken.Guild = guildTextFailUoW{repos.Guild}
				s.repositoryProvider = func() (dnfrepo.Group, bool) { return broken, true }
				want = currentGuildGenericError
			}
			before, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			guildDispatch(t, s, peers[0], uint16(dnfenum.CmdPacketSetGuildRecommandChannel), body)
			if got := guildReply(t, peers[0], uint16(dnfenum.CmdPacketSetGuildRecommandChannel)); !bytes.Equal(got, []byte{0, want}) {
				t.Fatalf("ACK=%x want=%d", got, want)
			}
			after, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected request changed registry")
			}
			for _, peer := range peers[1:] {
				if peer.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("rejected request refreshed peers")
				}
			}
		})
	}
}

func firstNonResidentGuildChannel(catalog *channelcatalog.Catalog) (channelcatalog.Channel, bool) {
	for _, channel := range catalog.Channels() {
		if !channelcatalog.IsResidentChannel(channel) {
			return channel, true
		}
	}
	return channelcatalog.Channel{}, false
}

type guildRecommendedChannelHookUoW struct {
	base dnfrepo.GuildUnitOfWork
	hook func()
}

func (u guildRecommendedChannelHookUoW) WithinGuild(ctx context.Context, owners []dnfrepo.GuildOwner, apply func(dnfrepo.Group) error) error {
	return u.base.WithinGuild(ctx, owners, func(tx dnfrepo.Group) error {
		u.hook()
		return apply(tx)
	})
}

func TestGuildRecommendedChannelRevalidatesGenerationAndDirectory(t *testing.T) {
	for _, mode := range []string{"stale_generation", "directory_replaced"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, repos, _ := guildRecommendedChannelFixture(t)
			ctx := context.Background()
			wrapped := repos
			wrapped.Guild = guildRecommendedChannelHookUoW{
				base: repos.Guild,
				hook: func() {
					if mode == "stale_generation" {
						s.mu.Lock()
						peers[0].characterGeneration++
						s.mu.Unlock()
						return
					}
					s.mu.Lock()
					s.catalog = nil
					s.mu.Unlock()
				},
			}
			s.repositoryProvider = func() (dnfrepo.Group, bool) { return wrapped, true }
			before, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			guildDispatch(t, s, peers[0], uint16(dnfenum.CmdPacketSetGuildRecommandChannel), nil)
			if mode == "directory_replaced" {
				if got := guildReply(t, peers[0], uint16(dnfenum.CmdPacketSetGuildRecommandChannel)); !bytes.Equal(got, []byte{0, 7}) {
					t.Fatalf("directory replacement ACK=%x", got)
				}
			} else if peers[0].conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("stale generation received an ACK")
			}
			after, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("stale request changed registry")
			}
			for _, peer := range peers[1:] {
				if peer.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("stale request refreshed peers")
				}
			}
		})
	}
}
