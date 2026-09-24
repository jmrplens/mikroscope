// @ts-check
/**
 * Writes the current version where a page put {{MIKROSCOPE_VERSION}}.
 *
 * The install commands are the text a reader copies, so they are the text that
 * must name the release that exists: `--remote-image
 * jmrplens/mikroscope-agent:1.2.0` stayed on five pages and their twins while
 * 1.2.1 fixed a data race in the agent, so the command installed the agent
 * with the defect (GEO audit, 2026-09-24). A component cannot sit inside a
 * code fence, so the fence carries the placeholder and this plugin replaces it
 * before Expressive Code renders the block. Expressive Code is a hast plugin,
 * so every mdast plugin runs before it, and its copy button copies the version
 * rather than the placeholder.
 *
 * The nodes read are the ones MDX leaves braces literal in: fenced code,
 * inline code, and a link or definition target. In prose MDX reads the braces
 * as an expression, and `{{MIKROSCOPE_VERSION}}` there fails the build with a
 * ReferenceError, which is the right outcome: prose takes <Version />.
 *
 * The plugin is a factory that looks at the page's source first, so a page
 * with no placeholder, which is most of them, runs no visitor at all and no
 * node crosses from Rust for it.
 *
 * The same substitution for the markdown twins, llms-full.txt and docs/ is
 * `reduceBody` in src/lib/page-markdown.mjs; src/lib/release.mjs has the
 * placeholder and the reason both exist.
 */
import { VERSION_PLACEHOLDER, withVersion } from "./release.mjs";

/**
 * @param {string} version the release to write, from readRelease()
 * @returns {(ctx: { source: string }) => object | null} a Sätteri mdast plugin entry
 */
export function versionPlaceholder(version) {
	/** @param {any} node @param {any} ctx @param {string} key */
	const replace = (node, ctx, key) => {
		const value = node[key];
		if (typeof value === "string" && value.includes(VERSION_PLACEHOLDER))
			ctx.setProperty(node, key, withVersion(value, version));
	};
	const plugin = {
		name: "mikroscope-version-placeholder",
		/** @param {any} node @param {any} ctx */
		code: (node, ctx) => replace(node, ctx, "value"),
		/** @param {any} node @param {any} ctx */
		inlineCode: (node, ctx) => replace(node, ctx, "value"),
		/** @param {any} node @param {any} ctx */
		link: (node, ctx) => replace(node, ctx, "url"),
		/** @param {any} node @param {any} ctx */
		definition: (node, ctx) => replace(node, ctx, "url"),
	};
	return (ctx) => (ctx.source.includes(VERSION_PLACEHOLDER) ? plugin : null);
}
