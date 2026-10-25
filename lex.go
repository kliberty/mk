package main

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

type tokenType int

const eof rune = '\000'

// Rune's that cannot be part of a bare (unquoted) string.
// Backtick must be here so lexBareWord transitions to lexBackQuotedWord
// instead of consuming it as a bare character. This matches Plan 9 mk's
// WORDCHR which excludes backtick from word characters.
const nonBareRunes = " \t\n\r\\=:#'\"$`"

// Return true if the string contains whitespace only.
func onlyWhitespace(s string) bool {
	return strings.TrimLeft(s, " \t\r\n") == ""
}

const (
	tokenError tokenType = iota
	tokenNewline
	tokenWord
	tokenPipeInclude
	tokenRedirInclude
	tokenColon
	tokenAssign
	tokenRecipe
	tokenAssignU    // =U= unexport assignment
	tokenAssignCond // ?= assignment, made only if the variable is unset
	tokenDoc        // "## text" after a rule or assignment: its description
)

func (typ tokenType) String() string {
	switch typ {
	case tokenError:
		return "[Error]"
	case tokenNewline:
		return "[Newline]"
	case tokenWord:
		return "[Word]"
	case tokenPipeInclude:
		return "[PipeInclude]"
	case tokenRedirInclude:
		return "[RedirInclude]"
	case tokenColon:
		return "[Colon]"
	case tokenAssign:
		return "[Assign]"
	case tokenRecipe:
		return "[Recipe]"
	case tokenAssignU:
		return "[AssignU]"
	case tokenAssignCond:
		return "[AssignCond]"
	case tokenDoc:
		return "[Doc]"
	}
	return "[MysteryToken]"
}

type token struct {
	typ  tokenType // token type
	val  string    // token string
	line int       // line where it was found
	col  int       // column on which the token began
}

func (t *token) String() string {
	switch t.typ {
	case tokenError:
		return t.val
	case tokenNewline:
		return "\\n"
	}

	return t.val
}

type lexer struct {
	input     string     // input string to be lexed
	output    chan token // channel on which tokens are sent
	start     int        // token beginning
	startcol  int        // column on which the token begins
	pos       int        // position within input
	line      int        // line within input
	col       int        // column within input
	errmsg    string     // set to an appropriate error message when necessary
	indented  bool       // true if the only whitespace so far on this line
	barewords bool       // lex only a sequence of words
}

// A lexerStateFun is simultaneously the the state of the lexer and the next
// action the lexer will perform.
type lexerStateFun func(*lexer) lexerStateFun

func (l *lexer) lexerror(what string) {
	if l.errmsg == "" {
		l.errmsg = what
	}
	l.emit(tokenError)
}

// Return the nth character without advancing.
func (l *lexer) peekN(n int) (c rune) {
	pos := l.pos
	var width int
	i := 0
	for ; i <= n && pos < len(l.input); i++ {
		c, width = utf8.DecodeRuneInString(l.input[pos:])
		pos += width
	}

	if i <= n {
		return eof
	}

	return
}

// Return the next character without advancing.
func (l *lexer) peek() rune {
	return l.peekN(0)
}

// Consume and return the next character in the lexer input.
func (l *lexer) next() rune {
	if l.pos >= len(l.input) {
		return eof
	}
	c, width := utf8.DecodeRuneInString(l.input[l.pos:])
	l.pos += width

	if c == '\n' {
		l.col = 0
		l.line += 1
		l.indented = true
	} else {
		l.col += 1
		if !strings.ContainsRune(" \t", c) {
			l.indented = false
		}
	}

	return c
}

// Skip and return the next character in the lexer input.
func (l *lexer) skip() {
	l.next()
	l.start = l.pos
	l.startcol = l.col
}

func (l *lexer) emit(typ tokenType) {
	l.output <- token{typ, l.input[l.start:l.pos], l.line, l.startcol}
	l.start = l.pos
	l.startcol = 0
}

// Consume the next run if it is in the given string.
func (l *lexer) accept(valid string) bool {
	if strings.ContainsRune(valid, l.peek()) {
		l.next()
		return true
	}
	return false
}

// Consume characters from the valid string until the next is not.
func (l *lexer) acceptRun(valid string) int {
	prevpos := l.pos
	for strings.ContainsRune(valid, l.peek()) {
		l.next()
	}
	return l.pos - prevpos
}

// Accept until something from the given string is encountered.
func (l *lexer) acceptUntil(invalid string) {
	for l.pos < len(l.input) && !strings.ContainsRune(invalid, l.peek()) {
		l.next()
	}

	if l.peek() == eof {
		l.lexerror(fmt.Sprintf("end of file encountered while looking for one of: %s", invalid))
	}
}

// Accept until something from the given string is encountered, or the end of the file.
func (l *lexer) acceptUntilOrEof(invalid string) {
	for l.pos < len(l.input) && !strings.ContainsRune(invalid, l.peek()) {
		l.next()
	}
}

// Skip characters from the valid string until the next is not.
func (l *lexer) skipRun(valid string) int {
	prevpos := l.pos
	for strings.ContainsRune(valid, l.peek()) {
		l.skip()
	}
	return l.pos - prevpos
}

// Skip until something from the given string is encountered.
func (l *lexer) skipUntil(invalid string) {
	for l.pos < len(l.input) && !strings.ContainsRune(invalid, l.peek()) {
		l.skip()
	}

	if l.peek() == eof {
		l.lexerror(fmt.Sprintf("end of file encountered while looking for one of: %s", invalid))
	}
}

// Start a new lexer to lex the given input.
func lex(input string) (*lexer, chan token) {
	// Files without a trailing newline are considered to have one.
	if len(input) > 0 && input[len(input)-1] != '\n' {
		input = input + "\n"
	}

	l := &lexer{input: input, output: make(chan token), line: 1, col: 0, indented: true}
	go l.run()
	return l, l.output
}

func lexWords(input string) (*lexer, chan token) {
	l := &lexer{input: input, output: make(chan token), line: 1, col: 0, indented: true, barewords: true}
	go l.run()
	return l, l.output
}

func (l *lexer) run() {
	for state := lexTopLevel; state != nil; {
		state = state(l)
	}
	close(l.output)
}

func lexTopLevel(l *lexer) lexerStateFun {
	for {
		l.skipRun(" \t\r")
		// emit a newline token if we are ending a non-empty line.
		if l.peek() == '\n' && !l.indented {
			if l.barewords {
				l.skip()
			} else {
				l.next()
				l.emit(tokenNewline)
			}
		}
		l.skipRun(" \t\r\n")

		if l.peek() == '\\' && l.peekN(1) == '\n' {
			l.next()
			l.next()
			l.indented = false
		} else {
			break
		}
	}

	if l.indented && l.col > 0 {
		return lexRecipe
	}

	c := l.peek()
	switch c {
	case eof:
		return nil
	case '#':
		return lexComment
	case '<':
		return lexInclude
	case ':':
		return lexColon
	case '=':
		return lexAssign
	case '?':
		if l.peekN(1) == '=' {
			return lexAssignCond
		}
	case '"':
		return lexDoubleQuotedWord
	case '\'':
		return lexSingleQuotedWord
	case '`':
		return lexBackQuotedWord
	}

	return lexBareWord
}

func lexColon(l *lexer) lexerStateFun {
	l.next()
	l.emit(tokenColon)
	return lexTopLevel
}

func lexAssignCond(l *lexer) lexerStateFun {
	l.next() // consume '?'
	l.next() // consume '='
	l.emit(tokenAssignCond)
	return lexTopLevel
}

func lexAssign(l *lexer) lexerStateFun {
	l.next() // consume '='
	if l.peek() == 'U' && l.peekN(1) == '=' {
		l.next() // consume 'U'
		l.next() // consume '='
		l.emit(tokenAssignU)
	} else {
		l.emit(tokenAssign)
	}
	return lexTopLevel
}

func lexComment(l *lexer) lexerStateFun {
	// A comment on a line of its own is like a blank line: it ends no
	// statement, so a rule's recipe may still follow it.
	wholeLine := l.indented
	defer func() {
		if wholeLine {
			l.indented = true
		}
	}()
	l.skip() // '#'
	// "##" after something else on the line is that line's description.
	doc := !wholeLine && !l.barewords && l.peek() == '#'
	line, textStart := l.line, l.pos+1
	for {
		l.skipUntil("\n")
		// Check if the character before the newline is a backslash.
		// If so, the comment continues on the next line (Plan 9 convention).
		if l.pos > 0 && l.input[l.pos-1] == '\\' {
			l.skip() // skip the newline
			continue
		}
		break
	}
	if doc {
		text := l.input[textStart:l.pos]
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[:i] // only the first line of a continued comment
		}
		text = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text), "\\"))
		l.output <- token{tokenDoc, text, line, 0}
	}
	return lexTopLevel
}

func lexInclude(l *lexer) lexerStateFun {
	l.next() // '<'
	if l.accept("|") {
		l.emit(tokenPipeInclude)
	} else {
		l.emit(tokenRedirInclude)
	}
	return lexTopLevel
}

func lexDoubleQuotedWord(l *lexer) lexerStateFun {
	l.next() // '"'
	for l.peek() != '"' && l.peek() != eof {
		l.acceptUntil("\\\"")
		if l.accept("\\") {
			l.accept("\"")
		}
	}

	if l.peek() == eof {
		l.lexerror("end of file encountered while parsing a quoted string.")
	}

	l.next() // '"'
	return lexBareWord
}

func lexBackQuotedWord(l *lexer) lexerStateFun {
	l.next() // '`'
	if l.peek() == '{' {
		// rc-style: `{cmd}
		l.next() // '{'
		l.acceptUntil("}")
		l.next() // '}'
	} else {
		// sh-style: `cmd`
		l.acceptUntil("`")
		l.next() // '`'
	}
	return lexBareWord
}

func lexSingleQuotedWord(l *lexer) lexerStateFun {
	l.next() // '\''
	l.acceptUntil("'")
	l.next() // '\''
	return lexBareWord
}

func lexRecipe(l *lexer) lexerStateFun {
	var val strings.Builder
	segStart := l.start
	for {
		l.acceptUntilOrEof("\n")
		l.acceptRun(" \t\n\r")
		if l.indented && l.col > 0 {
			continue // another recipe line
		}
		// The next line starts in column 0. Lines commented out there with
		// "#" do not end the recipe if more of it follows: they become empty
		// lines, so the recipe's line numbers stay the same.
		if l.peek() != '#' {
			break
		}
		n, ok := commentedOutLines(l.input[l.pos:])
		if !ok {
			break
		}
		val.WriteString(l.input[segStart:l.pos])
		val.WriteString(strings.Repeat("\n", strings.Count(l.input[l.pos:l.pos+n], "\n")))
		for end := l.pos + n; l.pos < end; {
			l.next()
		}
		segStart = l.pos
	}
	val.WriteString(l.input[segStart:l.pos])
	recipe := trimTrailingBlankLines(val.String())

	if !onlyWhitespace(recipe) {
		l.output <- token{tokenRecipe, recipe, l.line, l.startcol}
		l.start = l.pos
		l.startcol = 0
	}
	return lexTopLevel
}

// trimTrailingBlankLines removes blank lines from the end of a recipe: they
// separate it from what follows rather than belong to it.
func trimTrailingBlankLines(s string) string {
	last := strings.LastIndexFunc(s, func(r rune) bool { return !unicode.IsSpace(r) })
	if last < 0 {
		return s
	}
	if nl := strings.IndexByte(s[last:], '\n'); nl >= 0 {
		return s[:last+nl+1]
	}
	return s
}

// commentedOutLines reports whether s, which starts at the beginning of a
// line, begins with lines commented out by a "#" in column 0 (possibly with
// blank lines among them) that are followed by more of a recipe: an indented,
// non-blank line. If so, it returns the length of the commented-out lines.
func commentedOutLines(s string) (int, bool) {
	n := 0
	for n < len(s) {
		line := s[n:]
		if i := strings.IndexByte(line, '\n'); i >= 0 {
			line = line[:i+1]
		} else {
			return 0, false // the input ends without more recipe
		}
		switch {
		case line[0] == '#':
		case strings.TrimSpace(line) == "": // blank
		case line[0] == ' ' || line[0] == '\t':
			return n, true
		default:
			return 0, false
		}
		n += len(line)
	}
	return 0, false
}

func lexBareWord(l *lexer) lexerStateFun {
	// Accept up to a non-bare rune, or "?=", which is an assignment
	// operator even with no space before it (as in "X?=1").
	for l.pos < len(l.input) && !strings.ContainsRune(nonBareRunes, l.peek()) &&
		!(l.peek() == '?' && l.peekN(1) == '=') {
		l.next()
	}
	if l.peek() == eof {
		l.lexerror(fmt.Sprintf("end of file encountered while looking for one of: %s", nonBareRunes))
	}
	c := l.peek()
	if c == '"' {
		return lexDoubleQuotedWord
	} else if c == '\'' {
		return lexSingleQuotedWord
	} else if c == '`' {
		return lexBackQuotedWord
	} else if c == '\\' {
		c1 := l.peekN(1)
		if c1 == '\n' || c1 == '\r' {
			if l.start < l.pos {
				l.emit(tokenWord)
			}
			l.skip()
			l.skip()
			return lexTopLevel
		} else {
			l.next()
			l.next()
			return lexBareWord
		}
	} else if c == '$' {
		c1 := l.peekN(1)
		if c1 == '{' {
			return lexBracketExpansion
		} else {
			l.next()
			return lexBareWord
		}
	}

	if l.start < l.pos {
		l.emit(tokenWord)
	}

	return lexTopLevel
}

func lexBracketExpansion(l *lexer) lexerStateFun {
	l.next() // '$'
	l.next() // '{'
	l.acceptUntil("}")
	l.next() // '}'
	return lexBareWord
}
