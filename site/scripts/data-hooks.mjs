// Teaches plain Node the two import spellings src/data/*.ts use and Vite reads
// natively, so scripts/gen-docs.mjs can load the same data modules the site
// renders from, verbatim; and writes the release into src/data/rsc/*.json as
// the build does.
//
// Node strips types on its own, but it resolves no extensionless specifier,
// and src/data/*.ts import each other as `./measurements` because that is what
// the Astro build wants. Rewriting those imports to carry `.ts` would change
// ten typed modules to suit one script. dashboards.ts additionally reads the
// committed dashboards with Vite's `?raw`, which Node knows nothing about.
//
// Registered by importing this file, which has to happen before the module
// that needs it is loaded: a static import graph is resolved as a whole before
// any of it runs, so the script imports this file statically and then
// src/lib/page-markdown.mjs dynamically. It is never imported by the site: in
// the build's process the extensionless rule would reach every CommonJS
// `require("./x")` in node_modules as well.
import { readFileSync } from "node:fs";
import { registerHooks } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { readRelease, withVersion } from "../src/lib/release.mjs";

// src/data/rsc/*.json keep the release as {{MIKROSCOPE_VERSION}} (cmd/gen_rsc);
// the build writes the current one in as they are imported (rscReleaseData in
// astro.config.mjs), and so does this, for the same files and the same reason.
const RSC_DATA = /[\\/]src[\\/]data[\\/]rsc[\\/][^\\/]+\.json$/;
const { version } = readRelease(
	fileURLToPath(new URL("../..", import.meta.url)),
);

registerHooks({
	resolve(specifier, context, next) {
		// `import x from "…/file.json?raw"` — Vite's raw text import.
		if (specifier.endsWith("?raw")) {
			const url = new URL(specifier.slice(0, -4), context.parentURL);
			return { url: `${url.href}?raw`, format: "module", shortCircuit: true };
		}
		// `import { Lang } from "./measurements"` — TypeScript's extensionless
		// relative import, which resolves to the .ts file beside it.
		if (/^\.{1,2}\//.test(specifier) && path.extname(specifier) === "") {
			return next(`${specifier}.ts`, context);
		}
		return next(specifier, context);
	},
	load(url, context, next) {
		if (url.endsWith("?raw")) {
			const file = fileURLToPath(url.slice(0, -4));
			return {
				format: "module",
				shortCircuit: true,
				source: `export default ${JSON.stringify(readFileSync(file, "utf8"))};`,
			};
		}
		if (url.startsWith("file:") && RSC_DATA.test(fileURLToPath(url))) {
			return {
				format: "json",
				shortCircuit: true,
				source: withVersion(readFileSync(fileURLToPath(url), "utf8"), version),
			};
		}
		return next(url, context);
	},
});
