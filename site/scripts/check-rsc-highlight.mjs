#!/usr/bin/env node
/**
 * The script generator colours RouterOS exactly as Expressive Code does.
 *
 * A ```routeros fence is coloured at build time by Expressive Code, through
 * shiki and the grammar in src/languages/. The generator's install, uninstall
 * and router verify commands are written in the browser, so they are
 * coloured there by src/lib/rsc-highlight.mjs, a small interpreter of the same
 * grammar, with the colours in src/languages/routeros.palette.json. Two
 * highlighters drift apart silently: a grammar change that one reads and the
 * other does not, a theme that changes a colour, a Starlight release that
 * configures Expressive Code differently. This is what notices.
 *
 * It renders a corpus through Expressive Code set up the way Starlight 0.42
 * sets it up (its own night-owl themes through its own preprocessThemes and
 * applyStarlightUiThemeColors, Expressive Code's default contrast
 * adjustment) and through the tokenizer, and:
 *
 *   1. regenerates the palette, each scope stack the tokenizer yields mapped to
 *      the colours Expressive Code gives the characters under it, and fails
 *      when one stack takes two colours (the two split the text differently)
 *      or the result differs from the committed file;
 *   2. compares every character's colour, dark and light, the tokenizer's
 *      through the committed palette against Expressive Code's, and fails on
 *      any difference;
 *   3. fails when a scope the grammar can produce is in no palette entry, so
 *      the probe below has to reach every scope name the grammar has, and
 *      when Expressive Code styles a token with anything but a colour (a font
 *      style, a background), which the palette does not carry;
 *   4. fails on an Expressive Code option in astro.config.mjs or
 *      ec.config.mjs that the setup here does not model: a theme, a contrast
 *      minimum, a theme customisation, another shiki option;
 *   5. fails when a palette colour is under 4.5:1 (WCAG 2.2 AA, 1.4.3) on the
 *      generator's ground, `--ms-surface` in src/styles/theme.css, which is not
 *      the ground Expressive Code adjusted the colours for.
 *
 * The corpus: every case of the golden matrix (its install script, the
 * uninstall script src/lib/rsc.mjs renders for it, and its router verify
 * commands) and PROBE below. Whitespace is not compared: it has no colour to
 * see, and Expressive Code puts a line's indentation in a span of its own that
 * carries none.
 *
 * Plain Node, no build. Starlight's theming module is imported by its path in
 * the package, because the package does not export it; if a release moves it,
 * this fails on the import rather than on a wrong colour.
 *
 * Usage:
 *   node scripts/check-rsc-highlight.mjs           check
 *   node scripts/check-rsc-highlight.mjs --write   rewrite the palette, then check
 */
import { readFileSync, realpathSync, writeFileSync } from "node:fs";
import { findPackageJSON } from "node:module";
import path from "node:path";
import process from "node:process";
import { fileURLToPath, pathToFileURL } from "node:url";

import { createRenderer } from "@astrojs/starlight/expressive-code";
import * as prettier from "prettier";

import ecConfig from "../ec.config.mjs";
import grammar from "../src/languages/routeros.tmLanguage.json" with { type: "json" };
import committed from "../src/languages/routeros.palette.json" with { type: "json" };
import { readRelease, withVersion } from "../src/lib/release.mjs";
import { compile, coloursIn, tokenize } from "../src/lib/rsc-highlight.mjs";
import { renderUninstall, resolve, verifyCommands } from "../src/lib/rsc.mjs";

const SITE = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const PALETTE_FILE = path.join(SITE, "src/languages/routeros.palette.json");
const WRITE = process.argv.includes("--write");

/** @type {string[]} */
const problems = [];
const fail = (/** @type {string} */ msg) => problems.push(msg);

/**
 * A line of each construct the grammar has a rule for, so that every scope
 * it can produce is compared, whether or not a golden script reaches it
 * today. A terminal session runs to the end of a block, so it comes last.
 */
const PROBE = [
	"# a comment",
	':local s "a\\"b\\nc \\$ $v $[:len $x] $(1 + 2)"; # after a statement',
	':global "my-var" 5; :put $"my-var"; :put $1',
	':set s ($s . "x"); :log info "done"',
	":foreach k,v in=[/ip/route/find] do={ :put $k }",
	":for i from=1 to=10 step=2 do={ :put $i }",
	':onerror e in={ :error "x" } do={ :log warning $e }',
	':if ([/system/resource/get version] ~ "^7[.]2") do={ :return true } else={ :retry }',
	":while ($i < 4 && !$d || $a != 2 || $b >= 3) do={ :delay 1s }",
	"/ip/firewall/filter/add chain=input action=accept protocol=tcp dst-port=80,443,8000-8080 connection-state=established,related log=no disabled=yes",
	"/ip firewall filter add chain=forward",
	"/ip address",
	"/import file-name=install.rsc",
	"/user/group/add policy=read,!write password=<generate> mac-address=AA:BB:CC:DD:EE:FF address=10.0.0.1/24 v6=fd00::1/64 timeout=10s at=12:30:00 memory-max=64M count=3",
	'/interface/print as-value .proplist=name where name="x" or topics~"y" and !disabled and address in 10.0.0.0/8',
	"/interface/bridge/port/monitor [find] once; :put ($d->0 * 2)",
	':put [/file/get [find name="a"] contents]; { remove [find] }',
	':put "a line that goes on \\',
	'  here"',
	":foreach p in=[/x/find] do={ \\",
	"  :put ($p - 1 / 2 % 3) }",
	'/system/logging/print where action="disk"',
	"#  TOPICS  ACTION",
	"30 dns     disk",
	"Flags: X - disabled",
	"name: value here",
	" 0   ;;; a comment of print",
	"[admin@MikroTik] > /ip/address/print",
	" 0   192.168.88.1/24",
	"[admin@MikroTik] /ip address> print",
	"{... :put 1",
].join("\n");

/* ----------------------------------------------------- the options modelled */

/**
 * The top-level keys of the object literal after `key:` in `source`, and its
 * text. Enough of a parser for astro.config.mjs's `expressiveCode` object,
 * and it throws rather than guess when it cannot find one.
 * @param {string} source
 * @param {string} key
 */
function objectAt(source, key) {
	const at = new RegExp(`\\b${key}\\s*:\\s*\\{`).exec(source);
	if (!at) throw new Error(`no \`${key}: {\` in astro.config.mjs`);
	const start = at.index + at[0].length;
	let depth = 1;
	let i = start;
	/** @type {string[]} */
	const keys = [];
	let token = "";
	while (i < source.length && depth > 0) {
		const c = source[i];
		if (c === "/" && source[i + 1] === "/") {
			i = source.indexOf("\n", i);
			continue;
		}
		if (c === "/" && source[i + 1] === "*") {
			i = source.indexOf("*/", i) + 2;
			continue;
		}
		if (c === '"' || c === "'" || c === "`") {
			const close = source.indexOf(c, i + 1);
			i = close + 1;
			token = "";
			continue;
		}
		if ("{[(".includes(c)) depth++;
		if ("}])".includes(c)) depth--;
		if (depth === 1 && c === ":" && token.trim()) keys.push(token.trim());
		if (/[\w$]/.test(c)) token += c;
		else if (c !== ":") token = "";
		i++;
	}
	return { keys, text: source.slice(start, i - 1) };
}

{
	const astroConfig = readFileSync(path.join(SITE, "astro.config.mjs"), "utf8");
	const ec = objectAt(astroConfig, "expressiveCode");
	for (const k of ec.keys) {
		if (!["emitExternalStylesheet", "shiki"].includes(k)) {
			fail(
				`astro.config.mjs expressiveCode.${k}: an option this check does not model; model it here or leave it out`,
			);
		}
	}
	const shiki = objectAt(ec.text, "shiki");
	for (const k of shiki.keys) {
		if (k !== "langs")
			fail(`astro.config.mjs expressiveCode.shiki.${k}: not modelled here`);
	}
	// `langs: [routeros]`, or with a JSDoc cast: `[/** @type {any} */ (routeros)]`.
	const langs =
		/\blangs\s*:\s*\[\s*(?:\/\*\*[^*]*\*\/\s*\(\s*(\w+)\s*\)|(\w+))\s*\]/.exec(
			shiki.text,
		);
	const imported =
		langs &&
		new RegExp(
			`import\\s+${langs[1] ?? langs[2]}\\s+from\\s+"\\./src/languages/routeros\\.tmLanguage\\.json"`,
		).test(astroConfig);
	if (!imported) {
		fail(
			"astro.config.mjs expressiveCode.shiki.langs is not [the grammar in src/languages/routeros.tmLanguage.json]",
		);
	}
	for (const k of Object.keys(ecConfig)) {
		if (!["plugins", "logger"].includes(k))
			fail(`ec.config.mjs ${k}: an option this check does not model`);
	}
	for (const p of ecConfig.plugins ?? []) {
		// It regroups the spans of a `wrap` block and changes no colour.
		if (p.name !== "mikroscope-wrap-words")
			fail(`ec.config.mjs: plugin ${p.name} is not modelled here`);
	}
}

/* ------------------------------------------------------------------ corpus */

const { version } = readRelease(path.dirname(SITE));
const read = (/** @type {string} */ f) =>
	withVersion(
		readFileSync(path.join(SITE, "src/data/rsc", f), "utf8"),
		version,
	);
/** @type {import("../src/lib/rsc.mjs").Spec} */
const spec = JSON.parse(read("spec.json"));
/** @type {{ id: string, options: import("../src/lib/rsc.mjs").GenOptions }[]} */
const cases = JSON.parse(read("cases.json"));

/** @type {{ name: string, code: string }[]} */
const corpus = [{ name: "probe", code: PROBE }];
for (const c of cases) {
	corpus.push({ name: `${c.id} install`, code: read(`cases/${c.id}.rsc`) });
	corpus.push({
		name: `${c.id} uninstall`,
		code: renderUninstall(c.options, spec),
	});
	const r = resolve(c.options, spec);
	if (!r.values || !r.predicates)
		throw new Error(`case ${c.id} does not resolve`);
	corpus.push({
		name: `${c.id} verify`,
		code: verifyCommands(r.values, r.predicates).router.join("\n"),
	});
}

/* ------------------------------------------------------- Expressive Code */

const starlight = path.dirname(
	realpathSync(
		/** @type {string} */ (
			findPackageJSON("@astrojs/starlight", import.meta.url)
		),
	),
);
const { preprocessThemes, applyStarlightUiThemeColors } = await import(
	pathToFileURL(
		path.join(starlight, "dist/integrations/expressive-code/theming.js"),
	).href
);
// As Starlight's getStarlightEcConfigPreprocessor does with no `themes`, no
// `customizeTheme` and `useStarlightUiThemeColors` left to its default.
const { ec } = await createRenderer({
	themes: preprocessThemes(undefined),
	customizeTheme: (/** @type {any} */ theme) => {
		applyStarlightUiThemeColors(theme);
		return theme;
	},
	shiki: { langs: [/** @type {any} */ (grammar)] },
	logger: {
		warn(/** @type {string} */ message) {
			throw new Error(message);
		},
	},
});

/** @typedef {{ ch: string, props: Record<string, string> }} Char */

/**
 * Each line's characters, with the custom properties Expressive Code styles
 * each with (`--0` dark, `--1` light, `--0fs`…), inherited from the spans
 * around it.
 * @param {string} code
 * @returns {Promise<Char[][]>}
 */
async function expressiveCode(code) {
	const { renderedGroupAst } = await ec.render({
		code,
		language: "routeros",
		// With a title, the frames plugin leaves the code alone. Without one it
		// takes a comment in the first four lines that ends in a file name
		// (`Container name: a.b`, a name the spec allows) for the title and
		// deletes the line, and the two sides would differ by a line.
		meta: 'title="x"',
	});
	/** @param {any} n @param {(n: any) => boolean} pred @param {any[]} out */
	const all = (n, pred, out = []) => {
		if (pred(n)) out.push(n);
		for (const c of n.children ?? []) all(c, pred, out);
		return out;
	};
	/** @param {any} n @param {string} c */
	const hasClass = (n, c) =>
		n.type === "element" &&
		[].concat(n.properties?.className ?? []).includes(c);
	return all(renderedGroupAst, (n) => hasClass(n, "ec-line")).map((line) => {
		/** @type {Char[]} */
		const chars = [];
		/** @param {any} n @param {Record<string, string>} props */
		const walk = (n, props) => {
			if (n.type === "text") {
				for (const ch of n.value) chars.push({ ch, props });
				return;
			}
			const own = Object.fromEntries(
				String(n.properties?.style ?? "")
					.split(";")
					.filter((d) => d.trim().startsWith("--"))
					.map((d) => [
						d.slice(0, d.indexOf(":")).trim(),
						d.slice(d.indexOf(":") + 1).trim(),
					]),
			);
			for (const c of n.children ?? []) walk(c, { ...props, ...own });
		};
		walk(all(line, (n) => hasClass(n, "code"))[0], {});
		// An empty line is written as one "\n", so it keeps its height.
		if (chars.length === 1 && chars[0].ch === "\n") chars.length = 0;
		return chars;
	});
}

/* ------------------------------------------------------------- comparison */

const compiled = compile(grammar);

/** @type {{ where: string, ch: string, scope: string, props: Record<string, string> }[]} */
const rows = [];
for (const item of corpus) {
	const code = item.code.replace(/\n$/, "");
	const ecLines = await expressiveCode(code);
	const tkLines = tokenize(code, compiled);
	if (ecLines.length !== tkLines.length) {
		fail(
			`${item.name}: ${ecLines.length} lines in Expressive Code, ${tkLines.length} in the tokenizer`,
		);
		continue;
	}
	ecLines.forEach((chars, li) => {
		const scopes = tkLines[li].flatMap((t) => [...t.text].map(() => t.scope));
		const ecText = chars.map((c) => c.ch).join("");
		const tkText = tkLines[li].map((t) => t.text).join("");
		if (ecText !== tkText) {
			fail(
				`${item.name} line ${li + 1}: the text differs (${JSON.stringify(ecText)} / ${JSON.stringify(tkText)})`,
			);
			return;
		}
		chars.forEach((c, ci) => {
			if (/\s/.test(c.ch)) return;
			rows.push({
				where: `${item.name} line ${li + 1}:${ci + 1} ${JSON.stringify(ecText.slice(Math.max(0, ci - 12), ci + 12))}`,
				ch: c.ch,
				scope: scopes[ci],
				props: c.props,
			});
		});
	});
}

// What the palette carries: a colour per theme and nothing else.
for (const r of rows) {
	const other = Object.keys(r.props).filter((k) => k !== "--0" && k !== "--1");
	if (other.length > 0) {
		fail(
			`${r.where}: Expressive Code sets ${other.join(", ")}, which the palette does not carry`,
		);
	}
	if (!r.props["--0"] || !r.props["--1"]) {
		fail(`${r.where}: Expressive Code gives no colour for one of the themes`);
	}
}

/** @type {Map<string, Map<string, number>>} */
const seen = new Map();
for (const r of rows) {
	const key = JSON.stringify([r.props["--0"], r.props["--1"]]);
	const counts = seen.get(r.scope) ?? new Map();
	counts.set(key, (counts.get(key) ?? 0) + 1);
	seen.set(r.scope, counts);
}
/** @type {Record<string, [string, string]>} */
const palette = {};
for (const [scope, counts] of [...seen].sort(([a], [b]) =>
	a < b ? -1 : a > b ? 1 : 0,
)) {
	const ranked = [...counts].sort((a, b) => b[1] - a[1]);
	palette[scope] = JSON.parse(ranked[0][0]);
	if (ranked.length > 1) {
		fail(
			`scope ${JSON.stringify(scope)} takes ${ranked.length} colours in Expressive Code (${ranked.map(([k, n]) => `${k} ×${n}`).join(", ")}): the tokenizer and shiki split the text differently`,
		);
	}
}
if (!palette[""])
	fail("the corpus has no plain text, so the palette has no foreground");

const same = JSON.stringify(palette) === JSON.stringify(sortKeys(committed));
if (!same && WRITE) {
	const options = await prettier.resolveConfig(PALETTE_FILE);
	writeFileSync(
		PALETTE_FILE,
		await prettier.format(JSON.stringify(palette), {
			...options,
			parser: "json",
		}),
	);
	console.log(
		`rsc-highlight: wrote ${path.relative(SITE, PALETTE_FILE)} (${Object.keys(palette).length} scope stacks)`,
	);
} else if (!same) {
	fail(
		`src/languages/routeros.palette.json is not what Expressive Code colours today; run node scripts/check-rsc-highlight.mjs --write and review the diff`,
	);
}

// Every character through the palette the page ships.
const shipped = WRITE
	? palette
	: /** @type {Record<string, [string, string]>} */ (committed);
let differ = 0;
for (const r of rows) {
	const [dark, light] = coloursIn(shipped, r.scope);
	if (dark !== r.props["--0"] || light !== r.props["--1"]) {
		differ++;
		if (differ <= 20) {
			fail(
				`${r.where}: tokenizer ${dark}/${light} (${r.scope || "plain"}), Expressive Code ${r.props["--0"]}/${r.props["--1"]}`,
			);
		}
	}
}
if (differ > 20)
	fail(`… and ${differ - 20} more characters coloured differently`);

// Every scope the grammar can produce is reached by the corpus.
/** @type {Set<string>} */
const names = new Set();
/** @param {any} r */
const collect = (r) => {
	for (const n of [r.name, r.contentName])
		if (n) for (const s of n.split(" ")) names.add(s);
	for (const caps of [r.captures, r.beginCaptures, r.endCaptures]) {
		for (const c of Object.values(caps ?? {})) {
			if (/** @type {any} */ (c).name)
				for (const s of /** @type {any} */ (c).name.split(" ")) names.add(s);
		}
	}
	for (const p of r.patterns ?? []) collect(p);
};
for (const r of [...grammar.patterns, ...Object.values(grammar.repository)])
	collect(r);
const reached = new Set(Object.keys(palette).flatMap((k) => k.split(" ")));
for (const n of [...names].sort()) {
	if (!reached.has(n))
		fail(`no text in the corpus reaches ${n}; add a line to PROBE that does`);
}

// Legible on the generator's own ground.
const themeCss = readFileSync(path.join(SITE, "src/styles/theme.css"), "utf8");
/** @param {string} selector */
const surface = (selector) => {
	const block = new RegExp(
		`^${selector.replace(/[\\^$.*+?()[\]{}|]/g, "\\$&")}\\s*\\{([\\s\\S]*?)^\\}`,
		"m",
	).exec(themeCss);
	const value = block && /--ms-surface:\s*(#[0-9a-fA-F]{6})\s*;/.exec(block[1]);
	if (!value)
		throw new Error(`no --ms-surface in ${selector} in src/styles/theme.css`);
	return value[1];
};
const grounds = [surface(":root"), surface(':root[data-theme="light"]')];
/** @param {string} hex */
const luminance = (hex) => {
	const [r, g, b] = [1, 3, 5].map((i) => {
		const c = parseInt(hex.slice(i, i + 2), 16) / 255;
		return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
	});
	return 0.2126 * r + 0.7152 * g + 0.0722 * b;
};
/** @param {string} a @param {string} b */
const contrast = (a, b) => {
	const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p);
	return (x + 0.05) / (y + 0.05);
};
let lowest = [Infinity, Infinity];
for (const [scope, colours] of Object.entries(palette)) {
	colours.forEach((colour, theme) => {
		const ratio = contrast(colour, grounds[theme]);
		lowest[theme] = Math.min(lowest[theme], ratio);
		if (ratio < 4.5) {
			fail(
				`${scope || "plain text"}: ${colour} on ${grounds[theme]} is ${ratio.toFixed(2)}:1, under 4.5:1`,
			);
		}
	});
}

if (problems.length > 0) {
	console.error(
		`rsc-highlight: ${problems.length} problem${problems.length === 1 ? "" : "s"}\n`,
	);
	for (const p of problems) console.error(`  ${p}`);
	process.exit(1);
}
console.log(
	`rsc-highlight: ${rows.length} characters in ${corpus.length} texts (${cases.length} cases' install, uninstall and verify, and the probe), each coloured as Expressive Code colours it in both themes; ${Object.keys(palette).length} scope stacks, every scope of the grammar reached; lowest contrast on the generator's ground ${lowest[0].toFixed(2)}:1 dark, ${lowest[1].toFixed(2)}:1 light`,
);

/** @param {Record<string, any>} o */
function sortKeys(o) {
	return Object.fromEntries(
		Object.entries(o).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)),
	);
}
