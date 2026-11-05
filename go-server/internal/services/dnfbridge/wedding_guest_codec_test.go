package dnfbridge

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestWeddingGuestCurrentNativeRequestGoldens(t *testing.T) {
	// Independently emitted by current 7ae native senders on ACP936; opaque
	// bytes are fixture zeroes and are never treated as a trusted request ID.
	for _, row := range []struct {
		raw     string
		random  bool
		target  uint32
		guid    uint16
		channel byte
	}{
		{"000000000000000000000000000100000000000000", true, 0, 0, 0},
		{"000000000000000000000000000051c3000041010a", false, 50001, 321, 10},
		{"000000000000000000000000000052c300000000ff", false, 50002, 0, 255},
	} {
		body, err := hex.DecodeString(row.raw)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := decodeCurrentWeddingGuestLetterRequest(body)
		if !ok || got.Random != row.random || got.Target != row.target || got.RuntimeID != row.guid || got.Hint != row.channel {
			t.Fatalf("native letter decoded %+v/%t", got, ok)
		}
		for _, bad := range [][]byte{body[:20], append(append([]byte(nil), body...), 0)} {
			if _, ok := decodeCurrentWeddingGuestLetterRequest(bad); ok {
				t.Fatal("accepted partial/trailing letter")
			}
		}
	}
	body, _ := hex.DecodeString("785634121027000008000000d0c2bbe9bfecc0d6")
	got, ok := decodeCurrentWeddingMoneyGiftRequest(body)
	if !ok || got.Key != 0x12345678 || got.Gold != 10000 || got.Wish != "新婚快乐" {
		t.Fatalf("native gift decoded %+v/%t", got, ok)
	}
	for _, bad := range [][]byte{body[:11], body[:len(body)-1], append(append([]byte(nil), body...), 0), append(make([]byte, 13), body...)} {
		if _, ok := decodeCurrentWeddingMoneyGiftRequest(bad); ok {
			t.Fatal("accepted gift length mismatch")
		}
	}
	for _, name := range []string{"gift-live-length19", "gift-live-length18"} {
		got, ok := decodeCurrentWeddingMoneyGiftRequest(weddingGuestNativeBody(t, name))
		if !ok || got.Key != 5 || got.Gold != 10000 {
			t.Fatalf("short actual-native body %s: %+v/%t", name, got, ok)
		}
	}
}

type weddingGuestWireFixture struct {
	Name           string         `json:"name"`
	Classification byte           `json:"classification"`
	Opcode         uint16         `json:"opcode"`
	PayloadHex     string         `json:"payload_hex"`
	Expect         map[string]any `json:"expect"`
}

func TestWeddingGuestCurrentWireFixtures(t *testing.T) {
	var fixtures []weddingGuestWireFixture
	add := func(name string, class byte, opcode uint16, body []byte, expect map[string]any) {
		fixtures = append(fixtures, weddingGuestWireFixture{name, class, opcode, hex.EncodeToString(body), expect})
	}
	letterSuccess := buildCurrentWeddingGuestLetterACK(1, 0, 8)
	if !bytes.Equal(letterSuccess, []byte{1, 0, 0, 0, 0, 8, 0}) {
		t.Fatalf("1038 ACK %x", letterSuccess)
	}
	add("letter-success", 1, 1038, letterSuccess, map[string]any{"flag": 1, "result": 0, "remaining": 8})
	for _, code := range []int32{-1, -2, -3, -4, -5, -6, -7} {
		body := buildCurrentWeddingGuestLetterACK(1, code, 8)
		if len(body) != 5 {
			t.Fatal("letter failure included a success suffix")
		}
		add("letter-failure-"+strconv.FormatInt(int64(code), 10), 1, 1038, body, map[string]any{"flag": 1, "result": code})
	}
	add("letter-flag-zero", 1, 1038, buildCurrentWeddingGuestLetterACK(0, -1, 0), map[string]any{"flag": 0, "result": -1})
	giftSuccess := buildCurrentWeddingMoneyGiftACK(1, 0, 123)
	if !bytes.Equal(giftSuccess, []byte{1, 0, 0, 0, 0, 123, 0, 0, 0}) {
		t.Fatalf("1034 ACK %x", giftSuccess)
	}
	add("gift-success", 1, 1034, giftSuccess, map[string]any{"flag": 1, "result": 0, "key": 123})
	for _, code := range []int32{-1, -3, -4, -5, -6, -7, -8, -102, -112, 159} {
		body := buildCurrentWeddingMoneyGiftACK(1, code, 123)
		if len(body) != 5 {
			t.Fatal("gift failure included a success suffix")
		}
		add("gift-failure-"+strconv.FormatInt(int64(code), 10), 1, 1034, body, map[string]any{"flag": 1, "result": code})
	}
	add("gift-flag-zero", 1, 1034, buildCurrentWeddingMoneyGiftACK(0, -1, 0), map[string]any{"flag": 0, "result": -1})
	add("guest-letter", 0, 1005, buildCurrentWeddingGuestLetter(123, 8, 2000000000, 10, 0, []byte("groom"), []byte("bride"), false), map[string]any{"key": 123, "area": 8, "deadline": 2000000000, "channel": 10, "kind": 0, "gifted": false, "suffix": 0})
	ids, ok := buildCurrentWeddingGuestTargets([]uint32{50001, 50002})
	if !ok {
		t.Fatal("valid target IDs rejected")
	}
	add("invited-targets", 0, 1006, ids, map[string]any{"ids": []int{50001, 50002}})
	fixtures = append(fixtures, weddingCommentWireFixtures(t)...)
	if _, ok := buildCurrentWeddingGuestTargets([]uint32{1, 1}); ok {
		t.Fatal("duplicate target accepted")
	}
	if output := os.Getenv("DNFWEDDING_WIRE_FIXTURES"); output != "" {
		absolute, err := filepath.Abs(output)
		if err != nil {
			t.Fatal(err)
		}
		root := os.Getenv("DNFWEDDING_WIRE_FIXTURE_ROOT")
		if root == "" {
			t.Fatal("fixture export requires an explicit DNFWEDDING_WIRE_FIXTURE_ROOT")
		}
		root, err = filepath.Abs(root)
		if err != nil {
			t.Fatal(err)
		}
		relative, err := filepath.Rel(root, absolute)
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			t.Fatal("wire fixture output must stay under the explicit export root")
		}
		if err := os.MkdirAll(filepath.Dir(absolute), 0755); err != nil {
			t.Fatal(err)
		}
		raw, err := json.MarshalIndent(struct {
			Kind     string                    `json:"kind"`
			Fixtures []weddingGuestWireFixture `json:"fixtures"`
		}{"wedding-current-wire-fixtures-v1", fixtures}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(absolute, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(append(raw, '\n')); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
