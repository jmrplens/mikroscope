// @ts-check
// What install/manual-gui shows beside its steps: the WebFig captures and
// the install manifest's contents.
//
// Both come from files written by something that ran, never from a page:
// src/assets/webfig/captures.json by scripts/gen-webfig-captures.mjs, which
// takes the pictures while it installs the agent through WebFig in the
// virtual lab, and src/data/rsc/cases.json by cmd/gen_rsc, from the install
// steps spec the CLI renders its own manifest from. The components
// (WebfigCapture.astro, InstallManifest.astro) and the Markdown twin
// (page-markdown.mjs) read them through here, so a page and its twin say the
// same thing.
import captures from "../assets/webfig/captures.json" with { type: "json" };
import cases from "../data/rsc/cases.json" with { type: "json" };

/**
 * @typedef {{ file: string, menu: string, alt: { en: string, es: string } }} WebfigCaptureMeta
 */

/**
 * One capture's entry in captures.json: its file, where it is in WebFig,
 * and its alt text in each language.
 *
 * @param {string} name the stem, e.g. `veth-new`
 * @returns {WebfigCaptureMeta}
 */
export function webfigCapture(name) {
	/** @type {Record<string, WebfigCaptureMeta>} */
	const all = captures;
	const meta = all[name];
	if (!meta) {
		throw new Error(
			`no WebFig capture "${name}" in src/assets/webfig/captures.json; the stems are ${Object.keys(all).join(", ")}. See site/scripts/gen-webfig-captures.mjs.`,
		);
	}
	return meta;
}

/**
 * The install manifest a golden case writes, as the file on the router
 * holds it: one line per entry, each ending in a newline. cases.json keeps
 * RouterOS's escaped form, the two characters \n inside a quoted string.
 *
 * @param {string} id a case of src/data/rsc/cases.json, e.g. `pull-dockerhub`
 * @returns {string}
 */
export function manifestText(id) {
	/** @type {{ id: string, values: { manifest: string } }[]} */
	const all = /** @type {any} */ (cases);
	const found = all.find((c) => c.id === id);
	if (!found) {
		throw new Error(
			`no case "${id}" in src/data/rsc/cases.json; run make gen-rsc after changing the install steps`,
		);
	}
	return found.values.manifest.replaceAll("\\n", "\n");
}
