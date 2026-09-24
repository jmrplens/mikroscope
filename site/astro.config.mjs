// @ts-check
import { defineConfig } from "astro/config";
import starlight from "@astrojs/starlight";
import sitemap from "@astrojs/sitemap";
import starlightLinksValidator from "starlight-links-validator";
import { fileURLToPath } from "node:url";
import { isRedirectStub } from "./scripts/redirect-stub.mjs";
import { decodedFragments } from "./src/lib/decoded-fragments.mjs";
import { inlineCodeNowrap } from "./src/lib/inline-code.mjs";
import { nowrapValues } from "./src/lib/nowrap-values.mjs";
import { lastmodTable } from "./src/lib/lastmod.mjs";
import { metaCsp } from "./src/lib/meta-csp.mjs";
import { readRelease } from "./src/lib/release.mjs";
import { versionPlaceholder } from "./src/lib/version-placeholder.mjs";

const siteRoot = fileURLToPath(new URL(".", import.meta.url));
const siteBase = "/mikroscope";

// The release the site documents, from VERSION and its CHANGELOG.md heading.
// Read here with the filesystem rather than through src/data/release.ts,
// because this file is loaded by plain Node before Vite exists and that module
// imports with `?raw`. Both go through parseRelease, so they cannot disagree;
// a VERSION with no dated heading fails the build here, first.
const release = readRelease(fileURLToPath(new URL("..", import.meta.url)));

// Where the documentation is advertised, which is not where it is served.
//
// `site` below has to name GitHub Pages, because that is where the bytes are
// and it is what the canonical link, the sitemap and the hreflang pairs must
// agree with. What a reader is handed is the other one: jmrp.io 301s
// /docs/mikroscope and everything under it to the Pages URL, so a link that
// carries it reaches the same page and puts the canonical domain in the places
// a mention counts. Every absolute link written OUTSIDE the site — the README,
// the generated docs/, the dashboards — uses this one; the site's own internal
// links stay relative and never see it.
export const publicDocs = "https://jmrp.io/docs/mikroscope";

/**
 * When each page last changed, counting the data it renders: the newest
 * commit among the page's file and the src/data modules, dashboards and
 * VERSION it shows. src/lib/lastmod.mjs says what counts and why.
 *
 * Built once, on first use, and read by both places a page's date appears:
 * the sitemap's `<lastmod>` (getLastmod below) and Starlight's "Last updated",
 * which src/lastUpdated.mjs sets from the same object through
 * `virtual:mikroscope/lastmod`. Head.astro's `dateModified` is that route
 * value. One table, so the three agree. Lazy: git runs only once the sitemap
 * or a page render first asks for a date.
 * @type {Record<string, { date: string, from: string[] }> | undefined}
 */
let lastmod;
const pageDates = () => (lastmod ??= lastmodTable(siteRoot));

/**
 * The `<lastmod>` of a sitemap URL, from the page file behind it.
 * @param {string} pathname
 * @returns {string | undefined}
 */
function getLastmod(pathname) {
	const slug = pathname
		.replace(new RegExp(`^${siteBase}/?`), "")
		.replace(/\/$/, "");
	const candidates =
		slug === ""
			? ["src/content/docs/index.mdx"]
			: [`src/content/docs/${slug}.mdx`, `src/content/docs/${slug}/index.mdx`];
	for (const relativePath of candidates) {
		const page = pageDates()[relativePath];
		if (page) return page.date;
	}
	return undefined;
}

/**
 * A sitemap URL's language alternates plus `x-default`, which Starlight puts
 * in every page's head (pointing at the English page) and the sitemap
 * integration does not write. Without it the sitemap would announce two
 * alternates where the HTML announces three.
 * @param {{ url: string, lang: string }[] | undefined} links
 * @returns {{ url: string, lang: string }[] | undefined}
 */
function withDefaultLanguage(links) {
	const english = links?.find((link) => link.lang === "en");
	return english && links
		? [...links, { url: english.url, lang: "x-default" }]
		: links;
}

/**
 * Serves the page dates to src/lastUpdated.mjs as `virtual:mikroscope/lastmod`:
 * `{ "src/content/docs/index.mdx": "2026-09-24T12:24:25.000Z", … }`, keyed the
 * way Starlight spells `entry.filePath`. The same pattern Starlight's own
 * build uses for its git dates (`virtual:starlight/git-info`).
 *
 * In `astro dev` the table is built once, when the first page is served, and
 * a commit made while the server runs is not seen until it restarts.
 */
function pageDatesModule() {
	const id = "virtual:mikroscope/lastmod";
	const resolved = `\0${id}`;
	return {
		name: "mikroscope-page-dates",
		/** @param {string} source */
		resolveId: (source) => (source === id ? resolved : undefined),
		/** @param {string} module */
		load(module) {
			if (module !== resolved) return undefined;
			const dates = Object.fromEntries(
				Object.entries(pageDates()).map(([file, page]) => [file, page.date]),
			);
			return `export default ${JSON.stringify(dates)};`;
		},
	};
}

/**
 * A Content-Security-Policy written into every built page as a `<meta>`
 * element, by src/lib/meta-csp.mjs, which says what the policy allows and why
 * Astro's own `security.csp` is not used for it. GitHub Pages sets no CSP
 * header and allows a site none.
 *
 * Switch it off by setting this to false. Nothing else changes: the pages
 * are built exactly as before and no other file refers to it.
 *
 * It needs checking in a real browser whenever Starlight, Pagefind or an
 * inline script changes, because a blocked script fails in the console and
 * nowhere else. Against `astro preview`, at 1280 px and at 390 px, EN and ES,
 * with the console open, and with no "Content Security Policy" line in it:
 * search returns results (meta-csp.mjs says which path needs
 * 'wasm-unsafe-eval'); the theme select switches and a light theme survives a
 * reload; the phone header shows its theme button and language globe, and
 * both work; the language select changes language; a code block's copy
 * button copies; the drawer opens; the sidebar keeps its open groups across
 * pages; a missing /es/ address shows the Spanish 404 text.
 *
 * Checked 2026-09-24 in headless Chromium (Playwright 1.63), on a copy of the
 * site as built just before this policy existed, with the policy injected by
 * meta-csp.mjs: 108 pages loaded with no violation and no console error, and
 * each item above passed, most of them tried in one language only. The same
 * run with the hashes removed logged 13 violations on /start/ and left the
 * Spanish 404 in English, so the run can fail. The first build that ships the
 * policy was checked the same day, in headless Chromium (Playwright 1.63)
 * against a static server under /mikroscope/: all 116 pages at 1280x900 and at
 * 390x844 with no violation and no console error; search, the theme control
 * (select on desktop, button on the phone, each surviving a reload), the
 * language select, a code block's copy button and the phone drawer, in both
 * languages at both widths; and, as a control, an unhashed inline script and
 * an image from another origin injected into /start/ were both refused.
 */
const META_CSP = true;

/**
 * Adds the site's own plugins to the Sätteri processor Starlight uses, the
 * same way Starlight adds its own (it pushes onto
 * `config.markdown.processor.options.hastPlugins` and `mdastPlugins` at config
 * setup).
 *
 * The version placeholder is not optional the way the two hast plugins are: a
 * page without it would publish `{{MIKROSCOPE_VERSION}}` in a command a reader
 * copies, so a processor that cannot take it fails the build rather than
 * warning.
 * @returns {import("astro").AstroIntegration}
 */
function siteMarkdownPlugins() {
	return {
		name: "mikroscope-markdown-plugins",
		hooks: {
			"astro:config:setup": ({ config, logger }) => {
				const processor = /** @type {any} */ (config.markdown)?.processor;
				const mdast = processor?.options?.mdastPlugins;
				if (!Array.isArray(mdast)) {
					throw new Error(
						"the markdown processor has no mdastPlugins list, so {{MIKROSCOPE_VERSION}} in the pages' code would not be replaced. src/lib/version-placeholder.mjs is written for Sätteri.",
					);
				}
				mdast.push(versionPlaceholder(release.version));
				const plugins = processor?.options?.hastPlugins;
				if (!Array.isArray(plugins)) {
					logger.warn(
						"markdown processor has no hastPlugins list; inline code is not kept together and encoded fragments stay encoded",
					);
					return;
				}
				plugins.push(inlineCodeNowrap(), nowrapValues(), decodedFragments());
			},
		},
	};
}

/**
 * Pages that have moved since the site was published, old path → new one. The
 * old address keeps working rather than 404ing for anyone who bookmarked it or
 * linked to it, and the same map is what keeps those redirect stubs out of the
 * sitemap: they are `noindex` one-line documents, and everything in the
 * sitemap has to be a real page with a twin and an llms.txt entry.
 *
 * The target carries the base, because a redirect is written verbatim into the
 * stub's `<meta http-equiv="refresh">` and is not resolved against it.
 *
 * `RouterOS ports and kernel names` moved out of the fault case studies: it is
 * a reference — a table, what the agent emits, the kinds of port event — that
 * the case studies link to as a prerequisite rather than reading as one of
 * themselves.
 */
const movedPages = {
	"/playbooks/port-names": `${siteBase}/reference/port-names/`,
	"/es/playbooks/port-names": `${siteBase}/es/reference/port-names/`,
};

export default defineConfig({
	site: "https://jmrplens.github.io/mikroscope",
	base: siteBase,
	trailingSlash: "always",
	redirects: movedPages,
	integrations: [
		siteMarkdownPlugins(),
		starlight({
			title: "mikroscope",
			description:
				"Sub-second kernel telemetry from inside a MikroTik router, with the cost of the observer measured rather than claimed.",
			plugins: [
				starlightLinksValidator({
					errorOnRelativeLinks: false,
					errorOnFallbackPages: false,
				}),
			],
			defaultLocale: "root",
			locales: {
				root: { label: "English", lang: "en" },
				es: { label: "Español", lang: "es" },
			},
			favicon: "/favicon.svg",
			components: {
				// The mark inlined so the palette can paint its two tones.
				SiteTitle: "./src/components/overrides/SiteTitle.astro",
				// Theme and language as header buttons below md, where Starlight
				// hides them in the drawer.
				Header: "./src/components/overrides/Header.astro",
				// The drawer and its button on every page, the splash landing included.
				PageFrame: "./src/components/overrides/PageFrame.astro",
				// The JSON-LD graph, and the links to the markdown twin and llms.txt.
				Head: "./src/components/overrides/Head.astro",
				// The stock footer, then a line naming who wrote the site.
				Footer: "./src/components/overrides/Footer.astro",
			},
			routeMiddleware: [
				// The mark in the hero's image slot, on every page with a hero.
				"./src/routeMiddleware.ts",
				// "Last updated" from the page and the data it renders.
				"./src/lastUpdated.mjs",
			],
			head: [
				// Starlight sets og:title and og:description itself; the image is
				// the one thing it cannot know.
				{
					tag: "meta",
					attrs: {
						property: "og:image",
						content: "https://jmrplens.github.io/mikroscope/og.png",
					},
				},
				{ tag: "meta", attrs: { property: "og:image:width", content: "1200" } },
				{ tag: "meta", attrs: { property: "og:image:height", content: "630" } },
				{
					tag: "meta",
					attrs: { name: "twitter:card", content: "summary_large_image" },
				},
				{
					tag: "meta",
					attrs: {
						name: "twitter:image",
						content: "https://jmrplens.github.io/mikroscope/og.png",
					},
				},
				{
					tag: "link",
					attrs: {
						rel: "icon",
						href: "/mikroscope/favicon.ico",
						sizes: "32x32",
					},
				},
				{
					tag: "link",
					attrs: {
						rel: "apple-touch-icon",
						href: "/mikroscope/apple-touch-icon.png",
					},
				},
			],
			social: [
				{
					icon: "github",
					label: "GitHub",
					href: "https://github.com/jmrplens/mikroscope",
				},
			],
			editLink: {
				baseUrl: "https://github.com/jmrplens/mikroscope/edit/main/site/",
			},
			lastUpdated: true,
			pagination: true,
			customCss: [
				// theme.css first and unlayered, so its `:root` beats the tokens
				// Starlight declares inside `@layer starlight.base` without an
				// `!important`, and so every sheet after it can read them.
				"./src/styles/theme.css",
				"./src/styles/typography.css",
				"./src/styles/chrome.css",
				// The content components' panels; `.ms-scroll` lives here now.
				"./src/styles/components.css",
				// The landing's blocks, which no other page renders.
				"./src/styles/home.css",
				// Last: focus rings and the skip link must win.
				"./src/styles/a11y.css",
			],
			// External, not inline. Inline, Expressive Code puts a <style> inside
			// each MDX page's first code fence, and @astrojs/markdown-satteri 0.4.1
			// collapses that style's text into a `set:html` property that nothing
			// renders: the built /cost/ page shipped `<style set:html="…">`, its
			// fence lost `overflow-x: auto`, and the page scrolled sideways to
			// 845 px at a 390 px viewport (measured 2026-09-15). The `<Code>`
			// component path was unaffected.
			expressiveCode: { emitExternalStylesheet: true },
			// The information architecture, in reading order.
			//
			// A page is listed by its slug alone, and Starlight labels it with
			// that page's own title in each language: the English title here,
			// the Spanish twin's under /es/. A label written here as well was a
			// second copy of the title, and copies drift. Until 2026-09-24 this
			// list called the dashboards page "Two dashboards, one panel list"
			// (ES "Dos paneles, una sola lista") on every page, while the page
			// itself said "Five dashboards" and "Cinco dashboards". The Spanish
			// label cannot go missing either, because the i18n parity check
			// fails on a page without its twin.
			//
			// A label is written only where the sidebar deliberately says less
			// than the title. The group labels have no page to take a title
			// from, so each keeps its Spanish translation beside it.
			sidebar: [
				{
					label: "Start here",
					translations: { es: "Empezar aquí" },
					items: [
						{ slug: "start" },
						{ slug: "start/walkthrough" },
						{ slug: "start/questions" },
						{ slug: "start/compared" },
					],
				},
				{
					label: "What it costs",
					translations: { es: "Lo que cuesta" },
					items: [
						{ slug: "cost" },
						{ slug: "cost/rate-ceiling" },
						{ slug: "cost/limits" },
					],
				},
				{
					label: "Install",
					translations: { es: "Instalar" },
					items: [
						{ slug: "install" },
						{
							// Shorter than the title, "Getting the CLI onto your
							// machine", as the sidebar had it before labels came
							// from titles.
							label: "Getting the CLI",
							translations: { es: "Tener la CLI" },
							slug: "install/cli",
						},
						{ slug: "install/prerequisites" },
						{ slug: "install/routes" },
						{ slug: "install/firewall" },
						{ slug: "install/layout" },
						{ slug: "install/reaching-the-agent" },
					],
				},
				{
					label: "Record and capture",
					translations: { es: "Grabar y capturar" },
					items: [{ slug: "record" }, { slug: "record/triggers" }],
				},
				{
					label: "Collector and sinks",
					translations: { es: "Colector y destinos" },
					items: [
						{ slug: "sinks" },
						{ slug: "sinks/prometheus" },
						{ slug: "sinks/influxdb" },
						{ slug: "sinks/other" },
						{ slug: "sinks/api-tier" },
						{ slug: "sinks/derive" },
						{ slug: "sinks/detections" },
						{ slug: "sinks/device-info" },
					],
				},
				{
					label: "Dashboards",
					// "Dashboards" in Spanish too: the Spanish pages say
					// "dashboard" for a Grafana dashboard and keep "panel" for one
					// of its panels, and "Paneles" named the wrong one.
					translations: { es: "Dashboards" },
					items: [
						{ slug: "dashboards" },
						{ slug: "dashboards/import-and-check" },
						{ slug: "dashboards/alerts" },
					],
				},
				{
					label: "Reading the data",
					translations: { es: "Leer los datos" },
					items: [
						{ slug: "playbooks" },
						{ slug: "playbooks/idle" },
						{ slug: "playbooks/loop" },
						{ slug: "playbooks/cpu" },
						{ slug: "playbooks/packet-flood" },
						{ slug: "playbooks/flash-wear" },
						{ slug: "playbooks/port-errors" },
						{ slug: "playbooks/conntrack" },
					],
				},
				{
					label: "Limits",
					translations: { es: "Límites" },
					items: [
						{ slug: "limits" },
						{ slug: "limits/namespaces" },
						{ slug: "limits/privileged" },
						{ slug: "limits/source-floors" },
					],
				},
				{
					label: "Security",
					translations: { es: "Seguridad" },
					items: [
						{ slug: "security" },
						{ slug: "security/api-user" },
						{ slug: "security/expose" },
						{ slug: "security/installer" },
					],
				},
				{
					label: "Reference",
					translations: { es: "Referencia" },
					collapsed: true,
					items: [
						{ slug: "reference/cli" },
						{ slug: "reference/environment" },
						{ slug: "reference/http" },
						{ slug: "reference/metrics" },
						{ slug: "reference/measurements" },
						{ slug: "reference/port-names" },
						{ slug: "reference/troubleshooting" },
						{ slug: "reference/glossary" },
					],
				},
				{
					label: "About",
					translations: { es: "Acerca de" },
					collapsed: true,
					items: [
						{ slug: "about/status" },
						{ slug: "about/changelog" },
						{ slug: "about/lineage" },
					],
				},
				// Last, and its own group: everything above answers "how do I
				// use this", and these two answer "how do I change it". Mixed
				// into Reference and About they read as things a user has to
				// get through to reach what they came for.
				{
					label: "For contributors",
					translations: { es: "Para quien contribuye" },
					collapsed: true,
					items: [
						{
							// The Spanish label is shorter than the Spanish
							// title, which adds "a sí mismo", as the sidebar had
							// it before labels came from titles.
							label: "How the project tests itself",
							translations: { es: "Cómo se prueba el proyecto" },
							slug: "reference/testing",
						},
						{ slug: "about/brand" },
						{
							label: "Contributing (GitHub)",
							translations: { es: "Contribuir (GitHub)" },
							link: "https://github.com/jmrplens/mikroscope/blob/main/CONTRIBUTING.md",
							attrs: { target: "_blank", rel: "noopener" },
						},
					],
				},
			],
		}),
		sitemap({
			// The not-found pages are for a mistyped address, not for an index:
			// /404/ is what GitHub Pages serves, /es/404/ is its twin for the
			// language menu.
			filter: (page) => {
				const path = new URL(page).pathname.replace(/\/$/, "");
				if (/\/404$/.test(path)) return false;
				// The redirect stubs above are not pages.
				return !Object.keys(movedPages).some(
					(from) => path === `${siteBase}${from}`,
				);
			},
			// hreflang alternates in the sitemap too, the same three the HTML
			// head carries: English, Spanish, and x-default pointing at the
			// English page. The integration pairs a URL with its twin by the path
			// left after the locale prefix, so /cost/ and /es/cost/ become one
			// set; it has no x-default of its own, so serialize adds it.
			i18n: { defaultLocale: "en", locales: { en: "en", es: "es" } },
			serialize: (item) => ({
				...item,
				links: withDefaultLanguage(item.links),
				lastmod: getLastmod(new URL(item.url).pathname),
			}),
		}),
		// Last, so the policy is computed from the pages as they are served,
		// after every other integration has written to them.
		...(META_CSP ? [metaCsp({ skip: isRedirectStub })] : []),
	],
	vite: { plugins: [pageDatesModule()] },
});
