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
});
