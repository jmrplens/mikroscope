// Every heading id the site has published, held to the build.
//
// A heading's id is an address: the table of contents, a link from another
// page, a bookmark, an answer somewhere else on the web. It is computed from
// the heading's text, so a change that only meant to restyle a heading can
// move it, and nothing else fails: writing `--expose` as code moved
// install/reaching-the-agent's `#expose-on-the-routers-lan-address` to
// `#--expose-on-…` in both locales, and the build, the link validator and
// every gate passed (2026-09-24). The old id is pinned with `{#id}` on the
// heading now (astro.config.mjs), and this check is what notices the next one.
//
// scripts/anchors.txt lists `route#id` for every heading of every built page.
// The check fails when a listed id is gone, and when the build has an id the
// list does not, so a new heading is listed, and so protected, from the change
// that adds it.
//
// Usage: node scripts/check-anchors.mjs [--write] [dist-directory]
//   --write  adds the build's new ids to the list. It never removes one: when
//            a section is really gone, delete its line by hand, in the same
//            change, where a reviewer can see it.
import { readFileSync, readdirSync, writeFileSync } from "node:fs";
import { join, relative, resolve, sep } from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

import { isRedirectStub } from "./redirect-stub.mjs";

const args = process.argv.slice(2);
const WRITE = args.includes("--write");
const DIST = resolve(args.find((a) => !a.startsWith("--")) ?? "dist");
const LIST = fileURLToPath(new URL("anchors.txt", import.meta.url));

/** Every built page. */
function* htmlFiles(dir) {
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		const full = join(dir, entry.name);
		if (entry.isDirectory()) yield* htmlFiles(full);
		else if (entry.isFile() && entry.name.endsWith(".html")) yield full;
	}
}

/** `route#id` for each heading of the build, sorted. */
function built() {
	const out = new Set();
	for (const file of htmlFiles(DIST)) {
		const html = readFileSync(file, "utf8");
		if (isRedirectStub(html)) continue;
		const route = `/${relative(DIST, file).split(sep).join("/")}`.replace(
			/index\.html$/,
			"",
		);
		for (const m of html.matchAll(/<h[1-6]\b[^>]*?\sid="([^"]+)"/g))
			out.add(`${route}#${m[1]}`);
	}
	if (out.size === 0) {
		console.error(`No headings under ${DIST}. Run pnpm build first.`);
		process.exit(1);
	}
	return out;
}

const HEADER = [
	"# Every heading id the built site publishes, as route#id.",
	"# scripts/check-anchors.mjs fails when one goes missing: pin it with",
	"# `{#id}` on the heading instead. Regenerate with --write, which only adds.",
];

const listed = new Set(
	readFileSync(LIST, "utf8")
		.split("\n")
		.map((l) => l.trim())
		.filter((l) => l !== "" && !l.startsWith("#")),
);
const now = built();
const lost = [...listed].filter((a) => !now.has(a));
const added = [...now].filter((a) => !listed.has(a));

if (WRITE && added.length > 0) {
	const all = [...new Set([...listed, ...added])].sort();
	writeFileSync(LIST, `${[...HEADER, ...all].join("\n")}\n`);
	console.log(`anchors: added ${added.length} to scripts/anchors.txt`);
}

let failed = false;
if (lost.length > 0) {
	failed = true;
	console.error(
		`anchors: ${lost.length} published heading id(s) no longer exist in the build. ` +
			"Pin each on its heading with `{#id}`, or, if the section is gone, delete its line from scripts/anchors.txt:",
	);
	for (const a of lost) console.error(`  ${a}`);
}
if (added.length > 0 && !WRITE) {
	failed = true;
	console.error(
		`anchors: ${added.length} heading id(s) are not in scripts/anchors.txt. ` +
			"Run `node scripts/check-anchors.mjs --write` after the build to list them:",
	);
	for (const a of added.slice(0, 20)) console.error(`  ${a}`);
	if (added.length > 20) console.error(`  … and ${added.length - 20} more`);
}
if (failed) process.exit(1);
console.log(`anchors: ${now.size} heading ids, all listed, none lost`);
