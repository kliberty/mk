package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mattn/go-isatty"
)

var (
	// True if messages should be printed with fancy colors.
	// By default, if the output stream is not the terminal, colors are disabled.
	color bool

	// Default shell to use if none specified via $shell.
	defaultShell string

	// Do not drop shell arguments when calling with no further arguments
	// This works around `sh -c commands...` being a thing, but allows the `rc -v commands...` argument-less flags
	dontDropArgs bool

	// Lock on standard out, messages don't get interleaved too much.
	mkMsgMutex sync.Mutex

	// sched controls parallel recipe execution.
	sched scheduler

	// The maximum number of times an rule may be applied.
	// This limits recursion of both meta- and non-meta-rules!
	// Maybe, this shouldn't affect meta-rules?!
	maxRuleCnt int = 1

	// Pretend this target was recently modified (-w flag).
	pretendModified string
)

// scheduler controls parallel recipe execution, limiting the number of
// concurrent subprocesses.
type scheduler struct {
	allowed   int
	running   int
	cond      *sync.Cond
	exclusive sync.Mutex
}

// buildOpts holds build-mode configuration that is constant throughout a build.
type buildOpts struct {
	vars           map[string][]string
	unexportedVars map[string]bool
	dryrun         bool
	keepgoing      bool
	touchmode      bool
	forceIntermed  bool
	explain        bool
	rebuildall     bool
	rebuildTargets map[string]bool
	failed         atomic.Bool
}

// Wait until there is an available subprocess slot.
// Returns the 0-based slot number assigned to this job.
func (s *scheduler) reserve() int {
	s.cond.L.Lock()
	for s.running >= s.allowed {
		s.cond.Wait()
	}
	slot := s.running
	s.running++
	s.cond.L.Unlock()
	return slot
}

// Free up another subprocess to run.
func (s *scheduler) finish() {
	s.cond.L.Lock()
	s.running--
	s.cond.Signal()
	s.cond.L.Unlock()
}

// Acquire exclusive access, waiting for all running subprocesses to finish.
func (s *scheduler) reserveExclusive() {
	s.exclusive.Lock()
	stolenSubprocs := 0
	s.cond.L.Lock()
	stolenSubprocs = s.allowed - s.running
	s.running = s.allowed
	for stolenSubprocs < s.allowed {
		s.cond.Wait()
		stolenSubprocs += s.allowed - s.running
		s.running = s.allowed
	}
}

func (s *scheduler) finishExclusive() {
	s.running = 0
	s.cond.Broadcast()
	s.cond.L.Unlock()
	s.exclusive.Unlock()
}

// Ansi color codes.
const (
	ansiTermDefault   = "\033[0m"
	// ansiTermBlack   = "\033[30m"
	ansiTermRed       = "\033[31m"
	// ansiTermGreen  = "\033[32m"
	// ansiTermYellow = "\033[33m"
	ansiTermBlue      = "\033[34m"
	// ansiTermMagenta = "\033[35m"
	ansiTermBright    = "\033[1m"
	ansiTermUnderline = "\033[4m"
)

// Build a node's prereqs. Block until completed.
func mkNodePrereqs(g *graph, prereqs []*node, opts *buildOpts, required bool) nodeStatus {
	// When limited to a single process, build sequentially to preserve order.
	if sched.allowed == 1 {
		status := nodeStatusDone
		for i := range prereqs {
			prereqs[i].mutex.Lock()
			switch prereqs[i].status {
			case nodeStatusReady, nodeStatusNop:
				prereqs[i].mutex.Unlock()
				mkNode(g, prereqs[i], opts, required)
			default:
				prereqs[i].mutex.Unlock()
			}
			prereqs[i].mutex.Lock()
			s := prereqs[i].status
			prereqs[i].mutex.Unlock()
			if s == nodeStatusFailed {
				status = nodeStatusFailed
			}
		}
		return status
	}

	prereqstat := make(chan nodeStatus)
	pending := 0

	// build prereqs that need building
	for i := range prereqs {
		prereqs[i].mutex.Lock()
		switch prereqs[i].status {
		case nodeStatusReady, nodeStatusNop:
			go mkNode(g, prereqs[i], opts, required)
			fallthrough
		case nodeStatusStarted:
			prereqs[i].listeners = append(prereqs[i].listeners, prereqstat)
			pending++
		}
		prereqs[i].mutex.Unlock()
	}

	// wait until all the prereqs are built
	status := nodeStatusDone
	for pending > 0 {
		s := <-prereqstat
		pending--
		if s == nodeStatusFailed {
			status = nodeStatusFailed
		}
	}
	return status
}

// Build a target in the graph.
//
// This selects an appropriate rule (edge) and builds all prerequisites
// concurrently.
//
// Args:
//
//	g: Graph in which the node lives.
//	n: Node to (possibly) build.
//	dryrun: Don't actually build anything, just pretend.
//	required: Avoid building this node, unless its prereqs are out of date.
func mkNode(g *graph, n *node, opts *buildOpts, required bool) {
	// try to claim on this node
	n.mutex.Lock()
	if n.status != nodeStatusReady && n.status != nodeStatusNop {
		n.mutex.Unlock()
		return
	} else {
		n.status = nodeStatusStarted
	}
	n.mutex.Unlock()

	// when finished, notify the listeners
	finalstatus := nodeStatusDone
	defer func() {
		n.mutex.Lock()
		n.status = finalstatus
		for i := range n.listeners {
			n.listeners[i] <- n.status
		}
		n.listeners = n.listeners[0:0]
		n.mutex.Unlock()
	}()

	// there's no rules.
	if len(n.prereqs) == 0 {
		if !(n.r != nil && (n.r.attributes.virtual || n.r.attributes.forcedTimestamp)) && !n.exists {
			wd, _ := os.Getwd()
			mkError(fmt.Sprintf("don't know how to make %s in %s\n", n.name, wd))
		}
		finalstatus = nodeStatusNop
		return
	}

	// There is exactly one rule among the edges (all edges share the same
	// rule pointer, set by applyrules; newedge never creates edges with r==nil).
	var prereqs []*node
	var e *edge
	for i := range n.prereqs {
		if n.prereqs[i].r != nil {
			e = n.prereqs[i]
		}
		if n.prereqs[i].v != nil {
			prereqs = append(prereqs, n.prereqs[i].v)
		}
	}

	prereqsRequired := required && (e.r.attributes.virtual || !n.exists || opts.forceIntermed)
	if mkNodePrereqs(g, prereqs, opts, prereqsRequired) == nodeStatusFailed {
		finalstatus = nodeStatusFailed
	}

	uptodate := true
	if !e.r.attributes.virtual {
		n.updateTimestamp(opts.rebuildall)
		if !n.exists && required {
			if opts.explain {
				fmt.Fprintf(os.Stderr, "mk: %s does not exist\n", n.name)
			}
			uptodate = false
		} else if len(e.r.command) > 0 && (n.exists || required) {
			// P attribute: use custom program for staleness checking.
			// Uses OS environment (not mk vars) because the program is an
			// external tool (e.g. cmp -s), not a recipe.
			for i := range prereqs {
				args := append(append([]string{}, e.r.command[1:]...), n.name, prereqs[i].name)
				_, ok := subprocess(e.r.command[0], args, os.Environ(), "", false)
				if !ok {
					if opts.explain {
						fmt.Fprintf(os.Stderr, "mk: %s out of date via %s (P attribute)\n", n.name, prereqs[i].name)
					}
					uptodate = false
					break
				}
			}
		} else if n.exists || required {
			for i := range prereqs {
				if n.t.Before(prereqs[i].t) {
					if opts.explain {
						fmt.Fprintf(os.Stderr, "mk: %s older than %s\n", n.name, prereqs[i].name)
					}
					uptodate = false
				} else if prereqs[i].status == nodeStatusDone {
					if opts.explain {
						fmt.Fprintf(os.Stderr, "mk: %s stale because %s was rebuilt\n", n.name, prereqs[i].name)
					}
					uptodate = false
				}
			}
		}
	} else {
		if opts.explain && n.name != "" { // skip the root dummy node
			fmt.Fprintf(os.Stderr, "mk: %s is virtual\n", n.name)
		}
		uptodate = false
	}

	_, isrebuildtarget := opts.rebuildTargets[n.name]
	if isrebuildtarget || opts.rebuildall {
		if opts.explain && uptodate {
			fmt.Fprintf(os.Stderr, "mk: %s forced by -a/-w flag\n", n.name)
		}
		uptodate = false
	}

	// make another pass on the prereqs, since we know we need them now
	if !uptodate {
		if mkNodePrereqs(g, prereqs, opts, true) == nodeStatusFailed {
			finalstatus = nodeStatusFailed
		}
	}

	// Without -k, stop building when any recipe has failed.
	if !opts.keepgoing && opts.failed.Load() {
		finalstatus = nodeStatusFailed
	}

	// execute the recipe, unless the prereqs failed
	if !uptodate && finalstatus != nodeStatusFailed && len(e.r.recipe) > 0 {
		if opts.touchmode && !e.r.attributes.virtual {
			// Touch mode: update the target's timestamp without running the recipe.
			now := time.Now()
			if !n.exists {
				f, err := os.Create(n.name)
				if err == nil {
					f.Close()
				}
			} else {
				os.Chtimes(n.name, now, now)
			}
			n.updateTimestamp(opts.rebuildall)
		} else if !opts.touchmode {
			var nproc int
			if e.r.attributes.exclusive {
				sched.reserveExclusive()
				nproc = 0
			} else {
				nproc = sched.reserve()
			}

			if !dorecipe(n, e, opts, nproc) {
				finalstatus = nodeStatusFailed
				opts.failed.Store(true)
				// D attribute: delete the target file when the recipe fails.
				if e.r.attributes.delFailed {
					os.Remove(n.name)
				}
			}
			// U attribute: force timestamp so dependents see the target as updated
			// even if the recipe didn't modify the file.
			if finalstatus != nodeStatusFailed && e.r.attributes.update {
				n.t = time.Now()
			} else {
				n.updateTimestamp(opts.rebuildall)
			}

			if e.r.attributes.exclusive {
				sched.finishExclusive()
			} else {
				sched.finish()
			}
		}
	} else if finalstatus != nodeStatusFailed {
		if opts.explain && uptodate && !e.r.attributes.virtual {
			fmt.Fprintf(os.Stderr, "mk: %s is up to date\n", n.name)
		}
		finalstatus = nodeStatusNop
	}
}

func mkError(msg string) {
	mkPrintError(msg)
	os.Exit(1)
}

func mkPrintError(msg string) {
	if color {
		os.Stderr.WriteString(ansiTermRed)
	}
	fmt.Fprintf(os.Stderr, "error: %s\n", msg)
	if color {
		os.Stderr.WriteString(ansiTermDefault)
	}
}

func mkPrintRecipe(target string, recipe string, quiet bool) {
	mkMsgMutex.Lock()
	if !color {
		fmt.Printf("%s: ", target)
	} else {
		fmt.Printf("%s%s%s → %s",
			ansiTermBlue+ansiTermBright+ansiTermUnderline, target,
			ansiTermDefault, ansiTermBlue)
	}
	if quiet {
		if !color {
			fmt.Println("...")
		} else {
			fmt.Println("…")
		}
	} else {
		printIndented(os.Stdout, recipe, len(target)+3)
		if len(recipe) == 0 {
			os.Stdout.WriteString("\n")
		}
	}
	if color {
		os.Stdout.WriteString(ansiTermDefault)
	}
	mkMsgMutex.Unlock()
}

// parseArgs parses the flags in args. It returns the flag arguments as
// written (including flag values, such as the file in "-f file") and the
// remaining arguments (targets and var=value assignments). As with GNU
// tools, flags may appear anywhere: before, after or between the other
// arguments. "--" ends flag parsing, so everything after it is a remaining
// argument, even if it looks like a flag; "--" itself is in neither list.
func parseArgs(fs *flag.FlagSet, args []string) (flags, rest []string) {
	for {
		// Parse consumes leading flags, and a "--", and stops at the first
		// other argument.
		fs.Parse(args)
		remaining := fs.Args()
		consumed := args[:len(args)-len(remaining)]
		if n := len(consumed); n > 0 && consumed[n-1] == "--" {
			return append(flags, consumed[:n-1]...), append(rest, remaining...)
		}
		flags = append(flags, consumed...)
		if len(remaining) == 0 {
			return flags, rest
		}
		rest = append(rest, remaining[0])
		args = remaining[1:]
	}
}

// localFlags are the flags that describe only the current run (which mkfile
// to read, which directory to run in, which target to pretend changed), so
// they are left out of $MKFLAGS. GNU make likewise does not pass on its -f,
// -C and -W.
var localFlags = map[string]bool{"f": true, "C": true, "w": true}

// propagatedFlags returns flagArgs (flag arguments as returned by parseArgs)
// without the localFlags and their values. These flags all take a value,
// given either in the same argument ("-f=file") or in the next ("-f file").
func propagatedFlags(flagArgs []string) []string {
	var out []string
	for i := 0; i < len(flagArgs); i++ {
		name := strings.TrimLeft(flagArgs[i], "-")
		name, _, hasValue := strings.Cut(name, "=")
		if localFlags[name] {
			if !hasValue {
				i++ // skip the separate value
			}
			continue
		}
		out = append(out, flagArgs[i])
	}
	return out
}

func main() {
	var directory string
	var mkfilepath string
	var interactive bool
	var shallowrebuild bool
	var quiet bool
	var dotOutput bool
	var opts buildOpts

	flag.StringVar(&directory, "C", "", "directory to change in to")
	flag.StringVar(&mkfilepath, "f", "mkfile", "use the given file as mkfile")
	flag.BoolVar(&opts.dryrun, "n", false, "print commands without actually executing")
	flag.BoolVar(&opts.touchmode, "t", false, "touch targets instead of executing recipes")
	flag.BoolVar(&shallowrebuild, "r", false, "force building of just targets")
	flag.BoolVar(&opts.rebuildall, "a", false, "force building of all dependencies")
	flag.BoolVar(&opts.keepgoing, "k", false, "continue building after errors")
	flag.StringVar(&pretendModified, "w", "", "pretend `target` was recently modified")
	flag.IntVar(&sched.allowed, "p", -1, "maximum number of jobs to execute in parallel")
	flag.IntVar(&maxRuleCnt, "l", 1, "maximum number of times a specific rule can be applied (recursion)")
	flag.BoolVar(&interactive, "I", false, "prompt before executing rules")
	flag.BoolVar(&opts.forceIntermed, "i", false, "force rebuild of missing intermediates")
	flag.BoolVar(&opts.explain, "e", false, "explain why targets are out of date")
	flag.BoolVar(&quiet, "q", false, "don't print recipes before executing them")
	flag.BoolVar(&dotOutput, "dot", false, "print dependency graph in graphviz dot format and exit")
	flag.BoolVar(&color, "color", isatty.IsTerminal(os.Stdout.Fd()), "turn color on/off")
	flag.StringVar(&defaultShell, "shell", "sh -e", "default shell to use if none are specified via $shell")
	flag.BoolVar(&dontDropArgs, "F", false, "don't drop shell arguments when no further arguments are specified")
	// TODO(rjk): P9P mk command line compatability.
	flagArgs, args := parseArgs(flag.CommandLine, os.Args[1:])

	// Resolve parallelism: -p flag > $NPROC env > NumCPU
	if sched.allowed < 0 {
		if nproc := os.Getenv("NPROC"); nproc != "" {
			if n, err := strconv.Atoi(nproc); err == nil && n > 0 {
				sched.allowed = n
			} else {
				mkError(fmt.Sprintf("invalid $NPROC value: %q", nproc))
			}
		} else {
			sched.allowed = runtime.NumCPU()
		}
	}
	sched.cond = sync.NewCond(&sync.Mutex{})

	if directory != "" {
		err := os.Chdir(directory)
		if err != nil {
			mkError(fmt.Sprintf("changing directory to `%s' failed", directory))
		}
	}

	mkfile, err := os.Open(mkfilepath)
	if err != nil {
		mkError("no mkfile found")
	}
	input, _ := io.ReadAll(mkfile) // ReadAll on a regular file; error is not practically reachable.
	mkfile.Close()

	abspath, _ := filepath.Abs(mkfilepath)

	env := make(map[string][]string)
	for _, elem := range os.Environ() {
		vals := strings.SplitN(elem, "=", 2)
		env[vals[0]] = append(env[vals[0]], vals[1])
	}

	// Separate command-line variable overrides (VAR=value) from targets.
	var assignments, targets []string
	for _, arg := range args {
		if i := strings.Index(arg, "="); i > 0 && isValidVarName(arg[:i]) {
			assignments = append(assignments, arg)
		} else {
			targets = append(targets, arg)
		}
	}

	// As in Plan 9 mk, $MKFLAGS holds the flags followed by the assignments
	// and $MKARGS the targets given on the command line. They are set before
	// the mkfile is read, so its assignments can use them, and replace any
	// values inherited from a parent mk.
	env["MKFLAGS"] = append(propagatedFlags(flagArgs), assignments...)
	env["MKARGS"] = append([]string{}, targets...)

	rs := parse(string(input), mkfilepath, abspath, env)
	if quiet {
		for i := range rs.rules {
			rs.rules[i].attributes.quiet = true
		}
	}

	// Apply command-line variable overrides.
	for _, arg := range assignments {
		i := strings.Index(arg, "=")
		rs.vars[arg[:i]] = expand(arg[i+1:], rs.vars, true)
	}

	// build the first non-meta rule in the makefile, if none are given explicitly
	if len(targets) == 0 {
		for i := range rs.rules {
			if !rs.rules[i].ismeta {
				for j := range rs.rules[i].targets {
					targets = append(targets, rs.rules[i].targets[j].spat)
				}
				break
			}
		}
	}

	if len(targets) == 0 {
		fmt.Println("mk: nothing to mk")
		return
	}

	opts.rebuildTargets = make(map[string]bool)
	if shallowrebuild {
		for i := range targets {
			opts.rebuildTargets[targets[i]] = true
		}
	}

	// Create a dummy virtual rule that depends on every target
	root := rule{}
	root.targets = []pattern{{false, "", nil}}
	root.attributes = attribSet{virtual: true}
	root.prereqs = targets
	rs.add(root)

	if dotOutput {
		g := buildgraph(rs, "", opts.rebuildall)
		g.visualize(os.Stdout)
		return
	}

	opts.vars = rs.vars
	opts.unexportedVars = rs.unexportedVars

	if interactive {
		g := buildgraph(rs, "", opts.rebuildall)
		// Preview: dry-run to show what would be built.
		savedDryrun := opts.dryrun
		opts.dryrun = true
		mkNode(g, g.root, &opts, true)
		opts.dryrun = savedDryrun
		fmt.Print("Proceed? ")
		in := bufio.NewReader(os.Stdin)
		for {
			c, _, err := in.ReadRune()
			if err != nil {
				return
			} else if strings.ContainsRune(" \n\t\r", c) {
				continue
			} else if c == 'y' {
				break
			} else {
				return
			}
		}
	}

	g := buildgraph(rs, "", opts.rebuildall)

	// -w flag: pretend a target was recently modified.
	if pretendModified != "" {
		if n, ok := g.nodes[pretendModified]; ok {
			n.t = time.Now()
			n.flags |= nodeFlagProbable | nodeFlagForcedTime
		}
	}

	mkNode(g, g.root, &opts, true)
	if g.root.status == nodeStatusFailed {
		os.Exit(1)
	}
}

