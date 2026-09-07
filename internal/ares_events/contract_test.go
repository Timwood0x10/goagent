package ares_events

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// knownUnwired lists event constants that are intentionally not emitted by
// any production code path yet (capability reservations). The contract is
// "every SUBSCRIBED event must have an emitter" — these constants have no
// subscriber, so they are inert, not silently broken.
var knownUnwired = map[EventType]bool{
	EventHandoff:          true, // leader-sub handoff retired
	EventSubTaskScheduled: true, // superseded by the Task Fabric READY state
	EventSubTaskStarted:   true, // superseded by EventTaskStarted
	EventSubAgentFailed:   true, // agent deaths flow through agentfabric events
	EventMemoryDistilled:  true, // distillation writes via the experience repo
}

// TestEventContract_SubscribedMustHaveEmitter is the gate: for every event
// constant that production code SUBSCRIBES to, there must exist at least one
// production emitter (a literal reference to the same string, or Emit with
// the constant). This prevents a repeat of the EventSubTaskResult silent
// failure — a subscriber with no publisher.
//
// Mechanism: walk the repo (excluding this package and tests), find every
// occurrence of each event's string literal; a subscribed event with zero
// literal occurrences means nobody can publish it.
func TestEventContract_SubscribedMustHaveEmitter(t *testing.T) {
	repoRoot := ".."
	emittable := map[EventType]string{
		EventAgentStarted:          string(EventAgentStarted),
		EventAgentStopped:          string(EventAgentStopped),
		EventTaskCreated:           string(EventTaskCreated),
		EventTaskCompleted:         string(EventTaskCompleted),
		EventTaskFailed:            string(EventTaskFailed),
		EventTaskReady:             string(EventTaskReady),
		EventTaskAcquired:          string(EventTaskAcquired),
		EventTaskStarted:           string(EventTaskStarted),
		EventTaskYielded:           string(EventTaskYielded),
		EventTaskCheckpointed:      string(EventTaskCheckpointed),
		EventTaskPreempted:         string(EventTaskPreempted),
		EventTaskReleased:          string(EventTaskReleased),
		EventLLMCall:               string(EventLLMCall),
		EventToolCallStarted:       string(EventToolCallStarted),
		EventToolCallCompleted:     string(EventToolCallCompleted),
		EventMemoryDistilled:       string(EventMemoryDistilled),
		EventSubTaskResult:         string(EventSubTaskResult),
		EventFailoverTriggered:     string(EventFailoverTriggered),
		EventFailoverCompleted:     string(EventFailoverCompleted),
		EventStepRecoveryStarted:   string(EventStepRecoveryStarted),
		EventStepRecoveryCompleted: string(EventStepRecoveryCompleted),
		EventStepRecoveryFailed:    string(EventStepRecoveryFailed),
	}
	// Which events are actually subscribed (Subscribe calls referencing the
	// constant)? Parse this package's callers across the repo.
	subscribed := map[EventType]bool{}
	scanGoFiles(t, repoRoot, func(path string, line string) {
		for ev := range emittable {
			if strings.Contains(line, "ares_events."+constName(ev)) ||
				strings.Contains(line, constName(ev)) {
				if strings.Contains(line, "Subscribe") || strings.Contains(line, "EventFilter") {
					subscribed[ev] = true
				}
			}
		}
	})

	// Count emitters: literal string occurrences outside this package.
	emitCounts := map[EventType]int{}
	scanGoFiles(t, repoRoot, func(path string, line string) {
		for ev, literal := range emittable {
			if strings.Contains(line, `"`+literal+`"`) {
				emitCounts[ev]++
			}
		}
	})

	var broken []string
	for ev := range subscribed {
		if knownUnwired[ev] {
			continue
		}
		if emitCounts[ev] == 0 {
			broken = append(broken, string(ev))
		}
	}
	sort.Strings(broken)
	for _, ev := range broken {
		t.Errorf("event contract violated: %q is subscribed but has no emitter", ev)
	}
}

// constName maps an EventType value back to its Go constant name via the
// types.go declaration table (parsed once).
func constName(ev EventType) string {
	if constNames == nil {
		constNames = parseConstNames()
	}
	return constNames[ev]
}

var constNames map[EventType]string

// parseConstNames extracts "ConstName EventType = \"value\"" pairs from
// types.go so tests never hardcode the mapping.
func parseConstNames() map[EventType]string {
	out := map[EventType]string{}
	data, err := os.ReadFile("types.go")
	if err != nil {
		return out
	}
	re := regexp.MustCompile(`(Event[A-Za-z0-9]+)\s+EventType\s+=\s+"([^"]+)"`)
	for _, m := range re.FindAllStringSubmatch(string(data), -1) {
		out[EventType(m[2])] = m[1]
	}
	return out
}

// scanGoFiles walks all non-test Go files under root, invoking fn for every
// line (skipping _test.go and the ares_events package itself).
func scanGoFiles(t *testing.T, root string, fn func(path, line string)) {
	t.Helper()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if strings.Contains(path, "ares_events/") {
			return nil // the declaring package is excluded (self-references)
		}
		f, ferr := os.Open(path)
		if ferr != nil {
			return nil
		}
		defer func() { _ = f.Close() }() // test reader; close error is irrelevant
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fn(path, sc.Text())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}
