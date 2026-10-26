package aikenupgrade

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type initialTestSource map[string]string

func (s initialTestSource) ReadText(relativePath string) (string, error) {
	value, ok := s[relativePath]
	if !ok {
		return "", fmt.Errorf("missing %s", relativePath)
	}
	return value, nil
}

const initialTestMissingKeyGroup = `[group]
[indexes]
1 2 3
[/indexes]
[/group]
`

const initialTestMissingIndexesGroup = `[group]
[key]
@1@
[/group]
`

const initialTestUnclosedGroup = `[group]
[key]
@1@
[indexes]
1 2 3
[/indexes]
`

const initialTestUnclosedIndexesGroup = `[group]
[key]
@1@
[indexes]
1 2 3
`

func initialCatalogText(raw string) string {
	return strings.ReplaceAll(raw, "@", "`")
}

func initialTestGroupText(key string, indexes ...string) string {
	return initialCatalogText(strings.Join([]string{
		"[group]",
		"[key]",
		"@" + key + "@",
		"[indexes]",
		strings.Join(indexes, " "),
		"[/indexes]",
		"[/group]",
	}, "\n")) + "\n"
}

func initialTestGroupsText() string {
	return initialTestGroupText("1", "1", "2", "3") +
		initialTestGroupText("2", "11", "12") +
		initialTestGroupText("3", "21")
}

func initialTestWeight(grade, value string) string {
	return "@" + grade + "@ " + value
}

func initialTestGradeBlock(entries ...string) string {
	return initialCatalogText(strings.Join([]string{
		"[grade probability]",
		strings.Join(entries, " "),
		"[/grade probability]",
	}, "\n"))
}

func initialTestDefaultGradeBlock() string {
	return initialTestGradeBlock(
		initialTestWeight("A", "1000"),
		initialTestWeight("B", "2000"),
		initialTestWeight("C", "3000"),
		initialTestWeight("D", "4000"),
	)
}

func initialTestListLine(id, relativePath string) string {
	return initialCatalogText(id + " @" + relativePath + "@")
}

func initialTestListText() string {
	return strings.Join([]string{
		initialTestListLine("21", "Options/group3_a.etc"),
		initialTestListLine("1", "Options/group1_a.etc"),
		initialTestListLine("12", "Options/group2_b.etc"),
		initialTestListLine("2", "Options/group1_b.etc"),
		initialTestListLine("3", "Options/group1_c.etc"),
		initialTestListLine("11", "Options/group2_a.etc"),
	}, "\n")
}

func initialTestInfo(groups, gradeBlock string) string {
	parts := []string{"[custom ability type]", "@warriors dungeon@"}
	if groups != "" {
		parts = append(parts, groups)
	}
	if gradeBlock != "" {
		parts = append(parts, gradeBlock)
	}
	return initialCatalogText(strings.Join(parts, "\n") + "\n")
}

func initialTestSourceFrom(groups, gradeBlock, list string) initialTestSource {
	source := initialTestSource{
		initialCatalogPath: initialTestInfo(groups, gradeBlock),
	}
	if list != "" {
		source[initialCatalogListPath] = list
	}
	return source
}

func defaultInitialTestSource() initialTestSource {
	return initialTestSourceFrom(
		initialTestGroupsText(),
		initialTestDefaultGradeBlock(),
		initialTestListText(),
	)
}

func mustInitialTestCatalog(t *testing.T) *InitialCatalog {
	t.Helper()
	catalog, err := NewInitialCatalog(defaultInitialTestSource())
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestNewInitialCatalogCrossChecksGroupsAndListWithoutFixedOptionCount(t *testing.T) {
	catalog := mustInitialTestCatalog(t)
	wantOptions := []byte{1, 2, 3, 11, 12, 21}
	if !reflect.DeepEqual(catalog.options, wantOptions) {
		t.Fatalf("options = %v, want %v", catalog.options, wantOptions)
	}
	if catalog.gradeWeights != ([4]uint32{4000, 3000, 2000, 1000}) ||
		catalog.gradeWeightTotal != initialGradeWeightTotal {
		t.Fatalf("grade weights = %v total=%d", catalog.gradeWeights, catalog.gradeWeightTotal)
	}
	for option, want := range map[byte]int{1: 1, 3: 1, 11: 2, 12: 2, 21: 3} {
		got, ok := catalog.OptionGroup(option)
		if !ok || got != want {
			t.Fatalf("OptionGroup(%d) = %d,%v want %d,true", option, got, ok, want)
		}
	}
	if _, ok := catalog.OptionGroup(0); ok {
		t.Fatal("zero option unexpectedly has a group")
	}
	if _, ok := catalog.OptionGroup(4); ok {
		t.Fatal("unknown option unexpectedly has a group")
	}
}

func TestInitialCatalogValidAllowsEqualOptionsAndRejectsNonDropState(t *testing.T) {
	catalog := mustInitialTestCatalog(t)
	for grade := byte(0); grade <= 3; grade++ {
		state := [5]byte{1, 1, 0, 0, grade}
		if !catalog.Valid(state) {
			t.Fatalf("equal options with grade %d must be valid", grade)
		}
	}
	tests := []struct {
		name  string
		state [5]byte
	}{
		{name: "zero first option", state: [5]byte{0, 1, 0, 0, 0}},
		{name: "zero second option", state: [5]byte{1, 0, 0, 0, 0}},
		{name: "unknown first option", state: [5]byte{4, 1, 0, 0, 0}},
		{name: "unknown second option", state: [5]byte{1, 4, 0, 0, 0}},
		{name: "third option set", state: [5]byte{1, 2, 1, 0, 0}},
		{name: "set key set", state: [5]byte{1, 2, 0, 1, 0}},
		{name: "grade above three", state: [5]byte{1, 2, 0, 0, 4}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if catalog.Valid(test.state) {
				t.Fatalf("Valid(%v) = true", test.state)
			}
		})
	}
	var nilCatalog *InitialCatalog
	if nilCatalog.Valid([5]byte{1, 2, 0, 0, 0}) {
		t.Fatal("nil catalog accepted a state")
	}
}

func TestInitialCatalogRollUsesTwoIndependentOptionSlots(t *testing.T) {
	catalog := mustInitialTestCatalog(t)
	tests := []struct {
		name  string
		draws [3]uint32
		want  [5]byte
	}{
		{
			name:  "same option in both slots",
			draws: [3]uint32{2, 2, 0},
			want:  [5]byte{3, 3, 0, 0, 0},
		},
		{
			name:  "different groups and top grade",
			draws: [3]uint32{0, 5, 9999},
			want:  [5]byte{1, 21, 0, 0, 3},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			roll := func(bound uint32) (uint32, error) {
				calls++
				wantBound := uint32(len(catalog.options))
				if calls == 3 {
					wantBound = initialGradeWeightTotal
				}
				if bound != wantBound {
					t.Fatalf("draw %d bound = %d, want %d", calls, bound, wantBound)
				}
				if calls > len(test.draws) {
					t.Fatalf("unexpected draw %d", calls)
				}
				return test.draws[calls-1], nil
			}
			state, err := catalog.Roll(roll)
			if err != nil {
				t.Fatal(err)
			}
			if state != test.want {
				t.Fatalf("state = %v, want %v", state, test.want)
			}
			if calls != 3 {
				t.Fatalf("draw count = %d, want 3", calls)
			}
		})
	}
}

func TestInitialCatalogRollMapsEveryValidatedOption(t *testing.T) {
	catalog := mustInitialTestCatalog(t)
	for index, wantOption := range catalog.options {
		calls := 0
		state, err := catalog.Roll(func(bound uint32) (uint32, error) {
			calls++
			switch calls {
			case 1, 2:
				if bound != uint32(len(catalog.options)) {
					t.Fatalf("option bound = %d, want %d", bound, len(catalog.options))
				}
				return uint32(index), nil
			case 3:
				if bound != initialGradeWeightTotal {
					t.Fatalf("grade bound = %d, want %d", bound, initialGradeWeightTotal)
				}
				return 0, nil
			default:
				t.Fatalf("unexpected draw %d", calls)
				return 0, nil
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		if state[0] != wantOption {
			t.Fatalf("option draw %d = %d, want %d", index, state[0], wantOption)
		}
		if state[1] != wantOption {
			t.Fatalf("second option draw %d = %d, want %d", index, state[1], wantOption)
		}
	}
}

func TestInitialCatalogRollUsesEveryGradeThreshold(t *testing.T) {
	catalog := mustInitialTestCatalog(t)
	tests := []struct {
		name string
		draw uint32
		want byte
	}{
		{name: "D first", draw: 0, want: 0},
		{name: "D last", draw: 3999, want: 0},
		{name: "C first", draw: 4000, want: 1},
		{name: "C last", draw: 6999, want: 1},
		{name: "B first", draw: 7000, want: 2},
		{name: "B last", draw: 8999, want: 2},
		{name: "A first", draw: 9000, want: 3},
		{name: "A last", draw: 9999, want: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			roll := func(bound uint32) (uint32, error) {
				calls++
				switch calls {
				case 1, 2:
					if bound != uint32(len(catalog.options)) {
						t.Fatalf("option bound = %d", bound)
					}
					return 0, nil
				case 3:
					if bound != initialGradeWeightTotal {
						t.Fatalf("grade bound = %d", bound)
					}
					return test.draw, nil
				default:
					t.Fatalf("unexpected draw %d", calls)
					return 0, nil
				}
			}
			state, err := catalog.Roll(roll)
			if err != nil {
				t.Fatal(err)
			}
			if state[4] != test.want {
				t.Fatalf("grade = %d, want %d", state[4], test.want)
			}
			if state[2] != 0 || state[3] != 0 {
				t.Fatalf("fresh state zero pair = %v", state[2:4])
			}
		})
	}
}

func TestInitialCatalogAcceptsZeroWeightsForUnusedGrades(t *testing.T) {
	gradeBlock := initialTestGradeBlock(
		initialTestWeight("A", "0"),
		initialTestWeight("B", "0"),
		initialTestWeight("C", "0"),
		initialTestWeight("D", "10000"),
	)
	catalog, err := NewInitialCatalog(initialTestSourceFrom(
		initialTestGroupsText(),
		gradeBlock,
		initialTestListText(),
	))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	state, err := catalog.Roll(func(bound uint32) (uint32, error) {
		calls++
		if calls <= 2 {
			return 0, nil
		}
		if bound != initialGradeWeightTotal {
			t.Fatalf("grade bound = %d", bound)
		}
		return 9999, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if state[4] != 0 || !catalog.Valid([5]byte{1, 1, 0, 0, 3}) {
		t.Fatalf("zero-weight catalog state=%v", state)
	}
}

func TestNewInitialCatalogRejectsBadGroups(t *testing.T) {
	tests := []struct {
		name   string
		groups string
	}{
		{name: "missing groups", groups: ""},
		{name: "empty indexes", groups: initialTestGroupText("1") + initialTestGroupText("2", "11", "12") + initialTestGroupText("3", "21")},
		{name: "missing key", groups: initialCatalogText(initialTestMissingKeyGroup)},
		{name: "missing indexes", groups: initialCatalogText(initialTestMissingIndexesGroup)},
		{name: "missing group close", groups: initialCatalogText(initialTestUnclosedGroup)},
		{name: "missing indexes close", groups: initialCatalogText(initialTestUnclosedIndexesGroup)},
		{name: "zero group key", groups: initialTestGroupText("0", "1", "2", "3") + initialTestGroupText("2", "11", "12") + initialTestGroupText("3", "21")},
		{name: "oversized group key", groups: initialTestGroupText("256", "1", "2", "3") + initialTestGroupText("2", "11", "12") + initialTestGroupText("3", "21")},
		{name: "nondecimal group key", groups: initialTestGroupText("x", "1", "2", "3") + initialTestGroupText("2", "11", "12") + initialTestGroupText("3", "21")},
		{name: "duplicate group key", groups: initialTestGroupText("1", "1", "2", "3") + initialTestGroupText("1", "11", "12") + initialTestGroupText("3", "21")},
		{name: "duplicate option in group", groups: initialTestGroupText("1", "1", "2", "3", "1") + initialTestGroupText("2", "11", "12") + initialTestGroupText("3", "21")},
		{name: "duplicate option across groups", groups: initialTestGroupText("1", "1", "2", "3") + initialTestGroupText("2", "11", "12", "1") + initialTestGroupText("3", "21")},
		{name: "zero option", groups: initialTestGroupText("1", "1", "2", "3", "0") + initialTestGroupText("2", "11", "12") + initialTestGroupText("3", "21")},
		{name: "oversized option", groups: initialTestGroupText("1", "1", "2", "3", "256") + initialTestGroupText("2", "11", "12") + initialTestGroupText("3", "21")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewInitialCatalog(initialTestSourceFrom(
				test.groups,
				initialTestDefaultGradeBlock(),
				initialTestListText(),
			))
			if !errors.Is(err, ErrInitialCatalog) {
				t.Fatalf("error = %v, want ErrInitialCatalog", err)
			}
		})
	}
}

func TestNewInitialCatalogRejectsBadGradeProbability(t *testing.T) {
	valid := initialTestDefaultGradeBlock()
	tests := []struct {
		name       string
		gradeBlock string
	}{
		{name: "missing grade probability", gradeBlock: ""},
		{name: "duplicate grade probability", gradeBlock: valid + "\n" + valid},
		{
			name: "missing grade",
			gradeBlock: initialTestGradeBlock(
				initialTestWeight("A", "1000"),
				initialTestWeight("B", "2000"),
				initialTestWeight("C", "3000"),
			),
		},
		{
			name: "duplicate grade",
			gradeBlock: initialTestGradeBlock(
				initialTestWeight("A", "1000"),
				initialTestWeight("B", "2000"),
				initialTestWeight("C", "3000"),
				initialTestWeight("A", "4000"),
			),
		},
		{
			name: "negative weight",
			gradeBlock: initialTestGradeBlock(
				initialTestWeight("A", "1000"),
				initialTestWeight("B", "2000"),
				initialTestWeight("C", "3000"),
				initialTestWeight("D", "-1"),
			),
		},
		{
			name: "weight total low",
			gradeBlock: initialTestGradeBlock(
				initialTestWeight("A", "999"),
				initialTestWeight("B", "2000"),
				initialTestWeight("C", "3000"),
				initialTestWeight("D", "4000"),
			),
		},
		{
			name: "weight total high",
			gradeBlock: initialTestGradeBlock(
				initialTestWeight("A", "1001"),
				initialTestWeight("B", "2000"),
				initialTestWeight("C", "3000"),
				initialTestWeight("D", "4000"),
			),
		},
		{
			name: "noninteger weight",
			gradeBlock: initialTestGradeBlock(
				initialTestWeight("A", "1000"),
				initialTestWeight("B", "2000"),
				initialTestWeight("C", "3000"),
				initialTestWeight("D", "many"),
			),
		},
		{
			name: "extra token",
			gradeBlock: initialTestGradeBlock(
				initialTestWeight("A", "1000"),
				initialTestWeight("B", "2000"),
				initialTestWeight("C", "3000"),
				initialTestWeight("D", "4000"),
				"extra",
			),
		},
		{name: "missing close", gradeBlock: strings.Replace(valid, "[/grade probability]", "", 1)},
		{name: "wrong close", gradeBlock: strings.Replace(valid, "[/grade probability]", "[/grades]", 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewInitialCatalog(initialTestSourceFrom(
				initialTestGroupsText(),
				test.gradeBlock,
				initialTestListText(),
			))
			if !errors.Is(err, ErrInitialCatalog) {
				t.Fatalf("error = %v, want ErrInitialCatalog", err)
			}
		})
	}
}

func TestNewInitialCatalogRejectsBadList(t *testing.T) {
	valid := initialTestListText()
	groupOnePath := "Options/group1_a.etc"
	groupTwoPath := "Options/group1_b.etc"
	backslashPath := "Options" + string(rune(92)) + "group1_a.etc"
	tests := []struct {
		name   string
		mutate func(*initialTestSource)
	}{
		{
			name: "missing list",
			mutate: func(source *initialTestSource) {
				delete(*source, initialCatalogListPath)
			},
		},
		{
			name: "missing ID",
			mutate: func(source *initialTestSource) {
				(*source)[initialCatalogListPath] = strings.Replace(valid, initialTestListLine("21", "Options/group3_a.etc"), "", 1)
			},
		},
		{
			name: "extra ID",
			mutate: func(source *initialTestSource) {
				(*source)[initialCatalogListPath] = valid + "\n" + initialTestListLine("31", "Options/extra.etc")
			},
		},
		{
			name: "duplicate ID",
			mutate: func(source *initialTestSource) {
				(*source)[initialCatalogListPath] = strings.Replace(valid,
					initialTestListLine("21", "Options/group3_a.etc"),
					initialTestListLine("1", "Options/group3_a.etc"),
					1,
				)
			},
		},
		{
			name: "zero ID",
			mutate: func(source *initialTestSource) {
				(*source)[initialCatalogListPath] = strings.Replace(valid,
					initialTestListLine("21", "Options/group3_a.etc"),
					initialTestListLine("0", "Options/group3_a.etc"),
					1,
				)
			},
		},
		{
			name: "oversized ID",
			mutate: func(source *initialTestSource) {
				(*source)[initialCatalogListPath] = strings.Replace(valid,
					initialTestListLine("21", "Options/group3_a.etc"),
					initialTestListLine("256", "Options/group3_a.etc"),
					1,
				)
			},
		},
		{
			name: "parent path",
			mutate: func(source *initialTestSource) {
				(*source)[initialCatalogListPath] = strings.Replace(valid,
					initialTestListLine("1", groupOnePath),
					initialTestListLine("1", "../Options/group1_a.etc"),
					1,
				)
			},
		},
		{
			name: "absolute path",
			mutate: func(source *initialTestSource) {
				(*source)[initialCatalogListPath] = strings.Replace(valid,
					initialTestListLine("1", groupOnePath),
					initialTestListLine("1", "/Options/group1_a.etc"),
					1,
				)
			},
		},
		{
			name: "backslash path",
			mutate: func(source *initialTestSource) {
				(*source)[initialCatalogListPath] = strings.Replace(valid,
					initialTestListLine("1", groupOnePath),
					initialTestListLine("1", backslashPath),
					1,
				)
			},
		},
		{
			name: "dot segment",
			mutate: func(source *initialTestSource) {
				(*source)[initialCatalogListPath] = strings.Replace(valid,
					initialTestListLine("1", groupOnePath),
					initialTestListLine("1", "Options/./group1_a.etc"),
					1,
				)
			},
		},
		{
			name: "duplicate path",
			mutate: func(source *initialTestSource) {
				(*source)[initialCatalogListPath] = strings.Replace(valid,
					initialTestListLine("2", groupTwoPath),
					initialTestListLine("2", groupOnePath),
					1,
				)
			},
		},
		{
			name: "odd token count",
			mutate: func(source *initialTestSource) {
				(*source)[initialCatalogListPath] = valid + "\n7"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := defaultInitialTestSource()
			test.mutate(&source)
			_, err := NewInitialCatalog(source)
			if !errors.Is(err, ErrInitialCatalog) {
				t.Fatalf("error = %v, want ErrInitialCatalog", err)
			}
		})
	}
}

func TestInitialCatalogRollRejectsDrawErrorsAndOutOfBounds(t *testing.T) {
	catalog := mustInitialTestCatalog(t)
	boom := errors.New("draw failed")
	tests := []struct {
		name   string
		failAt int
		boom   bool
		value  uint32
	}{
		{name: "first slot error", failAt: 1, boom: true},
		{name: "second slot error", failAt: 2, boom: true},
		{name: "grade error", failAt: 3, boom: true},
		{name: "first slot out of bounds", failAt: 1, value: uint32(len(catalog.options))},
		{name: "second slot out of bounds", failAt: 2, value: uint32(len(catalog.options))},
		{name: "grade out of bounds", failAt: 3, value: initialGradeWeightTotal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			roll := func(bound uint32) (uint32, error) {
				calls++
				wantBound := uint32(len(catalog.options))
				if calls == 3 {
					wantBound = initialGradeWeightTotal
				}
				if bound != wantBound {
					t.Fatalf("draw %d bound = %d, want %d", calls, bound, wantBound)
				}
				if calls == test.failAt {
					if test.boom {
						return 0, boom
					}
					return test.value, nil
				}
				return 0, nil
			}
			state, err := catalog.Roll(roll)
			if err == nil {
				t.Fatal("Roll accepted a bad draw")
			}
			if !errors.Is(err, ErrInitialCatalog) {
				t.Fatalf("error = %v, want ErrInitialCatalog", err)
			}
			if test.boom && !errors.Is(err, boom) {
				t.Fatalf("error lost draw cause: %v", err)
			}
			if state != ([5]byte{}) {
				t.Fatalf("failure returned state %v", state)
			}
			if calls != test.failAt {
				t.Fatalf("draw count = %d, want %d (no fallback allowed)", calls, test.failAt)
			}
		})
	}
	if _, err := catalog.Roll(nil); !errors.Is(err, ErrInitialCatalog) {
		t.Fatalf("nil roll error = %v", err)
	}
}
