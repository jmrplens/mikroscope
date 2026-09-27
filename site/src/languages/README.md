# RouterOS grammar

`routeros.tmLanguage.json` is the TextMate grammar that colours RouterOS
script on this site. JSON takes no comments, so this file says where it comes
from, what reads it and what holds it to its behaviour.

## Where it comes from

It is derived from the RouterOS grammar of jmrp.io, the same author's site
(`src/languages/routeros.tmLanguage.json` at commit 604d2a8, 2026-02-16, MIT),
and extended for the scripts this project renders: the install and uninstall
scripts of the golden matrix, the generator's output, the `<ManualSteps>`
commands and the fences of the pages. Run against those, the jmrp.io grammar
failed 108 of the 158 assertions in `scripts/check-routeros-grammar.mjs`
(shiki 4.4.3, both regex engines, 2026-09-27). What changed:

- `do=`, `else=`, `on-error=`, `in=` and `while=` are control words, and the
  `{`, `[` or `(` after them opens a block, not a string.
- A `key=value` argument is a region: the key is a property name whatever its
  value, and the value is typed (string, `$var`, IPv4/IPv6 with prefix, range
  or port, MAC, duration, size, boolean, `<placeholder>`, list with `,` and
  `!`), ending at whitespace, `;` or a closer.
- `:if`, `:do`, `:while`, `:for`, `:foreach`, `:return`, `:onerror` and
  `:retry` are one keyword each, colon included; `:local`/`:global` are
  declarations; any other `:word` is a built-in.
- `$var` stops where a RouterOS name does (`$d->0`), `$"quoted"` and `$1` are
  variables, and a string interpolates `$var`, `$[…]` and `$(…)`.
- A menu path in slash form (`/container/envs/add`) or space form
  (`/ip firewall filter add`) is a tag, with the command after it a function,
  for any menu, `/disk`, `/import` and `/export` included; `[find …]` and
  `{ remove … }` are commands too.
- Durations, the regex after `~`, `->`, the `.` that joins strings, `=` as a
  comparison, a trailing `\` line continuation, print flags (`once`,
  `count-only`, `as-value`…) and `where` each have a scope; a `#` glued to a
  word (`ssid=Cafe#1`) is not a comment.
- A block that starts at a `[user@identity] >` prompt is a terminal session:
  prompt lines are commands, every other line is output. Without a prompt,
  only shapes a command line never has are output (`Flags:` lines, aligned
  `#` table headers, rows that start with a number, `key: value`).

Its aliases are `routeros` and `mikrotik`. Not `rsc`: GitHub colours an `rsc`
fence as Rascal, and `docs/*.md` carries the site's fences to GitHub
unchanged. GitHub has no RouterOS grammar (Linguist's `RouterOS Script` has
`tm_scope: none`), so a `routeros` fence renders there as plain text, as a
`text` fence did.

## What reads it

- Expressive Code, for every ` ```routeros ` fence and `<Code lang="routeros">`:
  `astro.config.mjs` registers it in `expressiveCode.shiki.langs`, and
  `ec.config.mjs` fails the build on a fence language no grammar knows.
- `src/lib/rsc-highlight.mjs`, the script generator's tokenizer, on the server
  and in the browser, with the colours in `routeros.palette.json`.

## What holds it

- `scripts/check-routeros-grammar.mjs` (in `pnpm lint`): the 158 assertions,
  through the shiki Expressive Code uses with both of its regex engines and
  through `src/lib/rsc-highlight.mjs`; a check over every RouterOS text the
  site shows, which the tokenizer must also scope as shiki does, character by
  character; and made-up grammars that hold the tokenizer's guards against
  rules that match without advancing to shiki's.
- `scripts/check-rsc-highlight.mjs` (in `pnpm lint`): the tokenizer against
  Expressive Code, character by character in both themes, and the palette
  regenerated from Expressive Code as Starlight configures it.
