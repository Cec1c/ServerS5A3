package dnfbridge

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"longheng.io/server/internal/modules/dnf/channelinfo"
)

func TestChannelScriptEncodingOccursAfterUTF8Projection(t *testing.T) {
	raw := []byte("[dungeon]\n`[granfloris]` `格兰之森` 3\n[/dungeon]\n[server]\n1\n11 `格兰之森` 0 `[granfloris]` 5 0 0 0 0 0 0 0 0 0 0\n[/server]\n")
	path := filepath.Join(t.TempDir(), "channel_info.etc")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	index, err := channelinfo.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	channel, ok := index.ServerChannel(1, 11)
	if !ok || channel.NameKey != "格兰之森" {
		t.Fatal("UTF-8 source lost its native name")
	}
	online, err := channelinfo.Build90CNOnlineScript(raw, 1, 0, 11)
	if err != nil {
		t.Fatal(err)
	}
	for _, encoding := range []string{"", "utf8", "gbk"} {
		t.Run(encoding, func(t *testing.T) {
			s := &Service{options: options{channelScriptEncoding: encoding}, channelScript: append([]byte(nil), online...)}
			body, err := s.buildScriptBody()
			if err != nil {
				t.Fatal(err)
			}
			plain, err := decryptCompressedChannelData(body, s.aesKey())
			if err != nil {
				t.Fatal(err)
			}
			plain = bytes.TrimRight(plain, "\x00")
			if encoding == "gbk" {
				if bytes.Contains(plain, []byte("格兰之森")) {
					t.Fatal("UTF-8 label leaked into GBK wire text")
				}
				plain, err = simplifiedchinese.GBK.NewDecoder().Bytes(plain)
				if err != nil {
					t.Fatal(err)
				}
			}
			if !bytes.Equal(plain, online) || !bytes.Equal(s.channelScript, online) {
				t.Fatal("wire projection or cached UTF-8 source changed")
			}
			stored, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(stored, raw) {
				t.Fatal("wire encoding modified external resource")
			}
		})
	}
}

func TestChannelScriptEncodingRejectsUnsupportedText(t *testing.T) {
	for _, tc := range []struct{ encoding, text string }{{"gbk", "🚀"}, {"utf16", "plain"}} {
		s := &Service{options: options{channelScriptEncoding: tc.encoding}, channelScript: []byte(tc.text)}
		if body, err := s.buildScriptBody(); err == nil || len(body) != 0 {
			t.Fatal("unsupported text produced a channel script packet")
		}
	}
}
