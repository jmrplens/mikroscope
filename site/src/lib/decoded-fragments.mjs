// @ts-check
/**
 * Writes a same-page link's fragment the way the heading spells its own id.
 *
 * The markdown pipeline percent-encodes every link destination it builds, so
 * the Spanish reference/measurements page's link to its own heading
 * `## Dónde difieren los dos almacenes` reached the built page as
 * `href="#d%C3%B3nde-difieren-los-dos-almacenes"`, while the heading's id, and
 * Starlight's table of contents with it, is `dónde-difieren-los-dos-almacenes`
 * (the one encoded fragment in the build of 2026-09-15). A browser decodes the
 * fragment before matching and reaches the heading either way, but nothing
 * that reads the markup does: pa11y reports `no anchor exists with that name`
 * for such a link, which is how the same defect was found on ghchronicle's
 * site, 28 times on its Spanish measurements page.
 *
 * Decoding is the direction that makes them agree, because the id is the one
 * spelling the page cannot change. Only `#…` fragments are touched: a path or
 * an absolute URL keeps whatever encoding it was written with, which is what
 * makes it a URL rather than a name.
 *
 * Ported from ghchronicle's rehype-decoded-fragments.mjs, a unified plugin
 * that walked the tree by hand, to the Sätteri shape this site's processor
 * takes: a filtered element visitor, so only `<a>` nodes cross from Rust.
 */

/** @param {string} fragment */
const decoded = (fragment) => {
	try {
		return decodeURIComponent(fragment);
	} catch {
		// A lone `%` is a valid character in an id and an invalid escape here.
		return fragment;
	}
};

/** The Sätteri hast plugin that decodes same-page fragments. */
export function decodedFragments() {
	return {
		name: "mikroscope-decoded-fragments",
		element: {
			filter: ["a"],
			/** @param {any} node @param {any} ctx */
			visit(node, ctx) {
				const href = node.properties?.href;
				if (typeof href !== "string" || !href.startsWith("#")) return;
				const fragment = decoded(href.slice(1));
				if (fragment !== href.slice(1))
					ctx.setProperty(node, "href", `#${fragment}`);
			},
		},
	};
}
