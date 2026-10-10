package card

import (
	"fmt"
	"sort"
	"strings"
)

// Shapes: what a card is made of.
//
// Two axes, kept apart (byz-kan CW-31): Type is the kind of work, exactly one
// per card; Sections are shaped blocks in cardData, any type may carry any
// section the catalog allows. On top of that, Darryn's card standard: a card
// is a founder-scannable top line plus an agent-executable brief. So each
// type has one required top-line section, every card needs acceptance (the
// "done when ..., proven by ..." lines), and every card is seeded with an
// empty brief so the agent sees the slots.
//
// Unknown section keys and unknown fields inside a section are refused. The
// catalog is finite on purpose: shape replaced the form.

// Types of work. Empty means story.
var Types = []string{"story", "defect", "task", "spike", "chore", "epic"}

// Section keys inside cardData.
const (
	SecStory      = "story"
	SecDefect     = "defect"
	SecTask       = "task"
	SecSpike      = "spike"
	SecChore      = "chore"
	SecEpic       = "epic"
	SecAcceptance = "acceptance"
	SecBrief      = "brief"
	SecScenarios  = "scenarios"
	SecUAT        = "uat"
	SecSource     = "source"
)

// Sizes and priorities. P3 is the highest priority; S is under a day, M a
// few days, L a week or more.
var (
	Sizes      = []string{"S", "M", "L"}
	Priorities = []string{"P0", "P1", "P2", "P3"}
)

// Section describes one block: its kind and the fields it may carry.
type Section struct {
	Key    string
	Kind   string   // object | strings | objects
	Fields []string // object: its keys; objects: keys of each item; strings: none
	Lists  []string // object fields that are string lists
	Hint   string
}

// Catalog is every section a card may carry.
var Catalog = []Section{
	{SecStory, "object", []string{"asA", "iWant", "soThat"}, nil, "Story: As a <asA>, I want <iWant>, so that <soThat>."},
	{SecDefect, "object", []string{"symptom", "repro", "expected", "actual"}, nil, "Bug: <symptom>. Expected <expected>, got <actual>. Repro if known."},
	{SecTask, "object", []string{"what", "soThat"}, nil, "Task: <what>, so that <soThat>. Ops, admin, data; no user story."},
	{SecSpike, "object", []string{"question", "timeboxMinutes", "approach", "findings", "outcome", "followUp"}, nil, "Question: <question>. Timeboxed. Findings and outcome filled when done."},
	{SecChore, "object", []string{"what", "why"}, nil, "Chore: <what>, so that <why>. Routine upkeep."},
	{SecEpic, "object", []string{"outcome"}, nil, "Epic: <outcome>. Children are cards whose parent is this card."},
	{SecAcceptance, "strings", nil, nil, "Done-when lines. Each one testable; write 'X, proven by Y' so the proof is the UAT."},
	{SecBrief, "object", []string{"state", "refs", "files", "prereqs", "rules", "gotchas", "escalate"}, []string{"refs", "files", "prereqs", "rules", "gotchas"}, "Agent brief. state: built / left / current truth. refs: PRs, ADRs, cards. files: paths touched. prereqs: true before starting. rules: MUST / NEVER. gotchas: traps. escalate: when to stop and who to tell."},
	{SecScenarios, "objects", []string{"name", "given", "when", "then"}, nil, "Given / When / Then, each able to fail."},
	{SecUAT, "strings", nil, nil, "Steps a tester runs."},
}

func section(key string) (Section, bool) {
	for _, s := range Catalog {
		if s.Key == key {
			return s, true
		}
	}
	return Section{}, false
}

// topLine is the one required section per type.
var topLine = map[string]string{
	"story": SecStory, "defect": SecDefect, "task": SecTask,
	"spike": SecSpike, "chore": SecChore, "epic": SecEpic,
}

// requiredFields inside the top-line section. Spike's question is filled
// from the title when absent, so it is never missing in practice.
var requiredFields = map[string][]string{
	SecStory:  {"asA", "iWant", "soThat"},
	SecDefect: {"symptom", "expected", "actual"},
	SecTask:   {"what", "soThat"},
	SecSpike:  {"question"},
	SecChore:  {"what", "why"},
	SecEpic:   {"outcome"},
}

// offered sections are worth suggesting, never auto-created.
var offered = map[string][]string{
	"story":  {SecScenarios, SecUAT},
	"defect": {SecScenarios, SecUAT},
	"task":   {SecScenarios, SecUAT},
	"spike":  {},
	"chore":  {},
	"epic":   {},
}

// Seeded returns the sections created empty for a type: the top line,
// acceptance, and the brief.
func Seeded(typ string) []string { return []string{topLine[typ], SecAcceptance, SecBrief} }

// Offered returns the sections suggested for a type.
func Offered(typ string) []string { return offered[typ] }

// TopLineSection names the required section for a type.
func TopLineSection(typ string) string { return topLine[typ] }

// NormalizeType canonicalises a type; empty means story. byz-kan's legacy
// "ticket" and the card standard's "feature" and "bug" are accepted spellings.
func NormalizeType(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "", "ticket", "feature":
		return "story", nil
	case "bug":
		return "defect", nil
	}
	for _, t := range Types {
		if t == s {
			return t, nil
		}
	}
	return "", fmt.Errorf("type %q; one of %s", s, strings.Join(Types, ", "))
}

// NormalizeSize accepts S, M, L in any case, or empty.
func NormalizeSize(s string) (string, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return "", nil
	}
	for _, x := range Sizes {
		if x == s {
			return x, nil
		}
	}
	return "", fmt.Errorf("size %q; one of S, M, L (under a day, a few days, a week or more)", s)
}

// NormalizePriority accepts 0..3 or P0..P3. P3 is the highest.
func NormalizePriority(s string) (int, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "P")
	switch s {
	case "0", "1", "2", "3":
		return int(s[0] - '0'), nil
	}
	return 0, fmt.Errorf("priority %q; one of P0, P1, P2, P3 (P3 highest)", s)
}

// Empty returns the blank shape for a section.
func Empty(key string) any {
	s, ok := section(key)
	if !ok {
		return map[string]any{}
	}
	switch s.Kind {
	case "object":
		m := map[string]any{}
		for _, f := range s.Fields {
			if contains(s.Lists, f) {
				m[f] = []any{}
			} else if f == "timeboxMinutes" {
				m[f] = 0
			} else {
				m[f] = ""
			}
		}
		return m
	default:
		return []any{}
	}
}

// Validate checks that cardData only carries catalog sections with catalog
// fields of the right kind. It does not check required content; Check does.
func Validate(data map[string]any) error {
	var errs []string
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k == SecSource {
			continue
		}
		s, ok := section(k)
		if !ok {
			errs = append(errs, fmt.Sprintf("unknown section %q", k))
			continue
		}
		if err := validateSection(s, data[k]); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("card shape: %s", strings.Join(errs, "; "))
	}
	return nil
}

func validateSection(s Section, v any) error {
	switch s.Kind {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object with %s", s.Key, strings.Join(s.Fields, ", "))
		}
		for f, fv := range m {
			if !contains(s.Fields, f) {
				return fmt.Errorf("%s.%s is not a field; %s has %s", s.Key, f, s.Key, strings.Join(s.Fields, ", "))
			}
			if contains(s.Lists, f) {
				if _, err := toStrings(fv); err != nil {
					return fmt.Errorf("%s.%s must be a list of strings", s.Key, f)
				}
				continue
			}
			if f == "timeboxMinutes" {
				switch fv.(type) {
				case float64, int, int64, nil:
				default:
					return fmt.Errorf("%s.timeboxMinutes must be a number", s.Key)
				}
				continue
			}
			if _, ok := fv.(string); !ok && fv != nil {
				return fmt.Errorf("%s.%s must be a string", s.Key, f)
			}
		}
	case "strings":
		if _, err := toStrings(v); err != nil {
			return fmt.Errorf("%s must be a list of strings", s.Key)
		}
	case "objects":
		list, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s must be a list of objects with %s", s.Key, strings.Join(s.Fields, ", "))
		}
		for i, item := range list {
			m, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("%s[%d] must be an object", s.Key, i)
			}
			for f := range m {
				if !contains(s.Fields, f) {
					return fmt.Errorf("%s[%d].%s is not a field; items have %s", s.Key, i, f, strings.Join(s.Fields, ", "))
				}
			}
		}
	}
	return nil
}

// Check is the refusal rule: the type's top-line fields and at least one
// acceptance line. Missing pieces are named so the agent can ask for them.
func Check(typ string, data map[string]any) error {
	var missing []string
	tl := topLine[typ]
	obj, _ := data[tl].(map[string]any)
	for _, f := range requiredFields[tl] {
		if v, _ := obj[f].(string); strings.TrimSpace(v) == "" {
			missing = append(missing, tl+"."+f)
		}
	}
	acc, _ := toStrings(data[SecAcceptance])
	if !anyFilled(acc) {
		missing = append(missing, "acceptance (at least one done-when line)")
	}
	if len(missing) > 0 {
		return fmt.Errorf("card refused; missing %s. A %s needs its %s section and acceptance", strings.Join(missing, ", "), typ, tl)
	}
	return nil
}

// Filled reports whether a section carries anything a reader would see.
func Filled(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(t) != ""
	case bool:
		return t
	case float64:
		return t != 0
	case int:
		return t != 0
	case []any:
		for _, x := range t {
			if Filled(x) {
				return true
			}
		}
		return false
	case []string:
		return anyFilled(t)
	case map[string]any:
		for _, x := range t {
			if Filled(x) {
				return true
			}
		}
		return false
	}
	return true
}

// TopLine renders the founder-scannable first line for a card.
func TopLine(typ string, data map[string]any) string {
	obj, _ := data[topLine[typ]].(map[string]any)
	get := func(k string) string {
		s, _ := obj[k].(string)
		return strings.TrimSpace(s)
	}
	switch typ {
	case "story":
		return fmt.Sprintf("Story: As a %s, I want %s, so that %s.", get("asA"), get("iWant"), get("soThat"))
	case "defect":
		line := fmt.Sprintf("Bug: %s. Expected %s, got %s.", get("symptom"), get("expected"), get("actual"))
		if r := get("repro"); r != "" {
			line += " Repro: " + r
		}
		return line
	case "task":
		return fmt.Sprintf("Task: %s, so that %s.", get("what"), get("soThat"))
	case "spike":
		line := "Question: " + get("question") + "."
		if tb, ok := obj["timeboxMinutes"].(float64); ok && tb > 0 {
			line += fmt.Sprintf(" Timebox %d min.", int(tb))
		} else if tb, ok := obj["timeboxMinutes"].(int); ok && tb > 0 {
			line += fmt.Sprintf(" Timebox %d min.", tb)
		}
		return line
	case "chore":
		return fmt.Sprintf("Chore: %s, so that %s.", get("what"), get("why"))
	case "epic":
		return "Epic: " + get("outcome") + "."
	}
	return ""
}

func toStrings(v any) ([]string, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case []string:
		return t, nil
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			s, ok := x.(string)
			if !ok {
				return nil, fmt.Errorf("not a string")
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("not a list")
}

func anyFilled(list []string) bool {
	for _, s := range list {
		if strings.TrimSpace(s) != "" {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
