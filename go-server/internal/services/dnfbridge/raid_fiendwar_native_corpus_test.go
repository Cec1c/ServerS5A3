package dnfbridge

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid/fiendwar"
)

// This test exports sender-produced native bodies for an external consumer.
// It never opens a game socket and never writes the PVF or player stores.

type r121Corpus struct {
	Schema                string               `json:"schema"`
	ReviewRound           string               `json:"review_round"`
	TaskLabel             string               `json:"task_label"`
	TaskMapping           string               `json:"task_mapping"`
	GeneratedAtUTC        string               `json:"generated_at_utc"`
	Source                r121Source           `json:"source"`
	PVF                   r121PVFConfig        `json:"pvf"`
	Caveats               []string             `json:"caveats"`
	SenderContracts       []r121SenderContract `json:"sender_contracts"`
	Scenarios             []r121Scenario       `json:"scenarios"`
	Coverage              r121Coverage         `json:"coverage"`
	NotNaturallyGenerated []r121Boundary       `json:"not_naturally_generated"`
}

type r121Source struct {
	RepositoryPath string `json:"repository_path"`
	Revision       string `json:"revision"`
	PVFPath        string `json:"pvf_path"`
	PVFSHA256      string `json:"pvf_sha256"`
	PVFBytes       int64  `json:"pvf_bytes"`
	ChannelID      int    `json:"channel_id"`
	ChannelType    int    `json:"channel_type"`
	BattleMode     byte   `json:"battle_mode"`
	HeaderSize     int    `json:"fixture_header_size"`
	HeaderMode     string `json:"fixture_header_mode"`
	BodyCodec      string `json:"body_codec"`
	Transport      string `json:"transport"`
}

type r121PVFConfig struct {
	Path                   string             `json:"path"`
	WaitingTown            int                `json:"waiting_town"`
	WaitingArea            int                `json:"waiting_area"`
	WaitingNPCs            []int              `json:"waiting_npcs"`
	MemberMax              int                `json:"member_max"`
	StartMinimumMembers    int                `json:"start_minimum_members"`
	WeeklyClearLimit       int                `json:"weekly_clear_limit"`
	DailyAccountEnterLimit int                `json:"daily_account_enter_limit"`
	EntryCostItem          uint32             `json:"entry_cost_item"`
	EntryCostCount         uint32             `json:"entry_cost_count"`
	DisconnectWaitSeconds  int                `json:"disconnect_wait_seconds"`
	Retry                  bool               `json:"retry"`
	RetryItem              uint32             `json:"retry_item"`
	StartDelaySeconds      int                `json:"start_delay_seconds"`
	PhaseTimeoutSeconds    []int              `json:"phase_timeout_seconds"`
	MovieEndSeconds        []int              `json:"movie_end_seconds"`
	DieImprisonSeconds     []int              `json:"die_imprison_seconds"`
	EnemyRates             []int              `json:"enemy_rates"`
	MerchantPositions      []int              `json:"merchant_positions"`
	MerchantPositionRows   []r121MerchantPos  `json:"merchant_position_rows"`
	MerchantRates          []r121MerchantRate `json:"merchant_rates"`
	Symbols                map[string]uint32  `json:"symbols"`
	Areas                  []r121PVFArea      `json:"areas"`
}

type r121MerchantPos struct {
	Area int `json:"area"`
	X    int `json:"x"`
	Y    int `json:"y"`
}

type r121MerchantRate struct {
	Kind            int `json:"kind"`
	DurationSeconds int `json:"duration_seconds"`
	Weight          int `json:"weight"`
}

type r121PVFArea struct {
	Index         int    `json:"index"`
	Kind          string `json:"kind"`
	WorldMap      int    `json:"worldmap"`
	Dungeon       int    `json:"dungeon"`
	Symbol        uint32 `json:"symbol"`
	SymbolName    string `json:"symbol_name"`
	LimitSeconds  int    `json:"limit_seconds"`
	MemberMax     int    `json:"member_max"`
	Next          []int  `json:"next"`
	FixtureMaze   *int   `json:"fixture_maze,omitempty"`
	FixtureBossID uint32 `json:"fixture_boss_id,omitempty"`
}

type r121SenderContract struct {
	Opcode              uint16 `json:"opcode"`
	Name                string `json:"name"`
	Classification      byte   `json:"classification"`
	BodySizeKind        string `json:"body_size_kind"`
	FixedBodySize       int    `json:"fixed_body_size"`
	RowWidth            int    `json:"row_width"`
	MaxRowsPerFrame     int    `json:"max_rows_per_frame"`
	Sender              string `json:"sender"`
	ConsumerExpectation string `json:"consumer_expectation"`
	Evidence            string `json:"evidence"`
}

type r121Scenario struct {
	ID              string         `json:"id"`
	Title           string         `json:"title"`
	Category        string         `json:"category"`
	GenerationMode  string         `json:"generation_mode"`
	StateSeeds      []string       `json:"state_seeds,omitempty"`
	Calls           []string       `json:"calls"`
	StateBefore     map[string]any `json:"state_before"`
	Packets         []r121Packet   `json:"packets"`
	StateAfter      map[string]any `json:"state_after"`
	ObservedOpcodes []uint16       `json:"observed_opcodes"`
	Assertions      []string       `json:"assertions,omitempty"`
}

type r121Packet struct {
	Index          int            `json:"index"`
	Opcode         uint16         `json:"opcode"`
	Classification byte           `json:"classification"`
	Sequence       uint16         `json:"sequence"`
	FrameHex       string         `json:"frame_hex"`
	BodyHex        string         `json:"body_hex"`
	BodyLen        int            `json:"body_len"`
	Semantics      map[string]any `json:"semantics"`
}

type r121Coverage struct {
	AllRequired bool             `json:"all_required"`
	Required    []uint16         `json:"required"`
	Opcodes     []r121CoverageOp `json:"opcodes"`
}

type r121CoverageOp struct {
	Opcode    uint16   `json:"opcode"`
	Count     int      `json:"count"`
	Scenarios []string `json:"scenarios"`
}

type r121Boundary struct {
	Subject string `json:"subject"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
}

func TestR121FiendWarNativeCorpus(t *testing.T) {
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the read-only runtime Script.pvf")
	}
	outDir := strings.TrimSpace(os.Getenv("DNFBRIDGE_FIENDWAR_CORPUS_DIR"))
	if outDir == "" {
		t.Skip("set DNFBRIDGE_FIENDWAR_CORPUS_DIR to export the native corpus")
	}

	source, err := r121SourceFromPVF(t, pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	corpus := r121Corpus{
		Schema:         "r121-fiendwar-native-corpus-v1",
		ReviewRound:    "R121",
		TaskLabel:      "a56",
		TaskMapping:    "The repository has no a56 field. This corpus uses the current FiendWar owner: internal channel type 42 and runtime raid battle mode 3.",
		GeneratedAtUTC: time.Now().UTC().Format(time.RFC3339Nano),
		Source:         source,
		Caveats: []string{
			"Bodies and frames were produced by the current Go senders from the read-only real PVF fixture; no game client or login server was started.",
			"frame_hex includes the test fixture's upper header. The requested native body is body_hex and remains plain because the fixture uses gameUpperBodyCodecPlain.",
			"The actual v143 dispatcher already admits class0 opcodes 1483..1491 through its class adapter. Its 1483 callback is empty; the v144 initializer is required for silent snapshot restores. This sender corpus does not prove rendering.",
			"PVF random draws (initial armies, neutral monsters, merchant position) are recorded exactly as generated once; reruns may choose different legal PVF values.",
			"No player PVF, database, EXE, deployment, game process, login process, doctor, bridge listener, or port 7001 was touched beyond the existing in-memory Go fixtures.",
		},
		SenderContracts: r121SenderContracts(),
		NotNaturallyGenerated: []r121Boundary{
			{Subject: "fresh initialization without a timer tick / 1487", Status: "delayed-natural", Reason: "The real PVF initializes three armies at t0 and arms create_monster for area 30. A 1487 monster row appears only after that PVF timer reaches its deadline; the corpus records the post-tick sender output."},
			{Subject: "fresh initialization without the warning timer / 1485", Status: "delayed-natural", Reason: "The real PVF warning_enemy timers fire later (the earliest in this draw is about 115 seconds). The corpus advances the real PVF timer deadline and then records the sender output."},
			{Subject: "active dynamic merchant area / 1490", Status: "random-real-PVF", Reason: "The initial create merchant action draws one of the configured real PVF positions. The corpus records that actual draw and its real expiry; it does not promise the same area on another run."},
			{Subject: "1485 warning deactivation rows", Status: "not-emitted", Reason: "The current sender has only an activation branch (warning true after false). It emits no row when a warning clears, so replay consumers must not wait for one."},
			{Subject: "game/login/UI acceptance", Status: "out-of-scope", Reason: "This is an in-memory sender corpus. It cannot prove native dispatch, UI rendering, animations, player asset changes, or live reconnect behavior."},
		},
	}

	f, peers := r121CorpusFixture(t, 8)
	p := peers[0]
	b := f.state.Battle
	corpus.PVF = r121PVFConfigFromBattle(t, b)
	corpus.Source.ChannelID = b.ChannelID
	corpus.Source.ChannelType = int(b.ChannelType)
	corpus.Source.BattleMode = b.Mode
	corpus.Source.HeaderSize = f.service.gameUpperHeaderSize()
	corpus.Source.HeaderMode = "raidSafetyFixture / gameUpperHeaderChannel13"
	corpus.Source.BodyCodec = f.service.gameUpperBodyCodecMode()
	if corpus.Source.BodyCodec != gameUpperBodyCodecPlain {
		t.Fatalf("corpus requires a plain fixture body codec, got %q", corpus.Source.BodyCodec)
	}

	corpus.Scenarios = append(corpus.Scenarios, r121CaptureScenario(t, f, p,
		"initial_t0", "Fresh real-PVF battlefield", "initialization",
		"real PVF initialization; only runtime Started and battle clocks were made eligible for projection",
		[]string{"runtime Started=true", "EnterAfter is ten minutes in the past", "Deadline is ten minutes in the future"},
		[]string{"service.sendCurrentRaidProgressProjection writes the live board", "all bodies are plain fixture bodies"},
		[]string{"sendCurrentRaidProgressProjectionLocked", "buildCurrentFiendWarBoard", "sendGameUpperRawClass"},
		func() error { return f.service.sendCurrentRaidProgressProjection(p, *f.state, true, false) }))

	monsterDeadline, ok := r121EarliestTimer(b.FiendWar, "create_monster")
	if !ok {
		t.Fatal("real PVF did not expose a create_monster timer")
	}
	corpus.Scenarios = append(corpus.Scenarios, r121CaptureScenario(t, f, p,
		"progress_first_monster_timer", "First create_monster timer", "progress",
		"real PVF timer deadline followed by the current sender",
		[]string{fmt.Sprintf("FiendWarBattle.Tick(%d)", monsterDeadline.Unix())},
		[]string{"the tick creates neutral monsters through the real PVF create monsters action", "the next board diff emits 1487"},
		[]string{"FiendWarBattle.Tick", "sendCurrentRaidProgressProjection", "buildCurrentFiendWarBoard"},
		func() error {
			if err := b.FiendWar.Tick(monsterDeadline); err != nil {
				return err
			}
			b.ProgressVersion++
			return f.service.sendCurrentRaidProgressProjection(p, *f.state, false, false)
		}))

	warningDeadline, ok := r121EarliestTimer(b.FiendWar, "warning_enemy")
	if !ok {
		t.Fatal("real PVF did not expose a warning_enemy timer")
	}
	corpus.Scenarios = append(corpus.Scenarios, r121CaptureScenario(t, f, p,
		"progress_warning_timer", "First enemy warning timer", "progress",
		"real PVF warning_enemy deadline followed by the current sender",
		[]string{fmt.Sprintf("FiendWarBattle.Tick(%d)", warningDeadline.Unix())},
		[]string{"the PVF warning enemy actions set current warning flags", "the board diff emits one 1485 activation row per changed area"},
		[]string{"FiendWarBattle.Tick", "sendCurrentRaidProgressProjection", "buildCurrentFiendWarBoard"},
		func() error {
			if err := b.FiendWar.Tick(warningDeadline); err != nil {
				return err
			}
			b.ProgressVersion++
			return f.service.sendCurrentRaidProgressProjection(p, *f.state, false, false)
		}))

	corpus.Scenarios = append(corpus.Scenarios, r121CaptureScenario(t, f, p,
		"progress_member_route_to_radar", "Member movement to radar area 15", "progress",
		"real town-move planning for adjacent PVF areas, applied and confirmed in memory",
		[]string{"route 2 -> 3 -> 6 -> 10 -> 9 -> 14 -> 15"},
		[]string{"the service move planner validates each graph edge", "the board diff emits 1486 remove/add rows for character 19"},
		[]string{"prepareCurrentFiendWarMove", "applyCurrentFiendWarMove", "sendCurrentRaidProgressProjection"},
		func() error {
			if err := r121MoveRoute(f, p, []byte{3, 6, 10, 9, 14, 15}); err != nil {
				return err
			}
			return f.service.sendCurrentRaidProgressProjection(p, *f.state, false, false)
		}))

	corpus.Scenarios = append(corpus.Scenarios, r121CaptureScenario(t, f, p,
		"progress_radar_encounter_enter", "Radar encounter entry", "progress",
		"real PVF radar dungeon entered by the current battle state machine",
		[]string{"GroupAreas[1]=15", "FiendWarBattle.Enter(1, 4963, now)"},
		[]string{"the area deadline is set from the real PVF limit", "the board emits 1489 kind=12 and 596 for dungeon 4963"},
		[]string{"FiendWarBattle.Enter", "sendCurrentRaidProgressProjection", "buildCurrentFiendWarBoard"},
		func() error {
			now := time.Now()
			b.FiendWar.GroupAreas[b.MemberGroups[p.selectedCharacterID]] = 15
			if err := b.FiendWar.Enter(b.MemberGroups[p.selectedCharacterID], 4963, now); err != nil {
				return err
			}
			b.ProgressVersion++
			return f.service.sendCurrentRaidProgressProjection(p, *f.state, false, false)
		}))

	corpus.Scenarios = append(corpus.Scenarios, r121CaptureScenario(t, f, p,
		"progress_radar_clear", "Radar clear and reveal", "progress",
		"real PVF radar clear followed by the current sender",
		[]string{"FiendWarBattle.Clear(4963, now)"},
		[]string{"radar state becomes 1 through the PVF rules", "enemy types stop being hidden and 1491 carries the changed shared symbols"},
		[]string{"FiendWarBattle.Clear", "sendCurrentRaidProgressProjection", "buildCurrentFiendWarBoard"},
		func() error {
			if err := b.FiendWar.Clear(4963, time.Now()); err != nil {
				return err
			}
			b.ProgressVersion++
			return f.service.sendCurrentRaidProgressProjection(p, *f.state, false, false)
		}))

	reconnected := r121ReplacementSession(t, f, p)
	corpus.Scenarios = append(corpus.Scenarios, r121CaptureScenario(t, f, reconnected,
		"reconnect_active", "Reconnect during an active cleared-radar board", "reconnect",
		"fresh gameSession with empty projection owner bound to the same active character and battle",
		[]string{"new buffer connection", "same character, account, channel, instance and raid key", "raidProjection owner reset"},
		[]string{"a fresh consumer receives 1483 and the full current board without replaying 587/1484 opening transitions", "the current board is a snapshot rather than a replay of all historical deltas"},
		[]string{"bindGameSessionCharacter", "sendCurrentRaidProgressProjection", "sendCurrentFiendWarProjectionLocked"},
		func() error { return f.service.sendCurrentRaidProgressProjection(reconnected, *f.state, false, true) }))

	successF, successPeers := r121CorpusFixture(t, 2)
	successP := successPeers[0]
	corpus.Scenarios = append(corpus.Scenarios, r121CaptureScenario(t, successF, successP,
		"clear_progress_after_completion", "Completed battlefield board", "clear",
		"real service clear reductions for command centers 27/28/29 and both PVF bosses",
		[]string{"in-memory completed dungeon runtimes", "real PVF area graph and dungeon IDs", "real service group freeze and clear ownership"},
		[]string{"both bosses complete the PVF phase", "the final board hides the merchant and carries final symbols", "no reward item is fabricated in the corpus state"},
		[]string{"freezeCurrentRaidDungeonGroupEntry", "reduceCurrentRaidDungeonClearLocked", "sendCurrentRaidProgressProjection"},
		func() error {
			if err := r121DriveSuccess(t, successF, successPeers); err != nil {
				return err
			}
			successP.conn.(*bufferConn).write.Reset()
			return successF.service.sendCurrentRaidProgressProjection(successP, *successF.state, false, false)
		}))
	corpus.Scenarios = append(corpus.Scenarios, r121CaptureScenario(t, successF, successP,
		"clear_terminal", "Successful terminal result", "clear",
		"current terminal sender after the completed PVF phase and committed weekly intent",
		[]string{"PhaseCompleted=true and FiendWar.Complete=true", "weekly clear intent committed in the in-memory repository"},
		[]string{"1484 state3 precedes 601", "601 result type is 0 (success)", "587 state3 follows 601"},
		[]string{"sendCurrentAntonTerminalResult", "prepareCurrentFiendWarResult"},
		func() error { return successF.service.sendCurrentAntonTerminalResult(successP, *successF.state) }))

	failureF, failurePeers := r121CorpusFixture(t, 1)
	failureP := failurePeers[0]
	failureBattle := failureF.state.Battle
	now := time.Now()
	failureBattle.TimedOut = true
	failureBattle.FiendWar.Failed = true
	failureBattle.FiendWarEndedAt = now
	corpus.Scenarios = append(corpus.Scenarios, r121CaptureScenario(t, failureF, failureP,
		"failure_progress_before_terminal", "Failed board before terminal result", "failure",
		"real PVF battle placed in the production timeout/failure state",
		[]string{"TimedOut=true", "FiendWar.Failed=true", "FiendWarEndedAt=now"},
		[]string{"the board does not pre-open settlement", "the board does not replay 587, 1484 failure or 601 before the terminal sender"},
		[]string{"sendCurrentRaidProgressProjection", "buildCurrentFiendWarBoard"},
		func() error {
			return failureF.service.sendCurrentRaidProgressProjection(failureP, *failureF.state, false, false)
		}))
	corpus.Scenarios = append(corpus.Scenarios, r121CaptureScenario(t, failureF, failureP,
		"failure_terminal", "Failure terminal result", "failure",
		"current terminal sender for timed-out FiendWar",
		[]string{"same timeout state as failure_progress_before_terminal"},
		[]string{"601 result type is 3 (FiendWar failure)", "1484 state6 follows 601", "the failed terminal has no 587 success phase row"},
		[]string{"sendCurrentAntonTerminalResult", "prepareCurrentFiendWarResult"},
		func() error { return failureF.service.sendCurrentAntonTerminalResult(failureP, *failureF.state) }))
	corpus.Scenarios = append(corpus.Scenarios, r121CaptureScenario(t, failureF, failureP,
		"failure_terminal_idempotent", "Repeated failure terminal", "failure",
		"repeat the same terminal call on the same projection identity",
		nil,
		[]string{"the current sender emits zero packets because the terminal result was already committed"},
		[]string{"sendCurrentAntonTerminalResult"},
		func() error { return failureF.service.sendCurrentAntonTerminalResult(failureP, *failureF.state) }))
	failureReconnected := r121ReplacementSession(t, failureF, failureP)
	corpus.Scenarios = append(corpus.Scenarios, r121CaptureScenario(t, failureF, failureReconnected,
		"reconnect_after_failure", "Reconnect after failure", "reconnect",
		"fresh projection owner on the same failed battle",
		[]string{"new buffer connection", "same character, account, channel and instance"},
		[]string{"the snapshot omits start/clear prefixes because TimedOut is true", "the snapshot does not replay the 601/1484 failure terminal"},
		[]string{"bindGameSessionCharacter", "sendCurrentRaidProgressProjection"},
		func() error {
			return failureF.service.sendCurrentRaidProgressProjection(failureReconnected, *failureF.state, false, true)
		}))

	corpus.Coverage = r121BuildCoverage(corpus.Scenarios)
	if data, err := json.MarshalIndent(corpus, "", "  "); err != nil {
		t.Fatal(err)
	} else {
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(outDir, "fiendwar-native-corpus.json")
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("corpus=%s bytes=%d scenarios=%d all_required=%t", path, len(data), len(corpus.Scenarios), corpus.Coverage.AllRequired)
	}
	if !corpus.Coverage.AllRequired {
		var missing []uint16
		for _, op := range corpus.Coverage.Opcodes {
			if op.Count == 0 {
				missing = append(missing, op.Opcode)
			}
		}
		t.Fatalf("native corpus coverage missing opcodes: %v", missing)
	}
}

func r121CorpusFixture(t *testing.T, members int) (*raidStartGuardFixture, []*gameSession) {
	t.Helper()
	f, peers := actualCurrentFiendWar(t, members)
	now := time.Now()
	f.state.Started = true
	f.state.Battle.EnterAfter = now.Add(-10 * time.Minute)
	f.state.Battle.Deadline = now.Add(10 * time.Minute)
	f.state.Battle.TimedOut = false
	f.state.Battle.PhaseCompleted = false
	return f, peers
}

func r121SourceFromPVF(t *testing.T, path string) (r121Source, error) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		return r121Source{}, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return r121Source{}, err
	}
	repo := strings.TrimSpace(os.Getenv("DNFBRIDGE_CORPUS_REPO_ROOT"))
	if repo == "" {
		repo, err = os.Getwd()
		if err != nil {
			return r121Source{}, err
		}
	}
	return r121Source{
		RepositoryPath: repo,
		Revision:       strings.TrimSpace(os.Getenv("DNFBRIDGE_CORPUS_SOURCE_REVISION")),
		PVFPath:        path,
		PVFSHA256:      hex.EncodeToString(hash.Sum(nil)),
		PVFBytes:       size,
		Transport:      "in-memory bufferConn through the real Service sender; no socket, client, login or game process",
	}, nil
}

func r121PVFConfigFromBattle(t *testing.T, b *runtimeRaidBattle) r121PVFConfig {
	t.Helper()
	if b == nil || b.FiendWar == nil || b.FiendWar.Config == nil || b.FiendWar.Config.Raid == nil {
		t.Fatal("real FiendWar battle configuration missing")
	}
	c := b.FiendWar.Config
	out := r121PVFConfig{
		Path:                   fiendwar.FiendWarPVFPath,
		WaitingTown:            c.WaitingTown,
		WaitingArea:            c.WaitingArea,
		WaitingNPCs:            append([]int(nil), c.WaitingNPCs...),
		MemberMax:              c.Raid.MemberMax,
		StartMinimumMembers:    c.Raid.StartMinimumMembers,
		WeeklyClearLimit:       b.Configuration.WeeklyClearLimit,
		DailyAccountEnterLimit: c.DailyAccountEnterLimit,
		EntryCostItem:          c.Raid.EntryCost.ItemID,
		EntryCostCount:         c.Raid.EntryCost.Count,
		DisconnectWaitSeconds:  c.Raid.DisconnMemberWaitTime,
		Retry:                  b.FiendWarConfig.Retry,
		RetryItem:              b.FiendWarConfig.RetryItem.ItemID,
		StartDelaySeconds:      c.Raid.StartDelaySeconds,
		PhaseTimeoutSeconds:    append([]int(nil), c.Raid.PhaseTimeoutSeconds...),
		MovieEndSeconds:        append([]int(nil), c.Raid.MovieEndSeconds...),
		DieImprisonSeconds:     append([]int(nil), c.DieImprisonSeconds...),
		EnemyRates:             append([]int(nil), c.EnemyRates...),
		MerchantPositions:      append([]int(nil), c.MerchantPositions...),
		Symbols:                map[string]uint32{},
	}
	for name, id := range c.Symbols {
		out.Symbols[name] = id
	}
	for _, row := range c.MerchantPositionRows {
		out.MerchantPositionRows = append(out.MerchantPositionRows, r121MerchantPos(row))
	}
	for _, rate := range c.MerchantRates {
		out.MerchantRates = append(out.MerchantRates, r121MerchantRate{Kind: rate.Kind, DurationSeconds: rate.DurationSeconds, Weight: rate.Weight})
	}
	ids := make([]int, 0, len(c.Areas))
	for id := range c.Areas {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		a := c.Areas[id]
		row := r121PVFArea{Index: a.Index, Kind: a.Kind, WorldMap: a.WorldMap, Dungeon: a.Dungeon, Symbol: a.Symbol, SymbolName: r121SymbolName(c, a.Symbol), LimitSeconds: a.LimitSeconds, MemberMax: a.MemberMax, Next: append([]int(nil), a.Next...)}
		if maze, ok := b.FiendWar.MazeForArea(id); ok {
			v := maze
			row.FixtureMaze = &v
			row.FixtureBossID, _ = fiendwar.FiendWarFixtureBossID(a.Dungeon, maze)
		}
		out.Areas = append(out.Areas, row)
	}
	return out
}

func r121SenderContracts() []r121SenderContract {
	return []r121SenderContract{
		{1483, "currentFiendWarInit", 0, "empty", 0, 0, 0, "sendCurrentFiendWarProjectionLocked", "class0 snapshot initialization before start state and full board; no body bytes", "raid_fiendwar_projection.go; raid_fiendwar_wire.go"},
		{1484, "currentFiendWarStateNotice", 0, "fixed", 2, 0, 0, "sendCurrentFiendWarProjectionLocked; sendCurrentAntonTerminalResult", "2-byte state/argument controller notice: 2=start, 3=success, 6=failure", "raid_fiendwar_failure_presentation_test.go; raid_result_online.go"},
		{1485, "currentFiendWarWarning", 0, "counted", 0, 2, 255, "buildCurrentFiendWarBoard", "u8 count then (u8 area,u8 active) rows; sender emits activation only", "raid_fiendwar_wire.go appendRows/warnings"},
		{1486, "currentFiendWarUser", 0, "counted", 0, 13, 255, "buildCurrentFiendWarBoard", "u8 count then (u32 op,u8 area,u32 char,u32 recovery_unix) rows; op1 add/update op2 remove", "raid_fiendwar_wire.go userRow"},
		{1487, "currentFiendWarMonster", 0, "counted", 0, 10, 255, "buildCurrentFiendWarBoard", "u8 count then (u8 area,u32 unit_id,u8 type,u32 op) rows; type is hidden to zero until radar state1", "raid_fiendwar_wire.go monster rows; runtimeFiendWarRevealMonster"},
		{1488, "currentFiendWarEnemy", 0, "counted", 0, 14, 255, "buildCurrentFiendWarBoard", "u8 count then (u8 area,u32 unit_id,u8 type,u32 op,u32 2) rows; type is hidden to zero until radar state1", "raid_fiendwar_wire.go enemy rows"},
		{1489, "currentFiendWarTimer", 0, "counted", 0, 10, 255, "buildCurrentFiendWarBoard", "u8 count then (u8 area,u8 timer_kind,u32 end_unix,u32 duration) rows; kind 12 is the area deadline", "raid_fiendwar_wire.go timers; FiendWarTimerKinds"},
		{1490, "currentFiendWarMerchant", 0, "fixed", 1, 0, 0, "buildCurrentFiendWarBoard", "one u8 merchant area; zero means absent/complete/failed/expired", "raid_fiendwar_wire.go merchant projection"},
		{1491, "currentFiendWarSymbol", 0, "counted", 0, 8, 255, "buildCurrentFiendWarBoard", "u8 count then (u32 symbol_id,u32 value) rows for changed IDs 81..125; zero high IDs are suppressed", "raid_fiendwar_wire.go symbols"},
		{583, "shared raid symbols", 0, "counted", 0, 8, 255, "buildCurrentFiendWarBoard", "u8 count then sorted (u32 symbol_id,u32 value) pairs for shared IDs below 121", "raid.BuildSymbolBody; anton_symbols.go"},
		{585, "raid dungeon gates", 0, "formula", 0, 5, 255, "buildCurrentFiendWarBoard", "u8 flag=0,u8 count,(u32 dungeon,u8 state) rows,u32 infection=0", "raid.BuildDungeonStateBody"},
		{587, "raid start/phase state", 0, "fixed", 2, 0, 0, "sendCurrentFiendWarProjectionLocked; sendCurrentAntonTerminalResult", "2-byte state/argument: state2 starts battle, state3 follows successful result", "raid_fiendwar_projection.go; raid_result_online.go"},
		{596, "raid dungeon timer", 0, "fixed", 13, 0, 0, "buildCurrentFiendWarBoard", "u8 kind=1,u32 dungeon,u32 end_unix,u32 duration; zero end/duration removes timer", "raid.BuildTimerBody"},
		{597, "raid remaining time", 0, "fixed", 5, 0, 0, "buildCurrentFiendWarBoard", "u8 kind=0,u32 remaining_seconds for the viewer (area clock when available, otherwise raid clock)", "runtimeFiendWarViewerClockSeconds; raid.BuildRemainingTimeBody"},
		{601, "raid terminal result", 0, "fixed", 10, 0, 0, "sendCurrentAntonTerminalResult", "u8 type,u8 phase,u32 clear_seconds,u16 deaths,u8 client_rank,u8 reward_option; type0 success type3 FiendWar failure", "raid.BuildAntonResultBody; prepareCurrentFiendWarResult"},
	}
}

func r121CaptureScenario(t *testing.T, f *raidStartGuardFixture, p *gameSession, id, title, category, mode string, seeds, assertions, calls []string, fn func() error) r121Scenario {
	t.Helper()
	conn, ok := p.conn.(*bufferConn)
	if !ok {
		t.Fatalf("%s: session connection is %T, want *bufferConn", id, p.conn)
	}
	before := r121StateSnapshot(*f.state)
	conn.write.Reset()
	if fn != nil {
		if err := fn(); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	raw := append([]byte(nil), conn.write.Bytes()...)
	packets := r121ParsePackets(t, raw, f.service.gameUpperHeaderSize(), f.state.Battle)
	seen := map[uint16]bool{}
	for _, packet := range packets {
		seen[packet.Opcode] = true
	}
	opcodes := make([]uint16, 0, len(seen))
	for op := range seen {
		opcodes = append(opcodes, op)
	}
	sort.Slice(opcodes, func(i, j int) bool { return opcodes[i] < opcodes[j] })
	return r121Scenario{
		ID: id, Title: title, Category: category, GenerationMode: mode,
		StateSeeds: seeds, Calls: calls, StateBefore: before,
		Packets: packets, StateAfter: r121StateSnapshot(*f.state),
		ObservedOpcodes: opcodes, Assertions: assertions,
	}
}

func r121ParsePackets(t *testing.T, raw []byte, headerSize int, b *runtimeRaidBattle) []r121Packet {
	t.Helper()
	packets := make([]r121Packet, 0)
	for len(raw) > 0 {
		packet, rest := splitGameServerUpperPacketWithHeader(t, raw, headerSize)
		frameLen := len(raw) - len(rest)
		packets = append(packets, r121Packet{
			Index: len(packets), Opcode: packet.Header.MsgID,
			Classification: packet.Header.Classification, Sequence: packet.Header.Seq,
			FrameHex: hex.EncodeToString(raw[:frameLen]), BodyHex: hex.EncodeToString(packet.Body),
			BodyLen: len(packet.Body), Semantics: r121DecodeBody(t, packet.Header.MsgID, packet.Body, b),
		})
		raw = rest
	}
	return packets
}

func r121DecodeBody(t *testing.T, op uint16, body []byte, b *runtimeRaidBattle) map[string]any {
	t.Helper()
	c := b.FiendWar.Config
	out := map[string]any{"opcode": op, "opcode_name": r121OpcodeName(op), "plain_body_len": len(body)}
	switch op {
	case 1483:
		if len(body) != 0 {
			t.Fatalf("1483 body len=%d want0", len(body))
		}
		out["kind"] = "class0 reinitialization marker"
	case 1484:
		r121RequireLen(t, op, body, 2)
		out["state"] = body[0]
		out["state_name"] = map[byte]string{2: "start", 3: "success", 6: "failure"}[body[0]]
		out["argument"] = body[1]
	case 1485:
		rows := make([]map[string]any, 0, body[0])
		r121RequireCounted(t, op, body, 2)
		for i := 0; i < int(body[0]); i++ {
			row := body[1+i*2 : 1+(i+1)*2]
			rows = append(rows, map[string]any{"area": row[0], "area_ref": r121AreaRef(c, int(row[0])), "active": row[1] == 1})
		}
		out["count"] = body[0]
		out["row_width"] = 2
		out["rows"] = rows
	case 1486:
		rows := make([]map[string]any, 0, body[0])
		r121RequireCounted(t, op, body, 13)
		for i := 0; i < int(body[0]); i++ {
			row := body[1+i*13 : 1+(i+1)*13]
			action := binary.LittleEndian.Uint32(row[0:4])
			rows = append(rows, map[string]any{"action": action, "action_name": map[uint32]string{1: "add_or_update", 2: "remove"}[action], "area": row[4], "area_ref": r121AreaRef(c, int(row[4])), "character_id": binary.LittleEndian.Uint32(row[5:9]), "recovery_unix": binary.LittleEndian.Uint32(row[9:13])})
		}
		out["count"] = body[0]
		out["row_width"] = 13
		out["rows"] = rows
	case 1487:
		rows := make([]map[string]any, 0, body[0])
		r121RequireCounted(t, op, body, 10)
		for i := 0; i < int(body[0]); i++ {
			row := body[1+i*10 : 1+(i+1)*10]
			unitID, unitType := binary.LittleEndian.Uint32(row[1:5]), row[5]
			avatar := uint32(0)
			if unitType != 0 {
				avatar, _ = fiendwar.FiendWarOrcBossID(unitType)
			}
			rows = append(rows, map[string]any{"area": row[0], "area_ref": r121AreaRef(c, int(row[0])), "unit_id": unitID, "unit_type": unitType, "action": binary.LittleEndian.Uint32(row[6:10]), "consumer_avatar_id": avatar, "hidden_identity": unitType == 0})
		}
		out["count"] = body[0]
		out["row_width"] = 10
		out["rows"] = rows
	case 1488:
		rows := make([]map[string]any, 0, body[0])
		r121RequireCounted(t, op, body, 14)
		for i := 0; i < int(body[0]); i++ {
			row := body[1+i*14 : 1+(i+1)*14]
			unitID, unitType := binary.LittleEndian.Uint32(row[1:5]), row[5]
			avatar := uint32(0)
			switch {
			case unitType >= 5 && unitType <= 13:
				avatar = unitID
			case unitType != 0:
				avatar, _ = fiendwar.FiendWarArmyBossID(unitType)
			}
			rows = append(rows, map[string]any{"area": row[0], "area_ref": r121AreaRef(c, int(row[0])), "unit_id": unitID, "unit_type": unitType, "facility_type_name": r121FacilityTypeName(unitType), "action": binary.LittleEndian.Uint32(row[6:10]), "constant_field": binary.LittleEndian.Uint32(row[10:14]), "consumer_avatar_id": avatar, "hidden_identity": unitType == 0})
		}
		out["count"] = body[0]
		out["row_width"] = 14
		out["rows"] = rows
	case 1489:
		rows := make([]map[string]any, 0, body[0])
		r121RequireCounted(t, op, body, 10)
		for i := 0; i < int(body[0]); i++ {
			row := body[1+i*10 : 1+(i+1)*10]
			rows = append(rows, map[string]any{"area": row[0], "area_ref": r121AreaRef(c, int(row[0])), "timer_kind": row[1], "timer_kind_name": r121TimerKindName(row[1]), "end_unix": binary.LittleEndian.Uint32(row[2:6]), "duration": binary.LittleEndian.Uint32(row[6:10]), "removal": binary.LittleEndian.Uint32(row[2:10]) == 0})
		}
		out["count"] = body[0]
		out["row_width"] = 10
		out["rows"] = rows
	case 1490:
		r121RequireLen(t, op, body, 1)
		out["merchant_area"] = body[0]
		out["merchant_area_ref"] = r121AreaRef(c, int(body[0]))
		out["absent"] = body[0] == 0
	case 1491, 583:
		r121RequireCounted(t, op, body, 8)
		rows := make([]map[string]any, 0, body[0])
		for i := 0; i < int(body[0]); i++ {
			row := body[1+i*8 : 1+(i+1)*8]
			id := binary.LittleEndian.Uint32(row[0:4])
			rows = append(rows, map[string]any{"symbol_id": id, "symbol_name": r121SymbolName(c, id), "value": binary.LittleEndian.Uint32(row[4:8])})
		}
		out["count"] = body[0]
		out["row_width"] = 8
		out["symbols"] = rows
	case 585:
		if len(body) < 6 {
			t.Fatalf("585 body len=%d too short", len(body))
		}
		count := int(body[1])
		if len(body) != 2+count*5+4 {
			t.Fatalf("585 body len=%d count=%d", len(body), count)
		}
		rows := make([]map[string]any, 0, count)
		for i := 0; i < count; i++ {
			row := body[2+i*5 : 2+(i+1)*5]
			rows = append(rows, map[string]any{"dungeon": binary.LittleEndian.Uint32(row[0:4]), "state": row[4]})
		}
		out["flag"] = body[0]
		out["count"] = count
		out["rows"] = rows
		out["infection"] = binary.LittleEndian.Uint32(body[len(body)-4:])
	case 587:
		r121RequireLen(t, op, body, 2)
		out["battle_state"] = body[0]
		out["argument"] = body[1]
	case 596:
		r121RequireLen(t, op, body, 13)
		out["timer_kind"] = body[0]
		out["dungeon"] = binary.LittleEndian.Uint32(body[1:5])
		out["end_unix"] = binary.LittleEndian.Uint32(body[5:9])
		out["duration"] = binary.LittleEndian.Uint32(body[9:13])
	case 597:
		r121RequireLen(t, op, body, 5)
		out["timer_kind"] = body[0]
		out["remaining_seconds"] = binary.LittleEndian.Uint32(body[1:5])
	case 601:
		r121RequireLen(t, op, body, 10)
		out["result_type"] = body[0]
		out["result_name"] = map[byte]string{0: "success", 3: "fiendwar_failure"}[body[0]]
		out["phase"] = body[1]
		out["clear_seconds"] = binary.LittleEndian.Uint32(body[2:6])
		out["deaths"] = binary.LittleEndian.Uint16(body[6:8])
		out["client_rank"] = body[8]
		out["reward_option"] = body[9]
	default:
		t.Fatalf("unexpected corpus opcode %d body=%x", op, body)
	}
	return out
}

func r121RequireLen(t *testing.T, op uint16, body []byte, want int) {
	t.Helper()
	if len(body) != want {
		t.Fatalf("%d body len=%d want=%d", op, len(body), want)
	}
}

func r121RequireCounted(t *testing.T, op uint16, body []byte, width int) {
	t.Helper()
	if len(body) == 0 || len(body) != 1+int(body[0])*width {
		t.Fatalf("%d body len=%d count=%d width=%d", op, len(body), r121FirstByte(body), width)
	}
}

func r121FirstByte(body []byte) int {
	if len(body) == 0 {
		return -1
	}
	return int(body[0])
}

func r121OpcodeName(op uint16) string {
	return map[uint16]string{1483: "currentFiendWarInit", 1484: "currentFiendWarStateNotice", 1485: "currentFiendWarWarning", 1486: "currentFiendWarUser", 1487: "currentFiendWarMonster", 1488: "currentFiendWarEnemy", 1489: "currentFiendWarTimer", 1490: "currentFiendWarMerchant", 1491: "currentFiendWarSymbol", 583: "shared raid symbols", 585: "raid dungeon gates", 587: "raid start/phase state", 596: "raid dungeon timer", 597: "raid remaining time", 601: "raid terminal result"}[op]
}

func r121SymbolName(c *fiendwar.FiendWarBattleConfig, id uint32) string {
	for name, other := range c.Symbols {
		if other == id {
			return name
		}
	}
	return ""
}

func r121TimerKindName(kind byte) string {
	if kind == 12 {
		return "area_deadline"
	}
	for name, other := range fiendwar.FiendWarTimerKinds {
		if other == kind {
			return name
		}
	}
	return ""
}

func r121FacilityTypeName(kind byte) string {
	return map[byte]string{5: "radar", 6: "battery", 7: "top outpost", 8: "mid outpost", 9: "bot outpost", 10: "top command center", 11: "mid command center", 12: "bot command center", 13: "boss"}[kind]
}

func r121AreaRef(c *fiendwar.FiendWarBattleConfig, id int) map[string]any {
	a, ok := c.Areas[id]
	if !ok {
		return map[string]any{"index": id, "present": false}
	}
	return map[string]any{"index": id, "present": true, "kind": a.Kind, "dungeon": a.Dungeon, "worldmap": a.WorldMap, "limit_seconds": a.LimitSeconds, "member_max": a.MemberMax}
}

func r121StateSnapshot(state runtimeRaidState) map[string]any {
	b := state.Battle
	if b == nil {
		return map[string]any{"present": false}
	}
	out := map[string]any{
		"present": true, "raid_key": state.RaidKey, "instance_id": b.InstanceID, "started": state.Started,
		"timed_out": b.TimedOut, "phase_completed": b.PhaseCompleted, "enter_after_unix": r121Unix(b.EnterAfter),
		"deadline_unix": r121Unix(b.Deadline), "progress_version": b.ProgressVersion,
	}
	if b.FiendWar == nil {
		out["fiendwar"] = nil
		return out
	}
	f := b.FiendWar
	out["fiendwar"] = map[string]any{
		"complete": f.Complete, "failed": f.Failed, "next_unit_id": f.NextUnitID,
		"merchant_area": f.MerchantArea, "merchant_kind": f.MerchantKind,
		"merchant_expires_unix": r121Unix(f.MerchantExpiresAt),
	}
	areaIDs := make([]int, 0, len(f.Areas))
	for id := range f.Areas {
		areaIDs = append(areaIDs, id)
	}
	sort.Ints(areaIDs)
	areas := make([]map[string]any, 0)
	for _, id := range areaIDs {
		a := f.Areas[id]
		if a.Enemy.ID == 0 && a.Monster.ID == 0 && a.Group == 0 && !a.Warning && a.Deadline.IsZero() {
			continue
		}
		areas = append(areas, map[string]any{"area": id, "kind": f.Config.Areas[id].Kind, "enemy_id": a.Enemy.ID, "enemy_type": a.Enemy.Type, "monster_id": a.Monster.ID, "monster_type": a.Monster.Type, "group": a.Group, "warning": a.Warning, "deadline_unix": r121Unix(a.Deadline)})
	}
	out["areas"] = areas
	keys := make([]fiendwar.FiendWarTimerKey, 0, len(f.Timers))
	for key := range f.Timers {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Area != keys[j].Area {
			return keys[i].Area < keys[j].Area
		}
		return keys[i].Kind < keys[j].Kind
	})
	timers := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		timer := f.Timers[key]
		timers = append(timers, map[string]any{"area": key.Area, "kind": key.Kind, "started_unix": r121Unix(timer.StartedAt), "deadline_unix": r121Unix(timer.Deadline)})
	}
	out["timers"] = timers
	memberAreas := map[string]int{}
	for id, area := range f.MemberAreas {
		memberAreas[fmt.Sprint(id)] = area
	}
	out["member_areas"] = memberAreas
	groupAreas := map[string]int{}
	for group, area := range f.GroupAreas {
		groupAreas[fmt.Sprint(group)] = area
	}
	out["group_areas"] = groupAreas
	recovery := map[string]int64{}
	for id, at := range f.RecoveryUntil {
		if !at.IsZero() {
			recovery[fmt.Sprint(id)] = at.Unix()
		}
	}
	out["recovery_unix"] = recovery
	symbols := map[string]int{}
	for name, id := range f.Config.Symbols {
		if value := f.Symbols[id]; value != 0 {
			symbols[name] = value
		}
	}
	out["nonzero_symbols"] = symbols
	return out
}

func r121Unix(at time.Time) int64 {
	if at.IsZero() {
		return 0
	}
	return at.Unix()
}

func r121EarliestTimer(b *fiendwar.FiendWarBattle, kind string) (time.Time, bool) {
	var earliest time.Time
	for key, timer := range b.Timers {
		if key.Kind != kind || timer.Deadline.IsZero() {
			continue
		}
		if earliest.IsZero() || timer.Deadline.Before(earliest) {
			earliest = timer.Deadline
		}
	}
	return earliest, !earliest.IsZero()
}

func r121MoveRoute(f *raidStartGuardFixture, p *gameSession, target []byte) error {
	for _, area := range target {
		plan, err := f.service.prepareCurrentFiendWarMove(p, currentTownSetUserAreaRequest{TownID: 98, AreaID: area})
		if err != nil {
			return fmt.Errorf("prepare area %d: %w", area, err)
		}
		if err := f.service.applyCurrentFiendWarMove(plan); err != nil {
			return fmt.Errorf("apply area %d: %w", area, err)
		}
		plan.confirmed = true
	}
	return nil
}

func r121ReplacementSession(t *testing.T, f *raidStartGuardFixture, old *gameSession) *gameSession {
	t.Helper()
	q := f.session()
	q.selectedCharacterID = old.selectedCharacterID
	q.accountID = old.accountID
	q.channel = old.channel
	q.loginPacketSeen = true
	q.accountFromToken = true
	q.channelReconnect = true
	q.conn = &bufferConn{}
	f.service.bindGameSessionCharacter(q, q.selectedCharacterID)
	return q
}

func r121DriveSuccess(t *testing.T, f *raidStartGuardFixture, peers []*gameSession) error {
	t.Helper()
	if len(peers) < 2 {
		return fmt.Errorf("success corpus needs two boss-side sessions")
	}
	b := f.state.Battle
	now := time.Now()
	b.EnterAfter, b.Deadline = now.Add(-10*time.Minute), now.Add(10*time.Minute)
	centerPeer := peers[0]
	for _, area := range []int{27, 28, 29} {
		dungeon := uint32(b.FiendWar.Config.Areas[area].Dungeon)
		b.FiendWar.MemberAreas[centerPeer.selectedCharacterID] = area
		setCurrentFiendWarTestOrigin(centerPeer, area)
		r := raidCompletedRuntimeFixture(t, centerPeer, dungeon)
		if err := f.service.freezeCurrentRaidDungeonGroupEntry(centerPeer, r, nil); err != nil {
			return fmt.Errorf("freeze center %d: %w", area, err)
		}
		if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(centerPeer, r, now); err != nil || !changed {
			return fmt.Errorf("clear center %d changed=%t: %w", area, changed, err)
		}
	}
	if !b.FiendWar.AreaOpen(30) || !b.FiendWar.AreaOpen(31) {
		return fmt.Errorf("command-center clears did not open both PVF bosses")
	}
	runs := make([]*runtimeDungeonState, 2)
	for side, area := range []int{30, 31} {
		peer := peers[side]
		dungeon := uint32(b.FiendWar.Config.Areas[area].Dungeon)
		b.FiendWar.MemberAreas[peer.selectedCharacterID] = area
		setCurrentFiendWarTestOrigin(peer, area)
		runs[side] = raidCompletedRuntimeFixture(t, peer, dungeon)
		if err := f.service.freezeCurrentRaidDungeonGroupEntry(peer, runs[side], nil); err != nil {
			return fmt.Errorf("freeze boss %d: %w", area, err)
		}
	}
	for side, area := range []int{30, 31} {
		if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(peers[side], runs[side], now); err != nil || !changed {
			return fmt.Errorf("clear boss %d changed=%t: %w", area, changed, err)
		}
	}
	if !b.PhaseCompleted || !b.FiendWar.Complete {
		return fmt.Errorf("real PVF boss pair did not complete the phase")
	}
	if _, errs := f.service.commitCurrentRaidWeeklyIntents(now); len(errs) != 0 {
		return fmt.Errorf("weekly intent commit: %v", errs)
	}
	return nil
}

func r121BuildCoverage(scenarios []r121Scenario) r121Coverage {
	required := []uint16{1483, 1484, 1485, 1486, 1487, 1488, 1489, 1490, 1491, 583, 585, 587, 596, 597, 601}
	counts := map[uint16]int{}
	seenIn := map[uint16]map[string]bool{}
	for _, scenario := range scenarios {
		for _, packet := range scenario.Packets {
			counts[packet.Opcode]++
			if seenIn[packet.Opcode] == nil {
				seenIn[packet.Opcode] = map[string]bool{}
			}
			seenIn[packet.Opcode][scenario.ID] = true
		}
	}
	out := r121Coverage{AllRequired: true, Required: required}
	for _, op := range required {
		row := r121CoverageOp{Opcode: op, Count: counts[op]}
		for id := range seenIn[op] {
			row.Scenarios = append(row.Scenarios, id)
		}
		sort.Strings(row.Scenarios)
		if row.Count == 0 {
			out.AllRequired = false
		}
		out.Opcodes = append(out.Opcodes, row)
	}
	return out
}
