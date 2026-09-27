#!/usr/bin/env node
/**
 * The script generator, driven in a browser against the built site.
 *
 * scripts/check-rsc.mjs holds the renderer to the CLI in Node. This holds the
 * page to the renderer: a field wired to the wrong option, a control that
 * never reaches the output, a button that copies the wrong text or a policy
 * that blocks the bundle would all pass that check and fail this one. It
 * needs a build (`pnpm build`) and Playwright's Chromium, like pa11y, and is
 * not part of `pnpm lint`: `pnpm test:generator` runs it.
 *
 * Against `astro preview` (scripts/preview-server.mjs), it checks:
 *
 *   1. without JavaScript, the page shows the default script, which is
 *      `plan --rsc` for a Docker Hub pull (cases/pull-dockerhub.rsc), and no
 *      form;
 *   2. every case of the golden matrix, set through the form's own controls,
 *      renders its golden script byte for byte, its uninstall script as
 *      src/lib/rsc.mjs renders it, and, where the form can say the same
 *      --arch, Go's command line;
 *   3. an invalid field replaces the output with a list of errors that links
 *      to the field, and disables copy and download;
 *   4. the token: generated in the page, 32 characters of [A-Za-z0-9], in the
 *      script and never in the command line, the URL, the console or the
 *      page's storage; the "treat it as a credential" note appears with it;
 *   5. copy puts the output on the clipboard, and download saves it as
 *      mikroscope-install.rsc with the same bytes;
 *   6. the keyboard alone generates a token and opens Advanced;
 *   7. after the page has loaded, nothing it does makes a request, and the
 *      console has no Content Security Policy line and no error;
 *   8. at 1280 and 390 px, light and dark, English and Spanish, the page
 *      does not scroll sideways. Screenshots of each go to
 *      $GENERATOR_SHOTS when it is set, for a person to look at.
 *
 * Usage: node scripts/check-generator.mjs
 */
import { mkdirSync, readFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

import { chromium } from "playwright";

import { withPreview } from "./preview-server.mjs";
import { cliArgs, renderUninstall } from "../src/lib/rsc.mjs";
import { readRelease, withVersion } from "../src/lib/release.mjs";

const SITE = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const DATA = path.join(SITE, "src/data/rsc");
// The data keeps the release as {{MIKROSCOPE_VERSION}}; the built page has the
// current VERSION there, so the files are read with it written in.
const { version } = readRelease(path.dirname(SITE));
const read = (/** @type {string} */ file) =>
	withVersion(readFileSync(file, "utf8"), version);
/** @type {import("../src/lib/rsc.mjs").Spec} */
const spec = JSON.parse(read(path.join(DATA, "spec.json")));
/** @type {{ id: string, options: Record<string, any>, cliArgs: string }[]} */
const cases = JSON.parse(read(path.join(DATA, "cases.json")));
const golden = (/** @type {string} */ id) =>
	read(path.join(DATA, "cases", `${id}.rsc`));
const SHOTS = process.env.GENERATOR_SHOTS;

/** @type {string[]} */
const problems = [];
const fail = (/** @type {string} */ msg) => problems.push(msg);
let checks = 0;
const expect = (/** @type {boolean} */ ok, /** @type {string} */ msg) => {
	checks++;
	if (!ok) fail(msg);
};

/** @typedef {import("playwright").Page} Page */

/** The text of one output of the page. */
const out = (/** @type {Page} */ page, /** @type {string} */ name) =>
	page
		.locator(`[data-gen-out="${name}"]`)
		.first()
		.evaluate((el) => el.textContent ?? "");

/** Replace a text field's value the way a person does. */
async function type(
	/** @type {Page} */ page,
	/** @type {string} */ id,
	/** @type {string} */ value,
) {
	await page.locator(`#${id}`).fill(value);
}

/** Opens Advanced, where the restart, boot, name and timeout fields are. */
async function advanced(/** @type {Page} */ page) {
	await page.locator("details.ms-gen-advanced").evaluate((d) => {
		/** @type {HTMLDetailsElement} */ (d).open = true;
	});
}

/**
 * The form actions that set up each case of the golden matrix, from its
 * option object. A case whose options the form cannot say is an error here,
 * so a new golden case needs a line here or fails the check.
 * @param {Page} page
 * @param {Record<string, any>} o
 */
async function setCase(page, o) {
	const d = spec.defaults;
	const tar = o.remoteImage === "";
	if (tar) {
		await page.locator('input[name="gen-source"][value="tar"]').check();
		// The script is the same for every architecture; the form says which tar.
		const asset =
			o.arch === "amd64" ? "amd64" : o.arch === "arm" ? "armv7" : "arm64";
		await page.locator("#gen-arch").selectOption(asset);
	} else {
		const offered = await page
			.locator("#gen-registry option")
			.evaluateAll((els) =>
				els.map((e) => /** @type {HTMLOptionElement} */ (e).value),
			);
		if (offered.includes(o.remoteImage)) {
			await page.locator("#gen-registry").selectOption(o.remoteImage);
		} else {
			await page.locator("#gen-registry").selectOption("");
			await type(page, "gen-remote-other", o.remoteImage);
		}
	}
	if (o.ephemeral)
		await page.locator('input[name="gen-storage"][value="tmpfs"]').check();
	else if (o.disk !== "") {
		await page.locator('input[name="gen-storage"][value="other"]').check();
		await type(page, "gen-disk", o.disk);
	}
	for (const [key, id] of /** @type {const} */ ([
		["name", "gen-name"],
		["veth", "gen-veth"],
		["subnet", "gen-subnet"],
		["port", "gen-port"],
		["rateHz", "gen-rate"],
		["bufferS", "gen-buffer"],
		["memoryMax", "gen-memory-max"],
		["captureMB", "gen-capture"],
		["floorHz", "gen-floor"],
	])) {
		if (o[key] !== d[key]) await type(page, id, String(o[key]));
	}
	if (o.memLimitMB !== 0)
		await type(page, "gen-mem-limit", String(o.memLimitMB));
	for (const [key, idPart] of /** @type {const} */ ([
		["ifaceList", "iface"],
		["addrList", "addr"],
	])) {
		if (o[key] === d[key]) continue;
		if (o[key] === spec.bounds.listNone) {
			await page
				.locator(`#gen-${idPart}-list`)
				.selectOption(spec.bounds.listNone);
		} else {
			await page.locator(`#gen-${idPart}-list`).selectOption("");
			await type(page, `gen-${idPart}-other`, o[key]);
		}
	}
	if (o.triggers !== "") {
		await page
			.locator('input[name="gen-triggers-mode"][value="custom"]')
			.check();
		const want = new Map(
			o.triggers.split(",").map((/** @type {string} */ t) => {
				const m = /^([a-z-]+)(?:[<>]=(.+))?$/.exec(t);
				if (!m) throw new Error(`a trigger the form cannot say: ${t}`);
				return [m[1], m[2]];
			}),
		);
		for (const g of spec.triggers) {
			const box = page.locator(`[data-trigger="${g.name}"]`);
			if (want.has(g.name)) {
				await box.check();
				if (g.op !== "")
					await page
						.locator(`[data-threshold="${g.name}"]`)
						.fill(String(want.get(g.name)));
			} else await box.uncheck();
		}
	}
	if (o.privileged !== d.privileged) {
		await page.locator("#gen-privileged").setChecked(o.privileged);
	}
	if (o.token !== "") {
		// The form only generates tokens; a golden case carries a fixed fake
		// one, so the test puts it where Generate would.
		await page.locator("#gen-token").evaluate((el, value) => {
			/** @type {HTMLInputElement} */ (el).value = value;
			el.dispatchEvent(new Event("input", { bubbles: true }));
		}, o.token);
	}
	if (o.expose) {
		await page.locator("#gen-expose").check();
		await type(page, "gen-lan-address", o.lanAddress);
	}
	if (
		o.restartMaxCount !== d.restartMaxCount ||
		o.restartInterval !== d.restartInterval ||
		o.startOnBoot !== d.startOnBoot ||
		o.containerName !== d.containerName ||
		o.extractTimeout !== d.extractTimeout
	) {
		await advanced(page);
		if (o.restartMaxCount !== d.restartMaxCount)
			await type(page, "gen-restart-max", String(o.restartMaxCount));
		if (o.restartInterval !== d.restartInterval)
			await type(page, "gen-restart-interval", o.restartInterval);
		if (o.startOnBoot !== d.startOnBoot)
			await page.locator("#gen-start-on-boot").selectOption(o.startOnBoot);
		if (o.containerName !== d.containerName)
			await type(page, "gen-container-name", o.containerName);
		if (o.extractTimeout !== d.extractTimeout)
			await type(page, "gen-extract-timeout", o.extractTimeout);
	}
	// The form renders 150 ms after the last keystroke; a change renders at once.
	await page.locator("#gen-name").dispatchEvent("change");
}

/**
 * A page of the generator with its console, errors and requests recorded.
 * @param {import("playwright").BrowserContext} context
 * @param {URL} origin
 * @param {string} lang
 */
async function open(context, origin, lang) {
	const page = await context.newPage();
	/** @type {string[]} */
	const console_ = [];
	/** @type {string[]} */
	const requests = [];
	let loaded = false;
	page.on("console", (m) => console_.push(`${m.type()}: ${m.text()}`));
	page.on("pageerror", (e) => console_.push(`pageerror: ${e.message}`));
	// The site's own framework loads some of its bundles late (Starlight's
	// ui-core on first use); that is the page's code, from its own origin.
	// What may not happen is a request for data: a fetch, an XHR, a beacon,
	// a socket, or anything to another origin.
	const DATA_TYPES = new Set([
		"fetch",
		"xhr",
		"websocket",
		"eventsource",
		"ping",
		"other",
		"document",
	]);
	page.on("request", (r) => {
		if (!loaded || r.url().startsWith("blob:") || r.url().startsWith("data:"))
			return;
		if (
			DATA_TYPES.has(r.resourceType()) ||
			new URL(r.url()).origin !== origin.origin
		) {
			requests.push(`${r.resourceType()} ${r.url()}`);
		}
	});
	const url = new URL(
		`${lang === "es" ? "/mikroscope/es" : "/mikroscope"}/install/generator/`,
		origin,
	);
	await page.goto(url.href, { waitUntil: "networkidle" });
	loaded = true;
	return { page, console: console_, requests, url: url.href };
}

await withPreview(async (origin) => {
	const browser = await chromium.launch({ args: ["--no-sandbox"] });
	try {
		// 1. Without JavaScript.
		{
			const context = await browser.newContext({ javaScriptEnabled: false });
			const page = await context.newPage();
			await page.goto(new URL("/mikroscope/install/generator/", origin).href);
			expect(
				(await out(page, "install")) === golden("pull-dockerhub"),
				"without JavaScript, the page's script is not cases/pull-dockerhub.rsc",
			);
			expect(
				!(await page.locator("[data-ms-gen]").isVisible()),
				"without JavaScript, the form shows, and nothing would make it work",
			);
			expect(
				await page.locator("[data-gen-nojs]").isVisible(),
				"without JavaScript, the note that the form needs it is missing",
			);
			await context.close();
		}

		const context = await browser.newContext({
			viewport: { width: 1280, height: 900 },
			acceptDownloads: true,
		});
		await context.grantPermissions(["clipboard-read", "clipboard-write"], {
			origin: origin.origin,
		});

		// 2. Every case, through the form.
		for (const c of cases) {
			const {
				page,
				console: log,
				requests,
			} = await open(context, origin, "en");
			await setCase(page, c.options);
			const script = await out(page, "install");
			expect(
				script === golden(c.id),
				`case ${c.id}: the form's script differs from cases/${c.id}.rsc`,
			);
			const uninstall = await out(page, "uninstall");
			expect(
				uninstall === renderUninstall(c.options, spec),
				`case ${c.id}: the form's uninstall script differs from renderUninstall()`,
			);
			const cmd = await out(page, "cli");
			// The form says the architecture for a tar and leaves it to the router
			// for a pull; a golden tar case with --arch auto has no form of its own.
			const sayable = !(
				c.options.remoteImage === "" && c.options.arch === "auto"
			);
			if (sayable)
				expect(
					cmd === c.cliArgs,
					`case ${c.id}: the form's command is ${JSON.stringify(cmd)}, Go's ${JSON.stringify(c.cliArgs)}`,
				);
			else
				expect(
					cmd === cliArgs({ ...c.options, arch: "arm64" }, spec),
					`case ${c.id}: the form's command is ${JSON.stringify(cmd)}`,
				);
			expect(
				!(await page.locator("[data-gen-errors]").isVisible()),
				`case ${c.id}: the form reports errors`,
			);
			if (c.options.token !== "") {
				expect(
					!cmd.includes(c.options.token),
					`case ${c.id}: the token is on the command line`,
				);
				expect(
					await page.locator(".ms-gen-warning").isVisible(),
					`case ${c.id}: no "treat it as a credential" note beside a script with a token`,
				);
			}
			expect(
				requests.length === 0,
				`case ${c.id}: the page made requests: ${requests.join(", ")}`,
			);
			expect(
				!log.some((l) => /Content Security Policy|pageerror|^error/i.test(l)),
				`case ${c.id}: console: ${log.join(" | ")}`,
			);
			await page.close();
		}

		// 3. An invalid field.
		{
			const { page } = await open(context, origin, "en");
			await type(page, "gen-port", "70000");
			await page.locator("#gen-port").dispatchEvent("change");
			expect(
				await page.locator("#gen-err-port").isVisible(),
				"an out-of-range port shows no error under the field",
			);
			expect(
				(await page.locator("#gen-port").getAttribute("aria-invalid")) ===
					"true",
				"an out-of-range port is not marked aria-invalid",
			);
			expect(
				!(await page.locator('[data-gen-out="install"]').isVisible()),
				"an invalid form still shows a script",
			);
			expect(
				await page.locator('[data-gen-copy="install"]').isDisabled(),
				"an invalid form leaves copy enabled",
			);
			expect(
				await page.locator('[data-gen-download="install"]').isDisabled(),
				"an invalid form leaves download enabled",
			);
			await page.locator('[data-gen-errors] a[data-gen-field="port"]').click();
			expect(
				await page
					.locator("#gen-port")
					.evaluate((el) => el === document.activeElement),
				"the error link does not focus the field",
			);
			// A field inside the closed Advanced group: the link opens it.
			await type(page, "gen-port", "9123");
			await page.locator("details.ms-gen-advanced").evaluate((d) => {
				/** @type {HTMLDetailsElement} */ (d).open = true;
			});
			await type(page, "gen-restart-interval", "10x");
			await page.locator("details.ms-gen-advanced summary").click();
			await page.locator("#gen-name").dispatchEvent("change");
			await page
				.locator('[data-gen-errors] a[data-gen-field="restartInterval"]')
				.click();
			expect(
				await page
					.locator("details.ms-gen-advanced")
					.evaluate((d) => /** @type {HTMLDetailsElement} */ (d).open),
				"the error link leaves Advanced closed",
			);
			await page.close();
		}

		// 4, 5. The token, copy and download.
		{
			const { page, console: log, url } = await open(context, origin, "en");
			await page.locator('[data-gen-token="generate"]').click();
			const token = await page.locator("#gen-token").inputValue();
			expect(
				/^[A-Za-z0-9]{32}$/.test(token),
				`the generated token is ${JSON.stringify(token.replace(/./g, "x"))}, not 32 of [A-Za-z0-9]`,
			);
			const script = await out(page, "install");
			expect(
				script.includes(`key=TOKEN value="${token}"`),
				"the generated token is not in the script",
			);
			expect(
				!(await out(page, "cli")).includes(token),
				"the token is on the command line",
			);
			expect(page.url() === url, "the page's address changed");
			expect(!page.url().includes(token), "the token is in the URL");
			const stored = await page.evaluate(() => [
				localStorage.length,
				sessionStorage.length,
				document.cookie,
			]);
			// Starlight keeps the theme in localStorage; nothing else may be there.
			const keys = await page.evaluate(() => Object.keys(localStorage));
			expect(
				keys.every((k) => k.startsWith("starlight-")) &&
					stored[1] === 0 &&
					stored[2] === "",
				`the page stored something: ${JSON.stringify(keys)}`,
			);
			expect(
				!log.some((l) => l.includes(token)),
				"the token reached the console",
			);
			await page.locator('[data-gen-copy="install"]').click();
			const clip = await page.evaluate(() => navigator.clipboard.readText());
			expect(
				clip === script,
				"Copy install script put something else on the clipboard",
			);
			const [download] = await Promise.all([
				page.waitForEvent("download"),
				page.locator('[data-gen-download="install"]').click(),
			]);
			expect(
				download.suggestedFilename() === "mikroscope-install.rsc",
				`the download is named ${download.suggestedFilename()}`,
			);
			const file = await download.path();
			expect(
				file !== null && readFileSync(file, "utf8") === script,
				"the downloaded .rsc differs from the script on the page",
			);
			const [unDownload] = await Promise.all([
				page.waitForEvent("download"),
				page.locator('[data-gen-download="uninstall"]').click(),
			]);
			expect(
				unDownload.suggestedFilename() === "mikroscope-uninstall.rsc",
				`the uninstall download is named ${unDownload.suggestedFilename()}`,
			);
			await page.locator('[data-gen-token="clear"]').click();
			expect(
				(await page.locator("#gen-token").inputValue()) === "",
				"Clear left the token",
			);
			expect(
				!(await out(page, "install")).includes("key=TOKEN"),
				"Clear left the token in the script",
			);
			expect(
				!log.some((l) => /Content Security Policy|pageerror|^error/i.test(l)),
				`console: ${log.join(" | ")}`,
			);
			await page.close();
		}

		// 6. The keyboard alone.
		{
			const { page } = await open(context, origin, "en");
			await page.locator("#gen-name").focus();
			let reached = false;
			for (let i = 0; i < 60 && !reached; i++) {
				await page.keyboard.press("Tab");
				reached = await page.evaluate(
					() =>
						document.activeElement?.getAttribute("data-gen-token") ===
						"generate",
				);
			}
			expect(reached, "Tab from the first field never reaches Generate");
			await page.keyboard.press("Enter");
			expect(
				/^[A-Za-z0-9]{32}$/.test(await page.locator("#gen-token").inputValue()),
				"Enter on Generate made no token",
			);
			let summary = false;
			for (let i = 0; i < 20 && !summary; i++) {
				await page.keyboard.press("Tab");
				summary = await page.evaluate(
					() => document.activeElement?.tagName === "SUMMARY",
				);
			}
			expect(summary, "Tab never reaches the Advanced summary");
			await page.keyboard.press("Enter");
			expect(
				await page
					.locator("details.ms-gen-advanced")
					.evaluate((d) => /** @type {HTMLDetailsElement} */ (d).open),
				"Enter on Advanced does not open it",
			);
			await page.close();
		}
		await context.close();

		// 8. Widths, themes and languages.
		if (SHOTS) mkdirSync(SHOTS, { recursive: true });
		for (const width of [1280, 390]) {
			for (const scheme of /** @type {const} */ (["light", "dark"])) {
				const ctx = await browser.newContext({
					viewport: { width, height: 900 },
					colorScheme: scheme,
				});
				for (const lang of ["en", "es"]) {
					const { page, console: log } = await open(ctx, origin, lang);
					await page.locator('input[name="gen-source"][value="tar"]').check();
					await page.locator('[data-gen-token="generate"]').click();
					await page.locator("#gen-expose").check();
					const sideways = await page.evaluate(
						() =>
							document.documentElement.scrollWidth -
							document.documentElement.clientWidth,
					);
					expect(
						sideways <= 0,
						`${lang} ${width}px ${scheme}: the page scrolls ${sideways}px sideways`,
					);
					expect(
						!log.some((l) =>
							/Content Security Policy|pageerror|^error/i.test(l),
						),
						`${lang} ${width}px ${scheme}: console: ${log.join(" | ")}`,
					);
					if (SHOTS) {
						await page.locator("[data-ms-gen]").screenshot({
							path: path.join(SHOTS, `form-${lang}-${width}-${scheme}.png`),
						});
						await page.locator('[data-gen-part="script"]').screenshot({
							path: path.join(SHOTS, `script-${lang}-${width}-${scheme}.png`),
						});
					}
					await page.close();
				}
				await ctx.close();
			}
		}
	} finally {
		await browser.close();
	}
});

if (problems.length > 0) {
	console.error(
		`test:generator: ${problems.length} of ${checks} checks failed\n`,
	);
	for (const p of problems) console.error(`  ${p}`);
	process.exit(1);
}
console.log(
	`test:generator: ${checks} checks passed: ${cases.length} cases through the form, byte for byte; errors, token, copy, download, keyboard; no request, no CSP line; 1280 and 390 px, light and dark, EN and ES`,
);
