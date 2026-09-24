// Where a page lives, in the three forms the rest of the site needs it.
//
// Origin and base come from the Astro config rather than being restated, so a
// rename of either reaches the twins, the llms indexes and the structured data
// without a second edit. `site` already carries the base path, so only its
// origin is read here and the base is applied separately; joining both would
// publish /mikroscope/mikroscope/ URLs.
//
// Inside the build that object is Astro's. A Node script that reuses this
// module has no `import.meta.env` at all, so it has to pass the same two values
// through the environment. scripts/gen-docs.mjs loads this file through
// page-markdown.mjs but never calls anything here that builds a URL: docs/
// carries the advertised URL, which it reads out of astro.config.mjs itself.
// The fallback is the environment rather than a second copy of the values,
// because a default written here is a default that can disagree with the
// config and still build.
const ENV = import.meta.env ?? globalThis.process?.env ?? {};

const ORIGIN = new URL(ENV.SITE ?? "https://jmrplens.github.io").origin;

const BASE = (ENV.BASE_URL ?? "/").replace(/\/$/, "");

/**
 * The route of a docs collection entry: "" for the English home, "es" for the
 * Spanish one, "sinks/prometheus" for a page.
 *
 * The loader already strips the extension and a trailing `/index`, but an
 * entry id of "index" reaches here from an `index.mdx` at the collection root.
 *
 * @param {string} id collection entry id
 * @returns {string} the route, without leading or trailing slash
 */
export function routeOf(id) {
	const withoutExtension = id.replace(/\.mdx?$/, "");
	return withoutExtension === "index"
		? ""
		: withoutExtension.replace(/\/index$/, "");
}

/** @param {string} route @returns {string} the page path, base included. */
export const pagePath = (route) => (route ? `${BASE}/${route}/` : `${BASE}/`);

/** @param {string} route @returns {string} the absolute page URL. */
export const pageUrl = (route) => `${ORIGIN}${pagePath(route)}`;

/**
 * The markdown twin of a page sits inside the page's own directory, so the
 * twin of /sinks/prometheus/ is /sinks/prometheus/index.md. Appending ".md" to a
 * directory URL would address /sinks/prometheus.md, a path GitHub Pages does not map to
 * anything the page owns.
 *
 * @param {string} route @returns {string} the twin path, base included.
 */
export const twinPath = (route) => `${pagePath(route)}index.md`;

/** @param {string} route @returns {string} the absolute twin URL. */
export const twinUrl = (route) => `${ORIGIN}${twinPath(route)}`;

/** @param {string} route @returns {"en" | "es"} the locale a route is served in. */
export const localeOf = (route) =>
	route === "es" || route.startsWith("es/") ? "es" : "en";

/** @param {string} route @returns {string} the route with any "es/" prefix removed. */
export const withoutLocale = (route) =>
	route === "es" ? "" : route.replace(/^es\//, "");

/**
 * The same page in the other language: "es/sinks/prometheus" for
 * "sinks/prometheus" and back, "es" for the English home and "" for the
 * Spanish one. Whether that page was built is the caller's question; this only
 * says where it would be.
 *
 * @param {string} route @returns {string} the route of the page's translation.
 */
export const translationRoute = (route) =>
	localeOf(route) === "es"
		? withoutLocale(route)
		: route === ""
			? "es"
			: `es/${route}`;

/**
 * The two not-found routes. Starlight builds the English one as dist/404.html,
 * which GitHub Pages serves for every address that does not exist; the Spanish
 * one is an ordinary page at /es/404/, which the language menu of the English
 * one points at. Neither is a document: no structured-data node, no
 * breadcrumb, no markdown twin, no llms.txt entry, and both are noindex.
 */
export const NOT_FOUND_ROUTES = new Set(["404", "es/404"]);

export { BASE, ORIGIN };
