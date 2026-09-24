// One sidebar section's Spanish pages, concatenated as the same markdown the
// twins serve, at /es/llms/<section>.txt. The key is the English one, so the
// two files of a section differ only by the /es/ in front; src/lib/llms.mjs
// lists every one in /es/llms.txt with its size.
import type { APIRoute, GetStaticPaths } from "astro";

import { renderBundle, sectionKeys } from "../../../lib/llms.mjs";

export const getStaticPaths = (() =>
	sectionKeys().map((section) => ({
		params: { section },
	}))) satisfies GetStaticPaths;

export const GET: APIRoute = async ({ params }) =>
	new Response(await renderBundle("es", String(params.section)), {
		headers: { "content-type": "text/plain; charset=utf-8" },
	});
