// @ts-check
/**
 * Starlight route middleware: a page's "Last updated" date counts the data it
 * renders, not only its own .mdx file.
 *
 * The dates come from `virtual:mikroscope/lastmod`, which astro.config.mjs
 * builds with src/lib/lastmod.mjs; that file says what a page counts and why.
 * The sitemap's <lastmod> reads the same table, so the footer, the sitemap
 * and the JSON-LD `dateModified` that Head.astro takes from this route value
 * carry one date per page.
 *
 * Starlight's own choices are kept:
 * - no date where Starlight shows none: `lastUpdated: false` in a page's
 *   frontmatter, or a page with no git history;
 * - a date written in the frontmatter wins, because someone chose it.
 */
import { defineRouteMiddleware } from "@astrojs/starlight/route-data";
import lastmod from "virtual:mikroscope/lastmod";

/** @type {Record<string, string>} */
const dates = lastmod;

export const onRequest = defineRouteMiddleware((context) => {
	const route = context.locals.starlightRoute;
	if (!route.lastUpdated) return;
	if (route.entry.data.lastUpdated instanceof Date) return;
	const iso = route.entry.filePath ? dates[route.entry.filePath] : undefined;
	if (iso) route.lastUpdated = new Date(iso);
});
