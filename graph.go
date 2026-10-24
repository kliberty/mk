package main

import (
	"fmt"
	"io"
	"os"
	"slices"
	"sync"
	"time"
)

// A dependency graph
type graph struct {
	root       *node            // the intial target's node
	nodes      map[string]*node // map targets to their nodes
	rebuildall bool             // -a flag: ignore timestamps, rebuild everything
}

// An edge in the graph.
type edge struct {
	v       *node    // node this edge directs to
	stem    string   // stem matched for meta-rule applications
	matches []string // regular expression matches
	togo    bool     // this edge is going to be pruned
	r       *rule
}

// Current status of a node in the build.
type nodeStatus int

const (
	nodeStatusReady nodeStatus = iota
	nodeStatusStarted
	nodeStatusNop
	nodeStatusDone
	nodeStatusFailed
)

type nodeFlag int

const (
	nodeFlagCycle      nodeFlag = 0x0002
	nodeFlagReady      nodeFlag = 0x0004
	nodeFlagProbable   nodeFlag = 0x0100
	nodeFlagVacuous    nodeFlag = 0x0200
	nodeFlagForcedTime nodeFlag = 0x0400 // timestamp set by -w; don't overwrite
)

// A node in the dependency graph
type node struct {
	r         *rule             // rule to be applied
	name      string            // target name
	t         time.Time         // file modification time
	exists    bool              // does a non-virtual target exist
	prereqs   []*edge           // prerequisite rules
	status    nodeStatus        // current state of the node in the build
	mutex     sync.Mutex        // exclusivity for the status variable
	listeners []chan nodeStatus // channels to notify of completion
	flags     nodeFlag          // bitwise combination of node flags

	// checkedRequired is set once the node has been checked with required
	// set. After that, a nodeStatusNop result is final.
	checkedRequired bool
}

// needsVisit reports whether mkNode should (re)visit the node. A node found
// to need no work (nodeStatusNop) is revisited only if it is now required and
// was not when it was last checked. Must be called with n.mutex held.
func (n *node) needsVisit(required bool) bool {
	switch n.status {
	case nodeStatusReady:
		return true
	case nodeStatusNop:
		return required && !n.checkedRequired
	}
	return false
}

// Update a node's timestamp and 'exists' flag.
func (n *node) updateTimestamp(rebuildall bool) {
	if n.flags&nodeFlagForcedTime != 0 {
		return
	}
	info, err := os.Stat(n.name)
	if err == nil {
		n.t = info.ModTime()
		n.exists = true
		n.flags |= nodeFlagProbable
	} else {
		n.t = time.Unix(0, 0)
		n.exists = false
	}

	if rebuildall {
		n.flags |= nodeFlagProbable
	}
}

// Create a new node
func (g *graph) newnode(name string) *node {
	n := &node{name: name}
	n.updateTimestamp(g.rebuildall)
	g.nodes[name] = n
	return n
}

// Print a graph in graphviz format.
func (g *graph) visualize(w io.Writer) {
	fmt.Fprintln(w, "digraph mk {")
	targets := make([]string, 0, len(g.nodes))
	for t := range g.nodes {
		targets = append(targets, t)
	}
	slices.Sort(targets)
	for _, t := range targets {
		n := g.nodes[t]
		for i := range n.prereqs {
			if n.prereqs[i].v != nil {
				fmt.Fprintf(w, "    \"%s\" -> \"%s\";\n", t, n.prereqs[i].v.name)
			}
		}
	}
	fmt.Fprintln(w, "}")
}

// Create a new arc.
func (n *node) newedge(v *node, r *rule) *edge {
	e := &edge{v: v, r: r}
	n.prereqs = append(n.prereqs, e)
	return e
}

// Create a dependency graph for the given target.
func buildgraph(rs *ruleSet, target string, rebuildall bool) *graph {
	g := &graph{nodes: make(map[string]*node), rebuildall: rebuildall}

	// keep track of how many times each rule is visited, to avoid cycles.
	rulecnt := make([]int, len(rs.rules))
	g.root = applyrules(rs, g, target, rulecnt)
	g.cyclecheck(g.root)
	g.root.flags |= nodeFlagProbable
	g.vacuous(g.root)
	g.ambiguous(g.root)

	return g
}

// Recursively match the given target to a rule in the rule set to construct the
// full graph.
func applyrules(rs *ruleSet, g *graph, target string, rulecnt []int) *node {
	n, ok := g.nodes[target]
	if ok {
		return n
	}
	n = g.newnode(target)

	// does the target match a concrete rule?

	ks, ok := rs.targetrules[target]
	if ok {
		for ki := range ks {
			k := ks[ki]
			// Use > (not >=) so multi-target rules like "a b: b" can match
			// each target once before the limit kicks in. Metarules use >=.
			if rulecnt[k] > maxRuleCnt {
				continue
			}

			r := &rs.rules[k]

			// skip meta-rules
			if r.ismeta {
				continue
			}

			// skip rules that have no effect (but keep N-attributed rules)
			if r.recipe == "" && len(r.prereqs) == 0 && !r.attributes.forcedTimestamp {
				continue
			}

			n.flags |= nodeFlagProbable
			rulecnt[k] += 1
			if len(r.prereqs) == 0 {
				n.newedge(nil, r)
			} else {
				for i := range r.prereqs {
					n.newedge(applyrules(rs, g, r.prereqs[i], rulecnt), r)
				}
			}
			rulecnt[k] -= 1
		}
	}

	// find applicable metarules
	for k := range rs.rules {
		if rulecnt[k] >= maxRuleCnt {
			continue
		}

		r := &rs.rules[k]

		if !r.ismeta {
			continue
		}

		// skip rules that have no effect (but keep N-attributed rules)
		if r.recipe == "" && len(r.prereqs) == 0 && !r.attributes.forcedTimestamp {
			continue
		}

		// n attribute: skip metarule if the target is virtual-only
		// (doesn't exist on disk and no concrete rule matched)
		if r.attributes.nonvirtual && !n.exists && n.flags&nodeFlagProbable == 0 {
			continue
		}

		for j := range r.targets {
			mat := r.targets[j].match(target)
			if mat == nil {
				continue
			}

			var stem string
			var matches []string
			matchVars := make(map[string][]string)

			if r.attributes.regex {
				matches = mat
				for i := range matches {
					key := fmt.Sprintf("stem%d", i)
					matchVars[key] = matches[i : i+1]
				}
			} else if len(mat) > 1 {
				stem = mat[1]
			}

			rulecnt[k] += 1
			if len(r.prereqs) == 0 {
				e := n.newedge(nil, r)
				e.stem = stem
				e.matches = matches
			} else {
				for i := range r.prereqs {
					var prereq string
					if r.attributes.regex {
						prereq = expandRecipeSigils(r.prereqs[i], matchVars)
					} else {
						prereq = expandSuffixes(r.prereqs[i], stem)
					}

					e := n.newedge(applyrules(rs, g, prereq, rulecnt), r)
					e.stem = stem
					e.matches = matches
				}
			}
			rulecnt[k] -= 1
		}
	}

	return n
}

// Remove edges marked for pruning (edge.togo == true).
func (g *graph) pruneEdges(n *node) {
	count := 0
	for i := range n.prereqs {
		if !n.prereqs[i].togo {
			count++
		}
	}
	prereqs := make([]*edge, count)
	j := 0
	for i := range n.prereqs {
		if !n.prereqs[i].togo {
			prereqs[j] = n.prereqs[i]
			j++
		}
	}

	// TODO: We may have to delete nodes from g.nodes, right?

	n.prereqs = prereqs
}

// Remove vacous children of n.
func (g *graph) vacuous(n *node) bool {
	vac := n.flags&nodeFlagProbable == 0
	if n.flags&nodeFlagReady != 0 {
		return vac
	}
	n.flags |= nodeFlagReady

	for i := range n.prereqs {
		e := n.prereqs[i]
		if e.v != nil && g.vacuous(e.v) && e.r.ismeta {
			e.togo = true
		} else {
			vac = false
		}
	}

	// if a rule generated edges that are not togo, keep all of its edges
	for i := range n.prereqs {
		e := n.prereqs[i]
		if !e.togo {
			for j := range n.prereqs {
				f := n.prereqs[j]
				if e.r == f.r {
					f.togo = false
				}
			}
		}
	}

	g.pruneEdges(n)
	if vac {
		n.flags |= nodeFlagVacuous
	}

	return vac
}

// Check for cycles
func (g *graph) cyclecheck(n *node) {
	if n.flags&nodeFlagCycle != 0 && len(n.prereqs) > 0 {
		mkError(fmt.Sprintf("cycle in the graph detected at target %s", n.name))
	}
	n.flags |= nodeFlagCycle
	for i := range n.prereqs {
		if n.prereqs[i].v != nil {
			g.cyclecheck(n.prereqs[i].v)
		}
	}
	n.flags &= ^nodeFlagCycle
}

// Deal with ambiguous rules.
func (g *graph) ambiguous(n *node) {
	bad := 0
	var le *edge
	for i := range n.prereqs {
		e := n.prereqs[i]

		if e.v != nil {
			g.ambiguous(e.v)
		}
		if e.r.recipe == "" {
			continue
		}
		if le == nil || le.r == nil {
			le = e
		} else {
			if !le.r.equivRecipe(e.r) && !le.r.ismeta && e.r.ismeta {
				// Concrete rule takes priority over meta-rule.
				// Print the discarded meta-rule recipe as a diagnostic before pruning.
				mkPrintRecipe(n.name, e.r.recipe, false)
				e.togo = true
				continue
			}
			if !le.r.equivRecipe(e.r) {
				if bad == 0 {
					mkPrintError(fmt.Sprintf("mk: ambiguous recipes for %s\n", n.name))
					bad = 1
					g.trace(n.name, le)
				}
				g.trace(n.name, e)
			}
		}
	}
	if bad > 0 {
		mkError("")
	}
	g.pruneEdges(n)
}

// Print a trace of rules from target back through the dependency chain.
func (g *graph) trace(name string, e *edge) {
	fmt.Fprintf(os.Stderr, "\t%s", name)
outer:
	for {
		prereqname := ""
		if e.v != nil {
			prereqname = e.v.name
		}
		fmt.Fprintf(os.Stderr, " <-(%s:%d)- %s", e.r.file, e.r.line, prereqname)
		if e.v != nil {
			for i := range e.v.prereqs {
				if e.v.prereqs[i].r.recipe != "" {
					e = e.v.prereqs[i]
					continue outer
				}
			}
		}
		break
	}
}
