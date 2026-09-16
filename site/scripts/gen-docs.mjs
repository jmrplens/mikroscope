#!/usr/bin/env node
/**
 * Writes docs/ from the site's English pages.
 *
 * The repository carried two complete sets of documentation: 49 English pages
 * under site/src/content/docs, with their Spanish twins, and six hand-written
 * files under docs/ covering the same ground. They were edited in the same
 * commits, which is how they drifted in both directions at once. Neither copy
 * was wrong enough to look wrong, which is the point: a stale document is
 * still a valid document, and nothing fails.
 *
 * So there is one source now. The pages are the source, docs/ is output, and
 * `--check` is what makes that a fact rather than an intention. The reduction
 * from MDX to Markdown is src/lib/page-markdown.mjs, which reads the same
 * src/data modules the components do, so a figure on the page and the same
 * figure in docs/ are one number formatted once.
 *
 * What this adds on top of the reduction, and why:
 *
 *   - Several pages per file. docs/limits.md is the four pages under /limits/
 *     and the three under /cost/; docs/install.md the six under /install/.
 *     Six of the names are the ones README.md and CLAUDE.md already link to,
 *     so the mapping is a manifest here rather than one file per page.
 *   - Links into the site become absolute. A page writes /mikroscope/sinks/
 *     because it is served from there; the same text read on GitHub is a link
 *     to GitHub's own root.
 *   - Images become repository-relative. The built page serves a hashed,
 *     re-encoded asset; docs/ points at the source file, which is in the same
 *     checkout the reader already has.
 *   - Headings drop one level where a file holds more than one page, so the
 *     page's own title is the section heading above them.
 *
 * Every English page must be claimed by exactly one entry of MANIFEST or named
 * in NOT_IN_DOCS with a reason. A page added to the site therefore fails this
 * check until somebody decides where it belongs, which is the only way docs/
 * stays complete without anyone remembering that it exists.
 *
 * Usage:
 *   node scripts/gen-docs.mjs           # write docs/
 *   node scripts/gen-docs.mjs --check   # fail if docs/ is stale
 */
import { readFileSync, readdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

import { parseDocument } from "yaml";

import "./data-hooks.mjs";

// Dynamic, so the hooks the line above registers are in place before the data
// modules page-markdown.mjs imports are resolved; see data-hooks.mjs.
const { reduceBody } = await import("../src/lib/page-markdown.mjs");

const SITE = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const REPO = path.dirname(SITE);
const CONTENT = path.join(SITE, "src/content/docs");
const DOCS = path.join(REPO, "docs");

// The base path and the advertised origin as astro.config.mjs states them,
// read out of it rather than restated here. The site's own links are rooted at
// the base; every absolute link written OUTSIDE the site uses `publicDocs`,
// and the comment beside that declaration says why the two differ.
const config = readFileSync(path.join(SITE, "astro.config.mjs"), "utf8");
/** @param {RegExp} pattern @param {string} what @returns {string} */
function fromConfig(pattern, what) {
	const match = pattern.exec(config);
	if (!match) throw new Error(`astro.config.mjs declares no ${what}`);
	return match[1];
}
const BASE = fromConfig(/const siteBase\s*=\s*"([^"]+)"/, "base path");
const PUBLIC = fromConfig(
	/const publicDocs\s*=\s*"([^"]+)"/,
	"public docs URL",
);

/** @param {string} route @returns {string} the advertised URL of a page. */
const publicUrl = (route) => (route ? `${PUBLIC}/${route}/` : `${PUBLIC}/`);

// docs/<file> <- the site pages it holds, in the order they are read in.
//
// The first six names are the ones README.md and CLAUDE.md already link to and
// keep their old scope; the other five are the sections the site grew that the
// hand-written docs/ never had. The grouping is by what a reader arrives with
// a question about, which is how the sidebar is ordered too, so a file reads
// as one document rather than as pages stapled together.
const MANIFEST = [
	// What the thing is, and the five minutes that show it working.
	{
		file: "walkthrough.md",
		title: "Five minutes with a router",
		routes: ["start", "start/walkthrough"],
	},
	// Getting it onto a router: prerequisites, the four routes the image can
	// take, the writes, the firewall, where things land, and how the CLI
	// reaches the agent afterwards.
	{
		file: "install.md",
		title: "Installing the agent",
		routes: [
			"install",
			"install/prerequisites",
			"install/routes",
			"install/firewall",
			"install/layout",
			"install/reaching-the-agent",
		],
	},
	// What the observer costs and what the figures cannot say, then the floors
	// that produce them: one file, because "what it costs" and "what limits the
	// number" are the same question asked from two ends.
	{
		file: "limits.md",
		title: "What it costs and what it cannot see",
		routes: [
			"cost",
			"cost/rate-ceiling",
			"cost/limits",
			"limits",
			"limits/namespaces",
			"limits/privileged",
			"limits/source-floors",
		],
	},
	// Recording a window by hand, and letting the agent record one for you.
	{
		file: "record.md",
		title: "Recording and triggered capture",
		routes: ["record", "record/triggers"],
	},
	// The collector and everything downstream of it.
	{
		file: "sinks.md",
		title: "The collector and its sinks",
		routes: [
			"sinks",
			"sinks/prometheus",
			"sinks/influxdb",
			"sinks/other",
			"sinks/api-tier",
			"sinks/derive",
			"sinks/detections",
			"sinks/device-info",
		],
	},
	// The two committed dashboards, importing them, and the alert rules.
	{
		file: "dashboards.md",
		title: "Dashboards and alerts",
		routes: ["dashboards", "dashboards/import-and-check", "dashboards/alerts"],
	},
	// How to read what it shows: the idle shape, and seven faults read against it.
	{
		file: "playbooks.md",
		title: "How to read what it shows",
		routes: [
			"playbooks",
			"playbooks/idle",
			"playbooks/loop",
			"playbooks/port-names",
			"playbooks/cpu",
			"playbooks/packet-flood",
			"playbooks/flash-wear",
			"playbooks/conntrack",
		],
	},
	// What runs where, which credential lives where, and what the installer refuses.
	{
		file: "security.md",
		title: "Security",
		routes: [
			"security",
			"security/api-user",
			"security/expose",
			"security/installer",
		],
	},
	// The tables: flags, variables, endpoints, metric families, measurements.
	{
		file: "reference.md",
		title: "Reference",
		routes: [
			"reference/cli",
			"reference/environment",
			"reference/http",
			"reference/metrics",
			"reference/measurements",
			"reference/testing",
		],
	},
	// Where the project stands, what the mark is, and what it was built from.
	{
		file: "about.md",
		title: "About the project",
		routes: ["about/status", "about/brand", "about/lineage"],
	},
];

// The two pages that are not documentation, each with the reason it is not.
const NOT_IN_DOCS = new Map([
	[
		"",
		"the splash landing: its copy is a typed object (src/data/home.ts), the repository's README opens with the same pitch, and the rest of it is links to pages docs/ already holds",
	],
	[
		"404",
		"the not-found page: a hero and two links, for a mistyped address on a served site, which a file in a checkout cannot have",
	],
]);

// Named beside the generated files in the index, because the index is the
// first page of docs/ and these are where a reader is sent next.
const NEIGHBOURS = [
	[
		"../README.md",
		"What the project is, in one page, with the numbers it leads on",
	],
	[
		"../dashboards/README.md",
		"The two committed Grafana dashboards and their alert rules as files, with the datasource settings, including the InfluxDB datasource's two secure fields",
	],
	[
		"../site/",
		"The source these files are generated from, and the site that serves it",
	],
];

/**
 * The line that tells a reader who arrives here that this is output, and what
 * to edit instead. It names the source, because a file that says only "do not
 * edit" leaves the reader with nowhere to go.
 *
 * @param {string[]} sources the page files this one is generated from
 * @returns {string} the notice
 */
const notice = (sources) =>
	`Generated by \`site/scripts/gen-docs.mjs\` from ${
		sources.length === 0
			? "the pages of the documentation site"
			: sources.length === 1
				? `\`${sources[0]}\``
				: "the pages of the documentation site named under each heading below"
	}: do not edit this file. Change the page, and its Spanish twin beside it, then run \`pnpm run docs\` in \`site/\`.`;

/** "start/walkthrough.mdx" -> "start/walkthrough", "index.mdx" -> "". */
const routeOf = (relative) =>
	relative
		.replace(/\.mdx?$/, "")
		.replace(/(^|\/)index$/, "")
		.replace(/^\//, "");

/**
 * Every English page of the content collection, keyed by route.
 *
 * @returns {Map<string, { route: string, title: string, description: string, body: string, file: string, dir: string }>}
 */
function readPages() {
	const pages = new Map();
	/** @param {string} dir */
	function walk(dir) {
		for (const entry of readdirSync(dir, { withFileTypes: true })) {
			const full = path.join(dir, entry.name);
			if (entry.isDirectory()) {
				// The Spanish mirror is a translation of these same pages. docs/ is
				// English, as everything in the repository outside site/…/es is.
				if (path.relative(CONTENT, full) !== "es") walk(full);
				continue;
			}
			if (!entry.name.endsWith(".mdx") && !entry.name.endsWith(".md")) continue;
			const relative = path.relative(CONTENT, full).replaceAll(path.sep, "/");
			const source = readFileSync(full, "utf8");
			const split = /^---[ \t]*\r?\n([\s\S]*?)\r?\n---[ \t]*(?:\r?\n|$)/.exec(
				source,
			);
			if (!split) throw new Error(`${relative}: no frontmatter`);
			const front = parseDocument(split[1]).toJS() ?? {};
			if (!front.title) throw new Error(`${relative}: no frontmatter title`);
			if (!front.description)
				throw new Error(`${relative}: no frontmatter description`);
			const route = routeOf(relative);
			pages.set(route, {
				route,
				title: String(front.title),
				description: String(front.description),
				body: source.slice(split[0].length),
				file: `site/src/content/docs/${relative}`,
				dir: path.dirname(full),
			});
		}
	}
	walk(CONTENT);
	return pages;
}

// A scheme, which is what separates a target somewhere else from a file in
// this checkout.
const SCHEME = /^[a-z][a-z0-9+.-]*:/i;

// A link or an image, with its optional title: [text](target "title").
//
// The text may wrap: these pages are wrapped at a hundred columns, and a link
// whose text broke across a line would otherwise keep the site-rooted target
// every other one loses. A blank line ends the text, because that is a
// paragraph and not a link.
const TARGET =
	/(!?)(\[(?:[^\]\n]|\n(?![ \t]*\n))*\])\(([^)\s]+)((?:\s+"[^"]*")?)\)/g;

/**
 * Rewrites one link or image target for a file read from the repository rather
 * than served from the site.
 *
 * @param {string} target the target as the page writes it
 * @param {boolean} isImage whether it came from an image
 * @param {{ dir: string, route: string }} context the page it was written in
 * @returns {string} the target as docs/ should carry it
 */
function retarget(target, isImage, context) {
	// Something served from elsewhere is not a file in this checkout, and
	// resolving it against the page's directory would make it one: a directory
	// called https: with the host under it.
	if (SCHEME.test(target)) return target;
	if (isImage || target.startsWith(".")) {
		// An asset the page reaches by walking out of the content tree. The built
		// page serves a hashed copy of it; docs/ points at the file itself, which
		// the reader already has in the same checkout.
		return path
			.relative(DOCS, path.resolve(context.dir, target))
			.replaceAll(path.sep, "/");
	}
	// A heading on the page itself. Several pages land in one file here, so a
	// heading is no longer the only one of its name — "## See also" closes
	// nearly every page — and GitHub would resolve the anchor to the first of
	// them, which is the wrong section. The anchor is exact on the page it was
	// written for, so that is where it points.
	if (target.startsWith("#")) return `${publicUrl(context.route)}${target}`;
	// A link into the site. Rooted at the base path because that is where the
	// page is served from; read from GitHub the same text is a link to GitHub.
	if (target.startsWith(`${BASE}/`))
		return `${PUBLIC}${target.slice(BASE.length)}`;
	return target;
}

/**
 * One run of prose with every link and image in it retargeted, and then held
 * to the result.
 *
 * A target still rooted at the site's base path is a link to GitHub's own
 * root, and one still naming a heading resolves to whichever section of the
 * file happens to carry that name first. Neither fails anything downstream, so
 * they fail here.
 *
 * @param {string} prose a run of lines with no fenced code in it
 * @param {{ dir: string, file: string, route: string }} context
 * @returns {string} the prose
 */
function rewrite(prose, context) {
	const out = prose.replaceAll(
		TARGET,
		(_, bang, text, target, title) =>
			`${bang}${text}(${retarget(target, bang === "!", context)}${title})`,
	);
	const missed = out.includes("](#")
		? "a heading"
		: out.includes(`](${BASE}/`)
			? `${BASE}/`
			: "";
	if (missed) {
		throw new Error(
			`${context.file}: a link to ${missed} survived the rewrite, so docs/ would carry a ` +
				"target that resolves nowhere. Its text is shaped in a way site/scripts/gen-docs.mjs does not read.",
		);
	}
	return out;
}

/**
 * One heading of a page that lands in a file with others, dropped a level so
 * the page's own title is the section above it.
 *
 * @param {string} line
 * @param {{ file: string }} context
 * @returns {string} the line
 */
function demote(line, context) {
	return line.replace(/^(#{1,6})(\s)/, (_, hashes, space) => {
		if (hashes.length >= 6) {
			throw new Error(
				`${context.file}: a level ${hashes.length} heading cannot drop a level. ` +
					"Give this page a file of its own in site/scripts/gen-docs.mjs.",
			);
		}
		return `#${hashes}${space}`;
	});
}

/**
 * The body of one page as docs/ carries it: headings dropped a level where the
 * file holds more than one page, links and images retargeted.
 *
 * Fenced code is left exactly as it is, at whatever indentation it sits at:
 * <Steps> and <TabItem> both reduce to list items, so most of the code in
 * these pages is three or more columns in. Many of these fences open with a
 * shell comment on its own line, which is not a heading, and several carry
 * RouterOS paths such as `/container/print`, which are not links.
 *
 * @param {string} markdown the reduced page body
 * @param {{ demote: boolean, dir: string, file: string, route: string }} context
 * @returns {string} the body as it belongs in docs/
 */
function transplant(markdown, context) {
	let fence = null;
	const out = [];
	let prose = [];
	const flush = () => {
		if (prose.length > 0) out.push(rewrite(prose.join("\n"), context));
		prose = [];
	};
	for (const line of markdown.split("\n")) {
		const marker = /^[ \t]*(`{3,}|~{3,})/.exec(line);
		if (fence) {
			out.push(line);
			if (
				marker &&
				marker[1][0] === fence[0] &&
				marker[1].length >= fence.length
			)
				fence = null;
			continue;
		}
		if (marker) {
			flush();
			out.push(line);
			fence = marker[1];
			continue;
		}
		prose.push(context.demote ? demote(line, context) : line);
	}
	flush();
	return out.join("\n");
}

/**
 * One file of docs/.
 *
 * @param {{ file: string, title?: string, routes: string[] }} spec
 * @param {Map<string, any>} pages every English page
 * @returns {string} the file
 */
function render(spec, pages) {
	const many = spec.routes.length > 1;
	const parts = [
		`# ${spec.title ?? pages.get(spec.routes[0]).title}`,
		"",
		notice(spec.routes.map((route) => pages.get(route).file)),
	];
	for (const route of spec.routes) {
		const page = pages.get(route);
		if (many) parts.push("", `## ${page.title}`);
		parts.push(
			"",
			page.description,
			"",
			// An autolink rather than the bare URL, because a bare URL in prose is
			// not a link everywhere docs/ will be read.
			`Source: <${publicUrl(route)}>`,
			"",
			transplant(
				reduceBody({ body: page.body, file: page.file, locale: "en" }),
				{
					demote: many,
					dir: page.dir,
					file: page.file,
					route,
				},
			),
		);
	}
	return `${parts
		.join("\n")
		.replace(/\n{3,}/g, "\n\n")
		.trim()}\n`;
}

/**
 * The index: one row per generated file, in the order the site's sidebar puts
 * them, saying what its first page says about itself.
 *
 * @param {Map<string, any>} pages every English page
 * @returns {string} docs/README.md
 */
function renderIndex(pages) {
	const rows = MANIFEST.map((spec) => {
		const first = pages.get(spec.routes[0]);
		return `| [${spec.title ?? first.title}](${spec.file}) | ${first.description} |`;
	});
	return `${[
		"# Documentation",
		"",
		notice([]),
		"",
		"| File | What it covers |",
		"| --- | --- |",
		...rows,
		"",
		"Beside them, in the repository rather than on the site:",
		"",
		...NEIGHBOURS.map(([target, what]) => `- [${target}](${target}): ${what}`),
	].join("\n")}\n`;
}

const pages = readPages();

const claimed = new Map();
for (const spec of MANIFEST) {
	for (const route of spec.routes) {
		if (!pages.has(route)) {
			throw new Error(
				`site/scripts/gen-docs.mjs claims a page that does not exist: ${route}`,
			);
		}
		if (claimed.has(route)) {
			throw new Error(
				`${route} is claimed by both docs/${claimed.get(route)} and docs/${spec.file}`,
			);
		}
		claimed.set(route, spec.file);
	}
}
const orphans = [...pages.keys()].filter(
	(route) => !claimed.has(route) && !NOT_IN_DOCS.has(route),
);
if (orphans.length > 0) {
	console.error(
		`[docs] ${orphans.length} page(s) belong to no file of docs/:\n` +
			orphans.map((route) => `  /${route}/`).join("\n") +
			"\n  Add each to MANIFEST in site/scripts/gen-docs.mjs, or to NOT_IN_DOCS with the reason.",
	);
	process.exit(1);
}

const wanted = new Map([
	...MANIFEST.map((spec) => [spec.file, render(spec, pages)]),
	["README.md", renderIndex(pages)],
]);

/** @param {string} target @returns {string} the file, or "" if it is not there. */
function readSafely(target) {
	try {
		return readFileSync(target, "utf8");
	} catch {
		return "";
	}
}

const checking = process.argv.includes("--check");
const stale = [];
for (const [file, text] of wanted) {
	const target = path.join(DOCS, file);
	if (readSafely(target) === text) continue;
	if (checking) stale.push(file);
	else writeFileSync(target, text);
}

const extra = readdirSync(DOCS)
	.filter((name) => name.endsWith(".md"))
	.filter((name) => !wanted.has(name));
if (extra.length > 0) {
	console.error(
		`[docs] ${extra.length} file(s) under docs/ are generated by nothing:\n` +
			extra.map((name) => `  docs/${name}`).join("\n") +
			"\n  Delete them, or give them an entry in MANIFEST in site/scripts/gen-docs.mjs.",
	);
	process.exit(1);
}

if (checking) {
	if (stale.length > 0) {
		console.error(
			`[docs] ${stale.length} file(s) under docs/ no longer match the pages they are generated from:\n` +
				stale.map((name) => `  docs/${name}`).join("\n") +
				"\n  Refresh them with: pnpm run docs",
		);
		process.exit(1);
	}
	console.log(
		`[docs] ${wanted.size} files, generated from ${claimed.size} pages, all up to date.`,
	);
} else {
	console.log(
		`[docs] wrote ${wanted.size} files from ${claimed.size} pages under site/src/content/docs.`,
	);
}
