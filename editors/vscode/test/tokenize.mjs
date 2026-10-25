// Checks the TextMate grammar against ../example.mkfile using vscode-textmate,
// the tokenizer VS Code itself uses. Run with `npm test`.
//
// Recipes embed source.shell. If SHELL_GRAMMAR points at VS Code's
// shell-unix-bash.tmLanguage.json, it is used and shell scopes are checked
// too; otherwise an empty stand-in is used and only mk scopes are checked.
import fs from 'node:fs';
import path from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import vsctm from 'vscode-textmate';
import oniguruma from 'vscode-oniguruma';

const here = path.dirname(fileURLToPath(import.meta.url));
const require = createRequire(import.meta.url);
const wasm = fs.readFileSync(require.resolve('vscode-oniguruma/release/onig.wasm'));
await oniguruma.loadWASM(wasm.buffer);

const shellPath = process.env.SHELL_GRAMMAR;
const grammars = {
  'source.mk': path.join(here, '../syntaxes/mk.tmLanguage.json'),
};
const registry = new vsctm.Registry({
  onigLib: Promise.resolve({
    createOnigScanner: (s) => new oniguruma.OnigScanner(s),
    createOnigString: (s) => new oniguruma.OnigString(s),
  }),
  loadGrammar: async (scope) => {
    if (scope === 'source.shell') {
      if (shellPath) return vsctm.parseRawGrammar(fs.readFileSync(shellPath, 'utf8'), shellPath);
      return { scopeName: 'source.shell', patterns: [] };
    }
    const p = grammars[scope];
    return p ? vsctm.parseRawGrammar(fs.readFileSync(p, 'utf8'), p) : null;
  },
});
const grammar = await registry.loadGrammar('source.mk');

// Tokenize the example; record each token's text and scopes by line.
const lines = fs.readFileSync(path.join(here, '../../example.mkfile'), 'utf8').split('\n');
const tokens = [];
let state = vsctm.INITIAL;
lines.forEach((line, i) => {
  const r = grammar.tokenizeLine(line, state);
  for (const t of r.tokens) {
    tokens.push({ line: i + 1, text: line.slice(t.startIndex, t.endIndex), scopes: t.scopes });
  }
  state = r.ruleStack;
});

let failures = 0;
// expect(line, text, scope): the token `text` on `line` has `scope`
// (or, with `not`, does not have it). `text` may be part of a token.
function check(line, text, scope, { not = false } = {}) {
  const t = tokens.find((t) => t.line === line && t.text.includes(text));
  const ok = t && (t.scopes.some((s) => s.startsWith(scope)) !== not);
  if (!ok) {
    failures++;
    console.log(`FAIL line ${line} ${JSON.stringify(text)}: expected ${not ? 'no ' : ''}${scope}, got ${t ? t.scopes.join(' ') : 'no such token'}`);
  }
}
const at = (text) => lines.findIndex((l) => l.includes(text)) + 1;

check(1, '# Example', 'comment.line.number-sign.mk');
check(2, 'CC', 'variable.other.definition.mk');
check(2, '=', 'keyword.operator.assignment.mk');
check(5, 'SRC', 'variable.other.mk');
check(5, '%', 'keyword.other.placeholder.mk');
check(6, 'U', 'storage.modifier.attribute.mk');
check(at('PREFIX ?='), 'PREFIX', 'variable.other.definition.mk');
check(at('PREFIX ?='), '?=', 'keyword.operator.assignment.mk');
check(at('FILES = '), 'ls *.c', 'string.interpolated.backtick.mk');
check(at('RCFILES = '), 'ls *.c', 'string.interpolated.backtick.mk');
check(at('<config.mk'), '<', 'keyword.control.import.mk');
check(at('<config.mk'), 'config.mk', 'string.unquoted.include.mk');
check(at('<|'), '<|', 'keyword.control.import.pipe.mk');
check(at('<|'), 'CC', 'variable.other.mk');

let l = at('all:V:');
check(l, 'all', 'entity.name.function.target.mk');
check(l, 'V', 'storage.modifier.attribute.mk');
check(l, 'prog', 'entity.name.function.target.mk', { not: true });

l = at('prog: $OBJ');
check(l, 'prog', 'entity.name.function.target.mk');
check(l, 'OBJ', 'variable.other.mk');
check(l + 1, 'target', 'variable.other.mk');
check(l + 1, 'target', 'meta.recipe.mk');

l = at('%.o: %.c');
check(l, '%', 'keyword.other.placeholder.mk');
check(l + 1, 'stem', 'variable.other.mk');

l = at('quiet:$SC:');
check(l, 'SC', 'variable.other.mk');
check(l, 'SC', 'storage.modifier.attribute.mk');

l = at('rm -rf $$dir');
check(l, '$$', 'constant.character.escape.dollar.mk');
check(l, 'dir', 'variable.other.mk', { not: true });

// A continuation line belongs to the rule, not to a recipe.
l = at('deps: a');
check(l, '\\', 'constant.character.escape.line-continuation.mk');
check(l + 1, 'b c', 'meta.rule.mk');
check(l + 1, 'b c', 'meta.recipe.mk', { not: true });

l = at('report:VSpython3:');
check(l, 'VSpython3', 'storage.modifier.attribute.mk');

l = at('out%: Dockerfile');
check(l, 'out', 'entity.name.function.target.mk');
check(l + 2, 'prereq1', 'variable.other.mk');

if (shellPath) {
  check(at('rm -rf $$dir'), 'remove build output', 'comment.line.number-sign.shell');
  check(at('rm -rf $$dir'), 'rm', 'meta.recipe.mk');
}

console.log(failures ? `${failures} check(s) failed` : `all checks passed${shellPath ? ' (with shell grammar)' : ''}`);
process.exit(failures ? 1 : 0);
