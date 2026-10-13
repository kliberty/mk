// Various function for dealing with recipes.

package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
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
			if strings.IndexRune(" \t\n", c) >= 0 {
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

// Execute a recipe.
func dorecipe(target string, u *node, e *edge, dryrun bool) bool {
	vars := make(map[string][]string)
	vars["target"] = []string{target}
	if e.r.ismeta {
		if e.r.attributes.regex {
			for i := range e.matches {
				vars[fmt.Sprintf("stem%d", i)] = e.matches[i : i+1]
			}
		} else {
			vars["stem"] = []string{e.stem}
		}
	}

	// TODO: other variables to set
	// alltargets
	// newprereq

	prereqs := make([]string, 0)
	for i := range u.prereqs {
		if u.prereqs[i].r == e.r && u.prereqs[i].v != nil {
			prereqs = append(prereqs, u.prereqs[i].v.name)
			vars[fmt.Sprintf("prereq%d", i+1)] = []string{u.prereqs[i].v.name}
		}
	}
	vars["prereq"] = prereqs

	// Setup the shell in vars.
	sh, args := expandShell(defaultShell, []string{})
	if len(e.r.shell) > 0 {
		sh, args = expandShell(e.r.shell[0], e.r.shell[1:])
	}
	vars["shell"] = append([]string{sh}, args...)
	// Merge and construct the execution environment for this recipe.
	for k, v := range GlobalMkState {
		if _, ok := vars[k]; !ok {
			vars[k] = v
		} else {
			// log.Println("Skipping var: ", k)
		}
	}
	// for k, v := range vars {
	// 	fmt.Println("vars:", k, "=", v)
	// }
	// Build the command.
	// log.Println("input before:", e.r.recipe)
	input := expandRecipeSigils(e.r.recipe, vars)
	// log.Println("input after :", input)

	mkPrintRecipe(target, input, e.r.attributes.quiet)
	if dryrun {
		return true
	}


	// "\x01" is a magic constant that Plan9 rc uses to separate elements in an array.
	// TODO(rjk): Do the right thing for other shells that have arrays.
	env := os.Environ()
	// env := make([]string, 0)
	for k, v := range vars {
		env = append(env, k+"="+strings.Join(v, "\x01"))
	}

	_, success := subprocess(
		sh,
		args,
		env,
		input,
		false)

	return success
}

// Execute a subprocess (typically a recipe).
//
// Args:
//   program: Program path or name located in PATH
//   input: String piped into the program's stdin
//   capture_out: If true, capture and return the program's stdout rather than echoing it.
//
// Returns
//   (output, success)
//   output is an empty string of catputer_out is false, or the collected output from the profram is true.
//
//   success is true if the exit code was 0 and false otherwise
//
func subprocess(program string,
	args []string,
	env []string,
	input string,
	capture_out bool) (string, bool) {
	program_path, err := exec.LookPath(program)
	if err != nil {
		log.Fatal(err)
	}

	proc_args := []string{program}
	proc_args = append(proc_args, args...)

	stdin_pipe_read, stdin_pipe_write, err := os.Pipe()
	if err != nil {
		log.Fatal(err)
	}

	attr := os.ProcAttr{Env: env, Files: []*os.File{stdin_pipe_read, os.Stdout, os.Stderr}}

	output := make([]byte, 0)
	capture_done := make(chan bool)
	if capture_out {
		stdout_pipe_read, stdout_pipe_write, err := os.Pipe()
		if err != nil {
			log.Fatal(err)
		}

		attr.Files[1] = stdout_pipe_write

		go func() {
			buf := make([]byte, 1024)
			for {
				n, err := stdout_pipe_read.Read(buf)

				if err == io.EOF && n == 0 {
					break
				} else if err != nil {
					log.Fatal(err)
				}

				output = append(output, buf[:n]...)
			}

			capture_done <- true
		}()
	}

	proc, err := os.StartProcess(program_path, proc_args, &attr)
	if err != nil {
		log.Fatal(err)
	}

	go func() {
		_, err := stdin_pipe_write.WriteString(input)
		if err != nil {
			log.Fatal(err)
		}

		err = stdin_pipe_write.Close()
		if err != nil {
			log.Fatal(err)
		}
	}()

	state, err := proc.Wait()

	if attr.Files[1] != os.Stdout {
		attr.Files[1].Close()
	}

	if err != nil {
		log.Fatal(err)
	}

	// wait until stdout copying in finished
	if capture_out {
		<-capture_done
	}

	return string(output), state.Success()
}
