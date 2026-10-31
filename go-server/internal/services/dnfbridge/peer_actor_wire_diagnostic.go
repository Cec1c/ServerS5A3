package dnfbridge

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

func currentProjectionBodyDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:8])
}

func currentPeerActorWireDiagnostic(mode0, mode1 []byte) []any {
	fields := []any{"mode0_digest", currentProjectionBodyDigest(mode0), "mode1_digest", currentProjectionBodyDigest(mode1)}
	if len(mode0) >= 5+0x47+2 {
		fields = append(fields, "mode0_route", mode0[3], "mode0_owner", mode0[4],
			"mode0_object_key", binary.LittleEndian.Uint16(mode0[5+0x47:]))
	}
	if len(mode1) >= 23 {
		fields = append(fields, "mode1_owner", mode1[4], "mode1_object_key", binary.LittleEndian.Uint16(mode1[21:]))
	}
	if c, ok := currentSceneAppearanceBodyCompositionForLog(mode0); ok {
		fields = append(fields, "appearance_rows", c.Rows, "appearance_empty_rows", c.EmptyRows, "appearance_link_rows", c.LinkRows)
	}
	if n, ok := currentUserInfoMode1CreateRowCountForLog(mode1); ok {
		fields = append(fields, "mode1_create_rows", n)
	}
	return fields
}
