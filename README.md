Mk is a reboot of the Plan 9 `mk` command, which itself is [a successor to
make](http://www.cs.tufts.edu/~nr/cs257/archive/andrew-hume/mk.pdf).

This is a fork of [ctSkennerton/mk](https://github.com/ctSkennerton/mk) with
many additional features, bug fixes, and near-complete Plan 9 compatibility.

## Installation

```
go install github.com/sgrankin/mk@latest
```

## Why Plan 9 mk is better than make

Way back in the 90s, some smart guys at Bell Labs got together and decided to
write new operating system to replace Unix. The idea was to keep everything that
was great about Unix, but totally disregard backwards compatibility in a quest
for something better. The operating system they designed, Plan 9, had a lot of
terrific ideas, and though some were cherry picked, the OS as a whole never
really caught on.

Among the gems in Plan 9 was a rewrite of the venerable Unix make
command, in the form of `mk`. Simply put, `mk` is `make`, but with a large collection
of relatively minor improvements, adding up to something more consistent,
elegant, and powerful. To name a few specifics:

1. Recipes are delimited by any indentation, not tab characters in particular.
2. Phony targets are handled separately from file targets. Your mkfile won't
   be broken by having a file named 'clean'.
3. Attributes instead of weird special targets like `.SECONDARY:`.
4. Special variables like `$target`, `$prereq`, and `$stem` in place of
   make's pointlessly cryptic `$@`, `$^`, and `$*`.
5. In addition to suffix rules (e.g. `%.o: %.c`), mk has more powerful regular
   expression rules.
6. Sane handling of rules with multiple targets.
7. An optional attribute to delete targets when a recipe fails, so you aren't
   left with corrupt output.
8. Plan 9 mkfiles can not only include other mkfiles, but pipe in the output of
   recipes. Your mkfile can configure itself by doing something like
   `<|sh config.sh`.
9. A generalized mechanism to determine if a target is out of date, for when
   timestamps won't cut it.
10. Variables are expanded in recipes only if they are defined. That way you
    usually don't have to escape `$`.

Read [Maintaining Files on Plan 9 with Mk](http://doc.cat-v.org/plan_9/4th_edition/papers/mk)
for a good overview.

## Improvements over Plan 9 mk

This mk stays mostly faithful to Plan 9, but makes some improvements.

1. A clean implementation in Go, with no Plan 9 dependencies.
1. Parallel by default. Use `-p=1` to serialize.
1. Uses Go (RE2) regular expressions, which are perl-like, rather than Plan 9
   regexes.
1. Regex submatches are available as `$stem1`, `$stem2`, etc (in addition to
   `$stem0` for the whole match).
1. Blank lines are allowed in recipes. A recipe is any indented block of text,
   continuing until a non-indented line or end of file.
1. The `$shell` variable sets the default recipe shell; the `S` attribute
   overrides it per-rule.
1. Pretty colors.

## Usage

_See the [man page](mk.1.md) for full documentation, and the
[specification](doc/mk-spec.md) for precise semantics._

`mk [options] [target ...] [var=value ...]`

### Options

| Flag | Description |
|------|-------------|
| `-f file` | Use *file* as the mkfile (default: `mkfile`) |
| `-C dir` | Change directory before reading mkfile |
| `-n` | Dry run: print recipes without executing |
| `-r` | Force building of immediate targets |
| `-a` | Force building of all dependencies |
| `-k` | Keep going after errors |
| `-t` | Touch targets instead of executing recipes |
| `-e` | Explain why targets are out of date |
| `-w target` | Pretend *target* was recently modified |
| `-p N` | Maximum parallel jobs (default: number of CPUs, or `$NPROC`) |
| `-l N` | Maximum recursion depth for a rule (default: 1) |
| `-i` | Force rebuild of missing intermediates |
| `-I` | Interactive: prompt before executing rules |
| `-q` | Quiet: don't print recipes before executing |
| `-dot` | Print dependency graph in Graphviz dot format |
| `-color` | Force color output on/off |
| `-shell prog` | Default shell (default: `sh -e`) |

Command-line assignments (`var=value`) override the first assignment to that
variable in the mkfile.

## Non-shell recipes

Recipes can be executed by programs other than the shell using the
`S[command]` attribute. For example, a recipe written in Julia:

```make
mean.txt:Sjulia: input.txt
    println(open("$target", "w"),
            mean(map(parseint, eachline(open("$prereq")))))
```

## License

[BSD 2-clause](LICENSE).

Originally by [Daniel C. Jones](https://github.com/dcjones), with
contributions from [many others](https://github.com/ctSkennerton/mk).
