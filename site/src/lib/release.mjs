// @ts-check
/**
 * The release the site describes, parsed from the two files that already say
 * it: `VERSION`, which both binaries embed and the release workflow compares
 * with the tag, and the `## [x.y.z] - YYYY-MM-DD` heading CHANGELOG.md gives
 * that version.
 *
 * The site used to restate the version by hand, and it drifted three ways at
 * once: the JSON-LD said 1.0.0 on every page, the copyable install commands
 * pinned 1.2.0, and about/status said 1.2.2 (GEO audit of 2026-09-24). Only
 * the one paragraph a release touches kept up. So nothing under site/ writes a
 * version any more; it is read here at build time.
 *
 * This file is the parsing, and it does no I/O of its own except in
 * `readRelease`, because its callers reach the two files in two ways:
 *
 *   - src/data/release.ts imports them with Vite's `?raw`, which is what the
 *     Astro build bundles (components, the markdown twins, llms-full.txt) and
 *     what scripts/data-hooks.mjs teaches plain Node for gen-docs.mjs. A path
 *     built from `import.meta.url` would not survive that bundling.
 *   - astro.config.mjs and the check scripts read them with `readRelease`.
 *     The config is loaded by plain Node before Vite exists, so `?raw` is not
 *     available there, and it needs the version to hand to the markdown
 *     plugin below.
 *
 * Both end in `parseRelease`, so the two readers cannot disagree about what
 * the files say.
 *
 * The placeholder is for text a component cannot reach: a fenced code block
 * or an inline code span, where MDX renders braces literally. The pages write
 * `--remote-image jmrplens/mikroscope-agent:{{MIKROSCOPE_VERSION}}`, and every
 * renderer that emits page text substitutes it: the Sätteri plugin in
 * src/lib/version-placeholder.mjs for the HTML, and `reduceBody` in
 * src/lib/page-markdown.mjs for the markdown twins, llms-full.txt and docs/.
 * Prose uses <Version /> instead; see src/components/Version.astro.
 * scripts/check-version.mjs fails when the placeholder reaches dist/ or
 * docs/, and when a page pins a literal version where the placeholder belongs.
 */
import { readFileSync } from "node:fs";
import path from "node:path";

/** What a page writes where the current version belongs in code. */
export const VERSION_PLACEHOLDER = "{{MIKROSCOPE_VERSION}}";

/**
 * The placeholder's name alone. A check looks for this rather than the whole
 * token, because a placeholder that lands in an href arrives percent-encoded
 * (`%7B%7BMIKROSCOPE_VERSION%7D%7D`) and only the name survives that intact.
 */
export const VERSION_PLACEHOLDER_NAME = "MIKROSCOPE_VERSION";

export const REPO_URL = "https://github.com/jmrplens/mikroscope";

/** Three dot-separated numbers, which is all a release number has been. */
const SEMVER = /^\d+\.\d+\.\d+$/;

/**
 * @typedef {object} Release
 * @property {string} version "1.2.2", exactly as VERSION holds it
 * @property {string} tag "v1.2.2", the git tag the release workflow runs on
 * @property {string} date "2026-09-24", from the CHANGELOG heading
 * @property {string} notesUrl the GitHub release page of this tag
 * @property {string} releasesUrl every release, newest first
 * @property {string} changelogUrl CHANGELOG.md on main
 */

/**
 * @param {string} versionText the contents of VERSION
 * @param {string} changelogText the contents of CHANGELOG.md
 * @returns {Release}
 */
export function parseRelease(versionText, changelogText) {
	const version = versionText.trim();
	if (!SEMVER.test(version)) {
		throw new Error(
			`VERSION holds "${version}", which is not a release number (x.y.z). The site reads the version it documents from that file.`,
		);
	}
	const escaped = version.replaceAll(".", "\\.");
	const heading = new RegExp(
		`^## \\[${escaped}\\] - (\\d{4}-\\d{2}-\\d{2})[ \\t]*$`,
		"m",
	).exec(changelogText);
	if (!heading) {
		throw new Error(
			`CHANGELOG.md has no "## [${version}] - YYYY-MM-DD" heading for the version in VERSION. ` +
				"The site states the release date beside the version and reads it from that heading, " +
				"so a release has to add it in the same commit that bumps VERSION.",
		);
	}
	const tag = `v${version}`;
	return {
		version,
		tag,
		date: heading[1],
		notesUrl: `${REPO_URL}/releases/tag/${tag}`,
		releasesUrl: `${REPO_URL}/releases`,
		changelogUrl: `${REPO_URL}/blob/main/CHANGELOG.md`,
	};
}

/**
 * The release, read from the checkout. For callers outside Vite: the Astro
 * config and scripts/*.mjs. Inside the build, import src/data/release.ts.
 *
 * @param {string} repoRoot the repository root, where VERSION lives
 * @returns {Release}
 */
export function readRelease(repoRoot) {
	return parseRelease(
		readFileSync(path.join(repoRoot, "VERSION"), "utf8"),
		readFileSync(path.join(repoRoot, "CHANGELOG.md"), "utf8"),
	);
}

/**
 * The text with every placeholder replaced by the version.
 *
 * @param {string} text
 * @param {string} version
 * @returns {string}
 */
export const withVersion = (text, version) =>
	text.replaceAll(VERSION_PLACEHOLDER, version);
