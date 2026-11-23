package dnfbridge

import (
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	"math"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// The paired client differs from the inherited dnfenum table above 1449.
// RVA 0x00dcdf14 emits search 1450; 0x00dd609c emits account index 1451.
// RVA 0x01bd67c0 consumes member grades at 1452/1453, not a guild index.
const currentGuildSearchOpcode uint16 = 1450
const currentGuildAccountIndexOpcode uint16 = 1451
const currentGuildGradesOpcode uint16 = 1452
const currentGuildGradesNextOpcode uint16 = 1453
const currentGuildPageSize = 30
const currentGuildMembershipNotificationMsgID uint16 = 0x134
const currentGuildMemberOfflineChannel byte = 0xff
const currentGuildMemberUnrepresentedChannel byte = 0xfe
const currentGuildPermanentBenefitExpires uint32 = math.MaxInt32
const currentGuildMemberListChunkSize = 300
const currentGuildGradeListChunkSize = 500

// currentGuildMemberListApproxRowBytes sizes the pre-growth of one roster
// frame. Rows carry two DSTRs; 64 bytes covers the fixed fields plus a typical
// encoded name and memo without touching bytes.Buffer again per member.
const currentGuildMemberListApproxRowBytes = 64

type currentGuildPacketChunk struct {
	opcode uint16
	body   []byte
}

// currentGuildNameEncoder reuses one GB18030 transformer plus source and
// destination scratch buffers for a whole roster projection. transform.Append
// resets the transformer on each call, so one encoder may encode every name in
// a build; it must still be owned by the goroutine performing that build.
type currentGuildNameEncoder struct {
	encoder *encoding.Encoder
	scratch []byte
	source  []byte
}

func newCurrentGuildNameEncoder() *currentGuildNameEncoder {
	return &currentGuildNameEncoder{
		encoder: simplifiedchinese.GB18030.NewEncoder(),
		scratch: make([]byte, 0, 64),
	}
}

func (e *currentGuildNameEncoder) writeDstr(w *packetWriter, name string) {
	if e == nil || e.encoder == nil {
		w.writeRawDstr([]byte(name))
		return
	}
	if len(name) > cap(e.source) {
		e.source = make([]byte, len(name))
	} else {
		e.source = e.source[:len(name)]
	}
	copy(e.source, name)
	encoded, _, err := transform.Append(e.encoder, e.scratch[:0], e.source)
	if err != nil {
		w.writeRawDstr([]byte(name))
		return
	}
	e.scratch = encoded
	w.writeRawDstr(encoded)
}

type currentGuildSearchRequest struct {
	Filter dnfguild.SearchFilter
	Page   int
}

type currentGuildCharacterProjection struct {
	ServerGroup        byte
	CharacterName      string
	GuildName          string
	GuildID            uint32
	ApplicationPending byte
}

func parseCurrentGuildSearchRequest(body []byte) (currentGuildSearchRequest, bool) {
	if len(body) < 1 {
		return currentGuildSearchRequest{}, false
	}
	request := currentGuildSearchRequest{Filter: dnfguild.SearchFilter{Mode: dnfguild.SearchMode(body[0])}}
	if request.Filter.Mode == dnfguild.SearchTags {
		if len(body) != 6 {
			return currentGuildSearchRequest{}, false
		}
		// v105 VA 0x11d6310 assembles seven checkbox bits; 0x11ce710 sends
		// that u32 followed by the zero-based byte page from the selector.
		request.Filter.TagMask = binary.LittleEndian.Uint32(body[1:5])
		request.Page = int(body[5])
		return request, true
	}
	if request.Filter.Mode != dnfguild.SearchGuildName && request.Filter.Mode != dnfguild.SearchMasterName {
		return currentGuildSearchRequest{}, false
	}
	query, ok := parseCurrentGuildText(body[1:], dnfguild.SearchTextMaxBytes)
	if !ok {
		return currentGuildSearchRequest{}, false
	}
	request.Filter.Query = query
	return request, true
}

func parseCurrentGuildDSTR(body []byte, offset int) (string, int, bool) {
	raw, next, ok := readCurrentRawDstr(body, offset)
	if !ok {
		return "", offset, false
	}
	value, err := decodeCharacterName(raw)
	if err != nil {
		return "", offset, false
	}
	return strings.TrimSpace(value), next, true
}

func parseCurrentGuildUint32(body []byte, offset int) (uint32, int, bool) {
	if offset < 0 || offset > len(body)-4 {
		return 0, offset, false
	}
	return binary.LittleEndian.Uint32(body[offset : offset+4]), offset + 4, true
}

func currentGuildNumericID(value string) uint32 {
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 32)
	if err != nil {
		return 0
	}
	return uint32(parsed)
}

func buildCurrentGuildApplicationNoticeBody(guildID uint32, accountSlot byte, characterName string) []byte {
	var writer packetWriter
	if guildID == 0 || strings.TrimSpace(characterName) == "" {
		writer.writeUint32(0)
		writer.writeByte(0)
		writer.writeRawDstr(nil)
		return writer.bytes()
	}
	writer.writeUint32(guildID)
	writer.writeByte(accountSlot)
	writeClientNameDstr(&writer, characterName)
	return writer.bytes()
}

func buildCurrentGuildMembershipNotificationBody(guild *dnfguild.Guild) []byte {
	var writer packetWriter
	if guild == nil {
		writer.writeByte(0)
		return writer.bytes()
	}
	writer.writeByte(1)
	writer.writeUint32(guild.ID)
	writeClientNameDstr(&writer, guild.Name)
	writeClientNameDstr(&writer, guild.MasterName)
	writer.writeUint32(0)
	writer.writeUint32(uint32(len(guild.Members)))
	writer.writeByte(guild.JoinMode)
	writeClientNameDstr(&writer, guild.Promotion)
	return writer.bytes()
}

func buildCurrentGuildAccountListBody(characters []currentGuildCharacterProjection) []byte {
	if len(characters) > 255 {
		characters = characters[:255]
	}
	var writer packetWriter
	writer.writeByte(byte(len(characters)))
	for _, character := range characters {
		writeClientNameDstr(&writer, character.CharacterName)
		writeClientNameDstr(&writer, character.GuildName)
		writer.writeByte(character.ApplicationPending)
	}
	return writer.bytes()
}

func buildCurrentGuildAccountIndexBody(guilds []*dnfguild.Guild, characters []currentGuildCharacterProjection) []byte {
	rows := make([]*dnfguild.Guild, 0, min(len(guilds), 255))
	for _, guild := range guilds {
		if guild != nil {
			rows = append(rows, guild)
			if len(rows) == 255 {
				break
			}
		}
	}
	mappings := make([]currentGuildCharacterProjection, 0, min(len(characters), 255))
	for _, character := range characters {
		if character.GuildID != 0 {
			mappings = append(mappings, character)
			if len(mappings) == 255 {
				break
			}
		}
	}
	var writer packetWriter
	writer.writeByte(byte(len(rows)))
	for _, guild := range rows {
		writeCurrentGuildRichRow(&writer, guild)
	}
	writer.writeByte(byte(len(mappings)))
	for _, character := range mappings {
		writer.writeUint32(character.GuildID)
		writer.writeByte(character.ServerGroup)
		writeClientNameDstr(&writer, character.CharacterName)
	}
	return writer.bytes()
}

func buildCurrentGuildSearchListBody(total int, page int, guilds []*dnfguild.Guild) []byte {
	if total < 0 {
		total = 0
	}
	if page < 0 {
		page = 0
	}
	if page > 255 {
		page = 255
	}
	rows := make([]*dnfguild.Guild, 0, min(len(guilds), 255))
	for _, guild := range guilds {
		if guild != nil {
			rows = append(rows, guild)
			if len(rows) == 255 {
				break
			}
		}
	}
	var writer packetWriter
	writer.writeUint32(uint32(total))
	writer.writeByte(byte(page))
	writer.writeByte(byte(len(rows)))
	for _, guild := range rows {
		writeCurrentGuildRichRow(&writer, guild)
	}
	return writer.bytes()
}

func writeCurrentGuildRichRow(writer *packetWriter, guild *dnfguild.Guild) {
	writer.writeUint32(guild.ID)
	writer.writeByte(guild.JoinMode)
	writer.writeUint32(currentGuildTagsMask(guild))
	writer.writeUint16(uint16(min(len(guild.Members), 0xffff)))
	writer.writeByte(byte(min(guild.Level, 0xff)))
	writeClientNameDstr(writer, guild.Name)
	writeClientNameDstr(writer, guild.MasterName)
	writeClientNameDstr(writer, guild.Promotion)
}

func currentGuildTagsMask(guild *dnfguild.Guild) uint32 {
	if guild == nil {
		return 0
	}
	mask := uint32(0)
	for _, tag := range guild.Tags {
		if tag < 7 {
			mask |= 1 << tag
		}
	}
	return mask
}

func buildCurrentGuildRecommendationBody(guilds []*dnfguild.Guild) []byte {
	rows := make([]*dnfguild.Guild, 0, min(len(guilds), 255))
	for _, guild := range guilds {
		if guild != nil {
			rows = append(rows, guild)
			if len(rows) == 255 {
				break
			}
		}
	}
	var writer packetWriter
	writer.writeByte(byte(len(rows)))
	for _, guild := range rows {
		writer.writeUint32(guild.ID)
		writeClientNameDstr(&writer, guild.Name)
		writeClientNameDstr(&writer, guild.MasterName)
	}
	return writer.bytes()
}

func buildCurrentGuildSearchResultBody(guild *dnfguild.Guild) []byte {
	var writer packetWriter
	writer.writeUint32(guild.ID)
	writeClientNameDstr(&writer, guild.Name)
	writer.writeUint16(uint16(len(guild.Members)))
	writeClientNameDstr(&writer, guild.MasterName)
	writer.writeUint32(uint32(guild.Level))
	tags := guild.Tags
	if len(tags) > 255 {
		tags = tags[:255]
	}
	writer.writeByte(byte(len(tags)))
	for _, tag := range tags {
		writer.writeByte(tag)
	}
	writeClientNameDstr(&writer, guild.Promotion)
	writer.writeByte(guild.JoinMode)
	writer.writeUint32(uint32(guild.MemberLimit))
	return writer.bytes()
}

func currentGuildMemberOfflineSeconds(
	member dnfguild.Member,
	online bool,
	now time.Time,
) uint32 {
	if online {
		return 0
	}
	since := member.LastOnlineAt
	if since.IsZero() {
		since = member.JoinedAt
	}
	if since.IsZero() {
		return 0
	}
	elapsed := now.Sub(since).Seconds()
	if elapsed <= 0 {
		return 0
	}
	if elapsed >= float64(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(elapsed)
}

func buildCurrentGuildMemberListBody(guild *dnfguild.Guild, includeGuildIdentity, includeCursor bool, onlineChannels map[string]byte, now time.Time, region byte) []byte {
	var members []dnfguild.Member
	total := uint16(0)
	if guild != nil {
		if includeGuildIdentity {
			members = dnfguild.SortedMembers(guild)
			if len(members) > 0xffff {
				members = members[:0xffff]
			}
			total = uint16(len(members))
		}
	}
	return buildCurrentGuildMemberListChunkBodyWithEncoder(guild, includeGuildIdentity, includeCursor, total, members, onlineChannels, now, region, newCurrentGuildNameEncoder())
}

// DNF.exe VA 0x01fd2d10 / 0x02982940 appends op141 continuations into the
// roster vector opened by op67/op140 until vector.size() >= total. Splitting
// large guilds keeps each frame bounded (~18 KB at 300 members) while keeping
// small guilds identical to a single-frame reply.
func buildCurrentGuildMemberListChunks(guild *dnfguild.Guild, opcode uint16, full, cursor bool, onlineChannels map[string]byte, now time.Time, region byte) []currentGuildPacketChunk {
	if !full || guild == nil {
		// Continuation frames carry no member rows and therefore no names.
		return []currentGuildPacketChunk{{
			opcode: opcode,
			body:   buildCurrentGuildMemberListChunkBodyWithEncoder(guild, full, cursor, 0, nil, onlineChannels, now, region, nil),
		}}
	}
	names := newCurrentGuildNameEncoder()
	members := dnfguild.SortedMembers(guild)
	if len(members) > 0xffff {
		members = members[:0xffff]
	}
	total := uint16(len(members))
	if len(members) <= currentGuildMemberListChunkSize {
		return []currentGuildPacketChunk{{
			opcode: opcode,
			body:   buildCurrentGuildMemberListChunkBodyWithEncoder(guild, true, cursor, total, members, onlineChannels, now, region, names),
		}}
	}
	chunks := make([]currentGuildPacketChunk, 0, (len(members)+currentGuildMemberListChunkSize-1)/currentGuildMemberListChunkSize)
	chunks = append(chunks, currentGuildPacketChunk{
		opcode: opcode,
		body:   buildCurrentGuildMemberListChunkBodyWithEncoder(guild, true, cursor, total, members[:currentGuildMemberListChunkSize], onlineChannels, now, region, names),
	})
	for start := currentGuildMemberListChunkSize; start < len(members); start += currentGuildMemberListChunkSize {
		end := min(start+currentGuildMemberListChunkSize, len(members))
		chunks = append(chunks, currentGuildPacketChunk{
			opcode: uint16(dnfenum.CmdPacketGuildAllMemberListNext),
			body:   buildCurrentGuildMemberListChunkBodyWithEncoder(guild, false, true, total, members[start:end], onlineChannels, now, region, names),
		})
	}
	return chunks
}

func buildCurrentGuildMemberListChunkBody(guild *dnfguild.Guild, includeGuildIdentity, includeCursor bool, total uint16, members []dnfguild.Member, onlineChannels map[string]byte, now time.Time, region byte) []byte {
	return buildCurrentGuildMemberListChunkBodyWithEncoder(guild, includeGuildIdentity, includeCursor, total, members, onlineChannels, now, region, newCurrentGuildNameEncoder())
}

func buildCurrentGuildMemberListChunkBodyWithEncoder(guild *dnfguild.Guild, includeGuildIdentity, includeCursor bool, total uint16, members []dnfguild.Member, onlineChannels map[string]byte, now time.Time, region byte, names *currentGuildNameEncoder) []byte {
	var writer packetWriter
	if guild == nil {
		writer.writeUint32(0)
		if includeGuildIdentity {
			writer.writeRawDstr(nil)
			writer.writeUint16(0)
		}
		writer.writeUint16(0)
		if includeCursor {
			writer.writeUint32(0)
		}
		return writer.bytes()
	}
	if len(members) > 0 {
		grow := len(members)
		if grow > 0xffff {
			grow = 0xffff
		}
		writer.buffer.Grow(grow*currentGuildMemberListApproxRowBytes + 32)
	}
	writer.writeUint32(guild.ID)
	if includeGuildIdentity {
		names.writeDstr(&writer, guild.Name)
		writer.writeUint16(total)
	}
	if len(members) > 0xffff {
		members = members[:0xffff]
	}
	writer.writeUint16(uint16(len(members)))
	for _, member := range members {
		writer.writeUint32(currentGuildNumericID(member.CharacterID))
		names.writeDstr(&writer, member.Name)
		names.writeDstr(&writer, member.Memo)
		writer.writeUint16(uint16(member.Level))
		writer.writeByte(byte(numericCharacterStat(member.Job)))
		writer.writeByte(byte(numericCharacterStat(member.GrowType)))
		key1, key2 := region, region
		writer.writeByte(key1)
		writer.writeByte(key2)
		channel := currentGuildMemberOfflineChannel
		online := false
		if encoded, found := onlineChannels[member.CharacterID]; found {
			channel = encoded
			online = true
		}
		writer.writeByte(channel)
		writer.writeByte(0)
		writer.writeByte(0)
		writer.writeByte(member.Grade) // Matched roster record grade byte.
		writer.writeUint32(currentGuildMemberOfflineSeconds(member, online, now))
		writer.writeUint32(currentGuildNumericID(member.CharacterID))
		offlineListable := byte(0)
		if !online {
			offlineListable = 1
		}
		writer.writeByte(offlineListable)
		writer.writeRawDstr(nil)
		writer.writeByte(offlineListable)
		writer.writeByte(member.ServerGroup)
		// Paired client RVA 0x01bd2fca/0x01bd2fe4 reads two additional
		// optional account grouping fields. Their native defaults are zero;
		// account grouping is not advertised by this initial guild adapter.
		writer.writeByte(0)
		writer.writeByte(0)
	}
	if includeCursor {
		writer.writeUint32(0)
	}
	return writer.bytes()
}

func buildCurrentGuildManagementBody(guild *dnfguild.Guild, onlineCount int, now time.Time, viewerGrade byte) []byte {
	if guild == nil {
		guild = &dnfguild.Guild{}
	}
	var writer packetWriter
	writeClientNameDstr(&writer, guild.Name)
	writer.writeByte(boolByte(dnfguild.GradePermissionsFor(guild, viewerGrade)&2 != 0))
	writer.writeByte(0)
	writer.writeUint16(uint16(min(len(guild.Members), 0xffff)))
	writer.writeByte(0)
	writeClientNameDstr(&writer, guild.Notice)
	writer.writeByte(byte(guild.Level)) // actor template +844
	writer.writeByte(0)
	writer.writeByte(0)
	writer.writeByte(guild.Mark) // Guild mark byte.
	writer.writeUint32(guild.Experience)
	writeClientNameDstr(&writer, guild.MasterName)
	writeClientNameDstr(&writer, guild.MasterName)
	writer.writeUint16(uint16(min(onlineCount, 0xffff)))
	writeClientNameDstr(&writer, guild.Promotion)
	for grade := byte(0); grade < 6; grade++ {
		writer.writeUint32(dnfguild.GradePermissionsFor(guild, grade))
		var name []byte
		for _, r := range guild.GradeNames[grade] {
			encoded := rosterNameBytes(string(r))
			if len(name)+len(encoded) > 23 {
				break
			}
			name = append(name, encoded...)
		}
		writeFixedBytes(&writer, name, 23)
		writer.writeByte(0)
	}
	type benefitRow struct {
		typ     byte
		expires time.Time
		mode    byte
		value   uint32
	}
	rows := make([]benefitRow, 0, 8)
	for _, kind := range []byte{1, 3, 5, 7, 21} {
		if b, found := guild.Benefits[kind]; found && (b.Active(now) || (kind == 7 && b.Type == 7 && b.ExpiresAt.IsZero())) {
			mode := byte(0)
			if b.Type == 7 {
				mode = 1
			}
			rows = append(rows, benefitRow{typ: b.Type, expires: b.ExpiresAt, mode: mode, value: b.Value})
		}
	}
	if guild.MemberLimit > 0 {
		rows = append(rows, benefitRow{typ: 6, value: uint32(guild.MemberLimit)})
	}
	writer.writeByte(byte(len(rows)))
	for _, row := range rows {
		expires := uint32(0)
		if !row.expires.IsZero() {
			expires = uint32(row.expires.Unix())
		} else if row.typ == 5 {
			// DNF.exe VA 0x011a9f13 / 0x02978afc treats 0x7fffffff as permanent ("无期限");
			// sending 0 fails expires < now and hides the permanent-stats slot icon.
			expires = currentGuildPermanentBenefitExpires
		}
		writer.writeByte(row.typ)
		writer.writeUint32(expires)
		writer.writeByte(row.mode)
		writer.writeUint32(row.value)
	}
	writer.writeUint32(guild.Mileage)
	writer.writeByte(0)
	writer.writeUint32(0)
	writer.writeByte(0)
	writer.writeUint32(guild.RecommendedRegion)
	writer.writeUint32(guild.RecommendedChannel)
	writer.writeUint32(guild.AuraMark)
	writer.writeUint32(guild.AuraFrame)
	writer.writeUint16(0)
	writer.writeByte(boolByte(guild.WebPromotion))
	writer.writeByte(0)
	tags := currentGuildTagsMask(guild)
	writer.writeUint32(tags)
	writer.writeByte(guild.JoinMode)
	return writer.bytes()
}

func buildCurrentGuildApplicationListBody(applications []dnfguild.Application) []byte {
	var writer packetWriter
	writer.writeUint32(uint32(len(applications)))
	for _, application := range applications {
		writer.writeUint32(currentGuildNumericID(application.CharacterID))
		writeClientNameDstr(&writer, application.Name)
		writer.writeByte(byte(numericCharacterStat(application.Job)))
		writer.writeByte(byte(numericCharacterStat(application.GrowType)))
		writer.writeByte(byte(application.Level))
		writer.writeByte(0)
		writeClientNameDstr(&writer, application.Message)
		writer.writeUint32(uint32(application.CreatedAt.Unix()))
		writer.writeByte(0)
		writer.writeByte(application.ServerGroup)
	}
	return writer.bytes()
}

func buildCurrentGuildInfoBody(characterID uint32, guild *dnfguild.Guild) []byte {
	var writer packetWriter
	writer.writeUint32(characterID)
	if guild == nil {
		writer.writeRawDstr(nil)
	} else {
		writeClientNameDstr(&writer, guild.Name)
	}
	return writer.bytes()
}

// Matched RVA 0x01bd67c0: u32 guild, first-page u16 total, u16 count,
// then {u8 region, DSTR member name, u8 grade, u8 group, u8 representative}.
func buildCurrentGuildGradeBody(g *dnfguild.Guild, first bool) []byte {
	id := uint32(0)
	var members []dnfguild.Member
	if g != nil {
		id = g.ID
		if first {
			members = dnfguild.SortedMembers(g)
			if len(members) > 0xffff {
				members = members[:0xffff]
			}
		}
	}
	return buildCurrentGuildGradeChunkBody(id, first, uint16(len(members)), members)
}

// DNF.exe VA 0x01fd67c0 / 0x011bd430 streams op1452 followed by op1453 chunks
// until the accumulated vector reaches the first packet's u16 total.
func buildCurrentGuildGradeChunks(g *dnfguild.Guild, op uint16) []currentGuildPacketChunk {
	id := uint32(0)
	if g != nil {
		id = g.ID
	}
	if op != currentGuildGradesOpcode || g == nil {
		return []currentGuildPacketChunk{{
			opcode: op,
			body:   buildCurrentGuildGradeChunkBody(id, op == currentGuildGradesOpcode, 0, nil),
		}}
	}
	members := dnfguild.SortedMembers(g)
	if len(members) > 0xffff {
		members = members[:0xffff]
	}
	total := uint16(len(members))
	if len(members) <= currentGuildGradeListChunkSize {
		return []currentGuildPacketChunk{{
			opcode: currentGuildGradesOpcode,
			body:   buildCurrentGuildGradeChunkBody(id, true, total, members),
		}}
	}
	chunks := make([]currentGuildPacketChunk, 0, (len(members)+currentGuildGradeListChunkSize-1)/currentGuildGradeListChunkSize)
	chunks = append(chunks, currentGuildPacketChunk{
		opcode: currentGuildGradesOpcode,
		body:   buildCurrentGuildGradeChunkBody(id, true, total, members[:currentGuildGradeListChunkSize]),
	})
	for start := currentGuildGradeListChunkSize; start < len(members); start += currentGuildGradeListChunkSize {
		end := min(start+currentGuildGradeListChunkSize, len(members))
		chunks = append(chunks, currentGuildPacketChunk{
			opcode: currentGuildGradesNextOpcode,
			body:   buildCurrentGuildGradeChunkBody(id, false, total, members[start:end]),
		})
	}
	return chunks
}

func buildCurrentGuildGradeChunkBody(guildID uint32, first bool, total uint16, members []dnfguild.Member) []byte {
	var w packetWriter
	w.writeUint32(guildID)
	if first {
		w.writeUint16(total)
	}
	w.writeUint16(uint16(len(members)))
	for _, m := range members {
		w.writeByte(m.ServerGroup)
		writeClientNameDstr(&w, m.Name)
		w.writeByte(m.Grade)
		w.writeByte(0)
		w.writeByte(0)
	}
	return w.bytes()
}
