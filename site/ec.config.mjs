// @ts-check
/**
 * Expressive Code options that are functions, and so cannot sit in
 * astro.config.mjs: the `<Code>` component reads its options at render time
 * from a serialised copy of the inline ones, which drops a plugin, and from
 * this file, which keeps it. Both the markdown fences and `<Code>` get what is
 * here. The serialisable options stay in astro.config.mjs.
 */
import { defineEcConfig } from "@astrojs/starlight/expressive-code";
import { wrapWords } from "./src/lib/code-wrap-words.mjs";

export default defineEcConfig({
	// A `wrap` block breaks at its spaces only (src/lib/code-wrap-words.mjs).
	plugins: [wrapWords()],
	// A fence or <Code> whose language no grammar knows fails the build.
	// Expressive Code 0.44.2 only warns, renders the block as plain text and
	// lets the build exit 0, so a ```routeros fence built without the grammar
	// registered in astro.config.mjs would ship uncoloured and nothing would
	// say so. That warning is the only one Expressive Code 0.44.2 gives (the
	// one `logger.warn` in @expressive-code/plugin-shiki's dist/index.js), so
	// every warning fails the build: a release that rewords it, or adds
	// another, fails loudly instead of passing unnoticed. This logger replaces
	// the Astro one astro-expressive-code would pass (its createAstroRenderer
	// spreads this file's options after it), so Expressive Code's info and
	// errors go to the console with an `[expressive-code]` prefix, its own
	// fallback for a method a logger lacks.
	logger: {
		warn(message) {
			throw new Error(`[expressive-code] ${message}`);
		},
	},
});
