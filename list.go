// The -list flag: print the targets, patterns and variables of a mkfile.

package main

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// listEntry is one line of a -list section.
type listEntry struct {
	name string // the target, pattern, or "NAME = value"
	doc  string // its description, if any
}

// defaultTargets returns the targets mk builds when none are named: those
// of the first rule that is not a meta-rule.
func defaultTargets(rs *ruleSet) []string {
	var targets []string
	for i := range rs.rules {
		if !rs.rules[i].ismeta {
			for j := range rs.rules[i].targets {
				targets = append(targets, rs.rules[i].targets[j].spat)
			}
			break
		}
	}
	return targets
}

// writeList writes the targets, patterns and variables of rs. With pretty
// (for a terminal) it writes aligned sections with descriptions; otherwise a
// plain list, one name per line, for scripts and shell completion: the
// targets, then each variable as "NAME=". Patterns are left out of the plain
// list, since they are not names that can be typed as they are.
func writeList(w io.Writer, rs *ruleSet, pretty bool) {
	targets, patterns := listRules(rs)
	variables := listVariables(rs)

	if !pretty {
		for _, e := range targets {
			fmt.Fprintln(w, e.name)
		}
		for _, d := range uniqueVarNames(rs) {
			fmt.Fprintf(w, "%s=\n", d)
		}
		return
	}
	writeSection(w, "Targets:", targets)
	writeSection(w, "Patterns:", patterns)
	writeSection(w, "Variables (set with mk NAME=value; ?= ones also from the environment):", variables)
}

// listRules returns the targets and meta-rule patterns that can be built:
// those of rules with a recipe, and virtual ones. Each is listed once, in
// mkfile order, with the first description given for it in any rule.
func listRules(rs *ruleSet) (targets, patterns []listEntry) {
	docs := make(map[string]string)
	for i := range rs.rules {
		for _, t := range rs.rules[i].targets {
			if docs[t.spat] == "" {
				docs[t.spat] = rs.rules[i].doc
			}
		}
	}
	defaults := make(map[string]bool)
	for _, t := range defaultTargets(rs) {
		defaults[t] = true
	}

	seen := make(map[string]bool)
	for i := range rs.rules {
		r := &rs.rules[i]
		if r.recipe == "" && !r.attributes.virtual {
			continue // only adds prerequisites
		}
		for _, t := range r.targets {
			if seen[t.spat] {
				continue
			}
			seen[t.spat] = true
			e := listEntry{name: t.spat, doc: docs[t.spat]}
			if r.ismeta {
				patterns = append(patterns, e)
				continue
			}
			if defaults[t.spat] {
				e.doc = joinNonEmpty(e.doc, "(default)")
			}
			targets = append(targets, e)
		}
	}
	return targets, patterns
}

// uniqueVarNames returns the names of the variables the mkfile assigns, in
// order of first assignment.
func uniqueVarNames(rs *ruleSet) []string {
	var names []string
	seen := make(map[string]bool)
	for _, d := range rs.varDecls {
		if !seen[d.name] {
			seen[d.name] = true
			names = append(names, d.name)
		}
	}
	return names
}

// listVariables describes each variable the mkfile assigns as
// "NAME = value" or "NAME ?= value": ?= only if every assignment to it is
// ?=, since only then can the environment set it. The value is the current
// one; a note says if it came from the command line or the environment.
func listVariables(rs *ruleSet) []listEntry {
	var entries []listEntry
	for _, name := range uniqueVarNames(rs) {
		conditional, applied, doc := true, false, ""
		for _, d := range rs.varDecls {
			if d.name != name {
				continue
			}
			conditional = conditional && d.conditional
			applied = applied || d.applied
			if doc == "" {
				doc = d.doc
			}
		}
		op := "="
		if conditional {
			op = "?="
		}
		switch {
		case rs.overrides[name]:
			doc = joinNonEmpty(doc, "[command line]")
		case !applied:
			doc = joinNonEmpty(doc, "[environment]")
		}
		value := shorten(strings.Join(rs.vars[name], " "), 26)
		entries = append(entries, listEntry{
			name: strings.TrimSpace(fmt.Sprintf("%s %s %s", name, op, value)),
			doc:  doc,
		})
	}
	return entries
}

// writeSection writes a heading and its entries, with the descriptions
// aligned in a column. Nothing is written for an empty section.
func writeSection(w io.Writer, heading string, entries []listEntry) {
	if len(entries) == 0 {
		return
	}
	width := 0
	for _, e := range entries {
		width = max(width, utf8.RuneCountInString(e.name))
	}
	fmt.Fprintln(w, heading)
	for _, e := range entries {
		if e.doc == "" {
			fmt.Fprintf(w, "  %s\n", e.name)
			continue
		}
		pad := strings.Repeat(" ", width-utf8.RuneCountInString(e.name))
		fmt.Fprintf(w, "  %s%s  %s\n", e.name, pad, e.doc)
	}
}

// shorten limits s to n runes, ending it with "…" if it was cut.
func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func joinNonEmpty(a, b string) string {
	if a == "" {
		return b
	}
	return a + " " + b
}
