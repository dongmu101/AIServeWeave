// Package capprofile turns a backend's reported version string into
// capability evidence, using a per-adapter table of version floors. Every
// LLM adapter needs the same thing — "this capability is only claimed from
// version X on" — so the version parsing and floor comparison live here
// once instead of in each adapter's profile.go.
//
// The tables themselves stay with their adapters: what a given vLLM or
// Ollama release supports is adapter knowledge, while how a floor is
// applied is not.
package capprofile

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"AIServeWeave/common/runtime"
)

// Entry declares the capabilities a backend is known to support from
// MinVersion onward. Detail must cite the evidence — official documentation
// or a contract test — because a profile entry is the weakest capability
// source and is the one most likely to be wrong after an upstream release.
type Entry struct {
	MinVersion string
	Detail     string
	Caps       map[runtime.Capability]runtime.SupportLevel
}

// Table is one adapter's full set of version floors. Order is irrelevant:
// Resolve applies entries from lowest floor to highest, so a later release
// can refine what an earlier one declared.
type Table []Entry

// Resolve returns the capability evidence a backend reporting version
// should be credited with. Capabilities no entry mentions are absent from
// the result, which CapabilitySet.Resolve reports as unknown.
//
// An empty or unparsable version yields an empty set: without knowing the
// version there is no evidence, and inventing some would turn "never
// checked" into "supported".
func (t Table) Resolve(version string) runtime.CapabilitySet {
	set := make(runtime.CapabilitySet)
	parsed, ok := ParseVersion(version)
	if !ok {
		return set
	}

	applicable := make([]Entry, 0, len(t))
	for _, entry := range t {
		floor, ok := ParseVersion(entry.MinVersion)
		if !ok || Compare(parsed, floor) < 0 {
			continue
		}
		applicable = append(applicable, entry)
	}
	sort.SliceStable(applicable, func(i, j int) bool {
		a, _ := ParseVersion(applicable[i].MinVersion)
		b, _ := ParseVersion(applicable[j].MinVersion)
		return Compare(a, b) < 0
	})

	for _, entry := range applicable {
		for capability, level := range entry.Caps {
			set[capability] = runtime.CapabilityEvidence{
				Capability: capability,
				Level:      level,
				Source:     runtime.SourceRuntimeProfile,
				Detail:     fmt.Sprintf("%s; requires version >= %s", entry.Detail, entry.MinVersion),
			}
		}
	}
	return set
}

// ParseVersion extracts a major/minor/patch triple from a version string
// such as "0.32.14", "v0.6.3" or "0.5.4-rc1". Missing components read as
// zero, so "0.5" parses as 0.5.0. A string whose first component is not a
// number does not parse, which callers must treat as "version unknown"
// rather than as version zero.
func ParseVersion(v string) ([3]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" {
		return [3]int{}, false
	}
	if idx := strings.IndexAny(v, "-+ "); idx >= 0 {
		v = v[:idx]
	}

	parts := strings.Split(v, ".")
	var out [3]int
	for i := 0; i < 3 && i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 {
			if i == 0 {
				return [3]int{}, false
			}
			break // trailing junk after a valid prefix, e.g. "0.6.3post1"
		}
		out[i] = n
	}
	return out, true
}

// Compare orders two parsed versions, returning -1, 0 or 1.
func Compare(a, b [3]int) int {
	for i := range a {
		switch {
		case a[i] < b[i]:
			return -1
		case a[i] > b[i]:
			return 1
		}
	}
	return 0
}
