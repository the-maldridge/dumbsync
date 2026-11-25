package scope

import (
	"reflect"
	"strings"
	"testing"
)

func TestPatterns(t *testing.T) {
	cases := []struct {
		pattern string
		key     string
		want    bool
	}{
		// A lone star covers the tree: whatever a pattern names, it also
		// names everything below it.
		{"*", "x", true},
		{"*", "a/x", true},
		{"*", "a/b/c", true},

		// A star inside a pattern stops at the separator, so depth has to
		// be asked for.
		{"*.tmp", "x.tmp", true},
		{"*.tmp", "a/x.tmp", false},
		{"**/*.tmp", "x.tmp", true},
		{"**/*.tmp", "a/b.tmp", true},
		{"*", "x", true},

		// A question mark is one character of one segment, taken with that
		// entry's contents.
		{"?", "x", true},
		{"?", "xy", false},
		{"?", "a/b", true},

		// Naming a directory names what is under it, with or without the
		// trailing slash.
		{"docs", "docs", true},
		{"docs", "docs/a", true},
		{"docs", "docs/a/b", true},
		{"docs", "other", false},
		{"docs/", "docs/a/b", true},
		{"docs/", "other", false},

		// Patterns are anchored at the root, so a name lower down needs
		// the depth spelled out.
		{"vendor", "vendor", true},
		{"vendor", "src/vendor/x", false},
		{"src/vendor", "src/vendor/x.c", true},
		{"**/node_modules/**", "node_modules/x", true},
		{"**/node_modules/**", "app/node_modules/x/index.js", true},

		// ** standing on its own in a segment also covers the directory it
		// sits in.
		{"foo/bar/**", "foo/bar", true},
		{"foo/bar/**", "foo/bar/x", true},
		{"foo/bar/**", "foo/baz", false},

		// A backslash quotes the character after it.
		{`\*`, "*", true},
		{`\*`, "x", false},
		{`a\*b`, "a*b", true},
		{`a\*b`, "axb", false},
	}

	for _, c := range cases {
		rule, err := Parse(c.pattern, true)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.pattern, err)
		}

		if got := rule.Match(c.key); got != c.want {
			t.Errorf("%q covers %q = %v, want %v (regexp %s)", c.pattern, c.key, got, c.want, rule.re)
		}
	}
}

func TestWithoutRulesEverythingIsInScope(t *testing.T) {
	var rules Rules
	for _, key := range []string{"a", "b/c", "d/e/f"} {
		if !rules.Keep(key) {
			t.Errorf("%q is out of scope with no rules", key)
		}
	}
}

func TestLastMatchingRuleDecides(t *testing.T) {
	rules := parseAll(t,
		"-exclude *",
		"-include foo/",
		"-include bar/",
	)

	inScope := []string{"foo/a", "foo/deep/b", "bar/c"}
	out := []string{"other/d", "a", "foobar/e"}

	for _, key := range inScope {
		if !rules.Keep(key) {
			t.Errorf("%q excluded, want it back from an include that came after the exclude", key)
		}
	}

	for _, key := range out {
		if rules.Keep(key) {
			t.Errorf("%q in scope, want only foo and bar", key)
		}
	}

	// Reordered, the broad exclude is the last word, which is the price of
	// ordering meaning something.
	flattened := parseAll(t,
		"-include foo/",
		"-exclude *",
	)

	for _, key := range append([]string{"foo/a"}, inScope...) {
		if flattened.Keep(key) {
			t.Errorf("%q in scope after a trailing exclude everything", key)
		}
	}
}

func TestLaterRulesCanSaveADirectoryFromPruning(t *testing.T) {
	cases := []struct {
		rules   []string
		dir     string
		descend bool
		why     string
	}{
		{
			rules:   []string{"-exclude **", "-include foo/"},
			dir:     "foo",
			descend: true,
			why:     "foo itself is back in scope",
		},
		{
			rules:   []string{"-exclude **", "-include foo/"},
			dir:     "other",
			descend: false,
			why:     "nothing can come back from a directory no rule includes",
		},
		{
			rules:   []string{"-exclude *", "-include deep/nested/x"},
			dir:     "deep",
			descend: true,
			why:     "the include names something below this directory",
		},
		{
			rules:   []string{"-exclude *", "-include deep/nested/x"},
			dir:     "sibling",
			descend: false,
			why:     "the named file is not below this directory",
		},
		{
			rules:   []string{"-exclude **", "-include **/vendor/**"},
			dir:     "a",
			descend: true,
			why:     "a pattern that starts wild could match anything below",
		},
		{
			rules:   []string{"-include a", "-exclude **"},
			dir:     "a",
			descend: false,
			why:     "the exclude came last, so nothing is left to overrule it",
		},
	}

	for _, c := range cases {
		rules := parseAll(t, c.rules...)

		if got := Rules(rules).Descend(c.dir); got != c.descend {
			t.Errorf("Descend(%q) = %v, want %v: %s", c.dir, got, c.descend, c.why)
		}
	}
}

func TestUncoveredIncludesAreNamed(t *testing.T) {
	rules := parseAll(t,
		"-exclude **",
		"-include foo/",
		"-include nope/",
	)

	missing := rules.Uncovered([]string{"foo/a", "other/b"})
	if len(missing) != 1 || missing[0].Pattern != "nope/" {
		t.Fatalf("uncovered = %v, want only the include that names nothing", missing)
	}

	// Nothing at all in scope is reported as loudly as one bad name among
	// several good ones.
	if got := rules.Uncovered(nil); len(got) != 2 {
		t.Errorf("uncovered with no keys = %v, want both includes", got)
	}

	// An exclude covering nothing is not a mistake worth reporting.
	if got := parseAll(t, "-exclude gone/").Uncovered([]string{"a"}); got != nil {
		t.Errorf("uncovered reported an exclude: %v", got)
	}
}

func TestParseAcceptsPathsAndRejectsNonsense(t *testing.T) {
	for _, pattern := range []string{"foo", "foo/", "./foo", "**", "*", "a/b", `a\*b`, "a b", "foo.bar"} {
		if _, err := Parse(pattern, true); err != nil {
			t.Errorf("Parse(%q): %v", pattern, err)
		}
	}

	for _, pattern := range []string{"", "/", "/foo", "../x", "a/../b", "..", ".", "./", "a//b", "foo//", `foo\`} {
		if rule, err := Parse(pattern, true); err == nil {
			t.Errorf("Parse(%q) accepted a pattern that cannot name anything inside a tree: %v", pattern, rule)
		}
	}
}

func TestRulesCollectInArgumentOrder(t *testing.T) {
	var rules Rules
	exclude := &RuleFlag{Rules: &rules, Include: false}
	include := &RuleFlag{Rules: &rules, Include: true}

	for _, step := range []struct {
		flag    *RuleFlag
		pattern string
	}{
		{exclude, "*"},
		{include, "foo/"},
		{exclude, "foo/secret"},
		{include, "foo/secret/public"},
	} {
		if err := step.flag.Set(step.pattern); err != nil {
			t.Fatalf("Set(%q): %v", step.pattern, err)
		}
	}

	if len(rules) != 4 {
		t.Fatalf("collected %d rules, want 4 in the order given", len(rules))
	}

	if rules.Keep("foo/secret/public") != true || rules.Keep("foo/secret") != false || rules.Keep("other") != false {
		t.Errorf("decisions did not follow the order given: %v", rules)
	}

	// A rejected pattern changes nothing about what was already collected.
	before := len(rules)
	if err := include.Set("/absolute"); err == nil {
		t.Error("an absolute pattern was accepted")
	}
	if len(rules) != before {
		t.Errorf("a rejected pattern still joined the list: %v", rules)
	}
}

func TestRuleStringNamesItself(t *testing.T) {
	rules := parseAll(t, "-exclude *", "-include foo/")

	want := []string{"-exclude *", "-include foo/"}
	got := []string{rules[0].String(), rules[1].String()}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rules rendered as %v, want %v", got, want)
	}
}

// parseAll compiles rules written the way they would be given on a
// command line, so that a test reads like an invocation and the ordering
// under test is the ordering a person would type.
func parseAll(t *testing.T, specs ...string) Rules {
	t.Helper()

	out := make(Rules, 0, len(specs))
	for _, spec := range specs {
		var (
			pattern string
			include bool
		)

		switch {
		case strings.HasPrefix(spec, "-include "):
			pattern, include = strings.TrimPrefix(spec, "-include "), true
		case strings.HasPrefix(spec, "-exclude "):
			pattern = strings.TrimPrefix(spec, "-exclude ")
		default:
			t.Fatalf("%q is neither an -include nor an -exclude", spec)
		}

		rule, err := Parse(pattern, include)
		if err != nil {
			t.Fatalf("%s: %v", spec, err)
		}

		out = append(out, rule)
	}

	return out
}
