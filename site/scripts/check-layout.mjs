// What the built pages do in a real browser at a real width, which no HTML
// validator can see.
//
// Three faults this catches, each of which has happened here:
//
//   * A table wider than the phone. Every wide table on this site is wrapped
//     in ScrollTable, which scrolls inside its own box; one that is not makes
//     the whole page scroll sideways, and the reader loses the left margin of
//     every paragraph. The README's device table did exactly that (fixed
//     2026-09-16), and nothing in the build would have said so.
//   * A page that scrolls sideways at all, whatever caused it — a long
//     identifier, a code frame with no overflow, an image with a fixed width.
//   * A copy-to-clipboard button sitting on top of the code. Expressive Code
//     positions it over the top-right of the frame, and at 390 px it covered
//     text on 140 blocks across 60 pages (measured 2026-09-15). It now lives
//     in a strip of its own, and this is what keeps it there.
//
// Usage: node scripts/check-layout.mjs [dist-directory]
//        node scripts/check-layout.mjs --only /install/cli/
import { readFileSync, readdirSync } from "node:fs";
import { join, resolve } from "node:path";
import process from "node:process";

import { chromium } from "playwright";

import { withPreview } from "./preview-server.mjs";
import { isRedirectStub } from "./redirect-stub.mjs";

const only = process.argv.includes("--only")
	? process.argv[process.argv.indexOf("--only") + 1]
	: null;
const DIST = resolve(
	process.argv.find((a, i) => i > 1 && !a.startsWith("--") && a !== only) ??
		"dist",
);

// The widths that matter: the narrowest phone this site is designed for, and a
// laptop. The phone is where layout breaks; the laptop is where the copy
// button used to be invisible.
const VIEWPORTS = [
	{
		name: "390px",
		viewport: { width: 390, height: 844 },
		isMobile: true,
		hasTouch: true,
	},
	{ name: "1280px", viewport: { width: 1280, height: 900 } },
];

/** Every built page, as a site path, minus the pages that are not documents. */
function pages() {
	const out = [];
	const walk = (dir, prefix = "") => {
		for (const entry of readdirSync(dir, { withFileTypes: true })) {
			const rel = prefix ? `${prefix}/${entry.name}` : entry.name;
			if (entry.isDirectory()) walk(join(dir, entry.name), rel);
			else if (entry.name === "index.html") {
				if (isRedirectStub(readFileSync(join(dir, entry.name), "utf8")))
					continue;
				out.push(`/${rel.replace(/index\.html$/, "")}`);
			}
		}
	};
	walk(DIST);
	return out.sort();
}

/** What one page looks like at one width, as plain data. */
const inspect = () => {
	const problems = [];
	const doc = document.documentElement;
	// A page that scrolls sideways. 2px of slack: sub-pixel layout rounds up.
	if (doc.scrollWidth > doc.clientWidth + 2) {
		problems.push(
			`the page scrolls sideways: ${doc.scrollWidth}px of content in ${doc.clientWidth}px`,
		);
	}
	// Tables, each against the box it is in.
	for (const table of document.querySelectorAll(".sl-markdown-content table")) {
		const box = table.parentElement;
		const scrolls = box && box.scrollWidth > box.clientWidth + 2;
		const wrapped = table.closest("[data-ms-scroll], .ms-scroll");
		if (
			table.scrollWidth > (box?.clientWidth ?? 0) + 2 &&
			!wrapped &&
			!scrolls
		) {
			problems.push(
				`a table is ${table.scrollWidth}px wide in a ${box?.clientWidth}px box and is not wrapped for scrolling`,
			);
		}
	}
	// The copy button: present, visible, and clear of the code.
	for (const frame of document.querySelectorAll(".expressive-code .frame")) {
		const button = frame.querySelector(".copy button");
		if (!button) {
			problems.push("a code block has no copy button");
			continue;
		}
		const style = getComputedStyle(button);
		const box = button.getBoundingClientRect();
		if (
			style.display === "none" ||
			style.visibility === "hidden" ||
			Number(style.opacity) < 0.5
		) {
			problems.push(
				`a copy button is not visible (opacity ${style.opacity}, display ${style.display})`,
			);
			continue;
		}
		if (box.width < 16 || box.height < 16) {
			problems.push(
				`a copy button is ${Math.round(box.width)}×${Math.round(box.height)}px`,
			);
		}
		for (const line of frame.querySelectorAll(".ec-line .code")) {
			const text = line.getBoundingClientRect();
			const overlaps =
				box.left < text.right &&
				box.right > text.left &&
				box.top < text.bottom &&
				box.bottom > text.top;
			if (overlaps && line.textContent.trim() !== "") {
				problems.push(
					`the copy button overlaps code: ${line.textContent.trim().slice(0, 40)}`,
				);
				break;
			}
		}
	}
	return problems;
};

const paths = only ? [only] : pages();
const failures = [];
let checked = 0;

await withPreview(async (origin) => {
	const browser = await chromium.launch({ args: ["--no-sandbox"] });
	try {
		for (const { name, ...contextOptions } of VIEWPORTS) {
			const context = await browser.newContext(contextOptions);
			const page = await context.newPage();
			for (const path of paths) {
				await page.goto(new URL(path, origin).href, { waitUntil: "load" });
				const problems = await page.evaluate(inspect);
				checked += 1;
				for (const problem of problems)
					failures.push(`${path} @${name}: ${problem}`);
			}
			await context.close();
		}
	} finally {
		await browser.close();
	}
});

if (failures.length > 0) {
	console.error(
		`[layout] ${failures.length} problem(s) over ${checked} page renders:`,
	);
	for (const line of failures) console.error(`  ${line}`);
	process.exit(1);
}
console.log(
	`[layout] ${checked} page renders: nothing scrolls sideways, every table fits its box, every copy button is visible and clear of the code.`,
);
