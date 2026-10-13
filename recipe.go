// Various functions for dealing with recipes.

package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"os/exec"
	"strings"
	"unicode/utf8"
)

// Try to unindent a recipe, so that it begins an column 0. (This is mainly for
// recipes in python, or other indentation-significant languages.)
func stripIndentation(s string, mincol int) string {
	// trim leading whitespace
	reader := bufio.NewReader(strings.NewReader(s))
	output := ""
	for {
		line, err := reader.ReadString('\n')
		col := 0
		i := 0
		for i < len(line) && col < mincol {
			c, w := utf8.DecodeRuneInString(line[i:])
			if strings.ContainsRune(" \t\n", c) {
				col += 1
				i += w
			} else {
				break
			}
		}
		output += line[i:]

		if err != nil {
			break
		}
	}

	return output
}

// Indent each line of a recipe.
func printIndented(out io.Writer, s string, ind int) {
	indentation := strings.Repeat(" ", ind)
	reader := bufio.NewReader(strings.NewReader(s))
	firstline := true
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			if !firstline {
				io.WriteString(out, indentation)
			}
			io.WriteString(out, line)
		}
		if err != nil {
			break
		}
		firstline = false
	}
}

// Execute a recipe. Returns nil on success, or the error the shell exited with.
func dorecipe(n *node, e *edge, opts *buildOpts, nproc int) error {
	vars := maps.Clone(opts.vars)
	vars["target"] = []string{n.name}
	vars["nproc"] = []string{fmt.Sprintf("%d", nproc)}
	vars["pid"] = []string{fmt.Sprintf("%d", os.Getpid())}
	if e.r.ismeta {
		if e.r.attributes.regex {
			for i := range e.matches {
				vars[fmt.Sprintf("stem%d", i)] = e.matches[i : i+1]
			}
		} else {
			vars["stem"] = []string{e.stem}
		}
	}

	// alltarget: all targets of the rule
	alltarget := make([]string, 0, len(e.r.targets))
	for i := range e.r.targets {
		alltarget = append(alltarget, e.r.targets[i].spat)
	}
	vars["alltarget"] = alltarget

	prereqs := make([]string, 0)
	newprereq := make([]string, 0)
	prereqCount := 0
	for i := range n.prereqs {
		if n.prereqs[i].v != nil {
			prereqs = append(prereqs, n.prereqs[i].v.name)
			prereqCount++
			vars[fmt.Sprintf("prereq%d", prereqCount)] = []string{n.prereqs[i].v.name}
			// newprereq: prereqs that were rebuilt (out of date)
			if n.prereqs[i].v.status == nodeStatusDone {
				newprereq = append(newprereq, n.prereqs[i].v.name)
			}
		}
	}
	vars["prereq"] = prereqs
	vars["newprereq"] = newprereq
	// newmember: archive member names from newprereq (not supported — no lib(member) syntax)
	vars["newmember"] = []string{}

	// Setup the shell in vars.
	shellWords := e.r.shell
	if len(shellWords) == 0 {
		shellWords = []string{defaultShell}
	}
	sh, args := shellCommand(shellWords)
	// E attribute: don't pass -e to the shell (allow recipe to continue on errors).
	// Allocate a new slice to avoid mutating e.r.shell's backing array.
	if e.r.attributes.nonstop {
		filtered := make([]string, 0, len(args))
		for _, a := range args {
			if a != "-e" {
				filtered = append(filtered, a)
			}
		}
		args = filtered
	}
	vars["shell"] = append([]string{sh}, args...)

	// Build the command.
	input := expandRecipeSigils(e.r.recipe, vars)

	mkPrintRecipe(n.name, input, e.r.attributes.quiet)
	if opts.dryrun {
		return nil
	}

	// Construct the execution environment for this recipe.
	env := os.Environ()
	for k, v := range vars {
		// =U= variables are available for mk expansion but not exported to recipes.
		if opts.unexportedVars[k] {
			continue
		}
		env = append(env, k+"="+strings.Join(v, " "))
	}

	_, err := subprocess(
		sh,
		args,
		env,
		input,
		false)

	return err
}

// Execute a subprocess (typically a recipe).
//
// Args:
//
//	program: Program path or name located in PATH
//	input: String piped into the program's stdin
//	captureOut: If true, capture and return the program's stdout rather than echoing it.
//
// Returns the captured stdout (empty if captureOut is false) and, if the
// program exited with a non-zero status, the *exec.ExitError describing it.
// Failing to start the program at all is fatal.
func subprocess(program string,
	args []string,
	env []string,
	input string,
	captureOut bool,
) (string, error) {
	cmd := exec.Command(program, args...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(input)
	cmd.Stderr = os.Stderr

	var stdout bytes.Buffer
	if captureOut {
		cmd.Stdout = &stdout
	} else {
		cmd.Stdout = os.Stdout
	}

	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			log.Fatal(err)
		}
	}
	return stdout.String(), err
}
