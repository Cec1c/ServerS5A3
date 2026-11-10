package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"

	"longheng.io/server/internal/modules/dnf/wedding"
)

func TestWeddingCommentsCurrentCodecFixedRowsAndNarrowByteLengths(t *testing.T) {
	body := make([]byte, 8)
	binary.LittleEndian.PutUint32(body, 123)
	binary.LittleEndian.PutUint32(body[4:], 8)
	raw, _ := hex.DecodeString("d0c2bbe9bfecc0d6")
	body = append(body, raw...)
	q, valid := decodeCurrentWeddingCommentRequest(1040, body)
	if !valid || q.Key != 123 || q.Text != "新婚快乐" {
		t.Fatalf("comment request %+v/%t", q, valid)
	}
	if _, valid := decodeCurrentWeddingCommentRequest(1040, append(body, 0)); valid {
		t.Fatal("accepted NUL/trailing bytes")
	}
	if _, valid := decodeCurrentWeddingCommentRequest(1040, append(make([]byte, 13), body...)); valid {
		t.Fatal("accepted duplicated outer header as comment body")
	}
	deleteBody := make([]byte, 22)
	binary.LittleEndian.PutUint32(deleteBody[13:], 123)
	binary.LittleEndian.PutUint32(deleteBody[17:], 7)
	q, valid = decodeCurrentWeddingCommentRequest(1041, deleteBody)
	if !valid || q.Key != 123 || q.ID != 7 {
		t.Fatalf("delete %+v", q)
	}
	loadBody := make([]byte, 18)
	binary.LittleEndian.PutUint32(loadBody[13:], 123)
	q, valid = decodeCurrentWeddingCommentRequest(1042, loadBody)
	if !valid || q.Key != 123 {
		t.Fatalf("load %+v", q)
	}
	ack, valid := buildCurrentWeddingCommentsACK(123, []wedding.GuestComment{{ID: 7, Name: "author", Text: "新婚快乐", UpdatedAt: 2000000000}})
	if !valid || len(ack) != 13+42+8 || ack[0] != 1 || binary.LittleEndian.Uint32(ack[5:]) != 123 || binary.LittleEndian.Uint32(ack[9:]) != 1 {
		t.Fatalf("load ACK %x", ack)
	}
	row := ack[13:]
	if binary.LittleEndian.Uint32(row) != 7 || !bytes.Equal(row[4:10], []byte("author")) || row[33] != 0 || binary.LittleEndian.Uint32(row[34:]) != 2000000000 || binary.LittleEndian.Uint32(row[38:]) != 8 || !bytes.Equal(row[42:], raw) {
		t.Fatalf("native raw42+text %x", row)
	}
	if len(buildCurrentWeddingCommentACK(0, -1, 123, 1040)) != 5 || len(buildCurrentWeddingCommentACK(0, -1, 123, 1041)) != 9 || len(buildCurrentWeddingCommentACK(0, -1, 123, 1042)) != 13 {
		t.Fatal("failure reader minimum was shortened")
	}
	if got := buildCurrentWeddingCommentACK(1, 0, 7, 1041); binary.LittleEndian.Uint32(got[5:]) != 7 {
		t.Fatal("delete ACK replaced deleted ID with request key")
	}
}

func weddingCommentWireFixtures(t *testing.T) []weddingGuestWireFixture {
	t.Helper()
	var fixtures []weddingGuestWireFixture
	add := func(name string, op uint16, body []byte, expect map[string]any) {
		fixtures = append(fixtures, weddingGuestWireFixture{Name: name, Classification: 1, Opcode: op, PayloadHex: hex.EncodeToString(body), Expect: expect})
	}
	for _, op := range []uint16{1040, 1041, 1042} {
		id := uint32(7)
		if op == 1042 {
			id = 123
		}
		success := map[string]any{"flag": 1, "result": 0}
		failure := map[string]any{"flag": 1, "result": -1}
		if op == 1041 {
			success["deleted_id"], failure["deleted_id"] = 7, 7
		}
		if op == 1042 {
			success["key"], failure["key"] = 123, 123
			success["rows"], failure["rows"] = []any{}, []any{}
		}
		name := map[uint16]string{1040: "comment-update", 1041: "comment-delete", 1042: "comment-load"}[op]
		add(name+"-success", op, buildCurrentWeddingCommentACK(1, 0, id, op), success)
		add(name+"-failure", op, buildCurrentWeddingCommentACK(1, -1, id, op), failure)
		add(name+"-flag-zero-builder", op, buildCurrentWeddingCommentACK(0, -1, id, op), map[string]any{"flag": 0})
		// An independent native boundary sample, not an emitted production ACK:
		// these comment readers ignore every suffix after discriminator zero.
		add(name+"-flag-zero-minimum-boundary", op, []byte{0}, map[string]any{"flag": 0})
	}
	rows := []wedding.GuestComment{{ID: 7, Name: "author", Text: "新婚快乐", UpdatedAt: 2000000000}}
	body, valid := buildCurrentWeddingCommentsACK(123, rows)
	if !valid {
		t.Fatal("comment fixture could not encode")
	}
	add("comment-load-native-raw42-row", 1042, body, map[string]any{"flag": 1, "result": 0, "key": 123, "rows": []map[string]any{{"id": 7, "name": "author", "time": 2000000000, "text": "新婚快乐"}}})
	return fixtures
}
