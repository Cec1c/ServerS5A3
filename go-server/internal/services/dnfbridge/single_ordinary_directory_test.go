package dnfbridge

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelinfo"
)

func TestSingleOrdinaryDirectoryKeepsRaidsAndAllLoginReplies(t *testing.T) {
	raw := []byte("[server] 1\n10 `ordinary` 11 `[granfloris]` 0 0 ``\n23 `anton` 23 `[anton_raid]` 0 0 ``\n28 `luke` 32 `[luke_raid]` 0 0 ``\n261 `fiend` 42 `[fiendwar]` 0 0 ``\n[/server]\n")
	for _, profile := range []string{"official-24.9", "90cn"} {
		t.Run(profile, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "channel_info.etc")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			s := &Service{options: options{channelInfoFile: path, channelServerID: 1, currentClientVersion: profile}}
			catalog, script, err := s.loadChannelAssets()
			if err != nil {
				t.Fatal(err)
			}
			s.catalog, s.channelScript = catalog, script
			if c, err := s.loginChannel(); err != nil || c.ID != 10 {
				t.Fatalf("login landing=%+v %v", c, err)
			}
			update, err := s.buildChannelUpdateBody(nil)
			if err != nil {
				t.Fatal(err)
			}
			if int(binary.LittleEndian.Uint32(update[20:24])) != 10010 {
				t.Fatalf("update destination=%x", update)
			}
			for _, group := range []string{"", "cain", "[cain]"} {
				initial := s.channelsForRequestMode(group, true)
				if len(initial) != 1 || initial[0].ID != 10 {
					t.Fatalf("bootstrap=%+v", initial)
				}
				if len(s.channelsForRequestMode(group, false)) != 4 {
					t.Fatal("manual directory hid dedicated channels")
				}
			}
			ids := []int{}
			for _, c := range s.channelsForRequest("raid") {
				ids = append(ids, c.ID)
			}
			if !reflect.DeepEqual(ids, []int{23, 28, 261}) {
				t.Fatalf("raids=%v", ids)
			}
			online, err := channelinfo.Parse(script)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := online.ServerChannel(0, 19); ok {
				t.Fatal("removed19 was synthesized")
			}
			if _, ok := online.ServerChannel(0, 10); !ok {
				t.Fatal("real10 missing")
			}
		})
	}
}
