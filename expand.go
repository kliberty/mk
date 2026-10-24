// String substitution and expansion.

package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Expand a word. This includes substituting variables and handling quotes.
func expand(input string, vars map[string][]string, expandBackticks bool) []string {
	parts := make([]string, 0)
	expanded := ""
	var i, j int
	for i = 0; i < len(input); {
		j = strings.IndexAny(input[i:], "\"'`$\\")

		if j < 0 {
			expanded += input[i:]
			break
		}
		j += i

		expanded += input[i:j]
		c, w := utf8.DecodeRuneInString(input[j:])
		i = j + w

		var off int
		var out string
		switch c {
		case '\\':
			out, off = expandEscape(input[i:])
			expanded += out

		case '"':
			out, off = expandDoubleQuoted(input[i:], vars, expandBackticks)
			expanded += out

		case '\'':
			out, off = expandSingleQuoted(input[i:])
			expanded += out

		case '`':
			if expandBackticks {
				var outparts []string
				outparts, off = expandBackQuoted(input[i:], vars)
				if len(outparts) > 0 {
					outparts[0] = expanded + outparts[0]
					expanded = outparts[len(outparts)-1]
					parts = append(parts, outparts[:len(outparts)-1]...)
				}
			} else {
				// Re-include the backtick and emit the rest literally.
				expanded += "`" + input[i:]
				off = len(input) - i
			}

		case '$':
			var outparts []string
			outparts, off = expandSigil(input[i:], vars)
			if len(outparts) > 0 {
				firstpart := expanded + outparts[0]
				if len(outparts) > 1 {
					parts = append(parts, firstpart)
					if len(outparts) > 2 {
						parts = append(parts, outparts[1:len(outparts)-1]...)
					}
					expanded = outparts[len(outparts)-1]
				} else {
					expanded = firstpart
				}
			}
		}

		i += off
	}

	if len(expanded) > 0 {
		parts = append(parts, expanded)
	}

	return parts
}

// Expand following a '\\'
func expandEscape(input string) (string, int) {
	c, w := utf8.DecodeRuneInString(input)
	if c == '\t' || c == ' ' {
		return string(c), w
	}
	if c == '\n' {
		return "", w
	}
	return "\\" + string(c), w
}

// Expand a double quoted string starting after a '\"'
func expandDoubleQuoted(input string, vars map[string][]string, expandBackticks bool) (string, int) {
	// find the first non-escaped "
	i := 0
	j := 0
	for {
		j = strings.IndexAny(input[i:], "\"\\")
		if j < 0 {
			break
		}
		j += i

		c, w := utf8.DecodeRuneInString(input[j:])
		i = j + w

		if c == '"' {
			return strings.Join(expand(input[:j], vars, expandBackticks), " "), i
		}

		if c == '\\' {
			if i < len(input) {
				_, w := utf8.DecodeRuneInString(input[i:])
				i += w
			} else {
				break
			}
		}
	}

	return input, len(input)
}

// Expand a single quoted string starting after a '\''.
func expandSingleQuoted(input string) (string, int) {
	j := strings.Index(input, "'")
	if j < 0 {
		return input, len(input)
	}
	return input[:j], (j + 1)
}

var expandSigilNamelistPattern = regexp.MustCompile(`^\s*([^:]+)\s*:\s*([^%]*)%([^=]*)\s*=\s*([^%]*)%([^%]*)\s*`)

// Expand something starting with at '$'.
func expandSigil(input string, vars map[string][]string) ([]string, int) {
	c, w := utf8.DecodeRuneInString(input)
	var offset int
	var varname string
	if c == '$' { // escaping of "$" with "$$"
		return []string{"$"}, w
	} else if c == '{' { // match bracketed expansions: ${foo}, or ${foo:a%b=c%d}
		j := strings.IndexRune(input[w:], '}')
		if j < 0 {
			return []string{"$" + input}, len(input)
		}
		varname = input[w : w+j]
		offset = w + j + 1

		// is this a namelist?
		mat := expandSigilNamelistPattern.FindStringSubmatch(varname)
		if mat != nil && isValidVarName(mat[1]) {
			// ${varname:a%b=c%d}
			varname = mat[1]
			a, b, c, d := mat[2], mat[3], mat[4], mat[5]
			values, ok := vars[varname]
			if !ok {
				return []string{}, offset
			}

			pat := regexp.MustCompile(strings.Join([]string{`^\Q`, a, `\E(.*)\Q`, b, `\E$`}, ""))
			expandedValues := make([]string, 0, len(values))
			for _, value := range values {
				valueMatch := pat.FindStringSubmatch(value)
				if valueMatch != nil {
					expandedValues = append(expandedValues, expand(strings.Join([]string{c, valueMatch[1], d}, ""), vars, false)...)
				} else {
					// What case is this?
					expandedValues = append(expandedValues, value)
				}
			}

			return expandedValues, offset
		}
	} else { // bare variables: $foo
		// try to match a variable name
		i := 0
		j := i
		for j < len(input) {
			c, w = utf8.DecodeRuneInString(input[j:])
			if !(isalpha(c) || c == '_' || (j > i && isdigit(c))) {
				break
			}
			j += w
		}

		if j > i {
			varname = input[i:j]
			offset = j
		} else {
			offset = j + 1
			return []string{"$" + input[:offset]}, offset
		}
	}

	if isValidVarName(varname) {
		varvals, ok := vars[varname]
		if ok {
			return varvals, offset
		}

		// Find the subsitution in the environment.
		if varval, ok := os.LookupEnv(varname); ok {
			return []string{varval}, offset
		}

		return []string{"$" + input[:offset]}, offset
	}

	// Find the subsitution in the environment.
	if varval, ok := os.LookupEnv(varname); ok {
		return []string{varval}, offset
	}

	return []string{"$" + input}, len(input)
}



// Find and expand all sigils in a recipe, producing a flat string.
func expandRecipeSigils(input string, vars map[string][]string) string {
	expanded := ""
	for i := 0; i < len(input); {
		off := strings.IndexAny(input[i:], "$\\")
		if off < 0 {
			expanded += input[i:]
			break
		}
		expanded += input[i : i+off]
		i += off

		c, w := utf8.DecodeRuneInString(input[i:])
		if c == '$' {
			i += w
			ex, k := expandSigil(input[i:], vars)
			expanded += strings.Join(ex, " ")
			i += k
		} else if c == '\\' {
			i += w
			if i >= len(input) {
				expanded += "\\"
				break
			}
			c, w := utf8.DecodeRuneInString(input[i:])
			if c == '$' {
				expanded += "$"
			} else {
				expanded += "\\" + string(c)
			}
			i += w
		}
	}

	return expanded
}

// Expand all unescaped '%' and '&' characters with the stem.
func expandSuffixes(input string, stem string) string {
	expanded := make([]byte, 0)
	for i := 0; i < len(input); {
		j := strings.IndexAny(input[i:], "\\%&")
		if j < 0 {
			expanded = append(expanded, input[i:]...)
			break
		}
		j += i // make j absolute

		c, w := utf8.DecodeRuneInString(input[j:])
		expanded = append(expanded, input[i:j]...)
		if c == '%' || c == '&' {
			expanded = append(expanded, stem...)
			i = j + w
		} else { // c == '\\'
			j += w // advance past backslash
			if j < len(input) {
				c, w = utf8.DecodeRuneInString(input[j:])
				if c == '%' || c == '&' {
					expanded = append(expanded, byte(c))
				} else {
					expanded = append(expanded, '\\')
					expanded = append(expanded, input[j:j+w]...)
				}
				i = j + w
			} else {
				// trailing backslash: emit literally
				expanded = append(expanded, '\\')
				break
			}
		}
	}

	return string(expanded)
}

// Expand a backtick quoted string, by executing the contents.
// Supports sh-style `cmd` and rc-style `{cmd}.
func expandBackQuoted(input string, vars map[string][]string) ([]string, int) {
	// Determine style: rc-style starts with '{', sh-style uses closing '`'
	var cmd string
	var consumed int
	if len(input) > 0 && input[0] == '{' {
		// rc-style: `{cmd}
		j := strings.Index(input[1:], "}")
		if j < 0 {
			return []string{input}, len(input)
		}
		cmd = input[1 : 1+j]
		consumed = 1 + j + 1 // '{' + cmd + '}'
	} else {
		// sh-style: `cmd`
		j := strings.Index(input, "`")
		if j < 0 {
			return []string{input}, len(input)
		}
		cmd = input[:j]
		consumed = j + 1 // cmd + '`'
	}

	env := os.Environ()
	for key, values := range vars {
		env = append(env, key+"="+strings.Join(values, " "))
	}

	// TODO - might have $shell available by now, but maybe not?
	// It's not populated, regardless

	shellWords := vars["shell"]
	if len(shellWords) == 0 {
		shellWords = []string{defaultShell}
	}
	shell, shellargs := shellCommand(shellWords)

	output, err := subprocess(shell, shellargs, env, strings.NewReader(cmd), true)
	if err != nil {
		mkError(fmt.Sprintf("backtick expansion failed: `%s`: %v", cmd, err))
	}

	parts := make([]string, 0)
	_, tokens := lexWords(output)
	for t := range tokens {
		parts = append(parts, t.val)
	}

	return parts, consumed
}

// shellCommand splits a shell specification into the program to run and its
// arguments. The first word may hold several space-separated fields, as in the
// -shell flag ("sh -e") or a quoted S attribute (S"sh -e"); the remaining
// words are passed through unchanged. Flags are never dropped: recipes are fed
// to the shell on standard input, so the specification should not include -c.
func shellCommand(words []string) (string, []string) {
	var fields []string
	if len(words) > 0 {
		fields = strings.Fields(words[0])
	}
	if len(fields) == 0 {
		mkError("empty shell command")
	}
	args := append(fields[1:len(fields):len(fields)], words[1:]...)
	return fields[0], args
}
