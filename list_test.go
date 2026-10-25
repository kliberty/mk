package main

import (
	"bytes"
	"testing"
)

// The terminal form of -list: aligned sections with descriptions.
func TestListPretty(t *testing.T) {
	const mkfile = `CC ?= gcc ## C compiler
PREFIX = /usr/local ## Install location
OPT ?= -O2
LONG = aaaaaaaaaa bbbbbbbbbb cccccccccc dddddddddd

all:V: prog ## Build everything

prog: main.o ## The program
	cc -o prog main.o

## A whole-line comment is not a description, even with ##.
test:V: prog
	./prog --test

%.o: %.c ## Compile a C file
	cc -c $stem.c

hello-%:V:
	echo hello $stem

prog: config.h
`
	env := map[string][]string{"OPT": {"-O3"}, "PATH": {"/bin"}}
	overrides := map[string]bool{"PREFIX": true}
	env["PREFIX"] = []string{"/opt"}
	rs := parseWithOverrides(mkfile, "mkfile", "/mkfile", env, overrides)

	var buf bytes.Buffer
	writeList(&buf, rs, true)
	want := `Targets:
  all   Build everything (default)
  prog  The program
  test
Patterns:
  %.o      Compile a C file
  hello-%
Variables (set with mk NAME=value; ?= ones also from the environment):
  CC ?= gcc                          C compiler
  PREFIX = /opt                      Install location [command line]
  OPT ?= -O3                         [environment]
  LONG = aaaaaaaaaa bbbbbbbbbb ccc…
`
	if got := buf.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
