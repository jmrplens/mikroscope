// @ts-check
// A Content-Security-Policy for every built page, as a <meta> element.
//
// GitHub Pages sends no CSP header and lets a site set none, so a <meta
// http-equiv> is the only way to ship one. A meta policy cannot carry
// `frame-ancestors`, `report-uri` or `sandbox`, and those are not written.
//
// Why this is not Astro's `security.csp`. Astro 7.3.3 hashes the scripts it
// bundles or inlines itself, the scripts integrations inject, and its island
// runtime (`trackScriptHashes` in astro/dist/core/csp/common.js). It hashes no
// `<script is:inline>`, and a page only runs if every inline script has its
// hash. The built site had 9 distinct inline scripts that Astro does not hash
// (dist/, 2026-09-24). Six come from Starlight 0.42.2: ThemeProvider,
// ThemeSelect, the two in SidebarPersister, Search and the Tabs restore. Three
// come from this site: PageFrame, ThemeToggle and Localized404. Under
// `security.csp` all nine would be blocked. A stored light theme would not be
// applied on load, and the phone header's theme button would stay hidden. The
// sidebar would not restore its open groups or scroll to the current page. A
// missing /es/ address would get the English 404 text. Keeping them working
// would mean hand-listing their hashes, and those change silently whenever
// Starlight changes a line of them.
//
// So the policy is written after the build, from the bytes that are served:
// every inline script in the page is hashed as it stands in the file, so
// there is no list to keep. A script edited, added or removed changes its
// page's policy with it.
//
// What the policy allows, and why:
//
// - `script-src 'self' 'wasm-unsafe-eval'` plus the page's hashes. 'self'
//   covers the bundles under /_astro/ and Pagefind's scripts.
//   'wasm-unsafe-eval' lets a page compile WebAssembly and nothing else: it
//   does not allow eval() or new Function().
// - `worker-src 'self'`: Pagefind searches in a worker loaded from
//   /pagefind/pagefind-worker.js. A worker fetched from a URL takes its policy
//   from its own response headers, which GitHub Pages does not send, so this
//   page policy does not reach the WebAssembly Pagefind compiles there. When
//   the worker cannot start, Pagefind searches on the main thread, and that
//   path is the one 'wasm-unsafe-eval' keeps working. Measured 2026-09-24 in
//   headless Chromium (Playwright 1.63), on the built site with this policy
//   injected, searching "softirq": 5 results as written; 5 without
//   'wasm-unsafe-eval'; 5 with the worker blocked; 0 with the worker blocked
//   and no 'wasm-unsafe-eval', and a `script-src wasm-eval` violation. Other
//   browsers were not tried.
// - `style-src 'self' 'unsafe-inline'`. The 110 built pages carried 18,138
//   style attributes (2026-09-24): the depth of each table-of-contents entry,
//   Expressive Code's token colours and Starlight's icon sizes, in that order
//   of count. Allowing them by hash would take 'unsafe-hashes' and one hash
//   per distinct attribute. A CSS injection on a static site with no user
//   input is not the threat a policy here is for.
// - `img-src 'self' data:`: the stylesheets draw icons from data: SVGs.
// - `object-src 'none'`, `base-uri 'self'`, `form-action 'self'`: nothing on
//   the site uses a plugin, a <base> or a form that posts elsewhere, so none
//   of those can start to without a change to this file.
// - everything else falls back to `default-src 'self'`: the site loads no
//   font, frame or data from another origin.
//
// The meta element goes right after `<meta charset>`, the first thing in
// <head>. A policy governs only what the parser meets after it, and
// Starlight's theme script runs from <head>.
import { createHash } from "node:crypto";
import { readFileSync, readdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

/** The directives that do not depend on the page. */
const FIXED = [
	"default-src 'self'",
	"img-src 'self' data:",
	"style-src 'self' 'unsafe-inline'",
	"worker-src 'self'",
	"object-src 'none'",
	"base-uri 'self'",
	"form-action 'self'",
];

const SCRIPT_SOURCES = ["'self'", "'wasm-unsafe-eval'"];

// A <script> whose type is not one of these is a data block, which the
// browser never runs and a policy never governs: the page's JSON-LD graph is
// one.
const EXECUTABLE_TYPES = new Set([
	"",
	"module",
	"importmap",
	"speculationrules",
	"text/javascript",
	"application/javascript",
	"application/ecmascript",
	"text/ecmascript",
]);

const SCRIPT = /<script\b([^>]*)>([\s\S]*?)<\/script\s*>/gi;
const CHARSET = /<meta\s+charset=["']?utf-8["']?\s*\/?>/i;

/**
 * The hash source of every inline script the browser would run. The text is
 * hashed exactly as it sits between the tags, because a script element is
 * raw text: the browser hashes those same bytes.
 * @param {string} html
 * @returns {string[]} sorted, without repeats
 */
export function inlineScriptHashes(html) {
	const hashes = new Set();
	for (const [, attrs, body] of html.matchAll(SCRIPT)) {
		if (/\bsrc\s*=/i.test(attrs)) continue;
		const type = (/\btype\s*=\s*["']?([^"'\s>]*)/i.exec(attrs)?.[1] ?? "")
			.split(";")[0]
			.trim()
			.toLowerCase();
		if (!EXECUTABLE_TYPES.has(type)) continue;
		const digest = createHash("sha256").update(body, "utf8").digest("base64");
		hashes.add(`'sha256-${digest}'`);
	}
	return [...hashes].sort();
}

/**
 * The policy for one page.
 * @param {string} html
 * @returns {string}
 */
export function policyFor(html) {
	const script = ["script-src", ...SCRIPT_SOURCES, ...inlineScriptHashes(html)];
	return [...FIXED, script.join(" ")].join("; ");
}

/**
 * The page with its policy in <head>. Throws on a page it cannot place the
 * policy in safely, rather than shipping one that governs nothing.
 * @param {string} html
 * @param {string} where for the error message
 * @returns {string}
 */
export function withPolicy(html, where) {
	if (/http-equiv=["']?content-security-policy/i.test(html)) {
		throw new Error(
			`${where} already carries a CSP meta element; two policies would both apply`,
		);
	}
	const charset = CHARSET.exec(html);
	if (!charset) {
		throw new Error(
			`${where} has no <meta charset="utf-8"> to place the CSP after`,
		);
	}
	const at = charset.index + charset[0].length;
	const before = html.slice(0, at);
	if (/<script\b/i.test(before)) {
		throw new Error(
			`${where} runs a script before <meta charset>, where no CSP can reach it`,
		);
	}
	const meta = `<meta http-equiv="content-security-policy" content="${policyFor(html)}">`;
	return before + meta + html.slice(at);
}

/**
 * The integration: writes the policy into every built page, after every other
 * integration has run (list it last).
 * @param {{ skip?: (html: string) => boolean }} [options] pages to leave
 *   alone, such as the redirect stubs, which run nothing and which the gates
 *   recognise by their size
 * @returns {import("astro").AstroIntegration}
 */
export function metaCsp({ skip = () => false } = {}) {
	return {
		name: "mikroscope-meta-csp",
		hooks: {
			"astro:build:done": ({ dir, logger }) => {
				const root = fileURLToPath(dir);
				let pages = 0;
				const distinct = new Set();
				for (const entry of readdirSync(root, { recursive: true })) {
					const name = String(entry);
					if (!name.endsWith(".html")) continue;
					const file = path.join(root, name);
					const html = readFileSync(file, "utf8");
					if (skip(html)) continue;
					writeFileSync(file, withPolicy(html, name));
					for (const hash of inlineScriptHashes(html)) distinct.add(hash);
					pages += 1;
				}
				logger.info(
					`CSP written into ${pages} pages, allowing ${distinct.size} distinct inline scripts by hash`,
				);
			},
		},
	};
}
