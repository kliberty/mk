// Mkfiles are parsed into ruleSets, which as the name suggests, are sets of
// rules with accompanying recipes, as well as assigned variables which are
// expanding when evaluating rules and recipes.

package main

import (
	"fmt"
	"regexp"
	"unicode/utf8"
)

type attribSet struct {
	delFailed       bool // delete targets when the recipe fails
	nonstop         bool // don't stop if the recipe fails
	forcedTimestamp bool // N: target need not exist and has no recipe
	nonvirtual      bool // a meta-rule that will only match files
	quiet           bool // don't print the recipe
	regex           bool // regular expression meta-rule
	update          bool // treat the targets as if they were updated
	virtual         bool // rule is virtual (does not match files)
	exclusive       bool // don't execute concurrently with any other rule
	recipeFile      bool // F: pass the recipe to the shell as a file, not on stdin
}

// Error parsing an attribute
type attribError struct {
	found rune
}

// target and rereq patterns
type pattern struct {
	issuffix bool           // is a suffix '%' rule, so we should define $stem.
	spat     string         // simple string pattern
	rpat     *regexp.Regexp // non-nil if this is a regexp pattern
}

// Match a pattern, returning an array of submatches,
// or nil if it doesn't match.
func (p *pattern) match(target string) []string {
	if p.rpat != nil {
		return p.rpat.FindStringSubmatch(target)
	}

	if target == p.spat {
		return make([]string, 0)
	}

	return nil
}

// A single rule.
type rule struct {
	targets    []pattern // non-empty array of targets
	attributes attribSet // rule attributes
	prereqs    []string  // possibly empty prerequesites
	shell      []string  // command used to execute the recipe
	recipe     string    // recipe source
	command    []string  // command attribute
	ismeta     bool      // is this a meta rule
	file       string    // file where the rule is defined
	line       int       // line number on which the rule is defined
}

// Identical rule headers: same targets, attributes, and prerequisites.
func (r *rule) sameHeader(r2 *rule) bool {
	if len(r.targets) != len(r2.targets) {
		return false
	}
	for i := range r.targets {
		if r.targets[i].spat != r2.targets[i].spat {
			return false
		}
	}
	if r.attributes != r2.attributes {
		return false
	}
	if len(r.prereqs) != len(r2.prereqs) {
		return false
	}
	for i := range r.prereqs {
		if r.prereqs[i] != r2.prereqs[i] {
			return false
		}
	}
	return true
}

// Equivalent recipes.
func (r *rule) equivRecipe(r2 *rule) bool {
	if r.recipe != r2.recipe {
		return false
	}

	if len(r.shell) != len(r2.shell) {
		return false
	}

	for i := range r.shell {
		if r.shell[i] != r2.shell[i] {
			return false
		}
	}

	return true
}

// A set of rules.
type ruleSet struct {
	vars  map[string][]string
	rules []rule
	// map a target to an array of indexes into rules
	targetrules    map[string][]int
	unexportedVars map[string]bool // variables marked with =U= (not exported to recipe env)
}

// Read attributes for an array of strings, updating the rule.
func (r *rule) parseAttribs(inputs []string) *attribError {
	for i := 0; i < len(inputs); i++ {
		input := inputs[i]
		pos := 0
		for pos < len(input) {
			c, w := utf8.DecodeRuneInString(input[pos:])
			switch c {
			case 'D':
				r.attributes.delFailed = true
			case 'E':
				r.attributes.nonstop = true
			case 'N':
				r.attributes.forcedTimestamp = true
			case 'n':
				r.attributes.nonvirtual = true
			case 'Q':
				r.attributes.quiet = true
			case 'R':
				r.attributes.regex = true
			case 'U':
				r.attributes.update = true
			case 'V':
				r.attributes.virtual = true
			case 'X':
				r.attributes.exclusive = true
			case 'P':
				if pos+w < len(input) {
					r.command = append(r.command, input[pos+w:])
				}
				r.command = append(r.command, inputs[i+1:]...)
				return nil

			case 'F':
				r.attributes.recipeFile = true
			case 'S':
				if pos+w < len(input) {
					r.shell = append(r.shell, input[pos+w:])
				}
				r.shell = append(r.shell, inputs[i+1:]...)
				return nil

			default:
				return &attribError{c}
			}

			pos += w
		}
	}

	return nil
}

// Add a rule to the rule set.
// If a rule with an identical header (targets, attributes, prerequisites) and
// a recipe already exists, the new rule replaces it (Plan 9 convention).
func (rs *ruleSet) add(r rule) {
	if r.recipe != "" {
		for i := range rs.rules {
			if rs.rules[i].recipe != "" && r.sameHeader(&rs.rules[i]) {
				rs.rules[i] = r
				return
			}
		}
	}
	rs.rules = append(rs.rules, r)
	k := len(rs.rules) - 1
	for i := range r.targets {
		if r.targets[i].rpat == nil {
			rs.targetrules[r.targets[i].spat] = append(rs.targetrules[r.targets[i].spat], k)
		}
	}
}

func isValidVarName(v string) bool {
	if len(v) == 0 {
		return false
	}
	for i := 0; i < len(v); {
		c, w := utf8.DecodeRuneInString(v[i:])
		if i == 0 && !(isalpha(c) || c == '_') {
			return false
		} else if !(isalnum(c) || c == '_') {
			return false
		}
		i += w
	}
	return true
}

func isdigit(c rune) bool {
	return '0' <= c && c <= '9'
}

func isalpha(c rune) bool {
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

func isalnum(c rune) bool {
	return isalpha(c) || isdigit(c)
}

type assignmentError struct {
	what  string
	where token
}

// Parse and execute assignment operation.
// If unexported is true, the variable is marked as not exported to recipe environments.
func (rs *ruleSet) executeAssignment(ts []token, unexported bool) *assignmentError {
	assignee := ts[0].val
	if !isValidVarName(assignee) {
		return &assignmentError{
			fmt.Sprintf("target of assignment is not a valid variable name: \"%s\"", assignee),
			ts[0],
		}
	}

	// interpret tokens in assignment context
	input := make([]string, 0)
	for i := 1; i < len(ts); i++ {
		if ts[i].typ != tokenWord || (i > 1 && ts[i-1].typ != tokenWord) {
			if len(input) == 0 {
				input = append(input, ts[i].val)
			} else {
				input[len(input)-1] += ts[i].val
			}
		} else {
			input = append(input, ts[i].val)
		}
	}

	// expanded variables
	vals := make([]string, 0)
	for i := 0; i < len(input); i++ {
		vals = append(vals, expand(input[i], rs.vars, true)...)
	}

	rs.vars[assignee] = vals

	if unexported {
		rs.unexportedVars[assignee] = true
	} else {
		delete(rs.unexportedVars, assignee)
	}

	return nil
}
