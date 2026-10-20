// assets extracts the real channel table and listener ports from a supplied PVF.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/channelinfo"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func main() {
	pvf := flag.String("pvf", "data/dnf/Script.pvf", "supplied PVF (read only)")
	output := flag.String("output", "data/dnf/channel_info.etc", "decoded table output; refuses to replace different bytes")
	channel := flag.String("channel", "", "optional externally maintained channel table (read only)")
	flag.Parse()
	if err := run(*pvf, *output, *channel); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(pvf, output, channel string) error {
	archive, err := platformpvf.Open(pvf)
	if err != nil {
		return err
	}
	text, source, err := channelinfo.ReadPVFText(archive, []string{"etc/channel_info.etc"})
	if err != nil {
		return err
	}
	data := []byte(text)
	sourceName := source.Path
	if channel != "" {
		data, err = os.ReadFile(channel)
		if err != nil {
			return err
		}
		sourceName = "external:" + filepath.Base(channel)
	}
	index, err := channelinfo.Parse(data)
	if err != nil {
		return err
	}
	catalog, err := channelcatalog.New(index, channelcatalog.Options{ServerID: dnfenum.LoginChannelServerIndex})
	if err != nil {
		return err
	}
	if _, ok := catalog.Channel(dnfenum.BootstrapChannelID); !ok {
		return fmt.Errorf("PVF lacks bootstrap channel %d", dnfenum.BootstrapChannelID)
	}
	if _, err = channelinfo.Build90CNOnlineScript(data, dnfenum.LoginChannelServerIndex, 0, dnfenum.BootstrapChannelID); err != nil {
		return err
	}
	existing, err := os.ReadFile(output)
	if err == nil && !bytes.Equal(existing, data) {
		return fmt.Errorf("refusing to overwrite a different channel table: %s", output)
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if os.IsNotExist(err) {
		if err = os.MkdirAll(filepath.Dir(output), 0755); err != nil {
			return err
		}
		if err = os.WriteFile(output, data, 0644); err != nil {
			return err
		}
	}
	ports := make([]int, 0, len(catalog.Channels()))
	for _, c := range catalog.Channels() {
		ports = append(ports, c.Port)
	}
	sum := sha256.Sum256(data)
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"source": sourceName, "format": archive.Format(), "entries": archive.FileCount(),
		"sha256": hex.EncodeToString(sum[:]), "ports": ports, "channels": len(ports),
	})
}
