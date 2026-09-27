#!/usr/bin/env node
/**
 * The RouterOS grammar (src/languages/routeros.tmLanguage.json) scopes what
 * it is meant to, in every RouterOS text the site shows.
 *
 * Four parts, through the shiki Expressive Code itself loads (resolved
 * through its own dependencies, so a shiki upgrade there is what runs here)
 * and through the script generator's tokenizer, src/lib/rsc-highlight.mjs,
 * with a scope name per token:
 *
 *   1. CASES: an input and what each part of it must, or must not, be scoped
 *      as. Each is a construct of mikroscope's scripts that the jmrp.io
 *      grammar this one derives from got wrong (src/languages/README.md), or a
 *      shape a fix must not break. They run three ways. Through shiki with
 *      Oniguruma, the engine Expressive Code uses. Through shiki with its
 *      JavaScript engine, which translates Oniguruma's syntax for RegExp
 *      (oniguruma-to-es), so it shows that shiki could run the grammar
 *      without WebAssembly and not that RegExp reads a pattern as written.
 *      And through the tokenizer, which hands each pattern to RegExp as
 *      written and refuses, when it compiles the grammar, the syntax it knows
 *      RegExp reads differently: a pattern only Oniguruma reads fails there.
 *   2. The corpus: every golden case's install script, the uninstall script
 *      src/lib/rsc.mjs renders for it, the generator's defaults, every
 *      <ManualSteps> the pages use and every ```routeros fence in English and
 *      Spanish, held to rules no RouterOS text breaks: no unquoted value
 *      swallows a bracket, a quote or a `$`; every `$var`, `/path` and
 *      `:command` outside a string or comment is scoped as one; control words
 *      are keywords; no brace is part of a string token; a duration is a
 *      number; a line that starts with `#` is a comment throughout; no string
 *      is left open at the end of a line without a `\`. A line may be scoped
 *      as command output only on a page in OUTPUT_LINES, as often as it says.
 *   3. The corpus again, through the tokenizer: every character must carry
 *      the scopes shiki with Oniguruma gives it.
 *   4. SYNTHETIC: made-up grammars through the tokenizer and shiki with
 *      Oniguruma, character for character. They reach what this grammar
 *      does not, among it the three guards vscode-textmate keeps against a
 *      rule that matches without advancing, so the code that runs them in the
 *      browser is held to vscode-textmate before a grammar needs it.
 *
 * Plain Node, no build.
 *
 * Usage: node scripts/check-routeros-grammar.mjs
 */
import { readFileSync, readdirSync, realpathSync } from "node:fs";
import { createRequire, findPackageJSON } from "node:module";
import path from "node:path";
import process from "node:process";
import { fileURLToPath, pathToFileURL } from "node:url";

import grammar from "../src/languages/routeros.tmLanguage.json" with { type: "json" };
import { readRelease, withVersion } from "../src/lib/release.mjs";
import { compile, tokenize } from "../src/lib/rsc-highlight.mjs";
import {
	generatorDefaults,
	manualCode,
	renderUninstall,
} from "../src/lib/rsc.mjs";

const SITE = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const DOCS = path.join(SITE, "src/content/docs");

/**
 * Lines the grammar may scope as command output, per page (without its
 * locale), counted over the page's ```routeros fences. Any other line scoped
 * as output is a command that lost its colours.
 */
const OUTPUT_LINES = {
	// `/system/logging/print` and the table it prints: a header and one row.
	"playbooks/flash-wear.mdx": 2,
};

/**
 * Each case: an id, the input, and [needle, expected] pairs. The needle is
 * found in the input ("text@N" for its Nth occurrence, from 0); every token
 * under it with more than whitespace must carry a scope starting with
 * `expected` (".routeros" implied), or none but meta.* and punctuation.* for
 * "none", or no scope starting with the rest for "!…".
 */
const CASES = [
	// --- colon commands and control flow
	[
		"C1 :if keyword",
		':if ([:len [/file/find name="a"]] = 0) do={ :error "x" }',
		[
			[":if", "keyword.control"],
			[":len", "support.function"],
			[":error", "support.function"],
			["do", "keyword.control"],
			["{", "!string"],
			["=@1", "keyword.operator"],
		],
	],
	[
		"C2 :do / on-error",
		':do { /container/stop [find comment="T"] } on-error={}',
		[
			[":do", "keyword.control"],
			["on-error", "keyword.control"],
			["{}", "!string"],
			["find", "entity.name.function"],
		],
	],
	[
		"C3 :while",
		":local w 0; :while ($w < 120) do={ :delay 1s; :set w ($w + 1) }",
		[
			[":local", "storage.type"],
			["w", "variable.other"],
			[":while", "keyword.control"],
			["$w", "variable.other"],
			["1s", "constant.numeric"],
			[":set", "support.function"],
			["w@2", "variable.other"],
			["+", "keyword.operator"],
		],
	],
	[
		"C4 :foreach k,v in=",
		":foreach p in=[/interface/bridge/port/find] do={ :put $p }",
		[
			[":foreach", "keyword.control"],
			["p", "variable.other"],
			["in", "keyword.control"],
			["/interface/bridge/port", "entity.name.tag"],
			["find", "entity.name.function"],
			["[", "!string"],
			["do", "keyword.control"],
		],
	],
	[
		"C5 :for from/to/step",
		":for i from=1 to=10 step=2 do={ :put $i }",
		[
			[":for", "keyword.control"],
			["from", "keyword.control"],
			["to", "keyword.control"],
			["step", "keyword.control"],
			["10", "constant.numeric"],
		],
	],
	[
		"C6 else=",
		":if ($a) do={ :put 1 } else={ :put 2 }",
		[
			["else", "keyword.control"],
			["{@1", "!string"],
		],
	],
	[
		"C7 :global quoted name",
		':global "my-var" 5; :put $"my-var"',
		[
			[":global", "storage.type"],
			['"my-var"', "variable.other"],
			['$"my-var"', "variable.other"],
		],
	],
	// --- variables
	[
		"V1 $var not eating ->",
		":put ($d->0)",
		[
			["$d", "variable.other"],
			["->", "keyword.operator.accessor"],
			["0", "constant.numeric"],
		],
	],
	[
		"V2 $var in value",
		'/file/set [find name="m"] contents=$m',
		[
			["contents", "support.type.property-name"],
			["$m", "variable.other"],
			["find", "entity.name.function"],
		],
	],
	[
		"V3 $var before ]]",
		"[:len [/interface/bridge/host/find interface=$n]]",
		[
			["$n", "variable.other"],
			["]]", "!string"],
		],
	],
	[
		"V4 $var in string",
		':put "hosts=$n up $[:len $x]"',
		[
			["$n", "variable.other"],
			[":len", "support.function"],
			["$x", "variable.other"],
		],
	],
	// --- strings and escapes
	[
		"S1 escaped quote",
		':put "a \\"q\\" b"; :put 1',
		[
			['\\"', "constant.character.escape"],
			[":put@1", "support.function"],
			["1", "constant.numeric"],
		],
	],
	[
		"S2 \\$ and \\\\",
		':put "cost \\$5 \\\\ path"',
		[
			["\\$", "constant.character.escape"],
			["\\\\", "constant.character.escape"],
			["5", "string.quoted"],
		],
	],
	[
		"S3 \\n in manifest",
		':local m "a=1\\nb=2\\n"',
		[
			["\\n", "constant.character.escape"],
			["a=1", "string.quoted"],
		],
	],
	[
		"S4 hash in string",
		'/ip/firewall/filter/print where comment="a # b"',
		[
			["a # b", "string.quoted"],
			["a # b", "!comment"],
		],
	],
	[
		"S5 regex after ~",
		':if ([/system/resource/get version] ~ "^7[.]2[4-9]") do={}',
		[
			["^7[.]2[4-9]", "string.regexp"],
			["~", "keyword.operator"],
			["version", "none"],
		],
	],
	[
		"S6 topics~",
		'/log/print where topics~"container"',
		[
			["topics", "support.type.property-name"],
			["container", "string"],
			["where", "keyword"],
		],
	],
	// --- comments
	[
		"K1 line comment",
		"# install manifest mikroscope/x.txt",
		[["# install", "comment"]],
	],
	[
		"K2 comment after ;",
		":global a 1; # a comment",
		[["# a comment", "comment"]],
	],
	[
		"K3 # glued to value",
		"/interface/wifi/set [find] ssid=Cafe#1",
		[
			["Cafe#1", "string.unquoted"],
			["Cafe#1", "!comment"],
		],
	],
	["K4 indented comment", "  # indented", [["# indented", "comment"]]],
	// --- line continuation
	[
		"L1 trailing backslash",
		":foreach p in=[/x/find] do={ \\\n  :put $p }",
		[
			["\\", "constant.character.escape"],
			[":put", "support.function"],
		],
	],
	// --- key=value
	[
		"A1 quoted value key",
		'/system/package/find name="container" disabled=no',
		[
			["name", "support.type.property-name"],
			["disabled", "support.type.property-name"],
			["no", "constant.language.boolean"],
		],
	],
	[
		"A2 unquoted value",
		"/container/add interface=veth-mikroscope root-dir=disk1/ms logging=yes",
		[
			["interface", "support.type.property-name"],
			["veth-mikroscope", "string.unquoted"],
			["disk1/ms", "string.unquoted"],
			["yes", "constant.language.boolean"],
		],
	],
	[
		"A3 list with commas and !",
		"/user/group/add policy=read,api,!write",
		[
			["read", "string.unquoted"],
			[",", "punctuation.separator"],
			["!", "keyword.operator"],
			["write", "string.unquoted"],
		],
	],
	[
		"A4 IP list",
		"/ip/dns/set servers=1.1.1.1,8.8.8.8",
		[
			["1.1.1.1", "constant.numeric"],
			["8.8.8.8", "constant.numeric"],
		],
	],
	[
		"A5 enum boundary",
		"/ip/firewall/filter/add protocol=udp-lite chain=input-custom action=accept",
		[
			["udp-lite", "constant.language"],
			["input-custom", "string.unquoted"],
			["accept", "support.function"],
		],
	],
	[
		"A6 bool boundary",
		"/ip/dns/set allow-remote-requests=yes dns-log=yes",
		[
			["dns-log", "support.type.property-name"],
			["yes@1", "constant.language.boolean"],
		],
	],
	[
		"A7 paren value",
		"/ip/firewall/filter/add place-before=($d->0)",
		[
			["place-before", "support.type.property-name"],
			["$d", "variable.other"],
			["(", "!string"],
		],
	],
	[
		"A8 placeholder",
		"/user/add password=<generate> address=<collector host>/32",
		[
			["<generate>", "constant.other.placeholder"],
			["<collector host>", "constant.other.placeholder"],
		],
	],
	[
		"A9 image ref",
		"/container/add remote-image=jmrplens/mikroscope-agent:1.4.0",
		[["jmrplens/mikroscope-agent:1.4.0", "string.unquoted"]],
	],
	[
		"A10 .proplist",
		"/interface/print as-value .proplist=name",
		[
			[".proplist", "support.type.property-name"],
			["as-value", "support.type.property-name"],
		],
	],
	// --- literals
	[
		"N1 CIDR in value",
		"/ip/address/add address=172.30.10.1/30",
		[["172.30.10.1/30", "constant.numeric"]],
	],
	[
		"N2 duration in value",
		"/container/add restart-interval=10s memory-max=64M",
		[
			["10s", "constant.numeric"],
			["64M", "constant.numeric"],
		],
	],
	["N3 bare duration", ":delay 4s", [["4s", "constant.numeric"]]],
	[
		"N4 MAC",
		"/interface/bridge/host/print where mac-address=AA:BB:CC:DD:EE:FF",
		[["AA:BB:CC:DD:EE:FF", "constant.numeric"]],
	],
	[
		"N5 port list",
		"/ip/firewall/filter/add dst-port=80,443,8000-8080",
		[
			["80", "constant.numeric"],
			["8000-8080", "constant.numeric"],
		],
	],
	[
		"N6 IPv6",
		"/ipv6/address/add address=fd00::1/64",
		[["fd00::1/64", "constant.numeric"]],
	],
	[
		"N7 CIDR in where",
		"/ip/address/print where address in 172.30.10.0/30",
		[
			["172.30.10.0/30", "constant.numeric"],
			["in@1", "keyword.operator"],
		],
	],
	[
		"N8 version tag not numeric",
		"/container/add remote-image=ghcr.io/jmrplens/mikroscope-agent:1.4.0",
		[["1.4.0", "!constant.numeric"]],
	],
	// --- negation and operators
	[
		"O1 ! negation",
		":if (!([:len $x] = 0)) do={}",
		[["!", "keyword.operator"]],
	],
	[
		"O2 != and &&",
		':if ($dm != "yes" && $dm != "true") do={}',
		[
			["!=", "keyword.operator"],
			["&&", "keyword.operator"],
		],
	],
	["O3 concat", ':put ($n . " hosts")', [[".", "keyword.operator"]]],
	[
		"O4 and/or",
		'/log/print where topics~"a" or topics~"b"',
		[["or", "keyword.operator"]],
	],
	// --- paths
	[
		"P1 slash path + command",
		"/interface/veth/add name=x",
		[
			["/interface/veth", "entity.name.tag"],
			["add", "entity.name.function"],
		],
	],
	[
		"P2 /disk",
		"/disk/add type=tmpfs",
		[
			["/disk", "entity.name.tag"],
			["add", "entity.name.function"],
		],
	],
	[
		"P3 /import",
		"/import file-name=install.rsc",
		[["/import", "entity.name.function"]],
	],
	[
		"P4 space form",
		"/ip firewall filter add chain=input",
		[
			["/ip", "entity.name.tag"],
			["firewall", "entity.name.tag"],
			["filter", "entity.name.tag"],
			["add", "entity.name.function"],
		],
	],
	[
		"P5 space form dns",
		"/ip dns set servers=1.1.1.1",
		[
			["dns", "entity.name.tag"],
			["set", "entity.name.function"],
		],
	],
	[
		"P6 menu alone",
		"/ip address",
		[
			["/ip", "entity.name.tag"],
			["address", "entity.name.tag"],
		],
	],
	[
		"P7 relative find",
		'/container/start [find comment="T"]',
		[
			["find", "entity.name.function"],
			["/container", "entity.name.tag"],
			["start", "entity.name.function"],
		],
	],
	[
		"P8 print args",
		"/interface/bridge/port/monitor [find] once",
		[
			["monitor", "entity.name.function"],
			["once", "support.type.property-name"],
		],
	],
	[
		"P9 word not a command",
		":put [/system/clock/get time]",
		[["time", "none"]],
	],
	[
		"P10 CIDR is not a path",
		"/ip/address/print where address in NET/30",
		[
			["NET", "!entity.name.tag"],
			["/30", "!entity.name.tag"],
		],
	],
	// --- terminal session and output
	[
		"T1 prompt",
		"[admin@MikroTik] > /system/resource/print",
		[
			["[admin@MikroTik] >", "meta.prompt"],
			["[admin@MikroTik] >", "none"],
			["print", "entity.name.function"],
		],
	],
	[
		"T2 output after prompt",
		"[admin@MikroTik] > /ip/address/print\nFlags: X - disabled, I - invalid; D - dynamic\n #   ADDRESS          NETWORK   INTERFACE\n 0   192.168.88.1/24  192.168.88.0  bridge",
		[
			["Flags: X - disabled", "none"],
			["-", "none"],
			["ADDRESS", "none"],
			["192.168.88.1/24", "none"],
			["bridge", "none"],
		],
	],
	[
		"T3 output without prompt",
		'/system/logging/print where action="disk"\n#  TOPICS  ACTION\n30 dns     disk',
		[
			["#  TOPICS  ACTION", "!comment"],
			["30 dns", "none"],
			["30", "!constant.numeric"],
		],
	],
	[
		"T4 print comment ;;;",
		'[admin@MikroTik] > /container/print\n 0   ;;; mikroscope:mikroscope (managed by mikroscope)\n     name="x"',
		[
			[";;; mikroscope", "comment"],
			['name="x"', "none"],
		],
	],
	[
		"T6 not a prompt: [find …@…] > 0",
		'[find comment="a@b"] > 0',
		[
			["find", "entity.name.function"],
			[">", "keyword.operator"],
			["0", "constant.numeric"],
		],
	],
	[
		"T7 SAFE prompt, identity with a space",
		"[admin@My Router] <SAFE> /ip address> print",
		[
			["[admin@My Router] <SAFE> /ip address>", "none"],
			["print", "entity.name.function"],
		],
	],
	[
		"V5 positional args",
		":global f do={ :return ($1 + $2) }",
		[
			["$1", "variable.other"],
			[":return", "keyword.control"],
		],
	],
	[
		"T8 ALL-CAPS section comment stays a comment",
		"# INTERFACE CONFIGURATION\n/interface wireguard add \\\n    name=wg1 \\  # ← CUSTOMIZE",
		[
			["# INTERFACE CONFIGURATION", "comment"],
			["wireguard", "entity.name.tag"],
			["add", "entity.name.function"],
			["\\@1", "constant.character.escape"],
			["# ← CUSTOMIZE", "comment"],
		],
	],
	[
		"T5 prompt with menu",
		"[admin@MikroTik] /ip address> print",
		[["print", "entity.name.function"]],
	],
];

/* ------------------------------------------------------------------- shiki */

/**
 * The shiki Expressive Code uses: @astrojs/starlight → astro-expressive-code
 * → rehype-expressive-code → expressive-code → @expressive-code/plugin-shiki
 * → shiki, each resolved from the one before, as Node resolves them.
 */
function shikiOfExpressiveCode() {
	let from = pathToFileURL(path.join(SITE, "package.json")).href;
	for (const name of [
		"@astrojs/starlight",
		"astro-expressive-code",
		"rehype-expressive-code",
		"expressive-code",
		"@expressive-code/plugin-shiki",
		"shiki",
	]) {
		const found = findPackageJSON(name, from);
		if (!found) throw new Error(`cannot resolve ${name} from ${from}`);
		from = pathToFileURL(realpathSync(found)).href;
	}
	const require = createRequire(from);
	return (/** @type {string} */ entry) =>
		import(pathToFileURL(require.resolve(`shiki/${entry}`)).href);
}

const shiki = shikiOfExpressiveCode();
const { createHighlighterCore } = await shiki("core");
const { createOnigurumaEngine } = await shiki("engine/oniguruma");
const { createJavaScriptRegexEngine } = await shiki("engine/javascript");
// Scope names do not depend on the theme, but shiki needs one to tokenize
// (and reads a theme named "none" as no highlighting at all).
const THEME = {
	name: "scopes-only",
	type: "dark",
	fg: "#ffffff",
	bg: "#000000",
	settings: [],
};

/** @typedef {{ text: string, scopes: string[] }} Token */

/** @typedef {"oniguruma" | "javascript" | "rsc-highlight"} Engine */

/**
 * The generator's tokenizer, in the shape shiki's tokens are read in here.
 * Throws when it refuses the grammar.
 * @param {any} g
 * @returns {(code: string) => Token[][]}
 */
function rscHighlight(g) {
	const compiled = compile(g);
	return (code) =>
		tokenize(code, compiled).map((line) =>
			line.map((t) => ({
				text: t.text,
				scopes: t.scope === "" ? [] : t.scope.split(" "),
			})),
		);
}

/**
 * @param {Engine} engine
 * @returns {Promise<(code: string) => Token[][]>}
 */
async function tokenizer(engine) {
	if (engine === "rsc-highlight") return rscHighlight(grammar);
	const highlighter = await createHighlighterCore({
		themes: [THEME],
		langs: [grammar],
		engine:
			engine === "oniguruma"
				? await createOnigurumaEngine(shiki("wasm"))
				: createJavaScriptRegexEngine({ forgiving: false }),
	});
	return shikiTokens(highlighter, grammar);
}

/**
 * shiki's tokens for the grammar `g`, from a highlighter that has loaded it,
 * each with its scopes below the grammar's root.
 * @param {any} highlighter
 * @param {any} g
 * @returns {(code: string) => Token[][]}
 */
function shikiTokens(highlighter, g) {
	return (code) =>
		highlighter
			.codeToTokensBase(code, {
				lang: g.name,
				theme: THEME.name,
				includeExplanation: "scopeName",
			})
			.map((/** @type {any[]} */ line) =>
				line.flatMap((t) =>
					(t.explanation ?? [{ content: t.content, scopes: [] }]).map(
						(/** @type {any} */ e) => ({
							text: e.content,
							scopes: e.scopes
								.map((/** @type {any} */ s) => s.scopeName)
								.filter((/** @type {string} */ s) => s !== g.scopeName),
						}),
					),
				),
			);
}

/** @type {string[]} */
const problems = [];
/** What an exception says. @param {unknown} e */
const why = (e) => (e instanceof Error ? e.message : String(e));

/* ------------------------------------------------------------------- cases */

/**
 * The tokens under a needle, or null when the needle is not in the input.
 * @param {Token[][]} lines
 * @param {string} input
 * @param {string} needle
 */
function under(lines, input, needle) {
	let occurrence = 0;
	const m = /^(.+)@(\d+)$/s.exec(needle);
	if (m) [needle, occurrence] = [m[1], Number(m[2])];
	let at = -1;
	for (let i = 0; i <= occurrence; i++) at = input.indexOf(needle, at + 1);
	if (at < 0) return null;
	/** @type {(Token & { start: number, end: number })[]} */
	const flat = [];
	let offset = 0;
	for (const line of lines) {
		for (const t of line) {
			flat.push({ ...t, start: offset, end: offset + t.text.length });
			offset += t.text.length;
		}
		offset += 1;
	}
	return flat.filter(
		(t) => t.end > at && t.start < at + needle.length && t.text.trim() !== "",
	);
}

/**
 * Each character of `lines` with its scopes, space-separated.
 * @param {Token[][]} lines
 */
const perCharacter = (lines) =>
	lines.map((line) =>
		line.flatMap((t) => [...t.text].map((ch) => [ch, t.scopes.join(" ")])),
	);

/**
 * The first character shiki with Oniguruma (`a`) and the tokenizer (`b`)
 * scope differently in one text, or null when they agree.
 * @param {Token[][]} a
 * @param {Token[][]} b
 */
function firstDifference(a, b) {
	const [x, y] = [perCharacter(a), perCharacter(b)];
	for (let li = 0; li < Math.max(x.length, y.length); li++) {
		const [p, q] = [x[li] ?? [], y[li] ?? []];
		const text = (/** @type {string[][]} */ l) => l.map((c) => c[0]).join("");
		if (text(p) !== text(q)) {
			return `line ${li + 1}: the text differs, shiki ${JSON.stringify(text(p))}, rsc-highlight ${JSON.stringify(text(q))}`;
		}
		const ci = p.findIndex((c, i) => c[1] !== q[i][1]);
		if (ci >= 0) {
			return `line ${li + 1}:${ci + 1} ${JSON.stringify(text(p).slice(Math.max(0, ci - 12), ci + 12))}: shiki [${p[ci][1] || "-"}], rsc-highlight [${q[ci][1] || "-"}]`;
		}
	}
	return null;
}

let assertions = 0;
/** @type {((code: string) => Token[][]) | null} */
let generatorTokens = null;
for (const engine of /** @type {Engine[]} */ ([
	"oniguruma",
	"javascript",
	"rsc-highlight",
])) {
	/** @type {(code: string) => Token[][]} */
	let tokens;
	try {
		tokens = await tokenizer(engine);
	} catch (e) {
		problems.push(`${engine}: ${why(e).replace(/^rsc-highlight: /, "")}`);
		continue;
	}
	if (engine === "rsc-highlight") generatorTokens = tokens;
	for (const [id, input, checks] of CASES) {
		/** @type {Token[][]} */
		let lines;
		try {
			lines = tokens(input);
		} catch (e) {
			problems.push(`${engine} ${id}: ${why(e)}`);
			continue;
		}
		for (const [needle, expected] of checks) {
			assertions++;
			const covered = under(lines, input, needle);
			const has = (/** @type {Token} */ t, /** @type {string} */ prefix) =>
				t.scopes.some((s) => s.startsWith(prefix));
			const ok =
				covered !== null &&
				covered.length > 0 &&
				(expected === "none"
					? covered.every((t) =>
							t.scopes.every((s) => /^(meta|punctuation)\./.test(s)),
						)
					: expected.startsWith("!")
						? covered.every((t) => !has(t, expected.slice(1)))
						: covered.every((t) => has(t, expected)));
			if (!ok) {
				const got = covered
					? covered
							.map(
								(t) =>
									`${JSON.stringify(t.text)}=[${t.scopes.map((s) => s.replace(/\.routeros$/, "")).join(" ") || "-"}]`,
							)
							.join(" ")
					: "(not in the input)";
				problems.push(
					`${engine} ${id}: ${JSON.stringify(needle)} should be ${expected}, is ${got}`,
				);
			}
		}
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

/**
 * Every text, with where it is shown and, for a fence, its page without the
 * locale. A script shown in several places is read once.
 * @type {{ text: string, where: string, page: string }[]}
 */
const corpus = [];
const scripts = new Set();
const add = (/** @type {string} */ text, /** @type {string} */ where) => {
	const t = text.replace(/\n$/, "");
	if (scripts.has(t)) return;
	scripts.add(t);
	corpus.push({ text: t, where, page: "" });
};
for (const c of cases) {
	add(read(`cases/${c.id}.rsc`), `src/data/rsc/cases/${c.id}.rsc`);
	add(renderUninstall(c.options, spec), `renderUninstall(${c.id})`);
}
const defaults = generatorDefaults(spec);
add(defaults.script, "the generator's install script");
add(defaults.uninstall, "the generator's uninstall script");
add(
	defaults.verify.router.join("\n"),
	"the generator's router verify commands",
);

/** @returns {Generator<string>} */
function* pages(dir = DOCS) {
	for (const e of readdirSync(dir, { withFileTypes: true })) {
		const p = path.join(dir, e.name);
		if (e.isDirectory()) yield* pages(p);
		else if (p.endsWith(".mdx")) yield p;
	}
}
let fences = 0;
for (const file of pages()) {
	const rel = path.relative(DOCS, file).split(path.sep).join("/");
	const page = rel.replace(/^es\//, "");
	const source = readFileSync(file, "utf8");
	for (const m of source.matchAll(/<ManualSteps\b([^>]*)\/>/g)) {
		const props = Object.fromEntries(
			[...m[1].matchAll(/(\w+)="([^"]*)"/g)].map((a) => [a[1], a[2]]),
		);
		delete props.title;
		add(
			manualCode(props, spec, cases),
			`${rel}: <ManualSteps${m[1].trimEnd()} />`,
		);
	}
	const lines = source.split("\n");
	for (let i = 0; i < lines.length; i++) {
		// A fence's meta (```routeros title="…") does not take it out.
		const open = /^(\s*)(`{3,})routeros(?:\s.*)?$/.exec(lines[i]);
		if (!open) continue;
		const body = [];
		let j = i + 1;
		for (; j < lines.length && !/^\s*`{3,}\s*$/.test(lines[j]); j++) {
			body.push(
				lines[j].slice(
					Math.min(open[1].length, /^\s*/.exec(lines[j])?.[0].length ?? 0),
				),
			);
		}
		fences++;
		corpus.push({ text: body.join("\n"), where: `${rel}:${i + 1}`, page });
		i = j;
	}
}

const tokens = await tokenizer("oniguruma");
/** @type {Record<string, number>} */
const outputLines = {};
for (const { text, where, page } of corpus) {
	const source = text.split("\n");
	const lines = tokens(text);
	// 3. The generator's tokenizer, scope for scope.
	if (generatorTokens) {
		/** @type {string | null} */
		let differs;
		try {
			differs = firstDifference(lines, generatorTokens(text));
		} catch (e) {
			differs = why(e);
		}
		if (differs) problems.push(`${where}: ${differs}`);
	}
	lines.forEach((line, i) => {
		const at = `${where} line ${i + 1}`;
		const has = (/** @type {Token} */ t, /** @type {string} */ p) =>
			t.scopes.some((s) => s.startsWith(p));
		for (const t of line) {
			const quoted =
				has(t, "string.quoted") ||
				has(t, "string.regexp") ||
				has(t, "comment") ||
				has(t, "meta.program-output");
			const show = `${at}: ${JSON.stringify(t.text)}`;
			if (has(t, "string.unquoted") && /[[\]{}()$"]/.test(t.text))
				problems.push(
					`${show} is an unquoted value that swallows a bracket, a quote or a $`,
				);
			if (quoted) continue;
			if (/\$[A-Za-z_]/.test(t.text) && !has(t, "variable"))
				problems.push(`${show} holds a $var not scoped as a variable`);
			if (/^\/[a-z]/.test(t.text.trim()) && !has(t, "entity.name"))
				problems.push(`${show} is a menu path not scoped as one`);
			if (
				/^:[a-z]/.test(t.text.trim()) &&
				!(has(t, "keyword") || has(t, "storage") || has(t, "support.function"))
			)
				problems.push(`${show} is a :command not scoped as one`);
			if (
				/^(do|else|on-error|in)$/.test(t.text) &&
				!has(t, "keyword.control") &&
				!has(t, "keyword.operator.word")
			)
				problems.push(`${show} is a control word not scoped as a keyword`);
			if (/[{}]/.test(t.text) && has(t, "string"))
				problems.push(`${show} is a brace inside a string token`);
			if (
				/^\d+(ms|s|m|h|d|w)$/.test(t.text.trim()) &&
				!has(t, "constant.numeric")
			)
				problems.push(`${show} is a duration not scoped as a number`);
		}
		if (
			/^\s*#/.test(source[i]) &&
			!line.every(
				(t) =>
					!t.text.trim() || has(t, "comment") || has(t, "meta.program-output"),
			)
		)
			problems.push(
				`${at}: a line that starts with # is not a comment throughout`,
			);
		const last = line[line.length - 1];
		if (
			last &&
			(has(last, "string.quoted") || has(last, "string.regexp")) &&
			!/\\$/.test(source[i]) &&
			!/"\s*$/.test(source[i])
		)
			problems.push(`${at}: a string is still open at the end of the line`);
		if (line.some((t) => has(t, "meta.program-output"))) {
			if (page) outputLines[page] = (outputLines[page] ?? 0) + 1;
			else problems.push(`${at}: a script line is scoped as command output`);
		}
	});
}
for (const page of new Set([
	...Object.keys(OUTPUT_LINES),
	...Object.keys(outputLines),
])) {
	// Each page is read in English and in Spanish.
	const want =
		(OUTPUT_LINES[/** @type {keyof typeof OUTPUT_LINES} */ (page)] ?? 0) * 2;
	if ((outputLines[page] ?? 0) !== want) {
		problems.push(
			`${page}: ${outputLines[page] ?? 0} lines scoped as command output across both languages, OUTPUT_LINES expects ${want}`,
		);
	}
}

/* ------------------------------------------------------ synthetic grammars */

/**
 * Each: what it reaches, a grammar's `patterns` and `repository`, and inputs.
 * The three guards are vscode-textmate's (src/tokenizer.ts, _tokenizeString
 * and its scanNext); the tokenizer's comments say what each does.
 * @type {[what: string, grammar: object, inputs: string[]][]}
 */
const SYNTHETIC = [
	[
		"a region that opens and closes at one position, without advancing (first guard)",
		{
			patterns: [
				{
					begin: "(?=a)",
					end: "(?=a)",
					name: "r.x",
					patterns: [{ match: "b", name: "b.x" }],
				},
			],
		},
		["xab", "ab\nab", "a"],
	],
	[
		"a region that closes without advancing at the position it opened at on an earlier line (not the first guard)",
		{
			patterns: [
				{
					begin: "(?=b)",
					end: "(?=a)",
					name: "r.x",
					patterns: [{ match: "b", name: "b.x" }],
				},
				{ match: "a", name: "a.x" },
			],
		},
		["b\na", "bb\na b"],
	],
	[
		"the same region opened again where it opened, without advancing (second guard)",
		{
			patterns: [{ include: "#r" }],
			repository: {
				r: {
					begin: "(?=a)",
					end: "z",
					name: "r.x",
					patterns: [{ include: "#r" }, { match: "a", name: "a.x" }],
				},
			},
		},
		["aaz", "xa\nza", "a"],
	],
	[
		"a match that does not advance, inside a region (third guard)",
		{
			patterns: [
				{
					begin: "q",
					end: "z",
					name: "r.x",
					patterns: [{ match: "(?=a)", name: "e.x" }],
				},
			],
		},
		["qab\nzz", "qa", "q\na b z"],
	],
	[
		"a match that does not advance, at the root (third guard)",
		{
			patterns: [
				{ match: "(?=a)", name: "e.x" },
				{ match: "b", name: "b.x" },
			],
		},
		["bab", "a"],
	],
	[
		"another region opened without advancing inside one that was",
		{
			patterns: [
				{
					begin: "(?=x)",
					end: "(?!x)",
					name: "o.x",
					patterns: [
						{
							begin: "(?=x)",
							end: "y",
							name: "i.x",
							patterns: [{ match: "x", name: "xx.x" }],
						},
					],
				},
			],
		},
		["xxy x", "x\nxy"],
	],
	[
		"contentName, and captures standing in for begin and end captures",
		{
			patterns: [
				{
					begin: "(<)(\\w*)",
					end: "(>)",
					captures: { 1: { name: "p.x" }, 2: { name: "t.x" } },
					contentName: "c.x",
					name: "n.x",
					patterns: [{ match: "\\d+", name: "d.x" }],
				},
			],
		},
		["a<tag 12>b", "<x\n1>", "<>", "<a<b>>"],
	],
	[
		"nested captures, group 0 included",
		{
			patterns: [
				{
					match: "((a)(b(c)))(d)?",
					captures: {
						0: { name: "z.x" },
						1: { name: "o.x" },
						3: { name: "t.x" },
						4: { name: "f.x" },
						5: { name: "u.x" },
					},
				},
			],
		},
		["abcd abc xabcdx"],
	],
	[
		"empty and unmatched captures",
		{
			patterns: [
				{
					match: "(a*)(b)|(c)",
					captures: {
						1: { name: "e.x" },
						2: { name: "b.x" },
						3: { name: "c.x" },
					},
				},
			],
		},
		["b ab c"],
	],
	[
		"a region's end against a pattern that matches at the same position",
		{
			patterns: [
				{
					begin: "\\(",
					end: "\\)",
					name: "p.x",
					patterns: [
						{ match: "\\)+", name: "q.x" },
						{ begin: "\\(", end: "\\)", name: "i.x" },
					],
				},
			],
		},
		["(a)) (b(c)d)"],
	],
	[
		"an end at `$`, and a region across lines",
		{
			patterns: [
				{ begin: "#", end: "$", name: "c.x" },
				{
					begin: '"',
					end: '"',
					name: "s.x",
					patterns: [{ match: "\\\\.", name: "e.x" }],
				},
			],
		},
		["a # b\nc", '"a\nb\\"c" d'],
	],
	[
		"a lookbehind, searched from several positions",
		{
			patterns: [
				{ match: "(?<=a)b", name: "b.x" },
				{ match: "a", name: "a.x" },
			],
		},
		["abab bb"],
	],
	[
		"an end of (?!), a region no line closes",
		{
			patterns: [
				{
					begin: "^(?=\\[)",
					end: "(?!)",
					name: "s.x",
					patterns: [{ match: "^.+$", name: "o.x" }],
				},
			],
		},
		["a\n[b\nc\n", "[\n\n"],
	],
	[
		"`^` after a match in the middle of a line",
		{
			patterns: [
				{ match: "^a", name: "a.x" },
				{ match: "b", name: "b.x" },
			],
		},
		["ab\na\nba"],
	],
];

{
	const synthetic = SYNTHETIC.map(([what, g, inputs], i) => ({
		what,
		inputs,
		grammar: {
			name: `synthetic-${i}`,
			scopeName: `source.synthetic-${i}`,
			...g,
		},
	}));
	const highlighter = await createHighlighterCore({
		themes: [THEME],
		langs: synthetic.map((t) => t.grammar),
		engine: await createOnigurumaEngine(shiki("wasm")),
	});
	for (const { what, inputs, grammar: g } of synthetic) {
		/** @type {(code: string) => Token[][]} */
		let generator;
		try {
			generator = rscHighlight(g);
		} catch (e) {
			problems.push(`synthetic, ${what}: ${why(e)}`);
			continue;
		}
		const oniguruma = shikiTokens(highlighter, g);
		for (const input of inputs) {
			/** @type {string | null} */
			let differs;
			try {
				differs = firstDifference(oniguruma(input), generator(input));
			} catch (e) {
				differs = why(e);
			}
			if (differs)
				problems.push(
					`synthetic, ${what}, ${JSON.stringify(input)}: ${differs}`,
				);
		}
	}
}

if (problems.length > 0) {
	console.error(
		`routeros-grammar: ${problems.length} problem${problems.length === 1 ? "" : "s"}\n`,
	);
	for (const p of problems) console.error(`  ${p}`);
	process.exit(1);
}
console.log(
	`routeros-grammar: ${assertions} assertions (${CASES.length} cases; shiki with Oniguruma, shiki with JavaScript, rsc-highlight); ${corpus.length} RouterOS texts (${cases.length} cases, their uninstall, the generator, <ManualSteps>, ${fences} fences) clean, and scoped alike by rsc-highlight; ${SYNTHETIC.length} synthetic grammars scoped alike by both`,
);
