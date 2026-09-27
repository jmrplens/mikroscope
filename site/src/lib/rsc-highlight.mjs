// @ts-check
// RouterOS colouring for the text the script generator writes: the colours
// Expressive Code gives a ```routeros fence, for text that only exists once
// the reader has filled in the form.
//
// Expressive Code colours at build time, and the generator's install,
// uninstall and router verify commands are rewritten in the browser on every
// change to the form, so a fence cannot hold them. shiki in the browser
// measured 42 to 62 KB gzip with a JavaScript regex engine and 275 KB with
// Oniguruma's WebAssembly (esbuild, minified, 2026-09-27), with the 7.8 KB
// jmrp.io grammar this one derives from; it was not measured again with this
// 24.5 KB one. This interpreter of the part of TextMate the grammar uses, with
// this grammar and its palette, took the generator's bundle from 12.0 to
// 17.9 KB gzip (Vite build, gzip -9, same day). It reads the same grammar Expressive
// Code registers (src/languages/routeros.tmLanguage.json) and runs it the way
// vscode-textmate, shiki's tokenizer, does: each line with "\n" appended; at
// each position the leftmost match among the current rule's end and its
// patterns, the end first on a tie and then the order listed; begin/end
// regions with contentName, beginCaptures and endCaptures; nested captures;
// and its guards against rules that match without advancing. The colours
// come from src/languages/routeros.palette.json, which
// scripts/check-rsc-highlight.mjs regenerates from Expressive Code as
// Starlight configures it, and which it uses to compare this with Expressive
// Code character by character, in both themes, on every case of the golden
// matrix. scripts/check-routeros-grammar.mjs holds its scopes to shiki's on
// every RouterOS text the site shows, and on made-up grammars that reach the
// guards.
//
// A grammar key or a regex construct this does not implement throws when the
// grammar is compiled, so a grammar that grows one fails both checks instead
// of colouring differently in the generator than in a fence.
//
// Some differences between the engines are known and out of reach.
// Oniguruma, Expressive Code's regex engine, reads `\w`, `\d` and `\b` as
// Unicode, and JavaScript's RegExp as ASCII. `\s` takes U+0085 in Oniguruma
// and U+FEFF in JavaScript, and not the other. With the `m` flag, JavaScript's
// `.`, `^` and `$` also take "\r", U+2028 and U+2029 as line ends, where
// Oniguruma takes only "\n": shiki scopes the "\r" of "# comment\r" as the
// comment and this leaves it plain. None of these characters reaches the
// generator: every value the form accepts is ASCII (the patterns in
// src/data/rsc/spec.json's `bounds.regex`), so is every script the steps spec
// renders, and a script's lines are joined with "\n" alone.
//
// Pure: no eval, no WebAssembly; on the server it returns HTML, and in the
// browser it builds nodes with createElement and textContent, never innerHTML.

import grammar from "../languages/routeros.tmLanguage.json" with { type: "json" };
import palette from "../languages/routeros.palette.json" with { type: "json" };

/**
 * A grammar rule as the tokenizer runs it.
 * @typedef {object} Rule
 * @property {RegExp} [match]
 * @property {RegExp} [begin]
 * @property {RegExp} [end]
 * @property {string[]} names the rule's `name`, split at spaces
 * @property {string[]} contentNames
 * @property {(string[] | null)[]} captures by group number
 * @property {(string[] | null)[]} beginCaptures
 * @property {(string[] | null)[]} endCaptures
 * @property {Rule[]} patterns
 */

/**
 * One open begin/end region, as vscode-textmate's StateStack keeps it.
 * @typedef {object} Frame
 * @property {Rule} rule
 * @property {string[]} nameScopes the scopes of the region's begin and end
 * @property {string[]} contentScopes the scopes of what is between them
 * @property {number} enterPos where the scan that opened it started, on this line; -1 on a later one
 */

/** @typedef {{ text: string, scope: string }} Token */

const RULE_KEYS = new Set([
	"name",
	"contentName",
	"match",
	"captures",
	"begin",
	"end",
	"beginCaptures",
	"endCaptures",
	"patterns",
	"include",
	"comment",
]);

/**
 * Refuses the Oniguruma syntax, of what this knows of, that JavaScript's
 * RegExp without the `u` flag reads differently or not at all: the escapes
 * `\G`, `\A`, `\z`, `\Z`, `\h`, `\H`, `\K`, `\R`, `\X`, `\y`, `\Y`, `\N`,
 * `\O`, `\o`, `\g`, `\k`, `\p`, `\P`, `\e` and `\a`; `\x` without two hex
 * digits and `\u` without four (`\x{41}` is "A" in Oniguruma and 41 "x" in
 * JavaScript); back-references; possessive quantifiers; `{,n}`; a group other
 * than `(?:`, `(?=`, `(?!`, `(?<=` and `(?<!` (atomic, named, inline flags,
 * comments); a `[` inside a class (nested classes, POSIX brackets); `&&`
 * inside a class; and a class that opens with `]`. It is a list, not a
 * proof: syntax it does not name passes, and the grammar check runs its
 * assertions through this tokenizer as well as through shiki.
 * @param {string} src
 */
function checkPattern(src) {
	const refuse = (/** @type {string} */ what) => {
		throw new Error(`rsc-highlight: ${what} in /${src}/`);
	};
	let inClass = false;
	let quantified = false;
	for (let i = 0; i < src.length; i++) {
		const c = src[i];
		if (c === "\\") {
			const n = src[i + 1] ?? "";
			if (/[GAzZhHKRXyYNOogkpPea]/.test(n)) refuse(`\\${n}`);
			if (n === "x" && !/^[\dA-Fa-f]{2}$/.test(src.slice(i + 2, i + 4)))
				refuse("`\\x` without two hex digits");
			if (n === "u" && !/^[\dA-Fa-f]{4}$/.test(src.slice(i + 2, i + 6)))
				refuse("`\\u` without four hex digits");
			if (!inClass && /[1-9]/.test(n)) refuse("a back-reference");
			i++;
			quantified = false;
			continue;
		}
		if (inClass) {
			if (c === "[") refuse("a `[` inside a class");
			if (c === "&" && src[i + 1] === "&") refuse("a class intersection");
			if (c === "]") inClass = false;
			continue;
		}
		if (c === "[") {
			inClass = true;
			if (src[i + 1] === "^") i++;
			// Oniguruma reads `[]a]` as a class holding `]`, JavaScript as an
			// empty class followed by `a]`.
			if (src[i + 1] === "]") refuse("a class that opens with `]`");
			quantified = false;
			continue;
		}
		if (c === "{" && src[i + 1] === ",")
			refuse("`{,n}`, a literal in JavaScript");
		if (
			c === "(" &&
			src[i + 1] === "?" &&
			!/^\?(?::|=|!|<=|<!)/.test(src.slice(i + 1, i + 4))
		) {
			refuse("a group JavaScript does not read as Oniguruma does");
		}
		if (c === "+" && quantified) refuse("a possessive quantifier");
		// `*?` is lazy, and a `+` after it is not a quantifier's either.
		quantified = /[*+?}]/.test(c) && !(c === "?" && quantified);
	}
}

/**
 * The tokenizer's form of a grammar. Throws on anything it does not
 * implement.
 * @param {any} g a TextMate grammar, as JSON
 */
export function compile(g) {
	for (const k of Object.keys(g)) {
		if (
			![
				"$schema",
				"name",
				"aliases",
				"scopeName",
				"patterns",
				"repository",
				"comment",
				"displayName",
			].includes(k)
		) {
			throw new Error(`rsc-highlight: unsupported grammar key ${k}`);
		}
	}
	const repository = g.repository ?? {};
	/** @type {Map<string, RegExp>} one RegExp per source, so a search is remembered across rules */
	const regexes = new Map();
	/** @param {string} src */
	const re = (src) => {
		let rx = regexes.get(src);
		if (!rx) {
			checkPattern(src);
			// d: capture positions; g: search from lastIndex; m: `$` before the
			// "\n" every line carries, as in Oniguruma.
			rx = new RegExp(src, "dgm");
			regexes.set(src, rx);
		}
		return rx;
	};
	/** @param {unknown} name */
	const names = (name) => {
		if (name === undefined) return [];
		if (typeof name !== "string" || name.includes("$")) {
			throw new Error(
				`rsc-highlight: unsupported scope name ${JSON.stringify(name)}`,
			);
		}
		return name.split(" ").filter(Boolean);
	};
	/** @param {any} caps */
	const captures = (caps) => {
		/** @type {(string[] | null)[]} */
		const out = [];
		for (const [i, c] of Object.entries(caps ?? {})) {
			for (const k of Object.keys(c)) {
				if (k !== "name")
					throw new Error(`rsc-highlight: unsupported capture key ${k}`);
			}
			out[Number(i)] = names(c.name);
		}
		for (let i = 0; i < out.length; i++) out[i] ??= null;
		return out;
	};
	/** @type {Map<object, Rule>} each grammar rule compiled once, so recursion closes */
	const compiled = new Map();
	let begins = 0;
	/**
	 * @param {any} r
	 * @param {Set<string>} including the includes being expanded, to refuse a loop
	 * @returns {Rule[]}
	 */
	const expand = (r, including) => {
		for (const k of Object.keys(r)) {
			if (!RULE_KEYS.has(k))
				throw new Error(`rsc-highlight: unsupported rule key ${k}`);
		}
		if (r.include !== undefined) {
			if (typeof r.include !== "string" || !r.include.startsWith("#")) {
				throw new Error(`rsc-highlight: unsupported include ${r.include}`);
			}
			const key = r.include.slice(1);
			const target = repository[key];
			if (!target) throw new Error(`rsc-highlight: no repository entry ${key}`);
			if (including.has(key))
				throw new Error(`rsc-highlight: ${key} includes itself`);
			return expand(target, new Set([...including, key]));
		}
		const done = compiled.get(r);
		if (done) return [done];
		if (r.match !== undefined) {
			/** @type {Rule} */
			const rule = {
				match: re(r.match),
				names: names(r.name),
				contentNames: [],
				captures: captures(r.captures),
				beginCaptures: [],
				endCaptures: [],
				patterns: [],
			};
			compiled.set(r, rule);
			return [rule];
		}
		if (r.begin !== undefined) {
			if (r.end === undefined)
				throw new Error("rsc-highlight: begin without end");
			begins++;
			/** @type {Rule} */
			const rule = {
				begin: re(r.begin),
				end: re(r.end),
				names: names(r.name),
				contentNames: names(r.contentName),
				captures: [],
				// vscode-textmate: `captures` stands for either when the specific one is absent.
				beginCaptures: captures(r.beginCaptures ?? r.captures),
				endCaptures: captures(r.endCaptures ?? r.captures),
				patterns: [],
			};
			// Registered before its patterns are expanded: a region's patterns
			// reach itself (a string inside an interpolation inside a string).
			compiled.set(r, rule);
			rule.patterns = (r.patterns ?? []).flatMap((/** @type {any} */ p) =>
				expand(p, new Set()),
			);
			return [rule];
		}
		if (r.patterns !== undefined) {
			return r.patterns.flatMap((/** @type {any} */ p) => expand(p, including));
		}
		throw new Error("rsc-highlight: a rule with nothing to match");
	};
	/** @type {Rule} */
	const root = {
		names: [],
		contentNames: [],
		captures: [],
		beginCaptures: [],
		endCaptures: [],
		patterns: g.patterns.flatMap((/** @type {any} */ p) =>
			expand(p, new Set()),
		),
	};
	return { scopeName: String(g.scopeName), root, begins };
}

/** @typedef {ReturnType<typeof compile>} Compiled */

/**
 * The tokens of every line of `code`, each with its scopes below the
 * grammar's root, space-separated ("" for none).
 * @param {string} code
 * @param {Compiled} g
 * @returns {Token[][]}
 */
export function tokenize(code, g) {
	/** @type {Frame[]} */
	const stack = [
		{ rule: g.root, nameScopes: [], contentScopes: [], enterPos: -1 },
	];
	return code.split("\n").map((line) => tokenizeLine(line, stack, g.begins));
}

/**
 * @param {string} line without its "\n"
 * @param {Frame[]} stack open regions, carried from line to line
 * @param {number} begins how many begin/end rules the grammar has
 * @returns {Token[]}
 */
function tokenizeLine(line, stack, begins) {
	const text = `${line}\n`;
	const length = text.length;
	/** @type {Token[]} */
	const out = [];
	let produced = 0;
	/** @param {string[]} scopes @param {number} end */
	const produce = (scopes, end) => {
		if (end <= produced) return;
		const scope = scopes.join(" ");
		const last = out.at(-1);
		if (last && last.scope === scope) last.text += text.slice(produced, end);
		else out.push({ text: text.slice(produced, end), scope });
		produced = end;
	};
	/**
	 * vscode-textmate's handleCaptures: each capture over the scopes of the
	 * capture that encloses it, in group order.
	 * @param {(string[] | null)[]} caps
	 * @param {RegExpExecArray} m
	 * @param {string[]} base
	 */
	const capture = (caps, m, base) => {
		const indices = /** @type {RegExpIndicesArray} */ (m.indices);
		const maxEnd = /** @type {[number, number]} */ (indices[0])[1];
		/** @type {{ scopes: string[], end: number }[]} */
		const local = [];
		for (let i = 0; i < Math.min(caps.length, indices.length); i++) {
			const name = caps[i];
			const span = indices[i];
			if (!name || !span || span[0] === span[1]) continue;
			if (span[0] > maxEnd) break;
			while (local.length > 0 && local[local.length - 1].end <= span[0]) {
				const done = /** @type {{ scopes: string[], end: number }} */ (
					local.pop()
				);
				produce(done.scopes, done.end);
			}
			const outer = local.length > 0 ? local[local.length - 1].scopes : base;
			produce(outer, span[0]);
			local.push({ scopes: [...outer, ...name], end: span[1] });
		}
		while (local.length > 0) {
			const done = /** @type {{ scopes: string[], end: number }} */ (
				local.pop()
			);
			produce(done.scopes, done.end);
		}
	};
	// A search from an earlier position that found a match at or after `pos`,
	// or none, is still the answer from `pos`: the leftmost match does not
	// depend on where the search started. So each regex runs once per match it
	// yields on the line, not once per position.
	/** @type {Map<RegExp, { from: number, m: RegExpExecArray | null }>} */
	const memo = new Map();
	/** @param {RegExp} rx @param {number} pos */
	const find = (rx, pos) => {
		const c = memo.get(rx);
		if (c && c.from <= pos && (c.m === null || c.m.index >= pos)) return c.m;
		rx.lastIndex = pos;
		const m = rx.exec(text);
		memo.set(rx, { from: pos, m });
		return m;
	};
	// A region opened on an earlier line cannot be the one a rule that does
	// not advance re-enters on this one.
	for (const f of stack) f.enterPos = -1;
	// A bound on the steps a line takes while the guards below work. A step
	// that neither opens nor closes a region advances, so there are at most as
	// many as positions. At one position the regions opened without advancing
	// are each a different rule (the second guard), and closing one there ends
	// the line (the first), so a position opens at most `begins` of them and
	// one that advances. Each close pops a region opened on the line or open
	// when it started. Past the bound a guard has failed: throwing, rather
	// than looping forever, fails the checks and leaves the generator showing
	// the script plain (src/lib/rsc-ui.mjs). No line of the golden scripts or
	// of the ```routeros fences took more than one step per character (941
	// lines, 2026-09-27).
	const positions = length + 1;
	const limit = (2 * begins + 3) * positions + stack.length + 1;
	let steps = 0;
	let pos = 0;
	for (;;) {
		if (++steps > limit) {
			throw new Error(
				`rsc-highlight: ${limit} steps without finishing ${JSON.stringify(line)}`,
			);
		}
		const top = stack[stack.length - 1];
		/** @type {RegExpExecArray | null} */
		let best = top.rule.end ? find(top.rule.end, pos) : null;
		/** @type {Rule | null} */
		let rule = null;
		for (const r of top.rule.patterns) {
			const m = find(/** @type {RegExp} */ (r.match ?? r.begin), pos);
			if (m && (!best || m.index < best.index)) {
				best = m;
				rule = r;
			}
		}
		if (!best) {
			produce(top.contentScopes, length);
			break;
		}
		const start = best.index;
		const end = start + best[0].length;
		const advanced = end > pos;
		if (!rule) {
			// The region's end: its own scopes, without contentName.
			produce(top.contentScopes, start);
			top.contentScopes = top.nameScopes;
			capture(top.rule.endCaptures, best, top.nameScopes);
			produce(top.nameScopes, end);
			const popped = /** @type {Frame} */ (stack.pop());
			if (!advanced && popped.enterPos === pos) {
				// Opened and closed without advancing: vscode-textmate keeps it
				// open and gives up on the rest of the line.
				stack.push(popped);
				produce(popped.contentScopes, length);
				break;
			}
		} else {
			produce(top.contentScopes, start);
			const nameScopes = [...top.contentScopes, ...rule.names];
			if (rule.begin) {
				/** @type {Frame} */
				const frame = {
					rule,
					nameScopes,
					contentScopes: nameScopes,
					enterPos: pos,
				};
				capture(rule.beginCaptures, best, nameScopes);
				produce(nameScopes, end);
				frame.contentScopes = [...nameScopes, ...rule.contentNames];
				if (!advanced) {
					// The same region opened again where it was opened, without
					// advancing: vscode-textmate drops it and gives up on the line.
					let loops = false;
					for (
						let i = stack.length - 1;
						i >= 0 && stack[i].enterPos === pos;
						i--
					) {
						if (stack[i].rule === rule) loops = true;
					}
					if (loops) {
						produce(top.contentScopes, length);
						break;
					}
				}
				stack.push(frame);
			} else {
				capture(rule.captures, best, nameScopes);
				produce(nameScopes, end);
				if (!advanced) {
					// A match that does not advance: vscode-textmate closes the
					// region it is in and gives up on the line.
					if (stack.length > 1) stack.pop();
					produce(stack[stack.length - 1].contentScopes, length);
					break;
				}
			}
		}
		if (end > pos) pos = end;
	}
	// The "\n" was only there for the regexes.
	const last = out[out.length - 1];
	last.text = last.text.slice(0, -1);
	if (last.text === "") out.pop();
	return out;
}

/** @typedef {[dark: string, light: string]} Colours */

/** @type {Record<string, Colours>} */
const PALETTE = /** @type {any} */ (palette);

/** The colours of text no rule scopes: the theme's foreground. */
export const PLAIN = PALETTE[""];

/**
 * The colours `palette` gives a scope stack. One it lacks (the lint check
 * fails on any the golden matrix or its probe reaches) takes the colours of
 * its longest known tail, the innermost scopes being the ones a theme's rules
 * name, and else the plain text's.
 * @param {Record<string, Colours>} palette
 * @param {string} scope
 * @returns {Colours}
 */
export function coloursIn(palette, scope) {
	for (let s = scope; ;) {
		const c = palette[s];
		if (c) return c;
		const i = s.indexOf(" ");
		if (i < 0) return palette[""];
		s = s.slice(i + 1);
	}
}

/** @type {Compiled | Error | undefined} */
let routeros;

/**
 * The grammar, compiled on first use. A compile that fails, as it would in a
 * browser whose RegExp lacks lookbehind, is remembered and thrown again, not
 * retried on every change to the form.
 * @returns {Compiled}
 */
function compiledGrammar() {
	if (routeros === undefined) {
		try {
			routeros = compile(grammar);
		} catch (e) {
			routeros = e instanceof Error ? e : new Error(String(e));
		}
	}
	if (routeros instanceof Error) throw routeros;
	return routeros;
}

/** @param {Colours} a @param {Colours} b */
const same = (a, b) => a[0] === b[0] && a[1] === b[1];

/**
 * `code` as runs of text with their colours, adjacent runs of one colour
 * joined; a run in the plain text's colours has PLAIN itself. Their text,
 * concatenated, is `code`.
 * @param {string} code
 * @returns {{ text: string, colours: Colours }[]}
 */
export function highlight(code) {
	const g = compiledGrammar();
	/** @type {{ text: string, colours: Colours }[]} */
	const runs = [];
	const add = (/** @type {string} */ text, /** @type {Colours} */ colours) => {
		const c = same(colours, PLAIN) ? PLAIN : colours;
		const last = runs[runs.length - 1];
		if (last && same(last.colours, c)) last.text += text;
		else runs.push({ text, colours: c });
	};
	tokenize(code, g).forEach((line, i) => {
		if (i > 0) add("\n", PLAIN);
		for (const t of line) add(t.text, coloursIn(PALETTE, t.scope));
	});
	return runs;
}

/** The custom properties Expressive Code colours a token with: --0 dark, --1 light. */
const style = (/** @type {Colours} */ c) => `--0:${c[0]};--1:${c[1]}`;

/** The style of the element the runs go into: the plain text's colours. */
export const plainStyle = style(PLAIN);

/** @param {string} s */
const escape = (s) =>
	s.replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;");

/**
 * `code` as HTML for the server render: a span per coloured run, plain text
 * bare. Its text content is `code`.
 * @param {string} code
 */
export function highlightHtml(code) {
	return highlight(code)
		.map((r) =>
			r.colours === PLAIN
				? escape(r.text)
				: `<span style="${style(r.colours)}">${escape(r.text)}</span>`,
		)
		.join("");
}

/**
 * Replaces `el`'s content with `code`, coloured. Built from text nodes and
 * spans, so a value typed into the form is never read as HTML. Throws, with
 * `el` untouched, when the grammar does not compile.
 * @param {HTMLElement} el
 * @param {string} code
 */
export function highlightInto(el, code) {
	const runs = highlight(code);
	// Through CSSOM, which a Content-Security-Policy's style-src does not govern.
	el.style.setProperty("--0", PLAIN[0]);
	el.style.setProperty("--1", PLAIN[1]);
	el.replaceChildren(
		...runs.map((r) => {
			if (r.colours === PLAIN) return document.createTextNode(r.text);
			const span = document.createElement("span");
			span.style.setProperty("--0", r.colours[0]);
			span.style.setProperty("--1", r.colours[1]);
			span.textContent = r.text;
			return span;
		}),
	);
}
