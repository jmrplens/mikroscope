// The markdown twin of every documentation page, at the page's own path with
// `index.md` appended.
//
// Generated from the content collection rather than written by hand or copied
// out of the build: a twin that is not derived from the page it doubles goes
// stale the first time the page is edited, and nothing notices, because a stale
// twin is still a valid document. scripts/check-twins.mjs asserts the closure
// this route is meant to guarantee.
//
// The body is reduced by src/lib/page-markdown.mjs, the same function
// scripts/gen-docs.mjs writes docs/ with, so the twin and the docs/ file say
// the same thing. The landing needs no special case: its body is one
// `<Home content={en} />` tag, and the reduction walks src/data/home.ts for it.
import type { APIRoute, GetStaticPaths, InferGetStaticPropsType } from "astro";
import { getCollection } from "astro:content";

import { renderTwin } from "../lib/page-markdown.mjs";
import { NOT_FOUND_ROUTES, routeOf } from "../lib/site.mjs";

export const getStaticPaths = (async () => {
	const entries = await getCollection("docs");
	return (
		entries
			.map((entry) => ({ entry, route: routeOf(entry.id) }))
			// The not-found pages are not documents; see NOT_FOUND_ROUTES.
			.filter(({ route }) => !NOT_FOUND_ROUTES.has(route))
			.map(({ entry, route }) => ({
				// "sinks/prometheus" -> /sinks/prometheus/index.md, "" -> /index.md.
				params: { path: route ? `${route}/index` : "index" },
				props: { entry, route },
			}))
	);
}) satisfies GetStaticPaths;

type Props = InferGetStaticPropsType<typeof getStaticPaths>;

export const GET: APIRoute = ({ props }) => {
	const { entry, route } = props as Props;
	const { title, description } = entry.data;
	const file = entry.filePath ?? entry.id;
	if (!description) {
		throw new Error(`${file}: no frontmatter description to use as the lead.`);
	}
	return new Response(
		renderTwin({ route, title, description, body: entry.body ?? "", file }),
		{
			headers: { "content-type": "text/markdown; charset=utf-8" },
		},
	);
};
