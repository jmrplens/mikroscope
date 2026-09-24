// The English pages to read first, concatenated as the same markdown the twins
// serve: the fetch that gets a model the project without the reference tables.
// Which pages, and why, is CORE in src/lib/llms.mjs.
import type { APIRoute } from "astro";

import { renderBundle } from "../lib/llms.mjs";

export const GET: APIRoute = async () =>
	new Response(await renderBundle("en", "core"), {
		headers: { "content-type": "text/plain; charset=utf-8" },
	});
