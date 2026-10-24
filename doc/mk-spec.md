# mk Specification

This document describes the specification of Plan 9 mk, derived from:
- The mk(1) man page (plan9port)
- "Maintaining Files on Plan 9 with Mk" (Hume & Flandrena)
- The Plan 9 from User Space C source code

Where our Go implementation diverges from Plan 9, differences are noted inline as **[DIVERGENCE]**.

## 1. Overview

Mk reads a file (mkfile) describing dependencies between files and executes
commands to bring them up to date. A mkfile contains three types of statements:
**assignments**, **includes**, and **rules**.

When no target is specified on the command line, mk builds the targets of the
first non-metarule in the mkfile.

## 2. Lexical Structure

### 2.1 Lines

Mk processes input line by line. A logical line may span multiple physical lines
via backslash-newline continuation: the sequence `\<newline>` is deleted
(in non-recipe context) or replaced with a space (in recipe context).

Blank lines are ignored. Lines beginning with `#` (outside quotes) are comments
and are discarded up to the next newline.

### 2.2 Character Classes

Plan 9 mk defines word characters via `WORDCHR` (mk.h):

```c
#define WORDCHR(r) ((r) > ' ' && !utfrune("!\"#$%&'()*+,-./:;<=>?@[\\]^`{|}~", (r)))
```

Characters matching `WORDCHR` are: alphanumerics, underscore, and any character
with code point > 127 (Unicode). All ASCII punctuation terminates a word in
variable-name contexts.

**[DIVERGENCE]** Our `nonBareRunes` is much smaller: `` " \t\n\r\\=:#'\"$` ``.

### 2.3 Quoting

Three quoting mechanisms:

| Quote | Behavior |
|-------|----------|
| `'...'` | Literal string. No escapes. Terminated by matching `'`. |
| `"..."` | Interpreted string. `\"` escapes a literal quote, `\\` escapes backslash. Variables and backticks are expanded inside. |
| `` `...` `` | Command substitution (sh-style). See §2.5. |
| `` `{...} `` | Command substitution (rc-style). See §2.5. |

Single and double quotes can appear adjacent to bare words and to each other,
forming a single token: `foo'bar'"baz"` → `foobarbaz`.

### 2.4 Comments

A `#` character outside quotes begins a comment. Everything from `#` to the
end of the line is discarded.

If the last non-whitespace character before the newline is `\`,
the comment-ending newline is treated as a continuation (the next line is also
discarded).

### 2.5 Backtick (Command Substitution)

Backtick substitution executes a shell command and replaces the expression
with its output, which is then subject to normal word splitting.

Two forms are supported:
- `` `cmd` `` — sh-style: the command runs until the closing backtick.
- `` `{cmd} `` — rc-style: the command runs until the closing `}`.

Backtick expressions can appear adjacent to bare words:
`foo`echo bar`` produces `foobar`.

In Plan 9 mk, backtick expansion happens at lex time during line assembly.

**[DIVERGENCE]** Our implementation expands backticks during variable expansion,
not at lex time. This is architecturally different but functionally equivalent
for most cases.

### 2.6 Variable References

Two forms:
- `$name` — bare variable reference; `name` is the longest sequence of `WORDCHR` characters.
- `${name}` — bracketed variable reference.
- `${name:A%B=C%D}` — namelist pattern substitution (see §4).

`$$` produces a literal `$`.

In recipes, `\$` also produces a literal `$` (shell escaping).

Because mk substitutes recipe variables before the shell runs (see §3.2), a
variable the recipe sets for itself must be written `$$name` (or `\$name`).
A plain `$name` is replaced by mk's value for `name`, including one taken
from the environment, which then overrides the recipe's own assignment.

If a variable is not defined in mk's variable table or the environment, the
reference is preserved literally (e.g., `$undefined` remains `$undefined` in
the output). This differs from make, which expands undefined variables to the
empty string.

### 2.7 Line Classification

The first unquoted occurrence of `:`, `=`, or `<` determines the line type:

| Character | Line type |
|-----------|-----------|
| `:` | Rule |
| `=` | Assignment |
| `<` | Include (file) |
| `<\|` | Include (pipe/command) |

If none of these appears, it's a syntax error.

## 3. Assignments

```
name = value
name =U= value
```

The left side must be a single word (the variable name). The right side is
split into words which become the variable's value (a list).

The `U` attribute between `=` signs marks the variable as unexported
(not placed in the environment for recipes).

### 3.1 Precedence

Variable values have this precedence (highest first):
1. Command-line assignment (`mk name=value`)
2. Assignment statement in mkfile
3. Inherited from environment
4. Implicitly set by mk

### 3.2 Evaluation Timing

- Assignment values are evaluated **when read** (eagerly).
- Recipe bodies are evaluated **when executed** (lazily): mk substitutes
  variables just before running the recipe, then hands it to the shell.
- This means the last assignment wins for recipes, but earlier assignments
  affect subsequent rule headers.

## 4. Namelist Substitution

```
${var:A%B=C%D}
```

For each word in `$var` matching the pattern `A%B` (where `%` matches any
string), substitute `C%D` (replacing `%` with the matched portion).
`A`, `B`, `C`, `D` may be empty.

Equivalent to applying `s/A(.*)B/C\1D/` to each word.

Example: `${SRC:%.c=%.o}` transforms `a.c b.c` → `a.o b.o`.

## 5. Include Statements

### 5.1 File Include

```
<filename
```

Replaces itself with the contents of `filename`. Variables in `filename` are
expanded before opening. The included file is parsed recursively.

### 5.2 Pipe Include

```
<|command args
```

Executes `command args` via the shell, then parses the output as mkfile content.

## 6. Rules

### 6.1 Syntax

```
targets : attributes : prerequisites
    recipe
```

- **targets**: one or more words (file names or patterns).
- **attributes**: zero or more attribute characters between the colons.
- **prerequisites**: zero or more words.
- **recipe**: zero or more lines, each beginning with whitespace (tab or space).

The second colon is required if attributes are present. If no attributes,
the form is `targets : prerequisites`.

### 6.2 Recipe Execution

The entire recipe is passed to the shell as a single script (not line-by-line
like make). Leading whitespace is stripped from each recipe line, up to the
indentation level of the first line of the recipe. This allows recipes to be
indented with any consistent whitespace.

**[DIVERGENCE]** Plan 9 mk strips exactly one leading whitespace character per
recipe line. Our implementation strips up to the indentation column of the
first recipe line, which better supports indentation-sensitive languages like
Python.

By default, the shell is invoked with `-e` (exit on error). The `E` attribute
overrides this.

### 6.3 Automatic Variables

Available in recipes (set by mk before execution):

| Variable | Value |
|----------|-------|
| `$target` | The target being built |
| `$prereq` | All prerequisites |
| `$newprereq` | Out-of-date prerequisites only |
| `$stem` | String matched by `%` or `&` in metarule |
| `$stemN` | Nth regex subexpression match (R attribute) |
| `$alltarget` | All targets of the rule |
| `$prereqN` | Nth prerequisite (1-based): `$prereq1`, `$prereq2`, etc. |
| `$newmember` | Archive member names from `$newprereq` |
| `$nproc` | Slot number (0-based) of this parallel job |
| `$pid` | Process ID of mk |

**[DIVERGENCE]** `$newmember` is always empty because we do not support the
`lib(member)` archive syntax.

### 6.4 Attributes

| Attr | Name | Effect |
|------|------|--------|
| `D` | Delete | Remove target file if recipe fails |
| `E` | Errors | Don't pass `-e` to shell; continue on errors |
| `N` | No-recipe | Suppress error for non-existent target with no recipe |
| `n` | No-virtual | Metarule only matches targets that exist on disk |
| `P` | Program | Custom staleness test (see below) |
| `Q` | Quiet | Don't print recipe before execution |
| `R` | Regexp | Target is a regular expression (see §7.2) |
| `U` | Update | Force target timestamp update after recipe runs |
| `V` | Virtual | Target is not a file; always considered out of date |

The `S` attribute is present in plan9port mk but not in the original Plan 9 paper.

**[DIVERGENCE]** Our implementation adds:
- `X` — Exclusive: recipe acquires all parallel job slots before executing
- `F` — File: recipe is passed to the shell as a file, not on stdin (see below)

#### N (No-recipe)

Normally, mk reports an error when a target does not exist on disk and no
rule provides a recipe to create it. The `N` attribute suppresses this error.
This is used in the Plan 9 archive pattern where one rule establishes a
dependency and a separate rule provides the recipe:

```
lib.a(foo.o):N: foo.o       # foo.o dependency, no recipe — N prevents error
lib.a: lib.a(foo.o)         # this rule has the archiving recipe
    ar rv lib.a foo.o
```

#### n (No-virtual)

A metarule with the `n` attribute does not match targets that exist only as
virtual targets (no file on disk, no concrete rule). It only matches targets
that are "real" — either the file exists or a concrete (non-meta, non-virtual)
rule matched the target. This prevents metarules from generating spurious
build steps for abstract targets.

#### P (Program)

The `P` attribute replaces the default timestamp-based staleness check with a
custom command. The attribute consumes the remaining attribute text as the
command:

```
target:Pcheck-stale arg: prereq1 prereq2
    recipe
```

For each prerequisite, mk runs `check-stale arg target prereqN`. If the
command exits **non-zero**, the target is considered out of date.

`P` also works on virtual (`V`) rules, where there is no timestamp to
compare: the program alone decides whether the recipe runs, and receives the
target name as a label rather than a file. A virtual rule with `P` but no
prerequisites always runs, since there is nothing to compare.

#### U (Update)

After a recipe runs, mk re-reads the target's modification time from disk.
If the recipe didn't modify the file, dependents might not see it as updated.
The `U` attribute forces the target's timestamp to "now" after the recipe
completes, ensuring dependents are rebuilt regardless.

#### S (Shell)

Specifies an alternative shell for this rule's recipe (plan9port extension).
The attribute consumes the remaining attribute text:

```
all:VS"python3":
    import sys; print(sys.version)
```

#### F (File) **[DIVERGENCE]**

The recipe, after variable substitution, is written to a temporary file in
`$TMPDIR`, and the file's path is appended as the last argument to the shell
command (or the `S` program): `sh -e /tmp/mk-target-123`,
`python3 /tmp/mk-target-123`, `awk -f /tmp/mk-target-123`. The recipe's
standard input is mk's own, so commands in it can read input; without `F`,
a command that reads standard input consumes the rest of the recipe. The file
is removed when the recipe finishes, and on SIGINT, SIGTERM or SIGHUP, after
which mk ends with the same signal.

```
config:VF:
    printf 'name: '
    read name
    echo "name = $$name" > config
```

#### X (Exclusive) **[DIVERGENCE]**

The recipe acquires all parallel job slots before executing, ensuring no other
recipes run concurrently. Useful for recipes that are themselves parallel or
that must not overlap with other work (e.g., a link step).

### 6.5 No-Recipe Rules

A rule with prerequisites but no recipe adds those prerequisites to all other
rules with the same target that DO have recipes. This is the mechanism for
adding header file dependencies:

```
%.o: hdr.h          # no recipe — adds hdr.h as prereq
%.o: %.c            # has recipe — hdr.h is added here too
    cc $stem.c
```

### 6.6 No-Prerequisite Rules

A rule with no prerequisites and a recipe:
- For a real target: executes only if the target file doesn't exist.
- For a virtual target: always executes.

A rule with no prerequisites and no recipe is a "no-effect" rule — it is
ignored during graph construction.

### 6.7 Rule Replacement

When two rules have identical headers (same targets, attributes, and
prerequisites) and both have recipes, the later rule silently replaces the
earlier one. This is a Plan 9 convention that allows included mkfiles to
provide default rules that can be overridden:

```
<common.mk             # defines %.o: %.c with default flags
%.o: %.c               # overrides with project-specific recipe
    cc -DFOO $stem.c
```

## 7. Metarules

### 7.1 Pattern Metarules

Two intrinsic patterns:

| Pattern | Matches | Equivalent regex |
|---------|---------|-----------------|
| `%` | One or more of anything | `.+` |
| `&` | One or more of anything except `/` and `.` | `[^./]+` |

Only one `%` or `&` may appear in a target pattern.

In prerequisites, `%` or `&` is replaced by the stem (the string matched
in the target).

### 7.2 Regex Metarules

With the `R` attribute, the target is interpreted as a regular expression.
The pattern is automatically anchored: `^pattern$` is used for matching, so
the regex must match the entire target name.

Prerequisites can reference subexpressions with `\1` through `\9`.
Recipe variables `$stem0` (the full match) through `$stem9` provide the same
matches.

Example:
```
([^/]+)/([^/]+)\.o:R: \1/\2.c
    cc -o $target $stem1/$stem2.c
```

**[DIVERGENCE]** Plan 9 uses its own `regexp(6)` syntax. Our implementation
uses Go's `regexp` package (RE2 syntax), which does not support backreferences
or lookaheads in patterns.

### 7.3 Metarule Evaluation

Each metarule is applied at most once per target (controlled by `NREP`,
default 1) to prevent infinite cycles.

A metarule with the `n` (NOVIRT) attribute does not apply when the only
matching rule for the target has the `V` (virtual) attribute.

## 8. Dependency Graph

### 8.1 Construction (`applyrules`)

For each target, mk builds a dependency graph:

1. **Concrete rules first**: Look up rules whose target literally matches.
   Skip "no-effect" rules (no recipe AND no prerequisites).
   For each matching rule, create arcs to prerequisite nodes (recursively).

2. **Metarules second**: Iterate all metarules. For each:
   - Skip no-effect metarules.
   - If `n` attribute and target only has virtual arcs, skip.
   - Match target against pattern (% or regex).
   - Create arcs to prerequisite nodes (with stem substitution).

3. A rule counter (`cnt[]`) limits each rule to `NREP` applications per target.

### 8.2 Cycle Detection (`cyclechk`)

DFS traversal; if a node is revisited while its `CYCLE` flag is set, mk
reports an error and exits.

### 8.3 Vacuous Node Pruning (`vacuous`)

A node is **vacuous** if:
- It is not PROBABLE (no concrete rule matched it, and it has no existing file).
- ALL of its prerequisites are also vacuous.

Vacuous arcs from metarules are marked `TOGO` and deleted. However, if a rule
produced BOTH vacuous and non-vacuous arcs, the vacuous ones are retained
(they came from the same rule).

### 8.4 Ambiguity Resolution (`ambiguous`)

After pruning, if a node has arcs from multiple rules with different recipes:
- **Concrete beats meta**: If one recipe comes from a concrete rule and
  another from a metarule, the metarule's arc is deleted.
- **Otherwise**: The recipes are ambiguous. Mk prints a trace and exits.

The trace format shows the dependency chain:
```
mk: ambiguous recipes for target:
    target <-(file:line)- prereq1 <-(file:line)- prereq2
    target <-(file:line)- prereq3
```

### 8.5 Attribute Propagation (`attribute`)

After ambiguity resolution, attributes from rules are propagated to nodes:
- `V` → node gets `VIRTUAL` flag, time set to 0
- `N` → node gets `NORECIPE` flag
- `D` → node gets `DELETE` flag

## 9. Execution

### 9.1 Work Algorithm

For each node (depth-first):

1. If already `BEINGMADE`, skip (parallel build in progress).
2. If `MADE` but `PRETENDING`, and parent is out of date, unpretend.
3. If no prerequisites and no file exists: error (don't know how to make).
4. If no prerequisites and file exists: mark `MADE`.
5. Recursively process prerequisites. Track which are out of date.
6. If all prerequisites are ready and node is out of date:
   - Check if we can **pretend** (missing intermediate optimization).
   - If not, execute the recipe via `dorecipe`.

### 9.2 Missing Intermediates (Pretending)

If a target is up to date with respect to the transitive prerequisites
(skipping a missing intermediate), mk pretends the intermediate exists
rather than rebuilding it. This is useful for archives where object files
are deleted after archiving.

The `-i` flag forces rebuild of missing intermediates, disabling this
optimization.

### 9.3 Parallel Execution

The maximum number of concurrent recipes is determined by (in priority order):
the `-p` flag, the `$NPROC` environment variable, or the number of available
processors.

Recipes for independent prerequisites execute in parallel. The `$nproc`
variable tells each recipe its 0-based slot number.

### 9.4 Recipe Output

Before execution, mk prints the recipe (unless `Q` attribute or `-q` flag).
The recipe is expanded with variable values for display.

In Plan 9 mk, the `front()` function truncates long recipe displays to
5 fields with `...` in the middle.

## 10. Shell Interface

Recipes are passed to a shell for execution. The shell receives the recipe
text on standard input (recipes cannot read their own stdin).

The default shell is `sh` invoked with `-e` (exit on error). The `E` attribute
removes the `-e` flag for a specific rule.

Flags in the shell command (from `-shell`, the `shell` variable or the `S`
attribute) are passed to the shell exactly as written. Because the recipe
arrives on standard input, the command should not include `-c`.

In Plan 9 mk, the `MKSHELL` variable selects between pluggable shell
implementations (`sh` and `rc`), each with its own quoting and escaping rules.
Plan9port added the `S` attribute for per-rule shell override.

**[DIVERGENCE]** Our implementation uses a `shell` mk variable instead of
`MKSHELL`. The `-shell` flag sets the default shell from the command line.

## 11. Environment

### 11.1 Import/Export

On startup, mk imports all environment variables as mk variables.
Before executing each recipe, mk exports all variables to the environment.

Variables marked with the `U` attribute on assignment are not exported.

### 11.2 Internal Variables

These are set by mk and available in recipes:
`target`, `stem`, `prereq`, `pid`, `nproc`, `newprereq`, `alltarget`,
`newmember`, `stem0`–`stem9`.

### 11.3 Inter-Word Separator

List variables are exported to the environment using a separator character.
For `sh`, the separator is space (` `). For `rc`, it is `\x01` (SOH).

**[DIVERGENCE]** Our implementation always uses space.

## 12. Command Line

### 12.1 Targets and Variable Overrides

Non-flag arguments to mk are either variable overrides or targets:

- If an argument contains `=` and the left side is a valid variable name,
  it is a **variable override**: `mk CC=gcc` sets `$CC` to `gcc`.
  Command-line overrides have the highest precedence (see §3.1).
- Otherwise, the argument is a **target** to build.

If no targets are specified, mk builds the targets of the first non-metarule
in the mkfile.

### 12.2 Flags

| Flag | Effect |
|------|--------|
| `-a` | Force rebuild of all targets (ignore timestamps) |
| `-f file` | Use `file` instead of `mkfile` |
| `-i` | Force rebuild of missing intermediates |
| `-k` | Continue after errors (keep going) |
| `-n` | Dry run (print recipes without executing) |
| `-q` | Don't print recipes before execution |
| `-r` | Force rebuild of specified targets only (not their prerequisites) |
| `-t` | Touch targets instead of executing recipes |
| `-w target` | Treat `target` as recently modified |

**[DIVERGENCE]** Our implementation adds:
- `-p N` — Set parallelism level (default: `$NPROC` env, then number of CPUs)
- `-l N` — Max times a specific rule can be applied (default: 1)
- `-C dir` — Change to `dir` before reading mkfile
- `-I` — Interactive mode: prompt before executing rules
- `-dot` — Print dependency graph in Graphviz dot format and exit
- `-color` — Enable/disable color output (default: auto-detect TTY)
- `-shell cmd` — Default shell (default: `sh -e`)
- `-e` — Explain why targets are out of date (prints staleness decisions to stderr)

## Appendix A: Known Divergences Summary

| Area | Plan 9 | Our Go Implementation |
|------|--------|----------------------|
| Word characters | `WORDCHR`: all non-ASCII-punctuation | `nonBareRunes`: smaller set (`` \t\n\r\\=:#'"$` ``) |
| Backtick timing | Expanded at lex time | Expanded at eval time |
| Recipe indentation | Strips one leading whitespace char | Strips up to first line's indentation column |
| Shell interface | Pluggable Shell struct (sh, rc) with `MKSHELL` | `shell` variable; `-shell` flag; `S` attribute (from plan9port) |
| Environment separator | Shell-dependent (space for sh, \x01 for rc) | Always space |
| Archive members | `lib(member)` syntax with special handling | Not supported (`$newmember` always empty) |
| Recipe display | `front()` truncates to 5 fields | No truncation |
| Regex syntax | Plan 9 `regexp(6)` | Go RE2 (no backreferences or lookaheads) |
| Parallelism | `$NPROC` env var only | `-p` flag > `$NPROC` env > NumCPU |
| Additional attributes | — | `X` (exclusive execution), `F` (recipe as a file) |
| Additional flags | — | `-p`, `-l`, `-C`, `-I`, `-dot`, `-color`, `-shell` |

## Appendix B: Examples

### Basic mkfile

```
CC=cc
CFLAGS=-Wall -O2
OBJ=main.o util.o

prog: $OBJ
    $CC $CFLAGS -o $target $prereq

%.o: %.c
    $CC $CFLAGS -c $stem.c
```

The first rule (`prog`) is the default target. The `%.o: %.c` metarule
compiles any `.c` file into a `.o` file, with `$stem` set to the matching
filename without extension.

### Virtual targets

```
all:V: prog docs

clean:V:
    rm -f *.o prog

test:V: prog
    ./prog --test
```

Virtual targets are not files. Without `V`, mk would skip `clean` if a file
named `clean` existed.

### Conditional compilation with pipe include

```
<|sh -c 'if pkg-config --exists libpng; then echo "HAVE_PNG=1"; fi'

prog: main.o
    $CC -o $target $prereq
```

### Unexported variables

```
SECRET =U= my-api-key
CONFIG = production

deploy:V:
    echo "deploying $CONFIG"   # CONFIG is in env; SECRET is not
```

`$SECRET` is available in mk variable expansion but is not placed in the
environment for recipes.

### Custom staleness with P attribute

```
result.txt:Psh -c '[ "$1" -ot "$2" ] && exit 1; exit 0': input.txt
    process input.txt > result.txt
```

The `P` command receives `target prereq` as arguments. Non-zero exit means
out of date.

### Per-rule shell with S attribute

```
report.txt:VS"python3":
    import json, sys
    data = json.load(open('data.json'))
    print(f"Records: {len(data)}")

all:V: report.txt
    cat report.txt
```

### Regex metarule

```
([^/]+)\.o:R: \1.c
    cc -c -o $target $stem1.c
```

The regex is anchored (`^...$`). `\1` in prerequisites and `$stem1` in
recipes reference the first capture group.

### Parallel builds with exclusive linking

```
%.o: %.c
    cc -c -o $target $stem.c

prog:X: main.o util.o io.o
    cc -o $target $prereq
```

Object files compile in parallel. The `X` attribute on `prog` ensures the
link step waits for all slots, preventing it from running while compilations
are still in progress.
