#!/usr/bin/env node
/**
 * Every <Src path="…" /> names something this repository tracks.
 *
 * <Src> links a path to GitHub's `main` (src/lib/src-link.mjs says why main).
 * A path that was renamed, deleted or never committed still renders as a tidy
 * link, and GitHub answers it with a 404 that no gate here would ever request.
 * So each path is looked up in `git ls-files`, which is what main will hold once
 * the pull request carrying the page merges, and its spelling is held to what
 * it names: a directory ends in `/` (GitHub serves it under tree/), a file does
 * not (blob/).
 *
 * `git ls-files` rather than the working tree, because an untracked file is on
 * disk and not on GitHub. It reads the index and not the history, so it needs
 * no particular fetch depth. The same page is
 * read in both languages: check-i18n-parity.mjs already holds a twin to the
 * same `path`, and reading both costs nothing.
 *
 * Source only, so it needs no build.
 *
 * Usage: node scripts/check-src.mjs
 */
import { execFileSync } from "node:child_process";
import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

import { isRepoPath } from "../src/lib/src-link.mjs";

const SITE = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const REPO = path.dirname(SITE);
const CONTENT = path.join(SITE, "src/content/docs");

let listed;
try {
	listed = execFileSync("git", ["ls-files", "-z"], {
		cwd: REPO,
		encoding: "utf8",
		maxBuffer: 64 * 1024 * 1024,
	});
} catch (error) {
	console.error(
		`[src] git ls-files failed in ${REPO}, so no <Src> path can be checked: ${error.message}`,
	);
	process.exit(1);
}
const files = new Set(listed.split("\0").filter(Boolean));
const dirs = new Set();
for (const file of files) {
	const parts = file.split("/");
	for (let i = 1; i < parts.length; i += 1)
		dirs.add(`${parts.slice(0, i).join("/")}/`);
}

/** Every page under the content collection, as paths relative to it. */
function* pages(dir, prefix = "") {
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		const relative = prefix ? `${prefix}/${entry.name}` : entry.name;
		if (entry.isDirectory()) yield* pages(path.join(dir, entry.name), relative);
		else if (/\.mdx?$/.test(entry.name)) yield relative;
	}
}

// The tag, with its attributes up to the closing bracket, across lines. The
// path has to be a quoted string: an expression could be anything, and the
// point of the check is to read it.
const SRC = /<Src\b([^>]*?)\/?>/g;
const PATH = /\bpath=(?:"([^"]*)"|'([^']*)')/;

const problems = [];
let count = 0;
for (const relative of pages(CONTENT)) {
	const source = readFileSync(path.join(CONTENT, relative), "utf8");
	for (const m of source.matchAll(SRC)) {
		const line = source.slice(0, m.index).split("\n").length;
		const where = `src/content/docs/${relative}:${line}`;
		const attr = PATH.exec(m[1]);
		const target = attr ? (attr[1] ?? attr[2]) : undefined;
		count += 1;
		if (target === undefined) {
			problems.push(`${where}: <Src> has no quoted path="…"`);
		} else if (!isRepoPath(target)) {
			problems.push(
				`${where}: "${target}" is not a repository path (from the root, no leading slash, "..", "#" or query)`,
			);
		} else if (target.endsWith("/")) {
			if (!dirs.has(target))
				problems.push(
					`${where}: "${target}" is not a directory git tracks anything under`,
				);
		} else if (!files.has(target)) {
			problems.push(
				dirs.has(`${target}/`)
					? `${where}: "${target}" is a directory; write it "${target}/" so the link goes to tree/, not blob/`
					: `${where}: "${target}" is not a file git tracks, so its link on main is a 404`,
			);
		}
	}
}

if (problems.length > 0) {
	console.error(`[src] ${problems.length} problem(s):`);
	for (const problem of problems) console.error(`  ${problem}`);
	process.exit(1);
}
console.log(
	`[src] ${count} <Src> path(s), each a file or directory git tracks.`,
);
