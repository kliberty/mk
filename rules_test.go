package main

import (
	"fmt"
	"reflect"
	"regexp"
	"testing"
)

func TestMatchMetaRule(t *testing.T) {
	regex, err := regexp.Compile(`^data/processed/(\d+)/mapping_k10.bam.bai$`)
	if err != nil {
		t.Error("Failure to compile regex pattern")
	}
	pat := pattern{false, "data/processed/(\\d+)/mapping_k10.bam.bai", regex}
	matches := pat.match("data/processed/12345/mapping_k10.bam.bai")
	want := []string{"data/processed/12345/mapping_k10.bam.bai", "12345"}
	if matches[0] != want[0] || matches[1] != want[1] {
		t.Error("failed to match regular expression")
	}
}

func TestMatchSimplePattern(t *testing.T) {
	// Simple string pattern matching (no regex)
	pat := pattern{spat: "foo"}

	// Exact match returns empty slice
	matches := pat.match("foo")
	if matches == nil {
		t.Error("expected match for 'foo', got nil")
	}
	if len(matches) != 0 {
		t.Errorf("expected empty slice for exact match, got %v", matches)
	}

	// Non-match returns nil
	matches = pat.match("bar")
	if matches != nil {
		t.Errorf("expected nil for non-match, got %v", matches)
	}
}

func TestEquivRecipe(t *testing.T) {
	tests := []struct {
		name string
		a, b *rule
		want bool
	}{
		{
			name: "identical",
			a:    &rule{recipe: "build", shell: []string{"sh"}},
			b:    &rule{recipe: "build", shell: []string{"sh"}},
			want: true,
		},
		{
			name: "different_recipe",
			a:    &rule{recipe: "build", shell: []string{"sh"}},
			b:    &rule{recipe: "other", shell: []string{"sh"}},
			want: false,
		},
		{
			name: "different_shell",
			a:    &rule{recipe: "build", shell: []string{"sh"}},
			b:    &rule{recipe: "build", shell: []string{"bash"}},
			want: false,
		},
		{
			name: "different_shell_args",
			a:    &rule{recipe: "build", shell: []string{"sh"}},
			b:    &rule{recipe: "build", shell: []string{"sh", "-e"}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.a.equivRecipe(tt.b)
			if got != tt.want {
				t.Errorf("equivRecipe() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExecuteAssignment(t *testing.T) {
	tests := []struct {
		name   string
		tokens []token
		want   map[string][]string
		err    bool
	}{
		{
			name: "non_word_in_value",
			tokens: []token{
				{typ: tokenWord, val: "x"},
				{typ: tokenWord, val: "a"},
				{typ: tokenColon, val: ":"},
				{typ: tokenWord, val: "b"},
			},
			want: map[string][]string{"x": {"a:b"}},
		},
		{
			name: "value_starts_with_non_word",
			tokens: []token{
				{typ: tokenWord, val: "y"},
				{typ: tokenColon, val: ":"},
				{typ: tokenWord, val: "val"},
			},
			want: map[string][]string{"y": {":val"}},
		},
		{
			name: "invalid_varname",
			tokens: []token{
				{typ: tokenWord, val: "1bad"},
			},
			err: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs := &ruleSet{
				vars:           map[string][]string{},
				rules:          []rule{},
				targetrules:    map[string][]int{},
				unexportedVars: map[string]bool{},
			}
			err := rs.executeAssignment(tt.tokens, false, false)
			if tt.err {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for k, want := range tt.want {
				if !reflect.DeepEqual(rs.vars[k], want) {
					t.Errorf("vars[%q] = %v, want %v", k, rs.vars[k], want)
				}
			}
		})
	}
}

func TestIsValidVarName(t *testing.T) {
	tests := []struct {
		name string
		input string
		want bool
	}{
		{"alpha", "foo", true},
		{"underscore_prefix", "_bar", true},
		{"alpha_digit", "a1", true},
		{"digit_prefix", "1bad", false},
		{"hyphen", "a-b", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidVarName(tt.input)
			if got != tt.want {
				t.Errorf("isValidVarName(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func ruleAttributesNotSet(t *testing.T, r *rule) {
	t.Helper()
	noAttributes := attribSet{
		delFailed:      false,
		nonstop:        false,
		forcedTimestamp: false,
		nonvirtual:     false,
		quiet:          false,
		regex:          false,
		update:         false,
		virtual:        false,
		exclusive:      false,
	}
	if r.attributes != noAttributes {
		t.Error("rule attributes are not all false", r.attributes)
	}
}

func TestParseOneRuleWithAttributeLocalFiles(t *testing.T) {
	tests := []struct {
		attr  string
		field string
	}{
		{"D", "delFailed"},
		{"E", "nonstop"},
		{"n", "nonvirtual"},
		{"N", "forcedTimestamp"},
		{"Q", "quiet"},
		{"R", "regex"},
		{"U", "update"},
		{"V", "virtual"},
		{"X", "exclusive"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("attr_%s", tt.attr), func(t *testing.T) {
			mkfileAsString := fmt.Sprintf("somefile.txt:%s: a_prereq.csv\n\techo $target", tt.attr)
			env := make(map[string][]string)
			ruleSet := parse(mkfileAsString, "mkfile", "/mkfile", env)
			if len(ruleSet.rules) != 1 {
				t.Fatalf("expected 1 rule, got %d", len(ruleSet.rules))
			}
			rule := ruleSet.rules[0]
			if len(rule.prereqs) != 1 {
				t.Fatalf("expected 1 prereq, got %d", len(rule.prereqs))
			}
			if rule.prereqs[0] != "a_prereq.csv" {
				t.Errorf("prereq = %q, want %q", rule.prereqs[0], "a_prereq.csv")
			}
			f := reflect.ValueOf(rule.attributes).FieldByName(tt.field)
			if !f.IsValid() {
				t.Fatalf("no field %q in attribSet", tt.field)
			}
			if !f.Bool() {
				t.Errorf("attribute %s (%s) not set", tt.attr, tt.field)
			}
		})
	}
}
