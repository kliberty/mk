package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	inUse     []bool // inUse[i] is true while slot i ($nproc = i) is taken
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
// reserve waits for a free job slot, takes the lowest-numbered one and
// returns its number, which the recipe sees as $nproc.
func (s *scheduler) reserve() int {
	s.cond.L.Lock()
	for s.running >= s.allowed {
		s.cond.Wait()
	}
	slot := 0
	for s.inUse[slot] {
		slot++
	}
	s.inUse[slot] = true
	s.running++
	s.cond.L.Unlock()
	return slot
}

// Free up another subprocess to run.
// finish releases a slot taken by reserve.
func (s *scheduler) finish(slot int) {
	s.cond.L.Lock()
	s.inUse[slot] = false
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
			visit := prereqs[i].needsVisit(required)
			prereqs[i].mutex.Unlock()
			if visit {
				mkNode(g, prereqs[i], opts, required)
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
		if prereqs[i].needsVisit(required) {
			go mkNode(g, prereqs[i], opts, required)
			prereqs[i].listeners = append(prereqs[i].listeners, prereqstat)
			pending++
		} else if prereqs[i].status == nodeStatusStarted {
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
	}
	n.status = nodeStatusStarted
	n.checkedRequired = n.checkedRequired || required
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

	// Several rules may contribute prerequisites to a target, but at most one
	// of them has a recipe (ambiguous() enforces this). Use that rule's edge,
	// so a rule that only adds prerequisites can't hide the recipe. If no rule
	// has a recipe, use the last edge. Every edge has a rule (newedge never
	// creates one without).
	var prereqs []*node
	var e *edge
	for _, pe := range n.prereqs {
		if e == nil || e.r.recipe == "" {
			e = pe
		}
		if pe.v != nil {
			prereqs = append(prereqs, pe.v)
		}
	}

	prereqsRequired := required && (e.r.attributes.virtual || !n.exists || opts.forceIntermed)
	if mkNodePrereqs(g, prereqs, opts, prereqsRequired) == nodeStatusFailed {
		finalstatus = nodeStatusFailed
	}

	uptodate := true
	if e.r.attributes.virtual {
		// A virtual target has no timestamp, so it is always out of date,
		// unless a P program is there to compare it with its prerequisites
		// (as in Plan 9 mk).
		if len(e.r.command) > 0 && len(prereqs) > 0 {
			uptodate = !staleByProgram(n, e, prereqs, opts.explain)
		} else {
			if opts.explain && n.name != "" { // skip the root dummy node
				fmt.Fprintf(os.Stderr, "mk: %s is virtual\n", n.name)
			}
			uptodate = false
		}
	} else {
		n.updateTimestamp(opts.rebuildall)
		if !n.exists && required {
			if opts.explain {
				fmt.Fprintf(os.Stderr, "mk: %s does not exist\n", n.name)
			}
			uptodate = false
		} else if len(e.r.command) > 0 && (n.exists || required) {
			uptodate = !staleByProgram(n, e, prereqs, opts.explain)
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

			if err := dorecipe(n, e, opts, nproc); err != nil {
				finalstatus = nodeStatusFailed
				opts.failed.Store(true)
				msg := fmt.Sprintf("recipe for '%s' failed: %v", n.name, err)
				// D attribute: delete the target file when the recipe fails.
				if e.r.attributes.delFailed {
					if os.Remove(n.name) == nil {
						msg += fmt.Sprintf(", deleted '%s'", n.name)
					}
				}
				mkMsgMutex.Lock()
				mkPrintError(msg)
				mkMsgMutex.Unlock()
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
				sched.finish(nproc)
			}
		}
	} else if finalstatus != nodeStatusFailed {
		if opts.explain && uptodate {
			fmt.Fprintf(os.Stderr, "mk: %s is up to date\n", n.name)
		}
		finalstatus = nodeStatusNop
	}
}

// staleByProgram reports whether a P attribute's program says n is out of date
// with respect to any of its prerequisites. The program is run as
// "prog [args] target prereq" for each prerequisite in turn, and a non-zero
// exit means out of date. It uses the OS environment (not mk variables)
// because it is an external tool such as cmp -s, not a recipe.
func staleByProgram(n *node, e *edge, prereqs []*node, explain bool) bool {
	for _, p := range prereqs {
		args := append(append([]string{}, e.r.command[1:]...), n.name, p.name)
		if _, err := subprocess(e.r.command[0], args, os.Environ(), strings.NewReader(""), false); err != nil {
			// A non-zero exit means out of date; failing to run at all is an error.
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				mkError(fmt.Sprintf("P attribute program for %s: %v", n.name, err))
			}
			if explain {
				fmt.Fprintf(os.Stderr, "mk: %s out of date via %s (P attribute)\n", n.name, p.name)
			}
			return true
		}
	}
	return false
}

func mkError(msg string) {
	mkPrintError(msg)
	removeAllRecipeFiles()
	os.Exit(1)
}

func mkPrintError(msg string) {
	msg = "error: " + strings.TrimRight(msg, "\n")
	if color {
		msg = ansiTermRed + msg + ansiTermDefault
	}
	// Reset the colour before the newline: a newline printed while a colour
	// is active can leave that colour behind in the terminal.
	os.Stderr.WriteString(msg + "\n")
}

func mkPrintRecipe(target string, recipe string, quiet bool) {
	var b strings.Builder
	if !color {
		fmt.Fprintf(&b, "%s: ", target)
	} else {
		fmt.Fprintf(&b, "%s%s%s → %s",
			ansiTermBlue+ansiTermBright+ansiTermUnderline, target,
			ansiTermDefault, ansiTermBlue)
	}
	if quiet {
		if !color {
			b.WriteString("...")
		} else {
			b.WriteString("…")
		}
	} else {
		printIndented(&b, strings.TrimRight(recipe, "\n"), len(target)+3)
	}
	// Reset the colour before the final newline: a newline printed while a
	// colour is active can leave that colour behind in the terminal.
	if color {
		b.WriteString(ansiTermDefault)
	}
	b.WriteString("\n")

	mkMsgMutex.Lock()
	os.Stdout.WriteString(b.String())
	mkMsgMutex.Unlock()
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
	flag.BoolVar(&keepTempFiles, "keep-temp-files", false, "keep the temporary files of F recipes, for debugging")
	flag.BoolVar(&color, "color", isatty.IsTerminal(os.Stdout.Fd()), "turn color on/off")
	flag.StringVar(&defaultShell, "shell", "sh -e", "default shell to use if none are specified via $shell")
	// TODO(rjk): P9P mk command line compatability.
	flag.Parse()

	// Resolve parallelism: -p flag > $NPROC env > NumCPU
	pGiven := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "p" {
			pGiven = true
		}
	})
	if pGiven && sched.allowed < 1 {
		mkError(fmt.Sprintf("invalid -p value: %d (must be at least 1)", sched.allowed))
	}
	if !pGiven {
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
	sched.inUse = make([]bool, sched.allowed)

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

	rs := parse(string(input), mkfilepath, abspath, env)
	if quiet {
		for i := range rs.rules {
			rs.rules[i].attributes.quiet = true
		}
	}

	// Separate command-line variable overrides (VAR=value) from targets.
	var targets []string
	for _, arg := range flag.Args() {
		if i := strings.Index(arg, "="); i > 0 && isValidVarName(arg[:i]) {
			rs.vars[arg[:i]] = expand(arg[i+1:], rs.vars, true)
		} else {
			targets = append(targets, arg)
		}
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

