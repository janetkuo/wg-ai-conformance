package conformance

import (
	"flag"
	"testing"
)

// TestRegisteredFlagGroupsMatchFlags catches a registerFlagGroup prefix that
// matches no defined flag (e.g. a typo), which would silently leave that
// test's flags under the global group in -help.
func TestRegisteredFlagGroupsMatchFlags(t *testing.T) {
	seen := map[string]bool{}
	for _, g := range flagGroups {
		if seen[g.prefix] {
			t.Errorf("flag group prefix %q registered more than once", g.prefix)
		}
		seen[g.prefix] = true
	}
	_, _, byPrefix := groupFlags(flagGroups, flag.VisitAll)
	for _, g := range flagGroups {
		if len(byPrefix[g.prefix]) == 0 {
			t.Errorf("flag group %q (%s) matches no defined flag", g.prefix, g.title)
		}
	}
}
