package conformance

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// flagGroup groups the test-specific flags shown in -help.
type flagGroup struct {
	prefix string
	title  string
}

var flagGroups []flagGroup

// registerFlagGroup lists flags named prefix or prefix-* under title in the
// -help output. Call it from the init() of the test file that defines those
// flags, so adding a test does not require editing this file.
func registerFlagGroup(prefix, title string) {
	flagGroups = append(flagGroups, flagGroup{prefix: prefix, title: title})
}

// groupFlags assigns each non-"test." flag to the first registered group
// whose prefix it matches, or to the global group otherwise. Groups are
// returned sorted by title so the output does not depend on init() order.
func groupFlags(groups []flagGroup, visit func(func(*flag.Flag))) (global []*flag.Flag, sorted []flagGroup, byPrefix map[string][]*flag.Flag) {
	sorted = append([]flagGroup(nil), groups...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].title < sorted[j].title })
	byPrefix = map[string][]*flag.Flag{}
	visit(func(f *flag.Flag) {
		if strings.HasPrefix(f.Name, "test.") {
			return
		}
		for _, g := range sorted {
			if f.Name == g.prefix || strings.HasPrefix(f.Name, g.prefix+"-") {
				byPrefix[g.prefix] = append(byPrefix[g.prefix], f)
				return
			}
		}
		global = append(global, f)
	})
	return global, sorted, byPrefix
}

func TestMain(m *testing.M) {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of Kubernetes AI Conformance tests:\n\n")
		fmt.Fprintf(os.Stderr, "These tests require specific flags depending on the capability being tested.\n\n")

		global, groups, byPrefix := groupFlags(flagGroups, flag.VisitAll)

		printFlags := func(title string, flags []*flag.Flag) {
			if len(flags) == 0 {
				return
			}
			fmt.Fprintf(os.Stderr, "%s:\n", title)
			for _, f := range flags {
				// Use exactly the same format as Go's default flag printing
				_, _ = fmt.Fprintf(os.Stderr, "  -%s", f.Name)
				name, usage := flag.UnquoteUsage(f)
				if len(name) > 0 {
					_, _ = fmt.Fprintf(os.Stderr, " %s", name)
				}
				_, _ = fmt.Fprintf(os.Stderr, "\n    \t%s", strings.ReplaceAll(usage, "\n", "\n    \t"))
				if f.DefValue != "" {
					_, _ = fmt.Fprintf(os.Stderr, " (default %q)", f.DefValue)
				}
				_, _ = fmt.Fprintf(os.Stderr, "\n")
			}
			fmt.Fprintf(os.Stderr, "\n")
		}

		printFlags("Global Suite Flags (Apply to all tests)", global)
		for _, g := range groups {
			printFlags(g.title, byPrefix[g.prefix])
		}

		fmt.Fprintf(os.Stderr, "Standard Go Test Flags (e.g. -v, -run, -timeout, -short):\n")
		fmt.Fprintf(os.Stderr, "  Run 'go help testflag' for detailed documentation of standard flags.\n")
	}

	os.Exit(m.Run())
}
