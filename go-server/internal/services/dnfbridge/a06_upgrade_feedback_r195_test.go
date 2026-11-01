package dnfbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	proto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func a06R195Dispatch(s *Service, se *gameSession, transport string, body []byte) error {
	if transport == "legacy" {
		return s.handleGameCommand(se, 1, 1342, body)
	}
	frame, err := proto.BuildChannelPacket(1342, body, 1, 1)
	if err != nil {
		return err
	}
	return s.handleGameUpper(se, frame)
}

func a06R195Logs(t *testing.T, logs *bytes.Buffer, connID string, charID uint16, slot int16, events ...string) []map[string]any {
	t.Helper()
	allowed := map[string]bool{}
	for _, key := range strings.Fields("time level msg conn_id char_id opcode slot body_len slot_decoded reason native_reason before after item_id material_item_id changed_slots item_ids counts msg_id classification ack_success committed send_ok") {
		allowed[key] = true
	}
	var rows []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("invalid JSON diagnostic: %v", err)
		}
		msg, _ := row["msg"].(string)
		if !strings.HasPrefix(msg, "aiken-upgrade-") {
			continue
		}
		for key := range row {
			if !allowed[key] {
				t.Fatalf("unexpected diagnostic field %q", key)
			}
		}
		if row["conn_id"] != connID || row["char_id"] != float64(charID) || row["opcode"] != float64(1342) || row["slot"] != float64(slot) {
			t.Fatalf("wrong request attribution: %+v", row)
		}
		rows = append(rows, row)
	}
	if len(rows) != len(events) {
		t.Fatalf("diagnostic events=%d want=%v logs=%s", len(rows), events, logs.String())
	}
	for i, event := range events {
		if rows[i]["msg"] != event {
			t.Fatalf("event %d: %v want %s", i, rows[i], event)
		}
	}
	return rows
}

func a06R195ACK(t *testing.T, raw, want []byte) {
	t.Helper()
	p, rest := splitGameServerUpperPacket(t, raw)
	if p.Header.MsgID != 1342 || p.Header.Classification != 1 || !bytes.Equal(p.Body, want) || len(rest) != 0 {
		t.Fatalf("ACK=%+v tail=%d want=%v", p, len(rest), want)
	}
}

// No PVF required: malformed requests are still attributed and do not gain a
// speculative reason code. The diagnostic never serializes the request bytes.
func TestA06R195UpgradeMalformedFeedback(t *testing.T) {
	for _, body := range [][]byte{nil, {9}, {9, 0, 0x73}} {
		t.Run(fmt.Sprintf("len%d", len(body)), func(t *testing.T) {
			logs := new(bytes.Buffer)
			s := &Service{options: options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain}, logger: slog.New(slog.NewJSONHandler(logs, nil))}
			se := &gameSession{conn: &bufferConn{}, connID: "a06-malformed", selectedCharacterID: 1047}
			if err := s.handleCurrentAikenUpgrade(se, body); err != nil {
				t.Fatal(err)
			}
			a06R195ACK(t, se.conn.(*bufferConn).write.Bytes(), []byte{0, 0})
			rows := a06R195Logs(t, logs, se.connID, 1047, -1, "aiken-upgrade-request", "aiken-upgrade-rejected", "aiken-upgrade-send-result")
			if rows[0]["slot_decoded"] != false || rows[0]["body_len"] != float64(len(body)) || rows[1]["native_reason"] != float64(0) || rows[2]["send_ok"] != true || rows[2]["committed"] != false {
				t.Fatalf("unexpected rejection metadata: %+v", rows)
			}
		})
	}
}

// Real resource fixture, opt-in only; these are actual legacy/upper handlers,
// not tests of a detached reason-mapping helper.
func TestA06R195UpgradeFeedback(t *testing.T) {
	for _, transport := range []string{"legacy", "upper"} {
		for _, scenario := range []string{"missing_material", "locked", "empty_slot", "success"} {
			t.Run(transport+"/"+scenario, func(t *testing.T) {
				s, se, g, logs, _ := aikenR142Fixture(t, scenario)
				se.connID = "a06-" + transport + "-" + scenario
				s.logger = slog.New(slog.NewJSONHandler(logs, nil))
				before := aikenR142Load(t, g)
				if err := a06R195Dispatch(s, se, transport, []byte{9, 0}); err != nil {
					t.Fatal(err)
				}
				if scenario == "success" {
					aikenR142Outcome(t, se, true)
					rows := a06R195Logs(t, logs, se.connID, 19, 9, "aiken-upgrade-request", "aiken-upgrade-committed", "aiken-upgrade-send-result", "aiken-upgrade-send-result")
					commit := rows[1]
					if commit["before"] != float64(0) || commit["after"] != float64(1) || commit["item_id"] != float64(100050746) || commit["material_item_id"] != float64(10161536) ||
						!reflect.DeepEqual(commit["changed_slots"], []any{float64(9), float64(121)}) || !reflect.DeepEqual(commit["item_ids"], []any{float64(100050746), float64(10161536)}) || !reflect.DeepEqual(commit["counts"], []any{float64(1), float64(89)}) {
						t.Fatalf("wrong committed summary: %+v", commit)
					}
					if rows[2]["msg_id"] != float64(14) || rows[2]["classification"] != float64(0) || rows[3]["msg_id"] != float64(1342) || rows[3]["classification"] != float64(1) || rows[3]["ack_success"] != true {
						t.Fatalf("wrong send ordering: %+v", rows)
					}
					for _, row := range rows[2:] {
						if row["send_ok"] != true || row["committed"] != true {
							t.Fatalf("wrong send result: %+v", row)
						}
					}
					return
				}
				want, reason := byte(0), "invalid_or_unavailable"
				if scenario == "missing_material" {
					want, reason = 8, "materials_insufficient"
				}
				a06R195ACK(t, se.conn.(*bufferConn).write.Bytes(), []byte{0, want})
				rows := a06R195Logs(t, logs, se.connID, 19, 9, "aiken-upgrade-request", "aiken-upgrade-rejected", "aiken-upgrade-send-result")
				if rows[1]["reason"] != reason || rows[1]["native_reason"] != float64(want) || rows[2]["native_reason"] != float64(want) || rows[2]["send_ok"] != true || rows[2]["committed"] != false || rows[2]["ack_success"] != false {
					t.Fatalf("wrong rejection metadata: %+v", rows)
				}
				if !reflect.DeepEqual(before, aikenR142Load(t, g)) {
					t.Fatal("rejection changed inventory")
				}
			})
		}
	}
}

func TestA06R195UpgradeSuccessThenMaterialRejection(t *testing.T) {
	for _, transport := range []string{"legacy", "upper"} {
		t.Run(transport, func(t *testing.T) {
			s, se, g, logs, _ := aikenR142Fixture(t, "")
			se.connID = "a06-repeat-" + transport
			s.logger = slog.New(slog.NewJSONHandler(logs, nil))
			inv := aikenR142Load(t, g)
			mat := inv.Slots["0:121"]
			mat.Count = 11 // Existing real fixture's first-grade cost.
			inv.Slots["0:121"] = mat
			aikenR142Save(t, g, inv)
			if err := a06R195Dispatch(s, se, transport, []byte{9, 0}); err != nil {
				t.Fatal(err)
			}
			aikenR142Outcome(t, se, true)
			rows := a06R195Logs(t, logs, se.connID, 19, 9, "aiken-upgrade-request", "aiken-upgrade-committed", "aiken-upgrade-send-result", "aiken-upgrade-send-result")
			if !reflect.DeepEqual(rows[1]["item_ids"], []any{float64(100050746), float64(0)}) || !reflect.DeepEqual(rows[1]["counts"], []any{float64(1), float64(0)}) {
				t.Fatal("cleared material row not represented in committed summary")
			}
			before := aikenR142Load(t, g)
			logs.Reset()
			se.conn.(*bufferConn).write.Reset()
			if err := a06R195Dispatch(s, se, transport, []byte{9, 0}); err != nil {
				t.Fatal(err)
			}
			a06R195ACK(t, se.conn.(*bufferConn).write.Bytes(), []byte{0, 8})
			rows = a06R195Logs(t, logs, se.connID, 19, 9, "aiken-upgrade-request", "aiken-upgrade-rejected", "aiken-upgrade-send-result")
			if rows[1]["reason"] != "materials_insufficient" || rows[1]["native_reason"] != float64(8) || !reflect.DeepEqual(before, aikenR142Load(t, g)) {
				t.Fatal("second request did not preserve material rejection/rollback")
			}
			if _, inventedGrade := rows[1]["before"]; inventedGrade {
				t.Fatal("rejection pretended to have an authoritative grade result")
			}
		})
	}
}

type a06R195CountingAssets struct {
	repo.AccountCharacterAssetUnitOfWork
	calls *int
}

func (u a06R195CountingAssets) WithinAccountCharacterAssets(ctx context.Context, account, character string, apply func(repo.AccountInventoryRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	*u.calls++
	return u.AccountCharacterAssetUnitOfWork.WithinAccountCharacterAssets(ctx, account, character, apply)
}

type a06R195WriteFailure struct {
	bufferConn
	writes, fail int
	partial      bool
	err          error
}

func (c *a06R195WriteFailure) Write(b []byte) (int, error) {
	c.writes++
	if c.writes == c.fail {
		if c.partial {
			_, _ = c.bufferConn.Write(b[:1])
			return 1, c.err
		}
		return 0, c.err
	}
	return c.bufferConn.Write(b)
}

func TestA06R195UpgradeWriteFailureDoesNotRetryAssets(t *testing.T) {
	for _, transport := range []string{"legacy", "upper"} {
		for _, kind := range []string{"rejected_ack", "inventory14", "success_ack"} {
			for _, partial := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/partial%v", transport, kind, partial), func(t *testing.T) {
					scenario := ""
					if kind == "rejected_ack" {
						scenario = "missing_material"
					}
					s, se, g, logs, _ := aikenR142Fixture(t, scenario)
					se.connID = "a06-write-" + transport + "-" + kind
					s.logger = slog.New(slog.NewJSONHandler(logs, nil))
					before := aikenR142Load(t, g)
					provided, calls := g, 0
					provided.AccountAssets = a06R195CountingAssets{g.AccountAssets, &calls}
					s.repositoryProvider = func() (repo.Group, bool) { return provided, true }
					fault := errors.New("private-error-text-must-not-be-logged")
					conn := &a06R195WriteFailure{fail: 1, partial: partial, err: fault}
					if kind == "success_ack" {
						conn.fail = 2
					}
					se.conn = conn
					if err := a06R195Dispatch(s, se, transport, []byte{9, 0}); !errors.Is(err, fault) {
						t.Fatalf("send error lost: %v", err)
					}
					if calls != 1 || conn.writes != conn.fail {
						t.Fatalf("asset/send retry: transactions=%d writes=%d", calls, conn.writes)
					}
					if strings.Contains(logs.String(), fault.Error()) {
						t.Fatal("raw error text leaked into diagnostic")
					}
					events := []string{"aiken-upgrade-request", "aiken-upgrade-committed", "aiken-upgrade-send-result"}
					if kind == "success_ack" {
						events = append(events, "aiken-upgrade-send-result")
					} else if kind == "rejected_ack" {
						events[1] = "aiken-upgrade-rejected"
					}
					rows := a06R195Logs(t, logs, se.connID, 19, 9, events...)
					last := rows[len(rows)-1]
					if last["send_ok"] != false || last["committed"] != (kind != "rejected_ack") {
						t.Fatalf("incorrect failed-send metadata: %+v", last)
					}
					after := aikenR142Load(t, g)
					if kind == "rejected_ack" {
						if last["msg_id"] != float64(1342) || last["native_reason"] != float64(8) || last["ack_success"] != false || !reflect.DeepEqual(before, after) {
							t.Fatal("failed rejection ACK changed outcome/assets")
						}
					} else {
						if after.Slots["0:121"].Count != 89 || after.Slots["0:9"].RawEntry[0x76] != 1 {
							t.Fatal("committed upgrade was reverted or repeated")
						}
						wantID := float64(14)
						if kind == "success_ack" {
							wantID = 1342
							p, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
							wantTail := 0
							if partial {
								wantTail = 1
							}
							if p.Header.MsgID != 14 || p.Header.Classification != 0 || len(rest) != wantTail || rows[2]["send_ok"] != true || last["ack_success"] != true {
								t.Fatal("wrong 14/failed-ACK sequence")
							}
						}
						if last["msg_id"] != wantID {
							t.Fatalf("wrong failed opcode: %+v", last)
						}
					}
				})
			}
		}
	}
}
