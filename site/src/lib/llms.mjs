// The site's index for language models, and the bundles it lists.
//
// The llms.txt convention is that a site's /llms.txt maps that site. This one
// serves documentation, so its index lists documentation pages: one entry per
// page, in the order the sidebar presents them, carrying that page's own
// description. Publishing the repository's README or a hand-kept summary here
// would hand a model a project blurb where it asked for a table of contents,
// and the Spanish half of the site would be invisible through this channel,
// which is why each locale gets its own index.
//
// The order comes from Starlight's resolved configuration rather than from a
// second table restating it: a table like that drifts the first time a page
// moves, and the drift is invisible because both files still parse. A page in
// the collection that the sidebar does not list fails the build here rather
// than quietly disappearing from the index.
//
// Beside the index, each locale publishes its pages concatenated, as the same
// markdown their twins serve, in three cuts:
//
//   - llms-full.txt, every page: 878 128 bytes in English and 959 747 in
//     Spanish in the build of 2026-09-24, about 220 000 tokens by the estimate
//     below, which is more than many tools read in one go (GEO audit,
//     2026-09-24). So it is no longer the only cut.
//   - llms-core.txt, the pages that answer what the project is, what it
//     costs, what a router needs, what it cannot see and where it stands:
//     CORE below. The index prints its size beside the whole's.
//   - llms/<section>.txt, one per sidebar group, named after the group's
//     English label (llms/collector-and-sinks.txt), so a model after the sinks
//     or the reference tables fetches those and nothing else.
//
// Every bundle opens with the same facts the index does, and every link in
// every one of them is absolute (page-markdown.mjs, absoluteTargets), on the
// origin the canonical links and the sitemap use: a model reading text does
// not resolve `/mikroscope/cost/`, and a `#heading` in a file of fifty pages
// names the wrong one. The index gives each file's size and an estimate of its
// tokens, so a reader can decide before fetching it.
import { getCollection } from "astro:content";
import config from "virtual:starlight/user-config";
import goMod from "../../../go.mod?raw";
import licence from "../../../LICENSE?raw";

import { REQUIRES, SINKS_WORD } from "../data/home.ts";
import { release } from "../data/release.ts";
import { renderTwin } from "./page-markdown.mjs";
import {
	NOT_FOUND_ROUTES,
	localeOf,
	pageUrl,
	routeOf,
	withoutLocale,
} from "./site.mjs";

// The facts below say MIT and Go. Each is read from the file that makes it
// true, so a relicence or a rewrite fails the build instead of leaving every
// llms file saying the old thing.
if (!/^MIT License\b/.test(licence)) {
	throw new Error(
		"src/lib/llms.mjs: LICENSE is no longer the MIT licence, which the llms.txt facts state",
	);
}
if (!/^module github\.com\/jmrplens\/mikroscope$/m.test(goMod)) {
	throw new Error(
		"src/lib/llms.mjs: go.mod does not declare the mikroscope Go module, which the llms.txt facts state",
	);
}

const TEXT = {
	en: {
		title: "mikroscope documentation",
		home: "Home",
		otherLanguages: "Other languages",
		otherLabel: "Spanish documentation index",
		otherNote: "the same documentation in Spanish, page for page",
		machineReadable: "Machine-readable references",
		/** The project in one paragraph: what, under which licence, in what, for which routers, which release. */
		facts: () =>
			`mikroscope is open source under the MIT licence and written in Go: an agent that runs in a container on MikroTik RouterOS ${REQUIRES.routeros} or later (${REQUIRES.arches.join(", ")}), and a CLI that installs it, records, plots and forwards to ${SINKS_WORD.en} sinks. Current release: ${release.version}, ${release.date}.`,
		intro: (repo) =>
			`This is the index of the mikroscope documentation site. Every entry links one page and carries that page's own description. Source and issues live at ${repo}, and the releases page there carries the CLI archives, the agent image tars and the GHCR image.`,
		sizes:
			"Each file below holds pages concatenated as markdown, separated by `---`, with every link absolute. Sizes are bytes of UTF-8; tokens are the size divided by four, an estimate rather than a count.",
		tokens: (bytes) =>
			`${humanSize(bytes)}, about ${approxTokens(bytes)} tokens`,
		coreLabel: "Core documentation",
		core: (titles) => `the pages to read first, in this order: ${titles}`,
		fullLabel: "Full documentation",
		full: "every English page concatenated, in this order",
		sectionsIntro:
			"One file per section of the sidebar, its pages in the same order as above:",
		section: (n) => `the ${n === 1 ? "page" : `${n} pages`} of this section`,
		twinNote:
			"Every page listed above is also served as markdown at its own path with `index.md` appended, which is the cheapest way to read one page as text.",
		bundleTitle: {
			core: "mikroscope documentation: the core",
			full: "mikroscope documentation: every page",
			section: (label) => `mikroscope documentation: ${label}`,
		},
		bundleNote: (n, index) =>
			`${n} pages, in the order the site's sidebar lists them, separated by \`---\`. Each opens with its title, its description and the URL of the page it doubles. The index of every file: ${index}`,
	},
	es: {
		title: "Documentación de mikroscope",
		home: "Inicio",
		otherLanguages: "Otros idiomas",
		otherLabel: "Índice de la documentación en inglés",
		otherNote: "la misma documentación en inglés, página por página",
		machineReadable: "Referencias legibles por máquina",
		facts: () =>
			`mikroscope es código abierto con licencia MIT y está escrito en Go: un agente que corre en un contenedor en MikroTik RouterOS ${REQUIRES.routeros} o posterior (${REQUIRES.arches.join(", ")}) y una CLI que lo instala, graba, dibuja y reenvía a ${SINKS_WORD.es} destinos. Versión actual: ${release.version}, ${release.date}.`,
		intro: (repo) =>
			`Este es el índice del sitio de documentación de mikroscope. Cada entrada enlaza una página y lleva la descripción de esa página. El código y las incidencias están en ${repo}, y su página de releases lleva los archivos de la CLI, los tar de la imagen del agente y la imagen en GHCR.`,
		sizes:
			"Cada fichero de abajo reúne páginas concatenadas en markdown, separadas por `---` y con todos los enlaces absolutos. Los tamaños son bytes de UTF-8; los tokens son el tamaño entre cuatro, una estimación y no un recuento.",
		tokens: (bytes) =>
			`${humanSize(bytes)}, unos ${approxTokens(bytes)} tokens`,
		coreLabel: "Documentación esencial",
		core: (titles) =>
			`las páginas por las que empezar, en este orden: ${titles}`,
		fullLabel: "Documentación completa",
		full: "todas las páginas en español concatenadas, en este orden",
		sectionsIntro:
			"Un fichero por sección de la barra lateral, con sus páginas en el mismo orden que arriba:",
		section: (n) =>
			n === 1
				? "la página de esta sección"
				: `las ${n} páginas de esta sección`,
		twinNote:
			"Cada página de la lista se sirve también como markdown en su propia ruta con `index.md` al final, que es la forma más barata de leer una página como texto.",
		bundleTitle: {
			core: "Documentación de mikroscope: lo esencial",
			full: "Documentación de mikroscope: todas las páginas",
			section: (label) => `Documentación de mikroscope: ${label}`,
		},
		bundleNote: (n, index) =>
			`${n} páginas, en el orden de la barra lateral del sitio, separadas por \`---\`. Cada una empieza por su título, su descripción y la URL de la página que dobla. El índice de todos los ficheros: ${index}`,
	},
};

const REPO = "https://github.com/jmrplens/mikroscope";

/**
 * The core bundle: every page of the sidebar sections that hold the pages in
 * `sectionsHolding`, whatever else those sections hold, plus the pages in
 * `pages`, in sidebar order after the home page.
 *
 * The section /start/ opens ("Start here") is taken whole, so a page added
 * there is in the core without an edit here; so is the one /cost/ opens,
 * the question every other page defers to. The five pages beside them answer
 * what a router needs, why the floor is the kernel's, how a fault reads and
 * the one found in production, and where the project stands. What is left
 * out is reference: flags, variables, metric families, dashboards, each sink
 * in turn, which the section files carry. Sections are named by a page they
 * hold rather than by label, so relabelling one does not empty the core.
 */
const CORE = {
	sectionsHolding: ["start", "cost"],
	pages: [
		"install/prerequisites",
		"limits",
		"playbooks",
		"playbooks/loop",
		"about/status",
	],
};

/**
 * The sidebar as a flat list of sections, with the labels of one locale.
 *
 * Each section is keyed by its English label as a slug ("What it costs" is
 * `what-it-costs`, published as llms/what-it-costs.txt in both locales). The
 * first path segment of its pages would be shorter and is not unique: "For
 * contributors" opens with reference/testing, under the Reference group's
 * path.
 *
 * @param {"en" | "es"} locale
 * @returns {{ key: string, label: string, slugs: string[] }[]} sections in sidebar order
 */
function sections(locale) {
	/** @param {any} item @returns {string} */
	const label = (item) =>
		(locale === "es" ? item.translations?.es : undefined) ?? item.label ?? "";
	/** @param {any} item @returns {string[]} */
	const slugs = (item) =>
		item.slug !== undefined
			? [String(item.slug).replace(/^\//, "")]
			: (item.items ?? []).flatMap(slugs);

	/** @param {string} text @returns {string} "Collector and sinks" -> "collector-and-sinks" */
	const slugify = (text) =>
		text
			.normalize("NFKD")
			.replaceAll(/[\u0300-\u036f]/g, "")
			.toLowerCase()
			.replaceAll(/[^a-z0-9]+/g, "-")
			.replaceAll(/^-|-$/g, "");

	const table = (config.sidebar ?? []).map((group) => ({
		key: slugify(String(group.label ?? "")),
		label: label(group),
		slugs: slugs(group),
	}));
	const seen = new Set();
	for (const section of table) {
		if (section.key === "" || seen.has(section.key)) {
			throw new Error(
				`src/lib/llms.mjs: the sidebar group "${section.label}" would publish llms/${section.key}.txt, ` +
					"which is empty or taken by another group. Give it an English label of its own.",
			);
		}
		seen.add(section.key);
	}
	return table;
}

/**
 * The key of every section file, for the routes that publish them.
 *
 * @returns {string[]}
 */
export const sectionKeys = () =>
	sections("en")
		.filter((section) => section.slugs.length > 0)
		.map((section) => section.key);

/**
 * Every page of one locale, in no particular order, keyed by its locale
 * independent slug so the two halves are addressed the same way.
 *
 * @param {"en" | "es"} locale
 * @returns {Promise<Map<string, { route: string, title: string, description: string, body: string, file: string }>>}
 */
async function pagesOf(locale) {
	const pages = new Map();
	for (const entry of await getCollection("docs")) {
		const route = routeOf(entry.id);
		if (localeOf(route) !== locale) continue;
		// The not-found pages are in the collection and in no sidebar group, and
		// they are not documents; see NOT_FOUND_ROUTES.
		if (NOT_FOUND_ROUTES.has(route)) continue;
		const description = entry.data.description;
		if (!description) {
			throw new Error(
				`${entry.filePath ?? entry.id}: no frontmatter description. ` +
					"The llms.txt entry for a page is its description, so a page without one has nothing to say there.",
			);
		}
		pages.set(withoutLocale(route), {
			route,
			title: entry.data.title,
			description,
			// The landings are one `<Home content={en} />` tag; page-markdown.mjs
			// reduces that tag from src/data/home.ts, so they need no special case.
			body: entry.body ?? "",
			file: entry.filePath ?? entry.id,
		});
	}
	return pages;
}

/**
 * Fails when the sidebar and the collection disagree: a page the sidebar does
 * not list would be missing from the index, and a slug the collection does not
 * have would publish a dead link to every crawler that reads this file.
 *
 * @param {{ label: string, slugs: string[] }[]} table
 * @param {Map<string, unknown>} pages
 * @param {"en" | "es"} locale
 */
function assertSidebarCoversCollection(table, pages, locale) {
	const listed = new Set(table.flatMap((section) => section.slugs));
	const problems = [
		...[...listed]
			.filter((slug) => !pages.has(slug))
			.map(
				(slug) => `in the sidebar but not in the ${locale} collection: ${slug}`,
			),
		...[...pages.keys()]
			// The home page is the index itself, and heads the file rather than
			// sitting inside one of the sections.
			.filter((slug) => slug !== "" && !listed.has(slug))
			.map(
				(slug) =>
					`in the ${locale} collection but in no sidebar group: ${slug}`,
			),
	];
	if (problems.length > 0) {
		throw new Error(
			`src/lib/llms.mjs: the sidebar and the content collection disagree.\n  ${problems.join("\n  ")}`,
		);
	}
}

/** "38 KB" / "1.2 MB", the way a reader decides whether to fetch something. */
const humanSize = (bytes) =>
	bytes >= 1024 * 1024
		? `${(bytes / (1024 * 1024)).toFixed(1)} MB`
		: `${Math.round(bytes / 1024)} KB`;

/**
 * "31k": bytes over four, the common rule of thumb for English prose, rounded
 * to a thousand. Stated as an estimate wherever it is printed; no tokenizer
 * was run.
 */
const approxTokens = (bytes) => `${Math.max(1, Math.round(bytes / 4 / 1000))}k`;

/**
 * The files one locale publishes besides its index, in the order the index
 * lists them: the core, the whole, then one per section.
 *
 * @param {"en" | "es"} locale
 * @returns {{ id: string, path: string, title: string, label: string, note: (pages: Map<string, any>) => string, slugs: string[] }[]}
 */
function bundles(locale) {
	const text = TEXT[locale];
	const table = sections(locale);
	const prefix = locale === "es" ? "es/" : "";
	const inSidebar = table.flatMap((section) => section.slugs);

	const unknown = [...CORE.sectionsHolding, ...CORE.pages].filter(
		(slug) => !inSidebar.includes(slug),
	);
	if (unknown.length > 0) {
		throw new Error(
			`src/lib/llms.mjs: CORE names pages the sidebar does not list: ${unknown.join(", ")}`,
		);
	}
	const coreSections = new Set(
		table
			.filter((section) =>
				CORE.sectionsHolding.some((slug) => section.slugs.includes(slug)),
			)
			.flatMap((section) => section.slugs),
	);
	const core = [
		"",
		...inSidebar.filter(
			(slug) => coreSections.has(slug) || CORE.pages.includes(slug),
		),
	];

	return [
		{
			id: "core",
			path: `${prefix}llms-core.txt`,
			title: text.bundleTitle.core,
			label: text.coreLabel,
			note: (pages) =>
				text.core(
					core
						.map((slug) => (slug === "" ? text.home : pages.get(slug).title))
						.join("; "),
				),
			slugs: core,
		},
		{
			id: "full",
			path: `${prefix}llms-full.txt`,
			title: text.bundleTitle.full,
			label: text.fullLabel,
			note: () => text.full,
			slugs: ["", ...inSidebar],
		},
		// A group of external links only would publish an empty file.
		...table
			.filter((section) => section.slugs.length > 0)
			.map((section) => ({
				id: section.key,
				path: `${prefix}llms/${section.key}.txt`,
				title: text.bundleTitle.section(section.label),
				label: section.label,
				note: () => text.section(section.slugs.length),
				slugs: section.slugs,
			})),
	];
}

/** @param {"en" | "es"} locale @returns {string} the absolute URL of that locale's index. */
const indexUrl = (locale) =>
	`${pageUrl("")}${locale === "es" ? "es/llms.txt" : "llms.txt"}`;

/**
 * One bundle: a heading, the facts, what the file holds, then the twins.
 *
 * @param {"en" | "es"} locale
 * @param {string} id "core", "full", or a section key
 * @param {Map<string, any>} [known] the locale's pages, when the caller has them
 * @returns {Promise<string>}
 */
export async function renderBundle(locale, id, known) {
	const table = sections(locale);
	const pages = known ?? (await pagesOf(locale));
	assertSidebarCoversCollection(table, pages, locale);
	const bundle = bundles(locale).find((b) => b.id === id);
	if (bundle === undefined) {
		throw new Error(`src/lib/llms.mjs: no bundle "${id}" in ${locale}`);
	}
	const text = TEXT[locale];
	const head = [
		`# ${bundle.title}`,
		"",
		`> ${text.facts()}`,
		"",
		text.bundleNote(bundle.slugs.length, indexUrl(locale)),
		// A blank line before the rule: `---` straight under a line of text
		// would make that line a heading.
		"",
	].join("\n");
	return `${[head, ...bundle.slugs.map((slug) => renderTwin(pages.get(slug)))]
		.join("\n---\n\n")
		.trimEnd()}\n`;
}

/**
 * One locale's /llms.txt.
 *
 * @param {"en" | "es"} locale
 * @returns {Promise<string>}
 */
export async function renderIndex(locale) {
	const table = sections(locale);
	const pages = await pagesOf(locale);
	assertSidebarCoversCollection(table, pages, locale);

	const text = TEXT[locale];
	const home = pages.get("");
	const otherPath = locale === "es" ? "llms.txt" : "es/llms.txt";
	const url = (path) => `${pageUrl("")}${path}`;

	const lines = [
		`# ${text.title}`,
		"",
		`> ${home.description} ${text.facts()}`,
		"",
	];
	const push = (...items) => lines.push(...items, "");

	push(text.intro(REPO));
	push(
		`${text.home}: [${home.title}](${pageUrl(home.route)}): ${home.description}`,
	);

	for (const section of table) {
		push(`## ${section.label}`);
		lines.push(
			...section.slugs.map((slug) => {
				const page = pages.get(slug);
				return `- [${page.title}](${pageUrl(page.route)}): ${page.description}`;
			}),
			"",
		);
	}

	const all = bundles(locale);
	const entry = async (bundle) => {
		const bytes = Buffer.byteLength(
			await renderBundle(locale, bundle.id, pages),
		);
		return `- [${bundle.label}](${url(bundle.path)}) (${text.tokens(bytes)}): ${bundle.note(pages)}`;
	};
	push(`## ${text.machineReadable}`);
	push(text.sizes);
	lines.push(
		...(await Promise.all(
			all.filter((b) => b.id === "core" || b.id === "full").map(entry),
		)),
		"",
	);
	push(text.sectionsIntro);
	lines.push(
		...(await Promise.all(
			all.filter((b) => b.id !== "core" && b.id !== "full").map(entry),
		)),
		"",
	);
	push(text.twinNote);

	push(`## ${text.otherLanguages}`);
	lines.push(
		`- [${text.otherLabel}](${url(otherPath)}): ${text.otherNote}`,
		"",
	);

	// The index names itself, so a copy of this file that travelled says where
	// the current one lives.
	lines.push(`<!-- ${indexUrl(locale)} -->`);

	return `${lines
		.join("\n")
		.replace(/\n{3,}/g, "\n\n")
		.trimEnd()}\n`;
}

/**
 * One locale's /llms-full.txt: every page of that locale, in sidebar order,
 * as the same markdown its twin serves, under the facts.
 *
 * @param {"en" | "es"} locale
 * @returns {Promise<string>}
 */
export const renderFull = (locale) => renderBundle(locale, "full");
