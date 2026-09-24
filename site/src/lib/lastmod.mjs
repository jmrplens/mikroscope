// @ts-check
// When a page last changed: the newest commit among the page's own file and
// the data it renders.
//
// Starlight dates a page by its .mdx file alone, and the sitemap's <lastmod>
// used to do the same. For a page whose words live in src/data that date is
// wrong. The landing is one `<Home content={en} />` tag: its figures, its cost
// table and their provenance come from src/data/home.ts and measurements.ts.
// On 2026-09-24 the English landing still read "Last updated: Sep 16, 2026"
// (index.mdx, last commit 2026-09-16), above a measurement dated 2026-09-18.
// The Spanish landing read Sep 19 for the same content, because es/index.mdx
// had a later commit of its own.
//
// Here a page counts the files that decide what it says:
//
// - its own file;
// - every module under src/data that it reaches through a value import, even
//   when the import passes through a component or a lib file first;
// - every file that is not code and is reached the same way: an image the page
//   imports, the dashboards and alert rules that dashboards.ts reads with
//   `?raw`, the figures Figure.astro globs;
// - VERSION, when the page writes the version placeholder in its code.
//
// Code does not count. A component, a lib module or a stylesheet decides how a
// page looks, not what it says, and counting them would re-date every page
// whenever format.ts or a component's CSS changed. Type-only imports do not
// count either, because nothing crosses them at run time.
//
// The granularity is the file, as it is in Starlight's own implementation. A
// commit that touches measurements.ts re-dates every page that renders a
// figure from it, whether or not the lines that page shows changed. That errs
// towards "look again". The old rule erred the other way, telling a reader the
// landing had not changed while its numbers had.
//
// One table, used by both readers of these dates. astro.config.mjs builds it
// once, and hands the same object to the sitemap's `serialize` and, through a
// virtual module, to src/lastUpdated.mjs, which sets the Starlight route's
// `lastUpdated`. So the sitemap, the page footer and the JSON-LD
// `dateModified` (Head.astro reads the route's value) cannot disagree.
// Starlight's own build does the same: it computes the git dates when the
// config is set up and inlines them as `virtual:starlight/git-info`.
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import path from "node:path";

// What decides how a page looks rather than what it says. Anything else a page
// reaches is content: .json, .yaml, .svg, .webp, VERSION.
const CODE = new Set([
	".astro",
	".ts",
	".tsx",
	".mts",
	".js",
	".mjs",
	".cjs",
	".css",
]);

// Extensions tried, in order, for a specifier written without one. The data
// modules import each other as `./measurements`, and pages import components
// with the extension, so both spellings occur.
const EXTENSIONS = [".ts", ".mjs", ".js", ".astro", ".json", ".tsx"];

// Files reached but not counted, repo-relative, each with its reason.
//
// CHANGELOG.md: src/data/release.ts reads it only for the dated heading of the
// release VERSION names, and that heading lands in the same commit as the
// VERSION bump (.github/RELEASING.md), so VERSION's date already covers it.
// Counted whole, every entry added under "Unreleased" would re-date every page
// that shows the version.
const NOT_CONTENT = new Set(["CHANGELOG.md"]);

// `import x from "…"`, `import { a,\n b } from "…"`, `export { a } from "…"`.
// The `type` group separates the type-only imports. Quotes, backticks and
// semicolons are excluded from the span between the keyword and `from`, so a
// match cannot run across a string or into the next statement.
const FROM =
	/\b(import|export)\s+(type\s+)?[^;"'`]*?\bfrom\s*(["'])([^"']+)\3/g;
// `import "./side-effect.css"`.
const BARE = /\bimport\s*(["'])([^"']+)\1/g;
// `import.meta.glob("../data/figures/*.svg", …)`, with or without a type
// argument.
const GLOB = /\bimport\.meta\.glob\s*(?:<[^>]*>)?\s*\(\s*(["'])([^"']+)\1/g;

const VERSION_PLACEHOLDER = "{{MIKROSCOPE_VERSION}}";

/**
 * Resolves a relative specifier to an existing file, or undefined. Bare
 * specifiers (packages, `astro:content`, `virtual:…`) are not the site's files
 * and are skipped by the caller.
 * @param {string} from absolute path of the importing file
 * @param {string} specifier as written, possibly with a `?raw` query
 * @returns {string | undefined}
 */
function resolveRelative(from, specifier) {
	const base = path.resolve(path.dirname(from), specifier.replace(/\?.*$/, ""));
	const candidates = [
		base,
		...EXTENSIONS.map((ext) => base + ext),
		...EXTENSIONS.map((ext) => path.join(base, `index${ext}`)),
	];
	return candidates.find((file) => existsSync(file) && statSync(file).isFile());
}

/**
 * The files a glob pattern matches. Only a `*` inside the last segment is
 * supported, which is every pattern the site writes. A pattern this cannot
 * read throws, rather than quietly counting nothing.
 * @param {string} from absolute path of the file holding the glob
 * @param {string} pattern
 * @returns {string[]}
 */
function expandGlob(from, pattern) {
	const dir = path.resolve(path.dirname(from), path.dirname(pattern));
	const last = path.basename(pattern);
	if (/[*?[\]{}]/.test(path.dirname(pattern)) || /\*\*|[?[\]{}]/.test(last)) {
		throw new Error(
			`src/lib/lastmod.mjs: ${from} globs "${pattern}", and only a * in the last segment is understood here`,
		);
	}
	const match = new RegExp(
		`^${last.replace(/[.+^$()|\\]/g, "\\$&").replace(/\*/g, "[^/]*")}$`,
	);
	if (!existsSync(dir)) return [];
	return readdirSync(dir)
		.filter((name) => match.test(name))
		.map((name) => path.join(dir, name));
}

/**
 * Every file a module reaches through value imports and globs, itself
 * excluded. Specifiers that are not relative are not followed.
 * @param {string} file absolute path
 * @returns {string[]}
 */
function importsOf(file) {
	const source = readFileSync(file, "utf8");
	/** @type {string[]} */
	const found = [];
	for (const [, , typeOnly, , specifier] of source.matchAll(FROM)) {
		if (typeOnly || !specifier.startsWith(".")) continue;
		const target = resolveRelative(file, specifier);
		if (target) found.push(target);
	}
	for (const [, , specifier] of source.matchAll(BARE)) {
		if (!specifier.startsWith(".")) continue;
		const target = resolveRelative(file, specifier);
		if (target) found.push(target);
	}
	for (const [, , pattern] of source.matchAll(GLOB)) {
		if (pattern.startsWith(".")) found.push(...expandGlob(file, pattern));
	}
	return found;
}

/**
 * The files whose content a page renders, the page's own file first. Paths are
 * absolute.
 * @param {string} siteRoot absolute path of site/
 * @param {string} pageFile absolute path of an .mdx page
 * @returns {string[]}
 */
export function pageSources(siteRoot, pageFile) {
	const repoRoot = path.dirname(siteRoot);
	const dataDir = path.join(siteRoot, "src", "data") + path.sep;
	const nodeModules = `${path.sep}node_modules${path.sep}`;
	const counted = new Set([pageFile]);
	const seen = new Set([pageFile]);
	const queue = [pageFile];
	while (queue.length > 0) {
		const file = /** @type {string} */ (queue.shift());
		for (const target of importsOf(file)) {
			if (seen.has(target) || target.includes(nodeModules)) continue;
			seen.add(target);
			const isCode = CODE.has(path.extname(target));
			const isData = target.startsWith(dataDir);
			if (NOT_CONTENT.has(path.relative(repoRoot, target))) continue;
			if (isData || !isCode) counted.add(target);
			// Only code imports anything; a .json or an .svg is a leaf.
			if (isCode) queue.push(target);
		}
	}
	if (readFileSync(pageFile, "utf8").includes(VERSION_PLACEHOLDER)) {
		counted.add(path.join(repoRoot, "VERSION"));
	}
	return [...counted];
}

/**
 * The committer date of the newest commit that touched a file, or undefined
 * when the file has no history (not a git checkout, or not committed yet).
 * @param {string} siteRoot
 * @param {string} file absolute path
 * @param {Map<string, Date | undefined>} cache
 * @returns {Date | undefined}
 */
function newestCommit(siteRoot, file, cache) {
	if (cache.has(file)) return cache.get(file);
	/** @type {Date | undefined} */
	let date;
	try {
		const out = execFileSync(
			"git",
			["log", "-1", "--format=%cI", "--", path.relative(siteRoot, file)],
			{
				cwd: siteRoot,
				encoding: "utf-8",
				stdio: ["ignore", "pipe", "ignore"],
			},
		).trim();
		if (out) date = new Date(out);
	} catch {
		// Not a git checkout, or git is missing: no date, as Starlight does.
	}
	cache.set(file, date);
	return date;
}

/**
 * Every page under src/content/docs, with the date it last changed and the
 * files that date was taken from. Keys are site-relative with forward slashes,
 * which is how Starlight's `entry.filePath` spells them
 * ("src/content/docs/es/index.mdx").
 *
 * A page that has no history of its own gets no date at all, even when its
 * data files have one: a page written but not committed would otherwise be
 * dated by the last change to measurements.ts, a date from before it existed.
 * Starlight shows no date for such a page either.
 *
 * @param {string} siteRoot absolute path of site/
 * @returns {Record<string, { date: string, from: string[] }>}
 */
export function lastmodTable(siteRoot) {
	const docs = path.join(siteRoot, "src", "content", "docs");
	/** @type {Map<string, Date | undefined>} */
	const cache = new Map();
	/** @type {Record<string, { date: string, from: string[] }>} */
	const table = {};
	for (const entry of readdirSync(docs, { recursive: true })) {
		const name = String(entry);
		if (!/\.mdx?$/.test(name)) continue;
		const pageFile = path.join(docs, name);
		const own = newestCommit(siteRoot, pageFile, cache);
		if (!own) continue;
		let newest = own;
		/** @type {string[]} */
		const from = [];
		for (const file of pageSources(siteRoot, pageFile)) {
			const date = newestCommit(siteRoot, file, cache);
			if (!date) continue;
			if (date > newest) newest = date;
			from.push(path.relative(siteRoot, file).split(path.sep).join("/"));
		}
		const key = path.relative(siteRoot, pageFile).split(path.sep).join("/");
		table[key] = { date: newest.toISOString(), from };
	}
	return table;
}
