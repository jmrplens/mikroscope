// No Astro template directive survives into the built HTML.
//
// `set:html`, `class:list`, `is:inline` and the rest are instructions to the
// Astro compiler, and the compiler removes each one as it applies it. One that
// reaches dist/ as an attribute was never applied: the element it sat on
// renders without what the directive was for, and the page still validates,
// because an unknown attribute is not an HTML error. That is how
// @astrojs/markdown-satteri 0.4.1 shipped Expressive Code's inline stylesheet
// on this site as `<style set:html="…">`: the /cost/ page's code fence lost
// `overflow-x: auto` and the page scrolled sideways at a 390 px viewport
// (2026-09-15; astro.config.mjs's `expressiveCode` comment has the numbers).
// ghchronicle's site met the same defect through @astrojs/markdown-remark
// 7.3.1, with html-validate, htmlhint and pa11y all green.
//
// The scan reads attribute names, not text: an attribute value or the body of
// a <script> or <style> may quote a directive, and that is not a leak.
//
// Usage: node scripts/check-directives.mjs [dist-directory]
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { join, resolve } from "node:path";
import process from "node:process";

import { isRedirectStub } from "./redirect-stub.mjs";

const DIST = resolve(process.argv[2] ?? "dist");

if (!existsSync(DIST)) {
	console.error(`[directives] no ${DIST}. Run pnpm build first.`);
	process.exit(1);
}

// The directive namespaces Astro's compiler consumes.
const DIRECTIVE = /^(?:set|class|is|define|transition|client|server):/;
// Elements whose content is raw text, skipped whole so a quoted `<` in a
// script or a stylesheet is not read as a tag.
const RAW_TEXT = new Set(["script", "style"]);
// Sticky, so each match starts exactly where the scan stands. Slicing the
// document instead would cut a long attribute value in two, and a data URI
// can run to hundreds of kilobytes.
const TAG_NAME = /<([a-zA-Z][\w-]*)/y;
const ATTRIBUTE =
	/[\s/]*([^\s"'=<>/]+)(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]+))?/y;

function* htmlFiles(dir, prefix = "") {
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		const relative = prefix ? `${prefix}/${entry.name}` : entry.name;
		if (entry.isDirectory()) yield* htmlFiles(join(dir, entry.name), relative);
		else if (entry.isFile() && entry.name.endsWith(".html")) yield relative;
	}
}

/**
 * Every start tag in a document with its attribute names, skipping comments
 * and the content of raw text elements.
 *
 * @param {string} html
 * @returns {Generator<{ tag: string, names: string[], offset: number }>}
 */
function* startTags(html) {
	let at = 0;
	while (at < html.length) {
		const open = html.indexOf("<", at);
		if (open < 0) return;
		if (html.startsWith("<!--", open)) {
			const close = html.indexOf("-->", open + 4);
			at = close < 0 ? html.length : close + 3;
			continue;
		}
		TAG_NAME.lastIndex = open;
		const nameMatch = TAG_NAME.exec(html);
		if (!nameMatch) {
			at = open + 1;
			continue;
		}
		const tag = nameMatch[1].toLowerCase();
		const names = [];
		let i = TAG_NAME.lastIndex;
		while (i < html.length && html[i] !== ">") {
			ATTRIBUTE.lastIndex = i;
			const attr = ATTRIBUTE.exec(html);
			if (!attr) {
				i++;
				continue;
			}
			names.push(attr[1]);
			i = ATTRIBUTE.lastIndex;
		}
		yield { tag, names, offset: open };
		at = i + 1;
		if (RAW_TEXT.has(tag)) {
			const close = html.indexOf(`</${tag}`, at);
			at = close < 0 ? html.length : close;
		}
	}
}

const leaks = [];
let pages = 0;
for (const file of htmlFiles(DIST)) {
	if (isRedirectStub(readFileSync(join(DIST, file), "utf8"))) continue;
	pages++;
	const html = readFileSync(join(DIST, file), "utf8");
	for (const { tag, names, offset } of startTags(html)) {
		for (const name of names.filter((n) => DIRECTIVE.test(n))) {
			leaks.push(`${file} @${offset}: <${tag} ${name}>`);
		}
	}
}

if (leaks.length > 0) {
	console.error(
		`[directives] ${leaks.length} Astro directive(s) reached the built HTML unapplied:`,
	);
	for (const leak of leaks.slice(0, 20)) console.error(`  ${leak}`);
	if (leaks.length > 20) console.error(`  ... and ${leaks.length - 20} more`);
	process.exit(1);
}
console.log(
	`[directives] ${pages} pages: no Astro directive reached the HTML.`,
);
