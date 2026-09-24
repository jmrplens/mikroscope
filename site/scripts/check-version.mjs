#!/usr/bin/env node
/**
 * The pages name the release that exists, and never the placeholder for it.
 *
 * The install commands are copied, so a stale version in one installs a stale
 * agent: `--remote-image jmrplens/mikroscope-agent:1.2.0` was on five pages
 * and their twins while 1.2.1 fixed a data race in the agent (GEO audit,
 * 2026-09-24). Those commands now carry {{MIKROSCOPE_VERSION}}, which the
 * build replaces from VERSION (src/lib/release.mjs has the whole arrangement).
 * Each half of that can fail without anything else noticing, because a page
 * with a stale version, or with a literal placeholder, is still a valid page:
 *
 *   1. A page pins a literal version where the placeholder belongs. Read from
 *      the source, EN and ES, frontmatter included: an image reference or an
 *      archive name (`mikroscope-agent:1.2.0`, `mikroscope_1.2.0_…`), the
 *      installer's `VERSION=` or `--version`, or a download URL.
 *      A sentence about a past release that has to keep that release's number
 *      in one of those shapes is listed in HISTORICAL below, with the reason;
 *      "since 1.1.0" and the like are not in those shapes and need nothing.
 *   2. The placeholder in frontmatter. Starlight writes the title and the
 *      description into <title> and <meta> itself, where no plugin replaces
 *      anything.
 *   3. The placeholder in the output: docs/, and every text file of dist/ —
 *      the pages, their markdown twins, llms*.txt, the sitemap. The name is
 *      searched for rather than the whole token, because a placeholder in an
 *      href arrives percent-encoded.
 *   4. The output shows the version: every page whose source carries the
 *      placeholder carries the version in its built HTML, outside its scripts
 *      and styles (the JSON-LD names it on every page), and in its twin; and
 *      the literal pins of (1), read again from docs/, the twins and those
 *      pages' rendered HTML, are either the current version or HISTORICAL.
 *      That second read is what catches a pin that came from a data file
 *      rather than from a page, or one a renderer got wrong.
 *
 * (3) and (4) read a build, so this runs after `pnpm build`, like the other
 * gates that read dist/.
 *
 * Usage: node scripts/check-version.mjs [dist-directory]
 */
import { existsSync, readFileSync, readdirSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

import {
	VERSION_PLACEHOLDER,
	VERSION_PLACEHOLDER_NAME,
	readRelease,
} from "../src/lib/release.mjs";

const SITE = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const REPO = path.dirname(SITE);
const CONTENT = path.join(SITE, "src/content/docs");
const DOCS = path.join(REPO, "docs");
const DIST = path.resolve(process.argv[2] ?? path.join(SITE, "dist"));

const { version } = readRelease(REPO);

/**
 * The shapes a current-version pin takes in an install command. Each match's
 * first group is the version. A `v` prefix is allowed where the command could
 * carry one, so `download/v1.2.0/` is caught as well as `VERSION=1.2.0`.
 */
const PINS = [
	// jmrplens/mikroscope-agent:1.2.0, ghcr.io/…/mikroscope:1.2.0,
	// mikroscope_1.2.0_linux_x86_64.tar.gz, mikroscope-agent_1.2.0_linux_arm64.tar.gz
	/\bmikroscope(?:-agent)?[:_]v?(\d+\.\d+\.\d+)/g,
	// VERSION=1.2.0, the installer's variable
	/\bVERSION=v?(\d+\.\d+\.\d+)/g,
	// ./install.sh --version 1.2.0
	/--version[ =]v?(\d+\.\d+\.\d+)/g,
	// …/releases/download/v1.2.0/…
	/\/releases\/download\/v?(\d+\.\d+\.\d+)\//g,
];

/**
 * Pins that are about a past release on purpose, keyed by the page (without
 * its locale) and the exact text of the pin. Each one applies to the page and
 * its Spanish twin, and each one must still match something: an entry that
 * matches nothing fails, so the list cannot outlive what it excuses.
 */
const HISTORICAL = [
	{
		page: "install/routes.mdx",
		pin: "mikroscope-agent:1.0.1",
		why: "the tag the router pulled in the measured run of 2026-09-17, which the sentence keeps rather than following the release",
	},
];

const problems = [];
const fail = (message) => problems.push(message);

/** Every file under a directory, as paths relative to it. */
function* walk(dir, prefix = "") {
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		const relative = prefix ? `${prefix}/${entry.name}` : entry.name;
		if (entry.isDirectory()) yield* walk(path.join(dir, entry.name), relative);
		else if (entry.isFile()) yield relative;
	}
}

/** @param {string} text @returns {{ pin: string, version: string, line: number }[]} */
function pinsIn(text) {
	const found = [];
	for (const pattern of PINS) {
		for (const m of text.matchAll(pattern)) {
			found.push({
				pin: m[0],
				version: m[1],
				line: text.slice(0, m.index).split("\n").length,
			});
		}
	}
	return found;
}

/** The page a content file doubles, without its locale: "es/install/cli.mdx" -> "install/cli.mdx". */
const withoutLocale = (relative) => relative.replace(/^es\//, "");

/** Whether a pin is excused on a page, and marks the entry as used. */
const used = new Set();
function historical(page, pin) {
	const entry = HISTORICAL.find(
		(h) => (page === null || h.page === page) && pin.includes(h.pin),
	);
	if (entry && page !== null) used.add(entry);
	return entry !== undefined;
}

/* ---------------------------------------------------- 1 and 2: the source */

const placeholderPages = new Set();
for (const relative of walk(CONTENT)) {
	if (!/\.mdx?$/.test(relative)) continue;
	const source = readFileSync(path.join(CONTENT, relative), "utf8");
	const where = `src/content/docs/${relative}`;
	for (const { pin, line } of pinsIn(source)) {
		if (historical(withoutLocale(relative), pin)) continue;
		fail(
			`${where}:${line}: \`${pin}\` pins a version in an install command. Write ${VERSION_PLACEHOLDER} there (the build writes ${version}), or, if the sentence is about that release on purpose, add it to HISTORICAL in scripts/check-version.mjs with the reason.`,
		);
	}
	const front = /^---[ \t]*\r?\n([\s\S]*?)\r?\n---/.exec(source)?.[1] ?? "";
	if (front.includes(VERSION_PLACEHOLDER_NAME)) {
		fail(
			`${where}: the frontmatter carries ${VERSION_PLACEHOLDER}, which nothing replaces there: Starlight writes the title and description into the page head as they are.`,
		);
	}
	if (source.includes(VERSION_PLACEHOLDER)) placeholderPages.add(relative);
}
for (const entry of HISTORICAL) {
	if (!used.has(entry)) {
		fail(
			`scripts/check-version.mjs: HISTORICAL excuses \`${entry.pin}\` on ${entry.page}, which no longer has it. Remove the entry.`,
		);
	}
}

/* ------------------------------------------------- 3 and 4: the output */

/** Checks one generated text file: no placeholder, and no pin but the current or a historical one. */
function checkOutput(where, text, { pins }) {
	if (text.includes(VERSION_PLACEHOLDER_NAME)) {
		fail(
			`${where}: carries the placeholder ${VERSION_PLACEHOLDER} instead of ${version}.`,
		);
	}
	if (!pins) return;
	for (const { pin, version: pinned } of pinsIn(text)) {
		if (pinned === version || historical(null, pin)) continue;
		fail(
			`${where}: \`${pin}\` names ${pinned}, not the current ${version}, and is not in HISTORICAL. If it came from a data file rather than a page, that file needs the version from src/data/release.ts.`,
		);
	}
}

for (const name of readdirSync(DOCS).filter((n) => n.endsWith(".md"))) {
	checkOutput(`docs/${name}`, readFileSync(path.join(DOCS, name), "utf8"), {
		pins: true,
	});
}

if (!existsSync(path.join(DIST, "index.html"))) {
	fail(`no built site under ${DIST}. Run pnpm build first.`);
} else {
	const TEXT = /\.(html|md|txt|xml|json|webmanifest)$/;
	for (const relative of walk(DIST)) {
		if (!TEXT.test(relative)) continue;
		const text = readFileSync(path.join(DIST, relative), "utf8");
		// Pins are read from the markdown and the llms files, where a command is
		// one run of text; in the HTML, Expressive Code splits it into tokens.
		checkOutput(`dist/${relative}`, text, {
			pins: /\.(md|txt)$/.test(relative),
		});
	}
	for (const relative of placeholderPages) {
		const route = relative
			.replace(/\.mdx?$/, "")
			.replace(/(^|\/)index$/, "")
			.replace(/^\//, "");
		const dir = route ? `${route}/` : "";
		for (const built of [`${dir}index.html`, `${dir}index.md`]) {
			const file = path.join(DIST, built);
			if (!existsSync(file)) {
				fail(`dist/${built}: not built, for ${relative}`);
				continue;
			}
			// Scripts and styles out first: every page carries the version in its
			// JSON-LD (softwareVersion, the releaseNotes URL), so with them in, a
			// page whose visible commands were wrong would still "say" the version.
			// Then tags out, so a command Expressive Code split into spans reads
			// whole; its copy buttons keep the code in an attribute, which goes
			// with the tag.
			const html = built.endsWith(".html");
			let text = readFileSync(file, "utf8");
			if (html) {
				text = text
					.replaceAll(/<script\b[^>]*>[\s\S]*?<\/script>/gi, "")
					.replaceAll(/<style\b[^>]*>[\s\S]*?<\/style>/gi, "");
			}
			text = text.replaceAll(/<[^>]*>/g, "");
			if (!text.includes(version)) {
				fail(
					`dist/${built}: never says ${version} outside its scripts and styles, though src/content/docs/${relative} writes ${VERSION_PLACEHOLDER}`,
				);
			}
			// The HTML comes from a different renderer than the twin (the Sätteri
			// plugin in astro.config.mjs rather than reduceBody), so its commands
			// are read for pins too, as the twins' are in checkOutput.
			if (html) {
				for (const { pin, version: pinned } of pinsIn(text)) {
					if (pinned === version || historical(null, pin)) continue;
					fail(
						`dist/${built}: the rendered page shows \`${pin}\`, which names ${pinned}, not the current ${version}, and is not in HISTORICAL.`,
					);
				}
			}
		}
	}
}

if (problems.length > 0) {
	console.error(`[version] ${problems.length} problem(s):`);
	for (const problem of problems) console.error(`  ${problem}`);
	process.exit(1);
}
console.log(
	`[version] ${version}: no page pins another in an install command, ${placeholderPages.size} pages write it through ${VERSION_PLACEHOLDER}, and neither docs/ nor dist/ carries the placeholder.`,
);
