package dnfbridge

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelinfo"
)

// 同时校验原始目录、实际下载加密包和团队广告，避免只改显示而断开既有内部路由。
func TestSpecialChannelDirectoryNativeProjection(t *testing.T) {
	raw := []byte("[server] 1\n19 `ordinary` 11 `[granfloris]` 0 0 0 ``\n" +
		"23 `anton` 23 `[anton_raid]` 0 0 0 ``\n28 `luke` 32 `[luke_raid]` 0 0 0 ``\n" +
		"184 `fiend` 42 `[fiendwar]` 0 0 0 ``\n185 `fiend2` 42 `[fiendwar]` 0 0 0 ``\n" +
		"186 `beast` 42 `[beast]` 0 0 0 ``\n187 `empire` 39 `[tagtournament]` 0 0 0 ``\n[/server]\n")
	for _, profile := range []string{"official-24.9", "90cn", ""} {
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
			packet, err := s.buildScriptBody()
			if err != nil {
				t.Fatal(err)
			}
			plain, err := decryptCompressedChannelData(packet, s.aesKey())
			if err != nil {
				t.Fatal(err)
			}
			index, err := channelinfo.Parse(bytes.TrimRight(plain, "\x00"))
			if err != nil {
				t.Fatal(err)
			}
			source, err := channelinfo.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			for _, original := range source.Channels() {
				online, found := index.ServerChannel(0, original.ID)
				if !found {
					t.Fatalf("频道丢失：%d", original.ID)
				}
				wantType := original.Type
				if profile == "official-24.9" && (original.ID == 184 || original.ID == 185) {
					wantType = 45
				}
				if online.Type != wantType {
					t.Fatalf("原生频道类型：%d=%d，期望%d", original.ID, online.Type, wantType)
				}
				online.ServerID, online.Type = original.ServerID, original.Type
				if !reflect.DeepEqual(online, original) {
					t.Fatalf("非目标字段变化：%+v / %+v", online, original)
				}
			}
			for _, group := range []string{"raid", "attackraid"} {
				channels := s.channelsForRequest(group)
				ids := []int{}
				for _, c := range channels {
					ids = append(ids, c.ID)
				}
				if !reflect.DeepEqual(ids, []int{23, 28, 184, 185}) {
					t.Fatalf("团队目录%s=%v", group, ids)
				}
			}
			if route, ok := currentRaidRouteForChannel(42); !ok || route != 2 {
				t.Fatal("既有超时空内部路由被改动")
			}
			if channel, ok := catalog.Channel(184); !ok || channel.Type != 42 {
				t.Fatal("原始配置类型被改写")
			}
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, raw) {
				t.Fatal("正式源文本被改写")
			}
		})
	}
}
