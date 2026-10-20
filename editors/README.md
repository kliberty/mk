# Editor support for mkfiles

Syntax highlighting for mkfiles in VS Code, IntelliJ-based IDEs and Kate:
comments, assignments (including `var=U=value`), includes (`<file`,
`<|command`), rule headers (targets, the `:attributes:` field and
prerequisites), `%`/`&` meta-rule patterns, variables (`$var`, `${var}`,
`${var:A%B=C%D}`, `$$`), quoting and backquotes. Indented recipe lines are
highlighted as shell, with mk's own variables picked out.

Files named `mkfile`, `mkfile.*` and `*.mkfile` are recognised automatically,
as are Plan 9's `mkhdr`, `mkone`, `mkmany`, `mklib` and `mksyslib`. `*.mk`
is deliberately left alone, because every editor already uses it for GNU
make; see below to map it (or any other name) to mk yourself.

`example.mkfile` exercises every construct; open it to check the result.

| Directory | Format | Used by |
|---|---|---|
| `vscode/` | TextMate grammar, packaged as a VS Code extension | VS Code, IntelliJ |
| `kate/`   | KSyntaxHighlighting XML | Kate, KWrite, KDevelop |

## VS Code

Package and install the extension:

    cd editors/vscode
    npx @vscode/vsce package
    code --install-extension mk-plan9-0.1.0.vsix

To treat other files as mkfiles, add to your settings:

    "files.associations": { "*.mk": "mk" }

How much is coloured depends on the colour theme. Targets are scoped
`entity.name.function` and variables `variable.other`, as in VS Code's own
Makefile grammar; Dark+ and Dark Modern colour both, but some themes leave
them plain. "Developer: Inspect Editor Tokens and Scopes" shows the scope of
any token and which theme rule, if any, coloured it.

## IntelliJ IDEA, GoLand and other JetBrains IDEs

Settings → Editor → TextMate Bundles → **+**, and choose the
`editors/vscode` directory. It is listed as "mk (Plan 9 mkfiles)".

IntelliJ has no separate "mk" file type: every TextMate-highlighted file
belongs to one type, "Files supported via TextMate bundles". It only applies
to files IntelliJ would otherwise treat as plain text or unknown, and it
recognises a bundle's files by exact name or by extension only, so `mkfile`,
`mkhdr` and similar names and `*.mkfile` work, but `mkfile.*` does not. To
add another name, add it to `filenames` (or an extension to `extensions`) in
`editors/vscode/package.json`, then remove and re-add the bundle. A file
already opened as plain text may need "Override File Type" → "Files
supported via TextMate bundles".

Recipes use the IDE's bundled shell grammar. If they appear uncoloured,
check that the shell bundle is enabled in the same TextMate Bundles list.

## Kate (and KWrite, KDevelop)

    mkdir -p ~/.local/share/org.kde.syntax-highlighting/syntax
    cp editors/kate/mk.xml ~/.local/share/org.kde.syntax-highlighting/syntax/

Restart Kate. The highlighting is listed under Tools → Highlighting → Other
→ mk. To add file names, edit the mk entry under Settings → Configure Kate →
Open/Save → Modes & Filetypes.

## Limitations

- Recipes are always highlighted as shell, even for rules whose `S`
  attribute runs another program (for example `:Spython3:`).
- Backquoted commands and `<|` include lines are highlighted as mk text
  (strings and variables), not parsed as shell.
- In Kate, once a recipe line enters a shell construct, variables inside it
  take the shell's variable colour rather than mk's.

## Testing the TextMate grammar

`vscode/test/tokenize.mjs` runs VS Code's tokenizer over `example.mkfile`
and checks the scope of each construct:

    cd editors/vscode
    npm install
    npm test

Set `SHELL_GRAMMAR` to the path of VS Code's
`extensions/shellscript/syntaxes/shell-unix-bash.tmLanguage.json` to also
check recipes with the real shell grammar embedded. The Kate file validates
against the KSyntaxHighlighting schema (`data/schema/language.xsd` in KDE's
syntax-highlighting repository).
