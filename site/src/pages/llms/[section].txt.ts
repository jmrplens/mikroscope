// One sidebar section's English pages, concatenated as the same markdown the
// twins serve, at /llms/<section>.txt. The sections, and the key each file is
// named by, come from the sidebar through src/lib/llms.mjs, which lists every
// one of them in /llms.txt with its size.
import type { APIRoute, GetStaticPaths } from "astro";

import { renderBundle, sectionKeys } from "../../lib/llms.mjs";

export const getStaticPaths = (() =>
	sectionKeys().map((section) => ({
		params: { section },
	}))) satisfies GetStaticPaths;

export const GET: APIRoute = async ({ params }) =>
	new Response(await renderBundle("en", String(params.section)), {
		headers: { "content-type": "text/plain; charset=utf-8" },
	});
