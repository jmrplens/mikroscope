#!/usr/bin/env node
/**
 * The figures README.md quotes are the ones src/data/measurements.ts holds.
 *
 * The README is the one page about the project that no build renders: GitHub
 * shows it as it is committed, so it cannot use <Measured>, and its cost
 * paragraph types the install default's CPU and memory, the budget they are
 * held to and the campaign's conditions by hand. The engines that answered
 * questions about this project on 2026-09-24 were still quoting 1.0.0's
 * 2.85 % and 31.3 MiB from an older README, so a README that stays behind a
 * new campaign is not hypothetical. Each figure is rendered here the way the
 * site renders it (formatQuantity, describeCpu) and must appear in the README's
 * cost paragraph word for word, ignoring line breaks and the no-break spaces the
 * site puts between a number and its unit.
 *
 * Source only, so it needs no build.
 *
 * Usage: node scripts/check-readme.mjs
 */
import "./data-hooks.mjs";

import { readFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const readmePath = path.join(here, "..", "..", "README.md");

const { measurements, campaigns, describeCpu } =
	await import("../src/data/measurements.ts");
const { formatQuantity } = await import("../src/lib/format.ts");

/** One spelling for every run of spaces, no-break or not, and line breaks. */
const flat = (s) => s.replace(/[\s  ]+/g, " ");

const raw = readFileSync(readmePath, "utf8");
const en = (id) => flat(formatQuantity(measurements[id], "en"));

// The figures are looked for in the cost paragraph alone: the text between the
// end of the comment that names their ids and the next `## ` heading. Matched
// against the whole file, the campaign's date (inside the comment's campaign
// id), its RouterOS version (the device table) and its sink (the sink list)
// were always found somewhere else, and that paragraph could drift on them
// with the check still passing.
const commentStart = raw.indexOf(
	"<!-- The figures below come from site/src/data/measurements.ts",
);
const commentEnd = commentStart < 0 ? -1 : raw.indexOf("-->", commentStart);
const headingAfter = commentEnd < 0 ? -1 : raw.indexOf("\n## ", commentEnd);
if (commentStart < 0 || commentEnd < 0 || headingAfter < 0) {
	console.error(
		"[readme] README.md has no cost paragraph to check: the figure comment " +
			"(<!-- The figures below come from site/src/data/measurements.ts … -->) " +
			"followed by the paragraph and then a `## ` heading was not found.",
	);
	process.exit(1);
}
const comment = flat(raw.slice(commentStart, commentEnd));
const readme = flat(raw.slice(commentEnd + "-->".length, headingAfter));

// The campaign the README's cost paragraph names, which must be the one the
// four figures come from.
const ids = ["run.10hz.cpu", "run.10hz.rss", "budget.cpu", "budget.rss"];
const campaignId = measurements["run.10hz.cpu"].campaign;
const campaign = campaigns[campaignId];

/** @type {[string, string][]} what must appear, and where it comes from */
const expected = [
	[`${en("run.10hz.cpu")} of one core`, "run.10hz.cpu"],
	[`${en("run.10hz.rss")} of resident memory`, "run.10hz.rss"],
	[`≤ ${en("budget.rss")}`, "budget.rss"],
	[`≤ ${en("budget.cpu")}`, "budget.cpu"],
	[flat(describeCpu(campaign, "en")), `campaign ${campaignId}, cpu`],
	[`RouterOS ${campaign.routeros}`, `campaign ${campaignId}, routeros`],
	[`${campaign.windowS} s window`, `campaign ${campaignId}, windowS`],
	[campaign.date, `campaign ${campaignId}, date`],
	...(campaign.sinks ?? []).map((sink) => [
		sink,
		`campaign ${campaignId}, sinks`,
	]),
];

const problems = expected
	.filter(([text]) => !readme.includes(text))
	.map(
		([text, from]) =>
			`README.md's cost paragraph does not say "${text}" (from ${from} in src/data/measurements.ts)`,
	);

// The comment above the paragraph names the ids it quotes; a campaign that
// moves the figures to new ids should move the comment too.
for (const id of [...ids, campaignId]) {
	if (!comment.includes(id)) {
		problems.push(
			`README.md's figure comment does not name ${id}, which the cost paragraph quotes`,
		);
	}
}

if (problems.length > 0) {
	for (const p of problems) console.error(`[readme] ${p}`);
	console.error(
		"[readme] Rewrite the cost paragraph in README.md from src/data/measurements.ts.",
	);
	process.exit(1);
}
console.log(
	`[readme] ${expected.length} figures and conditions in README.md's cost paragraph match src/data/measurements.ts (campaign ${campaignId}).`,
);
