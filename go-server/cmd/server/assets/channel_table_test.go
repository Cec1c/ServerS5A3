package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type channelReader struct {
	called int
	text   string
}

func (r *channelReader) ReadText(path string) (string, error) {
	r.called++
	if path != "etc/channel_info.etc" {
		return "", os.ErrNotExist
	}
	return r.text, nil
}

func TestExternalChannelDoesNotReadPVFDirectory(t *testing.T) {
	input := []byte("external channel input\r\n")
	file := filepath.Join(t.TempDir(), "channel.override.etc")
	if err := os.WriteFile(file, input, 0o600); err != nil {
		t.Fatal(err)
	}
	reader := &channelReader{text: "different PVF input"}
	data, source, err := readChannelTable(reader, file)
	if err != nil || !bytes.Equal(data, input) || source != "external:channel.override.etc" || reader.called != 0 {
		t.Fatalf("data=%q source=%q reads=%d err=%v", data, source, reader.called, err)
	}
}

func TestMissingExternalChannelDoesNotFallBackToPVF(t *testing.T) {
	reader := &channelReader{text: "PVF must not replace the selected input"}
	_, _, err := readChannelTable(reader, filepath.Join(t.TempDir(), "missing.etc"))
	if !errors.Is(err, os.ErrNotExist) || reader.called != 0 {
		t.Fatalf("reads=%d err=%v", reader.called, err)
	}
}

func TestExplicitPVFExportRemainsAvailable(t *testing.T) {
	reader := &channelReader{text: "offline PVF export\n"}
	data, source, err := readChannelTable(reader, "")
	if err != nil || string(data) != reader.text || source != "etc/channel_info.etc" || reader.called != 1 {
		t.Fatalf("source=%q reads=%d err=%v", source, reader.called, err)
	}
}
