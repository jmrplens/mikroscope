// The closure between a page, its markdown twin and the llms.txt indexes.
//
// Each of those three is generated, and each of them is a valid document when
// it is wrong: a page whose twin was never emitted still renders, a twin left
// behind by a deleted page still parses, an index still lists a URL that now
// 404s. Nothing else in the pipeline reads them, so this is the class of drift
// that ships silently and is only noticed by whoever fetched the twin instead
// of the page, which is a machine, which does not file bugs.
//
// Beyond existing, a twin has to say what its page says where it matters
// most and where it has already been wrong: the provenance under a figure (the
// landing's twin named the 2026-09-15 campaign under the 2026-09-18 table the
// page described correctly, GEO audit of 2026-09-24), the landing's hero
// tagline, which the reduction of the body never saw, and links a model can
// follow, which means absolute ones. The llms files have to list each other
// in both languages alike and carry the release the build documents.
//
// Usage:
//   node scripts/check-twins.mjs [dist-directory]
//   node scripts/check-twins.mjs --only twins   # page and twin closure
//   node scripts/check-twins.mjs --only llms    # the llms.txt indexes and bundles
import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

import { readRelease } from "../src/lib/release.mjs";
import { isRedirectStub } from "./redirect-stub.mjs";

const args = process.argv.slice(2);
const positional = [];
let only = "all";
for (let i = 0; i < args.length; i += 1) {
	if (args[i] === "--only") {
		i += 1;
		only = args[i];
	} else {
		positional.push(args[i]);
	}
}
if (!["all", "twins", "llms"].includes(only)) {
	console.error(`[twins] unknown --only ${only}. Use "twins" or "llms".`);
	process.exit(1);
}
// An explicit directory is for reproducing a defect against a build kept
// aside; with none, the check reads the build that is there.
const DIST = resolve(positional[0] ?? "dist");

// The site root as the build itself states it, rather than as a constant this
// file would have to be kept in step with. The sitemap index is written by the
// sitemap integration from the same `site` and `base` every URL on the site is
// built from.
const sitemapIndex = join(DIST, "sitemap-index.xml");
if (!existsSync(sitemapIndex)) {
	console.error(
		`[twins] no sitemap-index.xml under ${DIST}. Run pnpm build first.`,
	);
	process.exit(1);
}
const SITE_ROOT = /<loc>([^<]+)\/sitemap-\d+\.xml<\/loc>/.exec(
	readFileSync(sitemapIndex, "utf8"),
)?.[1];
if (!SITE_ROOT) {
	console.error(
		"[twins] could not read the site root out of sitemap-index.xml",
	);
	process.exit(1);
}

const problems = [];
const fail = (message) => problems.push(message);

const REPO = dirname(dirname(dirname(fileURLToPath(import.meta.url))));
const release = readRelease(REPO);

// The base path every site link starts with, from the same sitemap.
const BASE = new URL(`${SITE_ROOT}/`).pathname;

/**
 * Text as a reader sees it, for comparing a page with its twin: every kind of
 * space one space, code marks out. The page keeps "RouterOS 7.24.2" on one
 * line with U+00A0 and the twin does not; the page writes code as <code>, the
 * twin as backticks.
 *
 * @param {string} text markdown
 * @returns {string}
 */
const plain = (text) =>
	text
		.replaceAll("`", "")
		.replaceAll(/[\s\u00a0\u2009\u202f]+/g, " ")
		.trim();

/**
 * The same for a fragment of HTML: tags out and entities decoded first.
 *
 * @param {string} html
 * @returns {string}
 */
function plainHtml(html) {
	let text = html;
	let previous;
	// Until nothing changes: one pass can join two halves of a tag into a new
	// one (`<scr<b>ipt>`).
	do {
		previous = text;
		text = text.replaceAll(/<[^>]*>/g, "");
	} while (text !== previous);
	return plain(
		text
			.replaceAll(/&#x([0-9a-f]+);/gi, (_, hex) =>
				String.fromCodePoint(Number.parseInt(hex, 16)),
			)
			.replaceAll(/&#(\d+);/g, (_, dec) => String.fromCodePoint(Number(dec)))
			.replaceAll("&quot;", '"')
			.replaceAll("&lt;", "<")
			.replaceAll("&gt;", ">")
			.replaceAll("&nbsp;", " ")
			.replaceAll("&amp;", "&"),
	);
}

/**
 * The lines of a markdown text that are not inside a fenced block, each with
 * its code spans blanked, so what is left is prose a link can sit in.
 *
 * @param {string} text
 * @returns {string[]}
 */
function proseLines(text) {
	const out = [];
	let fence = null;
	for (const line of text.split("\n")) {
		const marker = /^[ \t]*(`{3,}|~{3,})/.exec(line);
		if (fence !== null) {
			if (
				marker &&
				marker[1][0] === fence[0] &&
				marker[1].length >= fence.length
			)
				fence = null;
			continue;
		}
		if (marker) {
			fence = marker[1];
			continue;
		}
		out.push(line.replaceAll(/(`+)[^`]*?\1/g, ""));
	}
	return out;
}

/**
 * The first link target in a markdown text that a reader of the text alone
 * cannot follow: rooted at the site, or a bare fragment.
 *
 * @param {string} text
 * @returns {string | undefined}
 */
const relativeTarget = (text) =>
	proseLines(text)
		.map((line) => /\]\(((?:\/|#)[^)\s]*)/.exec(line)?.[1])
		.find(Boolean);

/** Every file under dist matching a name test, as dist-relative paths. */
function* files(dir, matches, prefix = "") {
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		const relative = prefix ? `${prefix}/${entry.name}` : entry.name;
		if (entry.isDirectory())
			yield* files(join(dir, entry.name), matches, relative);
		else if (entry.isFile() && matches(entry.name)) yield relative;
	}
}

// The not-found pages are not documents. 404.html is the one file GitHub Pages
// serves for every URL that does not exist; es/404/index.html is its Spanish
// counterpart, built as an ordinary page so the language menu has somewhere to
// go. Neither has a twin, and no index lists them.
const NOT_A_PAGE = new Set(["404.html", "es/404/index.html"]);

const pages = [...files(DIST, (name) => name === "index.html")]
	.filter(
		(path) =>
			!NOT_A_PAGE.has(path) &&
			!isRedirectStub(readFileSync(join(DIST, path), "utf8")),
	)
	.map((path) => path.replace(/index\.html$/, ""));
const twins = new Set([...files(DIST, (name) => name === "index.md")]);

if (only !== "llms") {
	if (pages.length === 0) {
		fail(`no built pages under ${DIST}. Run pnpm build first.`);
	}

	for (const page of pages) {
		const twin = `${page}index.md`;
		if (!twins.has(twin)) {
			fail(`/${page} has no markdown twin at /${twin}`);
			continue;
		}
		// An empty twin is worse than a missing one: it answers the request, so
		// whatever fetched it reads the page as having no content.
		const text = readFileSync(join(DIST, twin), "utf8");
		if (!/^#\s+\S/m.test(text) || text.trim().split("\n").length < 4) {
			fail(
				`/${twin} is empty or has no heading (${statSync(join(DIST, twin)).size} bytes)`,
			);
		}

		// The announcement, which is how anything finds the twin without guessing
		// the convention.
		const html = readFileSync(join(DIST, `${page}index.html`), "utf8");
		const link =
			/<link[^>]*rel="alternate"[^>]*type="text\/markdown"[^>]*>/.exec(html);
		if (!link) {
			fail(
				`/${page} does not announce its twin with <link rel="alternate" type="text/markdown">`,
			);
			continue;
		}
		const href = /href="([^"]+)"/.exec(link[0])?.[1];
		const target = href?.startsWith(SITE_ROOT)
			? href.slice(SITE_ROOT.length).replace(/^\//, "")
			: href?.replace(/^\/[^/]+\//, "");
		if (!href || !target || !existsSync(join(DIST, target))) {
			fail(
				`/${page} announces a twin at ${href}, which the build does not contain`,
			);
		}

		// Every provenance line the page shows, the twin shows too, word for word.
		const twinText = plain(text);
		for (const m of html.matchAll(
			/<p class="ms-provenance"[^>]*data-campaign="([^"]+)"[^>]*>([\s\S]*?)<\/p>/g,
		)) {
			if (!twinText.includes(plainHtml(m[2]))) {
				fail(
					`/${twin} does not carry the provenance its page shows for ${m[1]}: "${plainHtml(m[2]).slice(0, 90)}…"`,
				);
			}
		}

		// Links a model can follow without knowing where the file came from.
		const loose = relativeTarget(text);
		if (loose) {
			fail(
				`/${twin} links to ${loose}, which resolves only against the page it came from; twins carry absolute links`,
			);
		}
	}

	// The landings. The hero tagline is frontmatter, which the reduction of the
	// body does not see, so the twin is held to it here. And their links are
	// the one set on the site no validator reads: they live in a typed object
	// (src/data/home.ts), not in Markdown, which is all starlight-links-validator
	// checks, so a link to a heading that was renamed would ship. Each internal
	// link in the landing's <main>, fragment included, is resolved here.
	for (const landing of ["", "es/"]) {
		const htmlPath = join(DIST, `${landing}index.html`);
		const twinPath = join(DIST, `${landing}index.md`);
		if (!existsSync(htmlPath) || !existsSync(twinPath)) continue;
		const html = readFileSync(htmlPath, "utf8");
		const tagline = /<div class="tagline[^"]*"[^>]*>([\s\S]*?)<\/div>/.exec(
			html,
		)?.[1];
		if (tagline === undefined) {
			fail(`/${landing} has no hero tagline in its HTML`);
		} else if (
			!plain(readFileSync(twinPath, "utf8")).includes(plainHtml(tagline))
		) {
			fail(
				`/${landing}index.md does not carry the hero tagline its page opens with`,
			);
		}
		const main = /<main[\s\S]*?<\/main>/.exec(html)?.[0] ?? "";
		for (const m of main.matchAll(/href="([^"]+)"/g)) {
			const href = m[1].replaceAll("&amp;", "&");
			if (!href.startsWith(BASE)) continue;
			const [path, fragment] = href.split("#");
			const relative = decodeURIComponent(path.slice(BASE.length));
			const file =
				relative === "" || relative.endsWith("/")
					? `${relative}index.html`
					: relative;
			if (!existsSync(join(DIST, file))) {
				fail(`/${landing} links to ${href}, which the build does not contain`);
				continue;
			}
			if (fragment !== undefined && fragment !== "") {
				const id = decodeURIComponent(fragment);
				const target = readFileSync(join(DIST, file), "utf8");
				if (!target.includes(`id="${id}"`)) {
					fail(`/${landing} links to ${href}, but /${file} has no id "${id}"`);
				}
			}
		}
	}

	for (const twin of twins) {
		if (!pages.includes(twin.replace(/index\.md$/, ""))) {
			fail(`/${twin} has no page: the twin outlived whatever it doubled`);
		}
	}
}

if (only !== "twins") {
	const INDEXES = ["llms.txt", "es/llms.txt"];
	/** The bundles of one locale, as the build wrote them: the whole, the core and every section file. */
	const bundlesIn = (prefix) => [
		`${prefix}llms-full.txt`,
		`${prefix}llms-core.txt`,
		...(existsSync(join(DIST, `${prefix}llms`))
			? readdirSync(join(DIST, `${prefix}llms`))
					.filter((name) => name.endsWith(".txt"))
					.map((name) => `${prefix}llms/${name}`)
			: []),
	];
	const listedBy = new Map();

	for (const index of [...INDEXES, ...bundlesIn(""), ...bundlesIn("es/")]) {
		const path = join(DIST, index);
		if (!existsSync(path)) {
			fail(`/${index} was not generated`);
			continue;
		}
		const text = readFileSync(path, "utf8");
		if (text.trim().length === 0) {
			fail(`/${index} is empty`);
			continue;
		}
		// Every llms file opens with the facts, the current release among them:
		// in its head, before the first section or the first page.
		const lines = text.split("\n");
		const end = lines.findIndex(
			(line) => line === "---" || line.startsWith("## "),
		);
		const facts =
			lines
				.slice(0, end === -1 ? lines.length : end)
				.find((line) => line.startsWith("> ")) ?? "";
		if (!facts.includes(release.version) || !facts.includes(release.date)) {
			fail(
				`/${index} does not state the current release (${release.version}, ${release.date}) in its opening facts`,
			);
		}
		const loose = relativeTarget(text);
		if (loose) {
			fail(
				`/${index} links to ${loose}, which a model reading the file cannot resolve; llms files carry absolute links`,
			);
		}
		if (!INDEXES.includes(index)) continue;

		const listed = [...text.matchAll(/\]\((https?:\/\/[^)]+)\)/g)].map(
			(m) => m[1],
		);
		listedBy.set(index, listed);
		if (listed.length === 0) fail(`/${index} lists no pages`);
		for (const url of listed) {
			if (!url.startsWith(SITE_ROOT)) continue; // an outbound link, not a page
			const relative = url.slice(SITE_ROOT.length).replace(/^\//, "");
			const target =
				relative.endsWith("/") || relative === ""
					? `${relative}index.html`
					: relative;
			if (!existsSync(join(DIST, target))) {
				fail(`/${index} lists ${url}, which the build does not contain`);
			}
		}

		// The other direction: a page the index does not list is a page no model
		// reading this file will ever see, and so is a bundle.
		const locale = index.startsWith("es/") ? "es" : "en";
		const ours = pages.filter(
			(page) =>
				(page.startsWith("es/") || page === "es/") === (locale === "es"),
		);
		for (const page of ours) {
			if (!listed.includes(`${SITE_ROOT}/${page}`)) {
				fail(`/${index} does not list /${page}`);
			}
		}
		for (const bundle of bundlesIn(locale === "es" ? "es/" : "")) {
			if (!listed.includes(`${SITE_ROOT}/${bundle}`)) {
				fail(`/${index} does not list /${bundle}`);
			}
		}
	}

	// The two indexes list the same pages and the same files, each in its own
	// language: a bundle published in one language only, or a page one index
	// forgot, is a hole that parses.
	const shape = (index) =>
		new Set(
			(listedBy.get(index) ?? [])
				.filter((url) => url.startsWith(SITE_ROOT))
				.map((url) => url.slice(SITE_ROOT.length).replace(/^\//, ""))
				// The link each index makes to the other.
				.filter((relative) => !INDEXES.includes(relative))
				.map((relative) => relative.replace(/^es(\/|$)/, "")),
		);
	const en = shape("llms.txt");
	const es = shape("es/llms.txt");
	for (const relative of en) {
		if (!es.has(relative))
			fail(`/llms.txt lists /${relative}, /es/llms.txt has no twin of it`);
	}
	for (const relative of es) {
		if (!en.has(relative))
			fail(`/es/llms.txt lists /es/${relative}, /llms.txt has no twin of it`);
	}
}

if (problems.length > 0) {
	console.error(`[twins] ${problems.length} problem(s):`);
	for (const problem of problems) console.error(`  ${problem}`);
	process.exit(1);
}

console.log(
	only === "llms"
		? "[twins] both llms.txt indexes list pages and bundles that exist, alike in both languages, every llms file states the current release, and every link in them is absolute."
		: only === "twins"
			? `[twins] ${pages.length} pages, ${twins.size} twins, each announced, each with a page, each carrying its page's provenance and only absolute links; both landings' tagline and links hold.`
			: `[twins] ${pages.length} pages, ${twins.size} twins, and the llms.txt indexes and bundles closed.`,
);
