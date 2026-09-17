// One capture per dashboard section, of a demonstration database.
//
// The dashboards are 171 panels over 23 sections, and the pages that describe
// them had no picture of any of it. These are that picture: the InfluxDB
// dashboard, section by section, over a run of the same canned fake agent the
// end-to-end suites use, so nothing about a real router is in them.
//
// It does not fill the database itself. `test/e2e/docker` does that, and it
// leaves a manifest behind with the Grafana address, a token and the window
// the data is in:
//
//   make e2e-docker-up
//   MIKROSCOPE_CAPTURES=8m MIKROSCOPE_E2E_KEEP=1 \
//     go test -tags dockere2e -run TestFillStoreForCaptures -timeout 30m ./test/e2e/docker/
//   node site/scripts/gen-dashboard-captures.mjs
//
// Sections open one at a time: Grafana renders a collapsed row's panels only
// when it is expanded, and expanding all of them at once asks the store for
// 171 queries and lands several of them on a timeout.
//
// Usage: node scripts/gen-dashboard-captures.mjs [--only <section>]
import { mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

import { chromium } from "playwright";
import sharp from "sharp";

const SITE = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const REPO = path.dirname(SITE);
const MANIFEST = path.join(REPO, "test/e2e/docker/out/captures/grafana.json");
const OUT = path.join(SITE, "src/assets/dashboards");
const UID = "mikroscope-influxdb";
// Wide enough that a two-column panel row is two columns, and tall enough that
// a whole section is inside the viewport: Grafana renders a panel when it
// scrolls into view, so a section photographed from a short viewport is mostly
// empty boxes.
const VIEWPORT = { width: 1600, height: 3200 };

const only = process.argv.includes("--only")
	? process.argv[process.argv.indexOf("--only") + 1]
	: null;

let manifest;
try {
	manifest = JSON.parse(readFileSync(MANIFEST, "utf8"));
} catch (error) {
	console.error(
		`[captures] no manifest at ${MANIFEST}: fill the demonstration database first (see the header of this file).\n${error.message}`,
	);
	process.exit(1);
}

/** A file name from a section title: lowercase, words joined by hyphens. */
const slug = (title) =>
	title
		.toLowerCase()
		.replaceAll(/[^a-z0-9]+/g, "-")
		.replaceAll(/^-|-$/g, "");

mkdirSync(OUT, { recursive: true });

// The screenshots come out at twice the viewport (deviceScaleFactor 2), which
// is 3200 px of PNG and about 300 KB each. Downsampling to the width the page
// renders at and encoding as WebP is a quarter of that, and the repository
// carries these files forever.
const CAPTURE_WIDTH = 1600;
async function encode(pngPath, webpPath) {
	await sharp(pngPath)
		.resize({ width: CAPTURE_WIDTH, withoutEnlargement: true })
		.webp({ quality: 80 })
		.toFile(webpPath);
	rmSync(pngPath);
}

const browser = await chromium.launch({ args: ["--no-sandbox"] });
const context = await browser.newContext({
	viewport: VIEWPORT,
	deviceScaleFactor: 2,
	colorScheme: "dark",
});
// The token goes in a header rather than a login: a service account token is
// what the fill step left behind, and Grafana accepts it on every request.
await context.setExtraHTTPHeaders({
	Authorization: `Bearer ${manifest.token}`,
});
const page = await context.newPage();

// The range is the run, and nothing either side of it: a window wider than the
// data draws every graph with an empty half, which reads as an outage rather
// than as "the capture started here". A second of slack at each end is enough
// for the last bin.
const from = manifest.from - 1000;
const to = manifest.to + 1000;

const url = new URL(`/d/${UID}/`, manifest.grafana);
url.searchParams.set("from", String(from));
url.searchParams.set("to", String(to));
url.searchParams.set("kiosk", "1");
url.searchParams.set("theme", "dark");

await page.goto(url.href, { waitUntil: "networkidle", timeout: 120_000 });
await page.waitForTimeout(4000);

// The time picker and the refresh control are chrome, not dashboard, and they
// are sticky: they sit over the top of whatever is photographed.
await page.addStyleTag({
	content: `
		[data-testid="data-testid dashboard controls"],
		.dashboard-controls { display: none !important; }
	`,
});

// The dashboard carries two annotation layers — detections and triggers — and
// the fake agent fires both continuously, so on a 20-minute run every graph is
// behind a picket fence of vertical markers. They are a real feature and they
// are documented elsewhere; here they hide the data the captures exist to
// show, so both toggles go off before anything is photographed.
async function hideAnnotations() {
	// Grafana renders each layer as a checkbox with a label bound to it by id
	// (`data-layer-annotations-<n>`); the label is what takes the click.
	const labels = page.locator(
		'[data-testid^="data-testid Dashboard template variables submenu Label"]',
	);
	const count = await labels.count();
	for (let i = 0; i < count; i += 1) {
		const label = labels.nth(i);
		const name = (await label.textContent())?.trim() ?? "";
		if (!["detections", "triggers"].includes(name)) continue;
		const id = await label.getAttribute("for");
		const box = id ? page.locator(`#${id}`) : null;
		if (
			box &&
			(await box.count()) &&
			(await box.isChecked().catch(() => false))
		) {
			await label.click();
			await page.waitForTimeout(400);
		}
	}
	await page.waitForTimeout(500);
}

/** Waits for every panel query to finish, then for the charts to draw. */
async function settle() {
	// The refresh button becomes "Cancel" while queries are in flight.
	await page
		.waitForFunction(() => !document.body.innerText.includes("Cancel"), {
			timeout: 90_000,
		})
		.catch(() => {});
	await page
		.waitForFunction(
			() =>
				document.querySelectorAll('[aria-label="Panel loading bar"]').length ===
				0,
			{ timeout: 60_000 },
		)
		.catch(() => {});
	await page.waitForTimeout(2500);
}

/** Scrolls a section end to end so every panel in it is rendered once. */
async function renderAll(section) {
	const box = await section.boundingBox();
	if (!box) return;
	// Small steps, because Grafana renders a panel when it enters the viewport
	// and a jump the height of the viewport can take one past a panel without
	// it ever being visible.
	const step = 400;
	for (let y = 0; y < box.height + VIEWPORT.height; y += step) {
		await page.mouse.wheel(0, step);
		await page.waitForTimeout(250);
	}
	await page.mouse.wheel(0, -(box.height + 2 * VIEWPORT.height));
	await page.waitForTimeout(800);
}

// Grafana writes its test ids with the words "data-testid " inside the value.
const ROW_TITLE = '[data-testid^="data-testid dashboard-row-title"]';
const rows = await page.$$eval(ROW_TITLE, (nodes) =>
	nodes.map((n) => n.textContent.trim()),
);
if (rows.length === 0) {
	console.error(
		"[captures] the dashboard rendered no rows. Is the dashboard imported, and is the token still valid?",
	);
	await browser.close();
	process.exit(1);
}

const written = [];
// The Overview is not a collapsed row: it is what the dashboard opens on.
await hideAnnotations();
await settle();
const overview = page.locator(".react-grid-layout").first();
await overview.screenshot({ path: path.join(OUT, "00-overview.png") });
await encode(
	path.join(OUT, "00-overview.png"),
	path.join(OUT, "00-overview.webp"),
);
written.push("00-overview.webp");

let index = 1;
for (const title of rows) {
	// The Overview is the section the dashboard opens on and is captured above.
	if (title === "Overview") {
		index += 1;
		continue;
	}
	if (only && !title.toLowerCase().includes(only.toLowerCase())) {
		index += 1;
		continue;
	}
	const header = page.locator(ROW_TITLE, { hasText: title }).first();
	await header.scrollIntoViewIfNeeded();
	await header.click();
	// Panels are queried on expand and drawn when they scroll into view.
	await settle();
	const section = page
		.locator(`[data-testid="data-testid dashboard-row-wrapper-for-${title}"]`)
		.first();
	if (await section.count()) {
		await renderAll(section);
		await settle();
	}
	const stem = `${String(index).padStart(2, "0")}-${slug(title)}`;
	const target = (await section.count()) ? section : page.locator("body");
	await target.screenshot({ path: path.join(OUT, `${stem}.png`) });
	await encode(path.join(OUT, `${stem}.png`), path.join(OUT, `${stem}.webp`));
	written.push(`${stem}.webp`);
	console.log(`[captures] ${stem}.webp`);
	// Collapse it again: an open section pushes the next one off the viewport
	// and doubles the query load on the store.
	await header.click();
	await page.waitForTimeout(500);
	index += 1;
}

writeFileSync(
	path.join(OUT, "captures.json"),
	`${JSON.stringify({ generated: new Date().toISOString(), window: { from, to }, files: written }, null, 2)}\n`,
);
console.log(`[captures] ${written.length} file(s) in ${OUT}`);
await browser.close();
