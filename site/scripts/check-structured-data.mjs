// The JSON-LD graph and the Open Graph tags the site publishes, read back out
// of the build.
//
// They are the parts of a page written for machines alone, and until this
// check existed nothing in the repository read them. html-validate does not
// look inside a <script>, htmlhint does not either, pa11y has no reason to, and
// a link checker sees no links there. A graph could stop being JSON, lose the
// node that carries the author, or point every reference at an @id that is not
// in the document, and every other check would stay green.
//
// What it asserts is derived from each page's own URL, from the build around
// it and from the repository, not from what src/components/overrides/Head.astro
// believes: a check that asked the emitter what it emits would agree with the
// emitter's defects. So the version comes from VERSION and CHANGELOG.md, a
// page's first publication from git, its section from the sidebar the build
// rendered, its translation from the source tree, the RouterOS floor from
// install/prerequisites, and the lineage from about/lineage.
//
// It needs the repository's full history: a page's datePublished is the date
// git first saw its file, and a shallow clone cannot say. docs.yml checks out
// with `fetch-depth: 0`.
//
// Usage: node scripts/check-structured-data.mjs [dist-directory]
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { join, resolve } from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

import { readRelease } from "../src/lib/release.mjs";
import { isRedirectStub } from "./redirect-stub.mjs";

const DIST = resolve(process.argv[2] ?? "dist");
const SITE_DIR = fileURLToPath(new URL("..", import.meta.url));
const REPO_ROOT = resolve(SITE_DIR, "..");
const DOCS = join(SITE_DIR, "src", "content", "docs");
const PERSON_ID = "https://jmrp.io/#person";
const RELEASE = readRelease(REPO_ROOT);

// The nodes every page carries. They describe one website, one program and one
// person, so they must be the same bytes on every page: a consumer merging two
// pages by @id must never be handed two values for one property.
const SHARED_TYPES = [
	"Person",
	"WebSite",
	"SoftwareApplication",
	"SoftwareSourceCode",
];
// Open Graph wants language_TERRITORY. The English pages are British English
// and the Spanish ones are written for Spain.
const OG_LOCALE = { en: "en_GB", es: "es_ES" };
const WIKIDATA = /^https:\/\/www\.wikidata\.org\/wiki\/Q\d+$/;

const sitemapIndex = join(DIST, "sitemap-index.xml");
if (!existsSync(sitemapIndex)) {
	console.error(
		`[schema] no sitemap-index.xml under ${DIST}. Run pnpm build first.`,
	);
	process.exit(1);
}
// The site root as the build states it, so a domain or base rename needs no
// edit here.
const SITE_ROOT = /<loc>([^<]+)\/sitemap-\d+\.xml<\/loc>/.exec(
	readFileSync(sitemapIndex, "utf8"),
)?.[1];
if (!SITE_ROOT) {
	console.error(
		"[schema] could not read the site root out of sitemap-index.xml",
	);
	process.exit(1);
}
const BASE_PATH = new URL(SITE_ROOT).pathname.replace(/\/$/, "");

// Each page's <lastmod>, from every sitemap the index lists. The build hands
// the sitemap and Starlight's "Last updated" (which Head.astro's dateModified
// reads) one table, src/lib/lastmod.mjs, so the two must agree to the second.
const sitemapDates = new Map();
for (const [, name] of readFileSync(sitemapIndex, "utf8").matchAll(
	/<loc>[^<]*\/(sitemap-\d+\.xml)<\/loc>/g,
)) {
	const xml = readFileSync(join(DIST, name), "utf8");
	for (const [, entry] of xml.matchAll(/<url>([\s\S]*?)<\/url>/g)) {
		const loc = /<loc>([^<]+)<\/loc>/.exec(entry)?.[1];
		if (loc)
			sitemapDates.set(loc, /<lastmod>([^<]+)<\/lastmod>/.exec(entry)?.[1]);
	}
}
const ORIGIN = new URL(SITE_ROOT).origin;

const problems = [];

/** Standard output of a git command in the repository, or "" if it fails. */
function git(args) {
	try {
		return execFileSync("git", args, {
			cwd: REPO_ROOT,
			encoding: "utf8",
			stdio: ["ignore", "pipe", "ignore"],
		}).trim();
	} catch {
		return "";
	}
}

const shallow = git(["rev-parse", "--is-shallow-repository"]);
const fullHistory = shallow === "false";
if (!fullHistory) {
	problems.push(
		shallow === "true"
			? "the checkout is shallow, so no page's datePublished can be checked against git; check out with fetch-depth: 0"
			: "git could not be run here, so no page's datePublished can be checked against it",
	);
}
// The day the repository's history begins, which is the day it was published:
// the first commit is the public 1.0.0.
const rootCommit = git(["log", "--max-parents=0", "--format=%cI", "HEAD"])
	.split("\n")
	.filter(Boolean)
	.at(-1);
const FIRST_PUBLISHED = rootCommit
	? new Date(rootCommit).toISOString().slice(0, 10)
	: undefined;

function* htmlFiles(dir, prefix = "") {
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		const relative = prefix ? `${prefix}/${entry.name}` : entry.name;
		if (entry.isDirectory()) yield* htmlFiles(join(dir, entry.name), relative);
		else if (entry.isFile() && entry.name.endsWith(".html")) yield relative;
	}
}

/** The dist path a site URL addresses, or undefined if it is not on this site. */
function distPathOf(url) {
	if (typeof url !== "string" || !url.startsWith(SITE_ROOT)) return undefined;
	const relative = url.slice(SITE_ROOT.length).replace(/^\//, "");
	return relative === "" || relative.endsWith("/")
		? `${relative}index.html`
		: relative;
}

/** Whether a site URL names a file the build contains. */
const built = (url) => {
	const path = distPathOf(url);
	return Boolean(path) && existsSync(join(DIST, path));
};

/** The source file behind a route ("" for the English home), if one exists. */
function sourceOf(route) {
	const candidates =
		route === ""
			? ["index.mdx", "index.md"]
			: [
					`${route}.mdx`,
					`${route}.md`,
					`${route}/index.mdx`,
					`${route}/index.md`,
				];
	return candidates.map((file) => join(DOCS, file)).find(existsSync);
}

/** The route of the same page in the other language. */
const otherLanguage = (route) =>
	route === "es"
		? ""
		: route.startsWith("es/")
			? route.slice(3)
			: route === ""
				? "es"
				: `es/${route}`;

/** When git first saw a file, following renames: the committer date of the oldest add. */
function firstAdded(file) {
	const oldest = git([
		"log",
		"--follow",
		"--diff-filter=A",
		"--format=%cI",
		"--",
		file,
	])
		.split("\n")
		.filter(Boolean)
		.at(-1);
	return oldest ? new Date(oldest).toISOString() : undefined;
}

const decode = (text) =>
	text
		.replace(/&#(\d+);/g, (_, code) => String.fromCodePoint(Number(code)))
		.replace(/&#x([\da-f]+);/gi, (_, code) =>
			String.fromCodePoint(Number.parseInt(code, 16)),
		)
		.replace(/&quot;/g, '"')
		.replace(/&lt;/g, "<")
		.replace(/&gt;/g, ">")
		.replace(/&amp;/g, "&");

/** Every <meta> in the head, as an object of its attributes. */
function metaTags(html) {
	const head = html.slice(0, html.indexOf("</head>"));
	return [...head.matchAll(/<meta\s([^>]*)>/g)].map(([, attrs]) =>
		Object.fromEntries(
			[...attrs.matchAll(/([\w:-]+)="([^"]*)"/g)].map(([, key, value]) => [
				key,
				decode(value),
			]),
		),
	);
}

/**
 * The label of the sidebar group around the link the build marked as the
 * current page, or undefined when the sidebar does not list the page. Read
 * from the rendered sidebar, which is what a reader sees, rather than from the
 * configuration, which is what the emitter reads.
 */
function sidebarSection(html) {
	const start = html.indexOf('id="starlight__sidebar"');
	const end = html.indexOf("</sl-sidebar-pane>", start);
	if (start === -1 || end === -1) return undefined;
	// Each group is a <details> whose <summary> holds its label; a link outside
	// every group has no section.
	const groups = [];
	for (const [token, label] of html
		.slice(start, end)
		.matchAll(
			/<details\b|<\/details>|<span class="large[^"]*">([^<]*)<\/span>|<a\s[^>]*aria-current="page"/g,
		)) {
		if (token.startsWith("<details")) groups.push(undefined);
		else if (token === "</details>") groups.pop();
		else if (label !== undefined) {
			if (groups.length > 0) groups[groups.length - 1] = decode(label);
		} else return groups.at(-1);
	}
	return undefined;
}

/**
 * A text property both languages must carry: `[{"@value", "@language": "en"},
 * {"@value", "@language": "es"}]`, each non-empty. Returns the two values, or
 * reports and returns undefined.
 */
function bilingual(value, what, report) {
	const values = Array.isArray(value) ? value : [];
	const byLanguage = Object.fromEntries(
		values.map((item) => [item?.["@language"], item?.["@value"]]),
	);
	const languages = values.map((item) => item?.["@language"]).sort();
	if (
		languages.join() !== "en,es" ||
		!byLanguage.en?.trim() ||
		!byLanguage.es?.trim()
	) {
		report(
			`${what} is ${JSON.stringify(value)}, expected one non-empty value tagged "en" and one tagged "es"`,
		);
		return undefined;
	}
	return byLanguage;
}

// The RouterOS floor as the prerequisites page states it, in each language,
// so the software's requirements cannot fall behind the page a reader follows.
const floorOf = (file, pattern) =>
	pattern.exec(readFileSync(join(DOCS, file), "utf8"))?.[1];
const FLOOR = {
	en: floorOf("install/prerequisites.mdx", /RouterOS (\d+\.\d+) or later/),
	es: floorOf(
		"es/install/prerequisites.mdx",
		/RouterOS (\d+\.\d+) o posterior/,
	),
};
const FLOOR_WORDING = {
	en: (floor) => `RouterOS ${floor} or later`,
	es: (floor) => `RouterOS ${floor} o posterior`,
};
const LINEAGE = readFileSync(join(DOCS, "about", "lineage.mdx"), "utf8");

/**
 * The nodes every page shares, checked once: the identity check below holds
 * every other page to the same bytes.
 */
function checkSharedNodes(nodes, report) {
	const website = nodes.find((node) => node["@type"] === "WebSite");
	const software = nodes.find(
		(node) => node["@type"] === "SoftwareApplication",
	);
	const source = nodes.find((node) => node["@type"] === "SoftwareSourceCode");

	if (website) bilingual(website.description, "WebSite description", report);

	if (software) {
		if (software.softwareVersion !== RELEASE.version) {
			report(
				`softwareVersion is ${JSON.stringify(software.softwareVersion)}, but VERSION is ${RELEASE.version}`,
			);
		}
		if (software.releaseNotes !== RELEASE.notesUrl) {
			report(
				`releaseNotes is ${JSON.stringify(software.releaseNotes)}, expected ${RELEASE.notesUrl}`,
			);
		}
		// A shallow clone's first commit is its cut-off, not the public 1.0.0,
		// and the shallow checkout is reported once already.
		if (fullHistory && !FIRST_PUBLISHED) {
			report("the repository's first commit could not be read");
		} else if (fullHistory && software.datePublished !== FIRST_PUBLISHED) {
			report(
				`the software's datePublished is ${JSON.stringify(software.datePublished)}, but the repository's first commit, the public 1.0.0, is from ${FIRST_PUBLISHED}`,
			);
		}
		if (!built(software.installUrl)) {
			report(
				`installUrl ${JSON.stringify(software.installUrl)} is not a page the build contains`,
			);
		}
		const sameAs = software.sameAs;
		if (
			!Array.isArray(sameAs) ||
			sameAs.length === 0 ||
			new Set(sameAs).size !== sameAs.length ||
			sameAs.some(
				(url) =>
					typeof url !== "string" ||
					!url.startsWith("https://") ||
					url === software.url,
			)
		) {
			report(
				`the software's sameAs is ${JSON.stringify(sameAs)}, expected distinct https URLs other than its own url`,
			);
		}
		const alternateNames = [software.alternateName].flat();
		if (!alternateNames.length || alternateNames.some((name) => !name)) {
			report("the software has no alternateName");
		}
		if (!software.applicationSubCategory) {
			report("the software has no applicationSubCategory");
		}
		bilingual(software.description, "the software's description", report);
		bilingual(
			software.disambiguatingDescription,
			"the software's disambiguatingDescription",
			report,
		);
		const requirements = bilingual(
			software.softwareRequirements,
			"the software's softwareRequirements",
			report,
		);
		for (const language of ["en", "es"]) {
			if (!FLOOR[language]) {
				report(
					`install/prerequisites (${language}) no longer states a RouterOS floor this check can read`,
				);
				continue;
			}
			const wording = FLOOR_WORDING[language](FLOOR[language]);
			if (requirements && !requirements[language].includes(wording)) {
				report(
					`softwareRequirements (${language}) does not say "${wording}", which is the floor install/prerequisites states`,
				);
			}
		}
		if (!software.about) report("the software has no about");
		for (const topic of [software.about ?? []].flat()) {
			if (!topic?.name || !WIKIDATA.test(topic?.sameAs ?? "")) {
				report(
					`the software's about entry ${JSON.stringify(topic)} is not a named Wikidata entity`,
				);
			}
		}
	}

	if (source) {
		const language = source.programmingLanguage;
		if (
			language?.["@type"] !== "ComputerLanguage" ||
			language?.name !== "Go" ||
			!WIKIDATA.test(language?.sameAs ?? "")
		) {
			report(
				`programmingLanguage is ${JSON.stringify(language)}, expected a ComputerLanguage named Go with its Wikidata sameAs`,
			);
		}
		if (!software || source.targetProduct?.["@id"] !== software["@id"]) {
			report(
				`the source code's targetProduct is ${JSON.stringify(source.targetProduct)}, expected the software node`,
			);
		}
		const origins = [source.isBasedOn ?? []].flat();
		if (origins.length === 0) {
			report(
				"the source code names no isBasedOn, though about/lineage.mdx says where it came from",
			);
		}
		for (const origin of origins) {
			const repository = origin?.codeRepository;
			if (
				origin?.["@type"] !== "SoftwareSourceCode" ||
				!origin?.name ||
				typeof repository !== "string" ||
				!LINEAGE.includes(repository)
			) {
				report(
					`isBasedOn ${JSON.stringify(origin)} is not source code about/lineage.mdx links`,
				);
			}
			if (origin?.version && !LINEAGE.includes(`v${origin.version}`)) {
				report(
					`isBasedOn says ${origin.name} ${origin.version}, which about/lineage.mdx does not name`,
				);
			}
		}
	}
}

const pages = [];
const shared = new Map();
let sharedChecked = false;
for (const file of htmlFiles(DIST)) {
	if (isRedirectStub(readFileSync(join(DIST, file), "utf8"))) continue;
	const html = readFileSync(join(DIST, file), "utf8");
	const page = `/${file.replace(/index\.html$/, "")}`;
	const report = (message) => problems.push(`${page}: ${message}`);

	const scripts = [
		...html.matchAll(
			/<script[^>]+type="application\/ld\+json"[^>]*>([\s\S]*?)<\/script>/g,
		),
	];
	if (scripts.length !== 1) {
		report(`expected exactly 1 JSON-LD script, found ${scripts.length}`);
		continue;
	}
	const raw = scripts[0][1];
	// The serializer escapes these, so a page carrying one raw means a title or
	// description reached the document unescaped and could close the element.
	if (/[<>]/.test(raw)) {
		report(
			"the JSON-LD carries a raw < or >, which can break out of the script",
		);
	}

	let graph;
	try {
		graph = JSON.parse(raw);
	} catch (error) {
		report(`the JSON-LD does not parse: ${error.message}`);
		continue;
	}
	if (graph["@context"] !== "https://schema.org") {
		report(
			`@context is ${JSON.stringify(graph["@context"])}, expected https://schema.org`,
		);
	}
	const nodes = graph["@graph"];
	if (!Array.isArray(nodes) || nodes.length === 0) {
		report("no @graph array");
		continue;
	}

	const types = new Set(nodes.map((node) => node["@type"]));
	const isHome = page === "/" || page === "/es/";
	// Two not-found pages: Starlight's dist/404.html, and the Spanish one, built
	// as an ordinary page at /es/404/. Neither is a document, so neither carries
	// a document node or a breadcrumb.
	const isNotFound = file === "404.html" || file === "es/404/index.html";
	const expected = [
		...SHARED_TYPES,
		...(isNotFound ? [] : [isHome ? "WebPage" : "TechArticle"]),
		...(isNotFound || isHome ? [] : ["BreadcrumbList"]),
	];
	for (const type of expected) {
		if (!types.has(type)) report(`the graph has no ${type} node`);
	}

	const person = nodes.find((node) => node["@type"] === "Person");
	if (person && person["@id"] !== PERSON_ID) {
		report(
			`the Person node is ${person["@id"]}, expected the canonical ${PERSON_ID}`,
		);
	}
	if (person && !person.name) {
		report(
			"the Person node carries no name, so the identity fetch returned nothing usable",
		);
	}

	// Every reference this site writes either names a node of this graph or the
	// canonical person, which is published as its own document elsewhere. A
	// reference to anything else is a pointer no consumer can resolve. A node
	// that stands for another document (the page's translation, the code this
	// one is based on) is embedded with its type, URL and name, so it is not a
	// bare reference and does not match here.
	//
	// The Person node is exempt because it is not this site's to answer for: it
	// arrives from jmrp.io already pointing at that account's other projects and
	// profiles, which are documents in their own right and are not part of this
	// graph by design.
	const ids = new Set(nodes.map((node) => node["@id"]).filter(Boolean));
	const references = [
		...JSON.stringify(
			nodes.filter((node) => node["@type"] !== "Person"),
		).matchAll(/\{"@id":"([^"]+)"\}/g),
	];
	for (const [, id] of references) {
		if (!ids.has(id) && id !== PERSON_ID) {
			report(`references ${id}, which is not a node of the graph`);
		}
	}

	// The shared nodes: checked in full on the first page, then held to the
	// same bytes everywhere else.
	for (const type of SHARED_TYPES) {
		const node = nodes.find((candidate) => candidate["@type"] === type);
		if (!node) continue;
		const bytes = JSON.stringify(node);
		if (!shared.has(type)) shared.set(type, { bytes, page });
		else if (shared.get(type).bytes !== bytes) {
			report(
				`its ${type} node differs from the one on ${shared.get(type).page}`,
			);
		}
	}
	if (!sharedChecked) {
		checkSharedNodes(nodes, report);
		sharedChecked = true;
	}

	// The trail Head.astro emits on every document page: positions 1..n in
	// order, a name on every crumb, and no crumb linking a page the build lacks,
	// since one crumb pointing at a 404 invalidates the whole trail. A crumb may
	// be a name without a link, which is the documented form for a section with
	// no index page.
	const breadcrumb = nodes.find((node) => node["@type"] === "BreadcrumbList");
	if (breadcrumb) {
		(breadcrumb.itemListElement ?? []).forEach((item, index) => {
			if (item.position !== index + 1) {
				report(`breadcrumb item ${index + 1} is at position ${item.position}`);
			}
			if (!item.name) report(`breadcrumb item ${index + 1} has no name`);
			if (item.item && !built(item.item)) {
				report(
					`breadcrumb links ${item.item}, which the build does not contain`,
				);
			}
		});
	}

	const metas = metaTags(html);
	const metaValues = (key) =>
		metas
			.filter((tag) => tag.property === key || tag.name === key)
			.map((tag) => tag.content);
	const lang = /<html[^>]*\slang="([^"]+)"/.exec(html)?.[1];
	const locale = lang === "es" ? "es" : "en";
	if (lang !== "en" && lang !== "es") {
		report(`<html lang> is ${JSON.stringify(lang)}, expected en or es`);
	}
	const route = page.replace(/^\/|\/$/g, "");

	// Open Graph, on every page including the not-found ones.
	const ogLocale = metaValues("og:locale");
	if (ogLocale.join() !== OG_LOCALE[locale]) {
		report(
			`og:locale is ${JSON.stringify(ogLocale)}, expected exactly ${OG_LOCALE[locale]} for lang="${lang}"`,
		);
	}
	const [ogImage] = metaValues("og:image");
	if (!built(ogImage)) {
		report(
			`og:image ${JSON.stringify(ogImage)} is not a file the build contains`,
		);
	}
	const [imageAlt] = metaValues("og:image:alt");
	if (!imageAlt?.trim()) report("the page has no og:image:alt");
	if (metaValues("twitter:image:alt")[0] !== imageAlt) {
		report("twitter:image:alt is missing or differs from og:image:alt");
	}
	// The Person node's image is jmrp.io's portrait, not this site's card.
	for (const node of nodes) {
		if (node["@type"] === "Person") continue;
		if (node.image && node.image.url !== ogImage) {
			report(
				`the ${node["@type"]} node's image is ${JSON.stringify(node.image.url)}, but og:image is ${JSON.stringify(ogImage)}`,
			);
		}
	}

	const document = nodes.find(
		(node) => node["@type"] === "TechArticle" || node["@type"] === "WebPage",
	);
	const translated =
		!isNotFound &&
		Boolean(sourceOf(route)) &&
		Boolean(sourceOf(otherLanguage(route)));
	const alternate = metaValues("og:locale:alternate");
	const otherLocale = locale === "es" ? "en" : "es";
	if (alternate.join() !== (translated ? OG_LOCALE[otherLocale] : "")) {
		report(
			`og:locale:alternate is ${JSON.stringify(alternate)}, expected ${translated ? OG_LOCALE[otherLocale] : "none"}`,
		);
	}
	if (!isNotFound) {
		const ogType = metaValues("og:type").join();
		const expectedType = isHome ? "website" : "article";
		if (ogType !== expectedType) {
			report(`og:type is ${JSON.stringify(ogType)}, expected ${expectedType}`);
		}
	}

	if (document) {
		const canonical = `${ORIGIN}${BASE_PATH}${page}`;
		if (document.url !== canonical) {
			report(
				`the document node says url ${document.url}, expected ${canonical}`,
			);
		}
		// The twin it announces has to exist, or the cheap representation is a 404.
		const twin = document.encoding?.contentUrl;
		if (!built(twin)) {
			report(
				`announces its markdown at ${twin}, which the build does not contain`,
			);
		}

		if (isHome) {
			const software = nodes.find(
				(node) => node["@type"] === "SoftwareApplication",
			);
			if (!software || document.mainEntity?.["@id"] !== software["@id"]) {
				report(
					`the landing's mainEntity is ${JSON.stringify(document.mainEntity)}, expected the software node`,
				);
			}
		} else {
			const section = sidebarSection(html);
			if (document.articleSection !== section) {
				report(
					`articleSection is ${JSON.stringify(document.articleSection)}, but the sidebar puts the page under ${JSON.stringify(section)}`,
				);
			}
		}

		// Published is the day git first saw the page's file, and a file git has
		// never seen (a page not committed yet) claims no publication at all,
		// though it can carry a dateModified from the data it renders. Modified
		// can only follow published.
		const { datePublished, dateModified } = document;
		// A Spanish URL with no Spanish source is Starlight's fallback, which
		// renders the English page's file and so carries that file's dates.
		const sourceFile =
			sourceOf(route) ??
			(locale === "es" ? sourceOf(otherLanguage(route)) : undefined);
		if (fullHistory) {
			const added = sourceFile ? firstAdded(sourceFile) : undefined;
			if (datePublished !== added) {
				report(
					added
						? `datePublished is ${JSON.stringify(datePublished)}, but git first added ${sourceFile.slice(SITE_DIR.length)} on ${added}`
						: `datePublished is ${JSON.stringify(datePublished)}, but git has no commit that added the page's file`,
				);
			}
		}
		if (datePublished && !dateModified) {
			report("carries datePublished without dateModified");
		}
		if (
			sitemapDates.has(document.url) &&
			sitemapDates.get(document.url) !== dateModified
		) {
			report(
				`dateModified is ${JSON.stringify(dateModified)}, but the sitemap's <lastmod> for ${document.url} is ${JSON.stringify(sitemapDates.get(document.url))}`,
			);
		}
		if (
			datePublished &&
			dateModified &&
			!(new Date(datePublished) <= new Date(dateModified))
		) {
			report(
				`datePublished ${datePublished} is after dateModified ${dateModified}`,
			);
		}
		if (!isHome) {
			const article = {
				"article:published_time": datePublished,
				"article:modified_time": dateModified,
				"article:section": document.articleSection,
			};
			for (const [key, value] of Object.entries(article)) {
				const found = metaValues(key);
				if (found.join() !== (value ?? "")) {
					report(
						`${key} is ${JSON.stringify(found)}, but the graph says ${JSON.stringify(value)}`,
					);
				}
			}
		} else if (metas.some((tag) => tag.property?.startsWith("article:"))) {
			report("a landing carries article:* tags, and it is not an article");
		}
	}

	pages.push({ page, route, locale, document, translated, report });
}

// Translations: an English page whose Spanish source exists names it with
// workTranslation, and the Spanish page names the English one back with
// translationOfWork. Each side is checked against the other side's own node,
// so a URL, id, title or language that does not match what the other page
// says about itself is caught, and so is a link to a page the build lacks.
const byRoute = new Map(pages.map((entry) => [entry.route, entry]));
for (const { route, locale, document, translated, report } of pages) {
	if (!document) continue;
	const property = locale === "es" ? "translationOfWork" : "workTranslation";
	const reverse = locale === "es" ? "workTranslation" : "translationOfWork";
	const link = document[property];
	if (document[reverse]) {
		report(`carries ${reverse}, which belongs on the other language's page`);
	}
	if (!translated) {
		if (link) {
			report(`names a translation at ${link.url}, but no source for it exists`);
		}
		continue;
	}
	const other = byRoute.get(otherLanguage(route));
	if (!link) {
		report(`has a translation, but no ${property}`);
		continue;
	}
	if (!other?.document || !built(link.url)) {
		report(
			`${property} names ${link.url}, which is not a document the build contains`,
		);
		continue;
	}
	const target = other.document;
	for (const key of ["@type", "@id", "url", "inLanguage"]) {
		if (link[key] !== target[key]) {
			report(
				`${property} says ${key} ${JSON.stringify(link[key])}, but that page says ${JSON.stringify(target[key])}`,
			);
		}
	}
	const titleKey = target["@type"] === "WebPage" ? "name" : "headline";
	if (link[titleKey] !== target[titleKey]) {
		report(
			`${property} says ${titleKey} ${JSON.stringify(link[titleKey])}, but that page says ${JSON.stringify(target[titleKey])}`,
		);
	}
	if (target[reverse]?.["@id"] !== document["@id"]) {
		report(
			`${property} names ${link.url}, which does not name this page back with ${reverse}`,
		);
	}
}

if (pages.length === 0) {
	console.error(`[schema] no HTML under ${DIST}. Run pnpm build first.`);
	process.exit(1);
}
if (problems.length > 0) {
	console.error(
		`[schema] ${problems.length} problem(s) across ${pages.length} pages:`,
	);
	for (const problem of problems) console.error(`  ${problem}`);
	process.exit(1);
}
const translatedPairs = pages.filter(
	(entry) => entry.locale === "en" && entry.translated,
).length;
console.log(
	`[schema] ${pages.length} pages: one parseable graph each, every node type present and every reference resolved; ` +
		`the shared nodes identical on every page, every breadcrumb trail in order, named and built, ` +
		`softwareVersion ${RELEASE.version} as VERSION says, ` +
		`${translatedPairs} translation pairs naming each other, every datePublished the day git first saw the page, ` +
		`every dateModified equal to the sitemap's <lastmod> (${sitemapDates.size} URLs), ` +
		"and Open Graph locale, type, image and article tags in step with the graph.",
);
