// Package scope decides which keys a sync is allowed to touch.
//
// A sync has to agree with itself on both ends about what it is looking
// at, so what is out of scope is not copied, not compared, and not
// removed. The rules here are ordered, and the last one that matches a
// key makes the decision, which is what lets an operator write "-exclude
// everything, then bring back these two directories" and have it read
// the way it sounds.
package scope

import (
	"fmt"
	"io/fs"
	"regexp"
	"strings"
)

// Rule is one pattern and the decision it makes for a key it covers.
type Rule struct {
	// Pattern is the pattern as it was written, kept so that a rule
	// which did nothing can be named back to whoever typed it.
	Pattern string

	// Include is true when a covered key is kept rather than dropped.
	Include bool

	// re matches a whole key. Every pattern also covers everything below
	// what it names, so a directory and its contents are one decision.
	re *regexp.Regexp

	// literal is the part of the pattern before the first wildcard, used
	// to decide whether a rule can still say something about the contents
	// of a directory that looks excluded.
	literal string
}

// Match reports whether the rule covers key.
func (r Rule) Match(key string) bool {
	if r.re == nil {
		return false
	}

	return r.re.MatchString(key)
}

// String renders the rule the way it would be written on the command
// line, so that messages can show a rule instead of describing it.
func (r Rule) String() string {
	if r.Include {
		return "-include " + r.Pattern
	}

	return "-exclude " + r.Pattern
}

// Rules is an ordered set of rules. An empty set keeps everything, which
// is what a run without any rules asked for.
type Rules []Rule

// Keep reports whether key is in scope. The last rule covering the key
// decides; a key no rule covers is kept.
func (rs Rules) Keep(key string) bool {
	keep := true
	for _, r := range rs {
		if r.Match(key) {
			keep = r.Include
		}
	}

	return keep
}

// Descend reports whether a walk has to look inside dir, given that dir
// itself came out excluded. A later rule could still bring something
// back from under it, and a directory that is not looked at cannot have
// anything brought back from it.
//
// Pruning is only ever an optimization: a walk that descended every
// directory would end up with the same set of keys.
func (rs Rules) Descend(dir string) bool {
	if rs.Keep(dir) {
		return true
	}

	// Only the rules after the one that excluded dir can overrule it.
	decided := 0
	for i, r := range rs {
		if r.Match(dir) {
			decided = i + 1
		}
	}

	for _, r := range rs[decided:] {
		if r.reachesBelow(dir) {
			return true
		}
	}

	return false
}

// reachesBelow reports whether the rule could cover something inside
// dir. A rule whose pattern is still literal up to this point answers
// the question exactly; once wildcards have started the answer is that
// anything is possible, and the walk keeps going.
func (r Rule) reachesBelow(dir string) bool {
	if r.literal == "" {
		return true
	}

	return r.literal == dir || strings.HasPrefix(r.literal, dir+"/")
}

// Uncovered returns the include rules that match none of keys. An
// include that covers nothing is almost always a mistyped name, and with
// everything else excluded the run would then do nothing at all, which
// is indistinguishable from a healthy idle run.
func (rs Rules) Uncovered(keys []string) []Rule {
	var missing []Rule

	for _, r := range rs {
		if !r.Include {
			continue
		}

		found := false
		for _, key := range keys {
			if r.Match(key) {
				found = true
				break
			}
		}

		if !found {
			missing = append(missing, r)
		}
	}

	return missing
}

// Parse compiles one pattern. Patterns are named relative to the root of
// the tree, with forward slashes, and are not keys: a pattern may end in
// a slash, which means the same thing as leaving it off, and a leading
// "./" is accepted and ignored.
//
// A "*" covers one path segment, "**" covers anything including slashes,
// "?" covers one character other than a slash, and a backslash quotes
// the character after it. Whatever a pattern names, it also names
// everything below it, so "-include docs" and "-include docs/" are the
// same rule and both bring the directory and its contents.
func Parse(pattern string, include bool) (Rule, error) {
	p := strings.TrimPrefix(pattern, "./")
	p = strings.TrimSuffix(p, "/")
	if p == "" || p == "." || !fs.ValidPath(p) {
		return Rule{}, fmt.Errorf("%q does not name something inside a tree: use a path relative to its root, without a leading slash or a .. segment", pattern)
	}

	re, literal, err := compile(p)
	if err != nil {
		return Rule{}, fmt.Errorf("%q is not a usable pattern: %s", pattern, err)
	}

	return Rule{Pattern: pattern, Include: include, re: re, literal: literal}, nil
}

// MustParse compiles a pattern and panics when it is not usable, which
// suits patterns written in source rather than typed by a person.
// Quote escapes the characters a pattern would otherwise read as wildcards, so
// that a rule covers exactly the name given and everything below it. Names a
// program keeps for itself, such as the file it records its own state in, are
// quoted: a star in such a name should reach that one file and not the tree.
func Quote(name string) string {
	var out strings.Builder

	for i := 0; i < len(name); i++ {
		if name[i] == '*' || name[i] == '?' || name[i] == '\\' {
			out.WriteByte('\\')
		}

		out.WriteByte(name[i])
	}

	return out.String()
}

// MustParse compiles a pattern and panics when it is not usable, which

func MustParse(pattern string, include bool) Rule {
	r, err := Parse(pattern, include)
	if err != nil {
		panic(err)
	}

	return r
}

// compile turns a pattern into a regular expression anchored at both
// ends, and returns the literal text leading up to the first wildcard.
func compile(pattern string) (*regexp.Regexp, string, error) {
	var (
		out     strings.Builder
		literal string
		wild    bool
	)

	// write adds plain text to the expression, and to the literal prefix
	// until the first wildcard makes further text uncertain.
	write := func(text string) {
		out.WriteString(regexp.QuoteMeta(text))
		if !wild {
			literal += text
		}
	}

	out.WriteString("^")

	// Leading segments may be absent altogether, so "**/x" reaches both
	// "x" and "a/x".
	start := 0
	if rest := starRun(pattern, 0); rest > 1 && rest < len(pattern) && pattern[rest] == '/' {
		out.WriteString(`(?:.*/)?`)
		wild = true
		start = rest + 1
	}

	for i := start; i < len(pattern); {
		switch c := pattern[i]; c {
		case '\\':
			if i+1 >= len(pattern) {
				return nil, "", fmt.Errorf("ends with a backslash")
			}

			write(pattern[i+1 : i+2])
			i += 2

		case '/':
			// A run of stars that fills a path segment on its own crosses
			// separators, so "/**" also covers the directory above it and
			// "/**/" may stand for any number of segments.
			if end := starRun(pattern, i+1); end > i+1 {
				switch {
				case end == len(pattern):
					out.WriteString(`(?:/.*)?`)
					wild = true
					i = end
					continue
				case pattern[end] == '/':
					out.WriteString(`/(?:.*/)?`)
					wild = true
					i = end + 1
					continue
				}
			}

			write("/")
			i++

		case '*':
			end := starRun(pattern, i)
			if end-i > 1 {
				out.WriteString(".*")
			} else {
				out.WriteString("[^/]*")
			}
			wild = true
			i = end

		case '?':
			out.WriteString("[^/]")
			wild = true
			i++

		default:
			write(string(c))
			i++
		}
	}

	// Naming a thing also names what is under it, which is why "-include
	// docs" brings the whole directory along and why a lone "*" covers
	// the tree rather than only its top level.
	out.WriteString(`(?:/.*)?$`)

	re, err := regexp.Compile(out.String())
	if err != nil {
		return nil, "", err
	}

	return re, literal, nil
}

// starRun returns the index just past a run of "*" starting at i, or i
// when there is no such run.
func starRun(pattern string, i int) int {
	for i < len(pattern) && pattern[i] == '*' {
		i++
	}

	return i
}

// RuleFlag lets a flag register patterns in one shared ordered list, so
// that the order of interleaved -exclude and -include arguments
// survives to the decision.
type RuleFlag struct {
	// Rules is the list each pattern is appended to.
	Rules *Rules

	// Include is the decision this flag records.
	Include bool
}

// Set implements flag.Value.
func (f *RuleFlag) Set(value string) error {
	rule, err := Parse(value, f.Include)
	if err != nil {
		return err
	}

	if f.Rules == nil {
		return fmt.Errorf("no rule list to add %s to", value)
	}

	*f.Rules = append(*f.Rules, rule)

	return nil
}

// String implements flag.Value.
func (f *RuleFlag) String() string {
	if f.Rules == nil {
		return ""
	}

	parts := make([]string, 0, len(*f.Rules))
	for _, r := range *f.Rules {
		parts = append(parts, r.String())
	}

	return strings.Join(parts, " ")
}
