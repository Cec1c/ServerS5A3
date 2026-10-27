package dnfbridge

import (
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
)

type guildFollowDirectoryCase struct {
	Name      string `json:"name"`
	Channel   int    `json:"channel"`
	Directory int    `json:"directory"`
	Kind      byte   `json:"kind"`
	IP        string `json:"ip"`
	Port      int    `json:"port"`
	ACK       string `json:"ack"`
}

func TestGuildFollowDirectoryExport(t *testing.T) {
	var cases []guildFollowDirectoryCase
	for _, name := range []string{"same_channel", "different_channel"} {
		s, a, b, ac, _ := prepareUserChannelTest(t)
		s.options.channelAdvertiseID = 1
		s.options.serverIP = "127.0.0.1"
		a.channel, _ = s.catalog.Channel(38)
		if name == "same_channel" {
			a.channel = b.channel
		}
		if _, err := s.handleCurrentUserChannel(a, 804, 1, officialFriendRequest(291, 0, 0, "来来来")); err != nil {
			t.Fatal(err)
		}
		ack, _ := splitGameServerUpperPacket(t, ac.write.Bytes())
		if ack.Body[0] != 1 {
			t.Fatalf("follow lookup failed: %x", ack.Body)
		}
		cases = append(cases, guildFollowDirectoryCase{Name: name, Channel: b.channel.ID, Directory: s.channelAdvertiseID(),
			Kind: b.channel.Type, IP: s.options.serverIP, Port: b.channel.Port, ACK: hex.EncodeToString(ack.Body)})
	}
	data, err := json.MarshalIndent(cases, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	exportGuildSocialPacket(t, "follow-directory.json", data)
}

type guildFollowEndpointConn struct {
	bufferConn
	port int
}

func (c *guildFollowEndpointConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: c.port}
}

func TestGuildFollowTargetFromNativeEndpoint(t *testing.T) {
	folder := os.Getenv("DNF_GUILD_SOCIAL_NATIVE_DIR")
	if folder == "" {
		t.Skip("set native follow connection export directory")
	}
	data, err := os.ReadFile(filepath.Join(folder, "follow-native-connections.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []guildFollowDirectoryCase
	if err := json.Unmarshal(data, &cases); err != nil || len(cases) != 2 {
		t.Fatalf("native follow cases=%d error=%v", len(cases), err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			s, next := firstLoginChannelFixture(t, 11)
			target, ok := s.channelForConn(&guildFollowEndpointConn{port: c.Port})
			if !ok || target.ID != c.Channel || c.IP != s.options.serverIP {
				t.Fatalf("native endpoint mapped to different channel: case=%+v target=%+v ok=%t", c, target, ok)
			}
			if _, ok := s.channelForConn(&guildFollowEndpointConn{port: 65534}); ok {
				t.Fatal("unadvertised endpoint aliased a channel")
			}
			previousChannel, _ := s.catalog.Channel(21)
			if c.Name == "same_channel" {
				previousChannel = target
			}
			previous := &gameSession{conn: &bufferConn{}, channel: previousChannel, accountID: next.accountID,
				selectedCharacterID: 29, loginPacketSeen: true, accountFromToken: true, loginChannelFlowKey: next.loginChannelFlowKey}
			if err := s.handleCurrentChannelExit(previous, []byte{1}, "guild_follow_native_test"); err != nil {
				t.Fatal(err)
			}
			next.channel = target
			if err := s.handleCurrentGameEndpointRequest(next, 1, currentObservedChannelReconnectDisplayProbeSize); err != nil {
				t.Fatal(err)
			}
			assertFirstChannelLoginSuccess(t, s, next, target)
			if next.channel.ID != c.Channel || next.coldChannelLanding != nil || next.channelExitHandoff == nil {
				t.Fatal("follow endpoint lost authenticated explicit channel target")
			}
		})
	}
}
