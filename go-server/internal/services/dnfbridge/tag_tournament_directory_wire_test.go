package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"longheng.io/server/internal/modules/dnf/channelinfo"
)

// An NPC may refresh the complete cain directory before selecting its page.
// Verify the actual encrypted response, not merely the in-memory group filter.
// This contract ends at the server wire and does not claim UI acceptance.
func TestExternalTagTournamentCainAndNamedWireAgree(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_EMPIRE_CHANNEL_INFO")
	if path == "" {
		t.Skip("requires the approved complete external channel table")
	}
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(channelDirectoryOwnerRangeGateEnv, "1")
	for _, encoding := range []string{"utf8", "gbk"} {
		t.Run(encoding, func(t *testing.T) {
			s := &Service{options: options{
				channelInfoFile: path, channelServerID: 1, channelAdvertiseID: 1,
				currentClientVersion: "official-24.9", channelScriptEncoding: encoding,
				serverIP: "127.0.0.1",
			}}
			catalog, script, err := s.loadChannelAssets()
			if err != nil {
				t.Fatal(err)
			}
			s.catalog, s.channelScript = catalog, script
			if len(catalog.Channels()) != 119 {
				t.Fatalf("approved source must preserve 119 channels, got %d", len(catalog.Channels()))
			}
			scriptPacket, err := s.buildScriptBody()
			if err != nil {
				t.Fatal(err)
			}
			download, err := decryptCompressedChannelData(scriptPacket, s.aesKey())
			if err != nil {
				t.Fatal(err)
			}
			download = bytes.TrimRight(download, "\x00")
			if encoding == "gbk" {
				download, err = simplifiedchinese.GBK.NewDecoder().Bytes(download)
				if err != nil {
					t.Fatal(err)
				}
			}
			if !bytes.Equal(download, script) {
				t.Fatal("downloaded script differs after lossless decoding")
			}
			index, err := channelinfo.Parse(download)
			if err != nil {
				t.Fatal(err)
			}
			channel, ok := index.ServerChannel(1, 187)
			if !ok || channel.Type != 39 || channel.AreaKey != "tagtournament" || channel.NameKey != "帝国竞技场" {
				t.Fatalf("downloaded empire row: %+v found=%v", channel, ok)
			}
			for _, dungeon := range []int{320, 336} {
				mapped := index.ChannelsForDungeon(dungeon)
				if len(mapped) != 1 || mapped[0].ID != 187 {
					t.Fatalf("dungeon %d mapping: %+v", dungeon, mapped)
				}
			}

			readDirectory := func(group string, bootstrap bool, wantCount int) []byte {
				t.Helper()
				packet, err := s.buildChannelInfoBodyFor([]byte(group), bootstrap)
				if err != nil {
					t.Fatal(err)
				}
				plain, err := decryptCompressedChannelData(packet, s.aesKey())
				if err != nil || len(plain) < 12 {
					t.Fatalf("directory decrypt: bytes=%d err=%v", len(plain), err)
				}
				count := int(binary.LittleEndian.Uint32(plain[8:12]))
				if binary.LittleEndian.Uint32(plain[:4]) != 1 || binary.LittleEndian.Uint32(plain[4:8]) != 1 || count != wantCount || len(plain) < 12+count*48 {
					t.Fatalf("directory %q bootstrap=%v header=%x bytes=%d", group, bootstrap, plain[:12], len(plain))
				}
				var empire []byte
				for i := 0; i < count; i++ {
					row := plain[12+i*48 : 12+(i+1)*48]
					if string(bytes.TrimRight(row[:20], "\x00")) != "#ch.187" {
						continue
					}
					if empire != nil {
						t.Fatal("duplicate empire advertisement")
					}
					if binary.LittleEndian.Uint32(row[20:24]) != 500 || binary.LittleEndian.Uint32(row[24:28]) != 0 || binary.LittleEndian.Uint32(row[44:48]) != 10187 || string(bytes.TrimRight(row[28:44], "\x00")) != "127.0.0.1" {
						t.Fatalf("invalid empire wire row: %x", row)
					}
					empire = append([]byte(nil), row...)
				}
				return empire
			}
			manual := readDirectory("cain", false, 111)
			named := readDirectory("tagtournament", false, 1)
			if len(manual) != 48 || !bytes.Equal(manual, named) {
				t.Fatalf("manual/named empire row mismatch: %x / %x", manual, named)
			}
			if got := readDirectory("cain", true, 1); got != nil {
				t.Fatal("initial town login advertised the dedicated empire channel")
			}
		})
	}
	stored, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(stored, source) {
		t.Fatal("projection modified the external table")
	}
}
