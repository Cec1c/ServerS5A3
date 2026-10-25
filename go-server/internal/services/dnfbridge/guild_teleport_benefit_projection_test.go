package dnfbridge

import (
	"encoding/binary"
	"reflect"
	"testing"
	"time"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
)

type guildManagementBenefitRow struct {
	typ     byte
	expires uint32
	mode    byte
	value   uint32
}

func parseGuildManagementBenefitRows(t *testing.T, body []byte) []guildManagementBenefitRow {
	t.Helper()
	offset := 0
	take := func(size int) []byte {
		t.Helper()
		if size < 0 || offset+size > len(body) {
			t.Fatalf("management body ended at %d before %d bytes", offset, size)
		}
		value := body[offset : offset+size]
		offset += size
		return value
	}
	uint32Value := func() uint32 {
		return binary.LittleEndian.Uint32(take(4))
	}
	dstr := func() {
		_ = take(int(uint32Value()))
	}

	dstr()       // guild name
	take(5)      // permission, reserved, member count, reserved
	dstr()       // notice
	take(8)      // level, reserved, mark, experience
	dstr()       // master name
	dstr()       // master name copy
	take(2)      // online count
	dstr()       // promotion
	take(6 * 28) // six grade permission/name records
	count := int(take(1)[0])
	var rows []guildManagementBenefitRow
	for range count {
		rows = append(rows, guildManagementBenefitRow{
			typ:     take(1)[0],
			expires: uint32Value(),
			mode:    take(1)[0],
			value:   uint32Value(),
		})
	}
	return rows
}

func TestGuildTeleportBenefitManagementProjection(t *testing.T) {
	now := time.Unix(1700000000, 0)
	for _, tc := range []struct {
		name    string
		benefit *dnfguild.Benefit
		want    []guildManagementBenefitRow
	}{
		{name: "absent"},
		{name: "zero", benefit: &dnfguild.Benefit{Type: 7}, want: []guildManagementBenefitRow{{typ: 7, mode: 1}}},
		{
			name:    "remaining",
			benefit: &dnfguild.Benefit{Type: 7, Value: 110},
			want:    []guildManagementBenefitRow{{typ: 7, mode: 1, value: 110}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &dnfguild.Guild{}
			if tc.benefit != nil {
				g.Benefits = map[byte]dnfguild.Benefit{7: *tc.benefit}
			}
			rows := parseGuildManagementBenefitRows(t, buildCurrentGuildManagementBody(g, 0, now, 4))
			if !reflect.DeepEqual(rows, tc.want) {
				t.Fatalf("benefit rows=%+v want=%+v", rows, tc.want)
			}
		})
	}
}
