/**
 * The release this build documents: the version in VERSION and its date from
 * the matching CHANGELOG.md heading, read when the site is built.
 *
 * Import this from components, pages and src/lib/*.mjs:
 *
 *   import { release } from "../data/release";   // .astro, .ts
 *   import { release } from "../data/release.ts"; // .mjs (page-markdown.mjs)
 *
 *   release.version       "1.2.2"
 *   release.tag           "v1.2.2"
 *   release.date          "2026-09-24"
 *   release.notesUrl      https://github.com/jmrplens/mikroscope/releases/tag/v1.2.2
 *   release.releasesUrl   https://github.com/jmrplens/mikroscope/releases
 *   release.changelogUrl  https://github.com/jmrplens/mikroscope/blob/main/CHANGELOG.md
 *
 * The files are read with Vite's `?raw`, the same way dashboards.ts reads the
 * committed dashboards, so the Astro build and scripts/gen-docs.mjs (through
 * scripts/data-hooks.mjs) load them identically. The parsing, and the failure
 * when CHANGELOG.md has no dated heading for the version, is
 * src/lib/release.mjs, which astro.config.mjs and the check scripts call
 * directly because they run before, or outside, Vite.
 *
 * Nothing here is a claim about the release beyond its number and date: what
 * it was measured on lives in measurements.ts, and a figure taken on an older
 * release keeps that release's number in its own sentence.
 */
import versionText from "../../../VERSION?raw";
import changelogText from "../../../CHANGELOG.md?raw";
import { parseRelease, type Release } from "../lib/release.mjs";

export type { Release };

export const release: Release = parseRelease(versionText, changelogText);
