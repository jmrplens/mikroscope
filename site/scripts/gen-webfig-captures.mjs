// The manual install through WebFig, done for real in the virtual lab, and
// photographed as it goes.
//
// `install/manual-gui` shows every screen a reader fills in to install the
// agent without the CLI. These pictures are that install: this script logs
// into the lab router's WebFig, fills in and submits each form (the install
// manifest, the veth, the address, the two list memberships, the envlist,
// the container), starts the agent, checks that it answers, takes it all out
// again through the same GUI, and checks that the router is back to what it
// was. Each form is photographed filled in, before OK, and each list after.
// So the capture run is also the GUI route's end-to-end test (S15): when a
// WebFig label or a RouterOS default changes, this script fails, and the
// page is not left describing a GUI that no longer exists.
//
// What a run checks, stopping at the first failure: every form closes on OK
// (WebFig keeps a refused one open with RouterOS's reason, which is
// printed); the container's properties, read back over ssh, equal the golden
// /container/add's, after the form and again after the terminal step and
// the start; the agent answers /healthz on the lab's loopback port and to
// the router's own /tool/fetch; `mikroscope status` names every object as
// the install's; and after the removal the router's /export and residue
// (mikroscope-lab residue: containers, envs, veths, addresses, list members,
// firewall rules, disks, files) equal the ones taken before the install.
// The runs of 2026-09-27 on the x86_64 lab (CHR, RouterOS 7.24.4, KVM) took
// 4 min 0 s with the resets before and after, and 3 min 40 s without them. On the same lab and day a
// GUI install left an /export equal to the one the golden script's /import
// left, both routes, apart from the veth's two MAC addresses, which RouterOS
// draws at random for each veth (`--keep`, then `mikroscope-lab export`).
//
// Both image routes, one after the other, from the same starting point:
//
//   pull   Remote Image, the router pulls from Docker Hub. Removed through
//          the GUI, which is what the page's Remove section shows.
//   tar    the agent's image tar uploaded through Files, the container made
//          from File. Removed with `mikroscope uninstall --yes`, which reads
//          the manifest the GUI wrote: the page promises a GUI install is
//          removable the same way as a CLI one, and this is that promise
//          being checked.
//
// Every value typed comes from the golden scripts (src/data/rsc/cases/
// pull-dockerhub.rsc and default-tar.rsc, written by cmd/gen_rsc from the
// install steps spec): the same objects, names, comments and envlist entries
// `mikroscope install` writes, so `status` and `uninstall` recognise a GUI
// install as their own. Nothing here is typed from memory.
//
// It needs the lab, and drives it through the lab's driver, never anything
// else: WebFig and the agent on the lab's loopback ports, and `mikroscope-lab
// ssh|export|residue|profile|cli` for the checks. From the repository root:
//
//   make build agent-tars lab-tool
//   make lab-up                                  # x86_64, the default
//   bin/mikroscope-lab reset
//   bin/mikroscope-lab lock node site/scripts/gen-webfig-captures.mjs
//
// `lock` holds the lab's lock for the whole run, so no other checkout drives
// the router between two steps; the script refuses to start without it.
// LAB_STATE_DIR and LAB_INSTANCE pass through to the driver as usual, and the
// WebFig and agent ports are read from `mikroscope-lab status`, so an
// instance works the same.
//
// Secrets: the admin password is read from the lab's .env (the path the
// driver's `env` verb names) and typed into the login form; it is never
// printed, and the login page is photographed before it is typed, with the
// field empty. No capture shows a password or a token: this install sets no
// TOKEN entry, and WebFig's "Show Passwords" stays off.
//
// Usage: node site/scripts/gen-webfig-captures.mjs [--route pull|tar|both]
//        [--no-write] [--keep] [--headed]
//
//   --route     one route only (both by default). A one-route run writes its
//               pictures but not captures.json, which describes the set.
//   --no-write  run the install and every check, save no picture: the GUI
//               route's test without touching src/assets.
//   --keep      stop once the first route's agent answers, and leave it
//               installed (no removal, no comparison): for reading what the
//               GUI made, e.g. its export against a golden script's.
//   --headed    show the browser.
import { execFileSync } from "node:child_process";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

import { chromium } from "playwright";
import sharp from "sharp";

const SITE = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const REPO = path.dirname(SITE);
const OUT = path.join(SITE, "src/assets/webfig");
const RSC = path.join(SITE, "src/data/rsc");
const LAB = process.env.LAB_TOOL ?? path.join(REPO, "bin/mikroscope-lab");
const TAR = path.join(REPO, "build/agent-images/mikroscope-agent-amd64.tar");

const arg = (name) =>
	process.argv.includes(name)
		? process.argv[process.argv.indexOf(name) + 1]
		: null;
const ROUTES =
	(arg("--route") ?? "both") === "both" ? ["pull", "tar"] : [arg("--route")];
const WRITE = !process.argv.includes("--no-write");
const HEADED = process.argv.includes("--headed");
const KEEP = process.argv.includes("--keep");
if (!ROUTES.every((r) => r === "pull" || r === "tar")) {
	console.error("[webfig] --route takes pull, tar or both");
	process.exit(2);
}

const log = (...a) => console.log("[webfig]", ...a);
const die = (msg) => {
	console.error(`[webfig] ${msg}`);
	process.exit(1);
};

/* ------------------------------------------------------------- the lab */

if (!process.env.LAB_LOCK_HELD) {
	die(
		"run it under the lab's lock, so nothing else drives the router between two steps:\n  bin/mikroscope-lab lock node site/scripts/gen-webfig-captures.mjs",
	);
}

/** One call of the lab's driver; its stdout. Its stderr goes to ours. */
function lab(...args) {
	return execFileSync(LAB, args, {
		encoding: "utf8",
		stdio: ["ignore", "pipe", "inherit"],
		maxBuffer: 16 << 20,
	});
}

const status = lab("status");
const webfigURL = /WebFig (http:\/\/127\.0\.0\.1:\d+)/.exec(status)?.[1];
const agentPort = /agent 127\.0\.0\.1:(\d+)/.exec(status)?.[1];
if (!webfigURL || !agentPort || !/\(running\)/.test(status)) {
	die(`the lab is not up (mikroscope-lab status):\n${status}`);
}
if (!/containers 0, veths 0/.test(status)) {
	die(
		"the lab router already has a container or a veth: start from a clean router (mikroscope-lab reset)",
	);
}
log(`WebFig ${webfigURL}, agent 127.0.0.1:${agentPort}`);

/** The lab's admin login, from the .env the driver names. Never printed. */
function credentials() {
	const where = /credentials:\s+(\S+)/.exec(lab("env"))?.[1];
	if (!where) die("mikroscope-lab env named no credentials file");
	const env = {};
	for (const line of readFileSync(where, "utf8").split("\n")) {
		const m = /^([A-Z_]+)=(.*)$/.exec(line.trim());
		if (m) env[m[1]] = m[2].replace(/^["']|["']$/g, "");
	}
	if (!env.LAB_ADMIN_PASSWORD) die(`no LAB_ADMIN_PASSWORD in ${where}`);
	return {
		user: env.LAB_ADMIN_USER || "admin",
		password: env.LAB_ADMIN_PASSWORD,
	};
}

/* ------------------------------------------------- what the install is */

/**
 * The objects a golden script creates, read from its lines: the values the
 * GUI types are the ones the steps spec writes, byte for byte.
 */
function golden(id) {
	const cases = JSON.parse(readFileSync(path.join(RSC, "cases.json"), "utf8"));
	const c = cases.find((x) => x.id === id);
	if (!c) die(`no case ${id} in src/data/rsc/cases.json`);
	const script = readFileSync(path.join(RSC, "cases", `${id}.rsc`), "utf8");
	const v = c.values;
	/** key=value and key="value" pairs of one command, in order. */
	const props = (line) =>
		Object.fromEntries(
			[...line.matchAll(/([a-z-]+)=(?:"((?:[^"\\]|\\.)*)"|([^\s;"]+))/g)].map(
				(m) => [m[1], m[2] ?? m[3]],
			),
		);
	const one = (re) => {
		const m = re.exec(script);
		if (!m) die(`${id}.rsc has no line matching ${re}`);
		return props(m[0]);
	};
	const envs = [
		...script.matchAll(
			/\/container\/envs\/add list="([^"]+)" key=([A-Z_]+) value="([^"]*)"/g,
		),
	].map((m) => ({ list: m[1], key: m[2], value: m[3] }));
	const containerLine = /\/container\/add [^\n]*?comment="[^"]+"/.exec(script);
	if (!containerLine) die(`${id}.rsc has no /container/add`);
	return {
		id,
		values: v,
		tag: v.tag,
		manifestFile: v.manifestFile,
		manifestDir: v.manifestDir,
		// The manifest as the file holds it: cases.json keeps RouterOS's
		// escaped form, the two characters \n, which the router stores as a
		// newline (spec.json manifest.rule).
		manifest: v.manifest.replaceAll("\\n", "\n"),
		veth: one(/\/interface\/veth\/add [^\n]*/),
		address: one(/\/ip\/address\/add [^\n]*/),
		ifaceMember: /\/interface\/list\/member\/add /.test(script)
			? one(/\/interface\/list\/member\/add [^\n]*/)
			: null,
		addrMember: /\/ip\/firewall\/address-list\/add /.test(script)
			? one(/\/ip\/firewall\/address-list\/add [^\n]*/)
			: null,
		envs,
		container: props(containerLine[0]),
	};
}

const INSTALLS = {
	pull: golden("pull-dockerhub"),
	tar: golden("default-tar"),
};

/* ----------------------------------------------------- the captures */

/**
 * Every picture the page shows, in the order the run takes them: the stem,
 * where it is in WebFig, and what it shows, in English and Spanish. The
 * WebFig labels stay in English in both, as WebFig shows them.
 */
const SHOTS = {
	login: {
		menu: "WebFig login",
		en: "The WebFig login page: Login admin, the Password field empty, and the Login button.",
		es: "La página de acceso de WebFig: Login admin, el campo Password vacío y el botón Login.",
	},
	"manifest-new": {
		menu: "Files › New › Text File",
		en: "Files › New File: Name mikroscope/mikroscope.manifest.txt, and in Contents the manifest, from mikroscope-manifest=1 to dir=mikroscope/mikroscope.",
		es: "Files › New File: Name mikroscope/mikroscope.manifest.txt y, en Contents, el manifiesto, desde mikroscope-manifest=1 hasta dir=mikroscope/mikroscope.",
	},
	"manifest-list": {
		menu: "Files",
		en: "Files after OK: the new mikroscope directory and mikroscope/mikroscope.manifest.txt in it.",
		es: "Files tras OK: el nuevo directorio mikroscope y dentro mikroscope/mikroscope.manifest.txt.",
	},
	"veth-new": {
		menu: "Interfaces › VETH › New",
		en: "Interfaces › New Interface of type VETH: Comment is the mikroscope tag, Name veth-mikroscope, Address 172.30.10.2/30 and Gateway 172.30.10.1.",
		es: "Interfaces › New Interface de tipo VETH: Comment es la etiqueta de mikroscope, Name veth-mikroscope, Address 172.30.10.2/30 y Gateway 172.30.10.1.",
	},
	"veth-list": {
		menu: "Interfaces › VETH",
		en: "The VETH tab after OK: veth-mikroscope, with the mikroscope tag as its comment.",
		es: "La pestaña VETH tras OK: veth-mikroscope, con la etiqueta de mikroscope como comentario.",
	},
	"address-new": {
		menu: "IP › Addresses › New",
		en: "IP › Addresses › New Address: the mikroscope tag as Comment, Address 172.30.10.1/30 and Interface veth-mikroscope.",
		es: "IP › Addresses › New Address: la etiqueta de mikroscope como Comment, Address 172.30.10.1/30 e Interface veth-mikroscope.",
	},
	"address-list": {
		menu: "IP › Addresses",
		en: "IP › Addresses after OK: 172.30.10.1/30 on veth-mikroscope beside the router's own addresses.",
		es: "IP › Addresses tras OK: 172.30.10.1/30 en veth-mikroscope junto a las direcciones propias del router.",
	},
	"iface-member-new": {
		menu: "Interfaces › Interface List › New",
		en: "Interfaces › Interface List › New Interface List Member: List LAN, Interface veth-mikroscope, and the mikroscope tag as Comment.",
		es: "Interfaces › Interface List › New Interface List Member: List LAN, Interface veth-mikroscope y la etiqueta de mikroscope como Comment.",
	},
	"iface-member-list": {
		menu: "Interfaces › Interface List",
		en: "The Interface List tab after OK: veth-mikroscope is a member of LAN.",
		es: "La pestaña Interface List tras OK: veth-mikroscope es miembro de LAN.",
	},
	"addr-list-new": {
		menu: "IP › Firewall › Address Lists › New",
		en: "IP › Firewall › Address Lists › New: List LANs, Address 172.30.10.0/30, and the mikroscope tag as Comment.",
		es: "IP › Firewall › Address Lists › New: List LANs, Address 172.30.10.0/30 y la etiqueta de mikroscope como Comment.",
	},
	"addr-list-list": {
		menu: "IP › Firewall › Address Lists",
		en: "The Address Lists tab after OK: 172.30.10.0/30 in LANs beside the router's LAN range.",
		es: "La pestaña Address Lists tras OK: 172.30.10.0/30 en LANs junto al rango LAN del router.",
	},
	"env-new": {
		menu: "Container › Envs › New",
		en: "Container › Envs › New: List mikroscope-env, Key MIKROSCOPE_TAG, and the mikroscope tag as Value.",
		es: "Container › Envs › New: List mikroscope-env, Key MIKROSCOPE_TAG y la etiqueta de mikroscope como Value.",
	},
	"env-list": {
		menu: "Container › Envs",
		en: "The Envs tab with the seven entries of mikroscope-env: MIKROSCOPE_TAG, RATE_HZ 10, BUFFER_S 60, PORT 9123, ADDR 172.30.10.2, MEM_LIMIT_MB 16 and CAPTURE_MB 4.",
		es: "La pestaña Envs con las siete entradas de mikroscope-env: MIKROSCOPE_TAG, RATE_HZ 10, BUFFER_S 60, PORT 9123, ADDR 172.30.10.2, MEM_LIMIT_MB 16 y CAPTURE_MB 4.",
	},
	"container-new-pull-1": {
		menu: "Container › New",
		en: "Container › New Container, top of the form: the mikroscope tag as Comment, Remote Image registry-1.docker.io/jmrplens/mikroscope-agent with the release tag, and Root Dir mikroscope/mikroscope.",
		es: "Container › New Container, parte superior del formulario: la etiqueta de mikroscope como Comment, Remote Image registry-1.docker.io/jmrplens/mikroscope-agent con la etiqueta de la versión y Root Dir mikroscope/mikroscope.",
	},
	"container-new-pull-2": {
		menu: "Container › New",
		en: "The same form further down: Privileged ticked, Interface veth-mikroscope and Envlists mikroscope-env.",
		es: "El mismo formulario más abajo: Privileged marcado, Interface veth-mikroscope y Envlists mikroscope-env.",
	},
	"container-new-pull-3": {
		menu: "Container › New",
		en: "The bottom of the form: Memory Max 64M, Logging and Start On Boot ticked, Restart Policy on failure, Restart Interval 10 s and Restart Max Count 5.",
		es: "La parte inferior del formulario: Memory Max 64M, Logging y Start On Boot marcados, Restart Policy on failure, Restart Interval 10 s y Restart Max Count 5.",
	},
	"container-list-pulled": {
		menu: "Container",
		en: "The Container tab once the pull is over: one row, with the tag as Comment, its root dir, veth-mikroscope and mikroscope-env, and no R flag.",
		es: "La pestaña Container al terminar la descarga: una fila, con la etiqueta como Comment, su root dir, veth-mikroscope y mikroscope-env, y sin la marca R.",
	},
	"terminal-ignore": {
		menu: "Terminal",
		en: "WebFig's Terminal: /container/set with ignore-remote-image-change=yes and restart-policy=on-failure, then two reads that print true and on-failure.",
		es: "El Terminal de WebFig: /container/set con ignore-remote-image-change=yes y restart-policy=on-failure, y dos lecturas que imprimen true y on-failure.",
	},
	"container-start": {
		menu: "Container › Start",
		en: "The Container tab with the mikroscope row selected and Start in the Actions panel.",
		es: "La pestaña Container con la fila de mikroscope seleccionada y Start en el panel Actions.",
	},
	"container-running": {
		menu: "Container",
		en: "The Container tab after Start: the mikroscope container running.",
		es: "La pestaña Container tras Start: el contenedor mikroscope en marcha.",
	},
	"log-container": {
		menu: "Log",
		en: "Log, filtered on container: the pull finishing and the container starting.",
		es: "Log, filtrado por container: la descarga terminando y el contenedor arrancando.",
	},
	"terminal-healthz": {
		menu: "Terminal",
		en: "WebFig's Terminal: /tool/fetch of http://172.30.10.2:9123/healthz, and the first row of the agent's answer, which opens with \"ok\":true.",
		es: 'El Terminal de WebFig: /tool/fetch de http://172.30.10.2:9123/healthz, y la primera fila de la respuesta del agente, que empieza por "ok":true.',
	},
	"remove-container": {
		menu: "Container › Stop, Remove",
		en: "The Container tab with the stopped mikroscope row selected, before Remove.",
		es: "La pestaña Container con la fila parada de mikroscope seleccionada, antes de Remove.",
	},
	"remove-envs": {
		menu: "Container › Envs › Remove",
		en: "The Envs tab with the seven mikroscope-env entries selected, before Remove.",
		es: "La pestaña Envs con las siete entradas de mikroscope-env seleccionadas, antes de Remove.",
	},
	"remove-files": {
		menu: "Files › Remove",
		en: "Files with mikroscope/mikroscope.manifest.txt selected, before Remove: the container's root dir went with the container, and the mikroscope directory goes once it is empty.",
		es: "Files con mikroscope/mikroscope.manifest.txt seleccionado, antes de Remove: el root dir del contenedor se fue con el contenedor, y el directorio mikroscope se borra cuando queda vacío.",
	},
	"tar-upload": {
		menu: "Files › Upload…",
		en: "Files after Upload…: mikroscope.tar at the top of the router's storage, beside the mikroscope directory that holds the manifest.",
		es: "Files tras Upload…: mikroscope.tar en la raíz del almacenamiento del router, junto al directorio mikroscope que guarda el manifiesto.",
	},
	"container-new-tar-1": {
		menu: "Container › New",
		en: "Container › New Container for the image tar: File mikroscope.tar in place of Remote Image, and Root Dir mikroscope/mikroscope.",
		es: "Container › New Container para el tar de la imagen: File mikroscope.tar en lugar de Remote Image, y Root Dir mikroscope/mikroscope.",
	},
	"container-list-extracted": {
		menu: "Container",
		en: "The Container tab once the tar is extracted: one row, with the tag as Comment, its root dir, veth-mikroscope and mikroscope-env, and no R flag.",
		es: "La pestaña Container con el tar ya extraído: una fila, con la etiqueta como Comment, su root dir, veth-mikroscope y mikroscope-env, y sin la marca R.",
	},
	"tar-remove": {
		menu: "Files › Remove",
		en: "Files with mikroscope.tar selected, before Remove: the image is extracted and the tar is no longer needed.",
		es: "Files con mikroscope.tar seleccionado, antes de Remove: la imagen ya está extraída y el tar sobra.",
	},
};

/**
 * The window a list is in: its title, tabs, toolbar, table and Actions
 * panel. A list picture is cropped at its bottom edge.
 */
const LIST = ".wndw:visible";

/** The pictures taken, in page order, for captures.json. */
const taken = [];

/* --------------------------------------------------------------- WebFig */

// 1280 × 800, the laptop WebFig is laid out for: the side menu, the form and
// its Actions panel side by side. Twice the pixels, downsampled when encoded,
// so the text stays sharp at the width the page shows it.
const VIEWPORT = { width: 1280, height: 800 };
const SCALE = 2;
const MAX_WIDTH = 1600;

const browser = await chromium.launch({
	headless: !HEADED,
	args: ["--no-sandbox"],
});
const context = await browser.newContext({
	viewport: VIEWPORT,
	deviceScaleFactor: SCALE,
	colorScheme: "light",
});
const page = await context.newPage();
page.on("pageerror", (e) => log(`page error: ${e.message}`));

/**
 * Photographs the viewport, or the part of it `clip` names, and writes it as
 * WebP. `bottom` crops at the lowest edge of that element plus a margin, for
 * a list that fills a fraction of the screen.
 */
async function shot(stem, { bottom, clip } = {}) {
	if (!SHOTS[stem]) throw new Error(`no SHOTS entry for ${stem}`);
	// No focus ring on whichever field was typed into last.
	await page.evaluate(() => document.activeElement?.blur?.());
	await page.waitForTimeout(400);
	let area = clip ?? { x: 0, y: 0, ...VIEWPORT };
	// A form and its Actions panel end about 240 px short of the screen's
	// right edge, and the rest is empty: cropping there makes the fields
	// larger at the width the page shows the picture.
	const form = page.locator(".form--item-width:visible").first();
	if (!clip && (await form.count())) {
		const box = await form.boundingBox();
		if (box)
			area = {
				...area,
				width: Math.min(VIEWPORT.width, Math.ceil(box.x + box.width + 1)),
			};
		// A short form (an env, a list member) fills the top of the screen
		// and ends at its OK bar; below that is empty too.
		const panel = await page
			.locator("form.panel:visible")
			.first()
			.boundingBox();
		if (panel && !bottom) {
			area = {
				...area,
				height: Math.min(
					VIEWPORT.height,
					Math.ceil(panel.y + panel.height + 12),
				),
			};
		}
	}
	if (bottom) {
		const box = await page.locator(bottom).first().boundingBox();
		if (box) {
			area = {
				...area,
				height: Math.min(VIEWPORT.height, Math.ceil(box.y + box.height + 24)),
			};
		}
	}
	const hidden = await hideDatedColumns();
	const png = await page.screenshot({ clip: area });
	await showDatedColumns(hidden);
	if (WRITE) {
		mkdirSync(OUT, { recursive: true });
		await sharp(png)
			.resize({ width: MAX_WIDTH, withoutEnlargement: true })
			.webp({ quality: 82 })
			.toFile(path.join(OUT, `${stem}.webp`));
	}
	if (!taken.includes(stem)) taken.push(stem);
	log(`captured ${stem}`);
}

/**
 * The columns of a list that print when the picture was taken: Files' Last
 * Modified and Log's Time. A guide's pictures carry no date (the voice
 * rule the pages follow, which a gate cannot read in an image), so each is
 * hidden, header and cells, for the picture only.
 */
const DATED_COLUMNS = ["Last Modified", "Time"];

/** Hides the dated columns of the visible lists; how many cells it hid. */
async function hideDatedColumns() {
	return page.evaluate((names) => {
		let n = 0;
		for (const th of document.querySelectorAll("table th")) {
			if (!th.offsetParent || !names.includes(th.textContent.trim())) continue;
			const i = th.cellIndex;
			for (const tr of th.closest("table").rows) {
				const cell = tr.cells[i];
				if (!cell) continue;
				cell.dataset.msDated = "1";
				cell.style.display = "none";
				n += 1;
			}
		}
		return n;
	}, DATED_COLUMNS);
}

/** Puts the dated columns back after the picture. */
async function showDatedColumns(n) {
	if (!n) return;
	await page.evaluate(() => {
		for (const cell of document.querySelectorAll("[data-ms-dated]")) {
			cell.style.display = "";
			delete cell.dataset.msDated;
		}
	});
}

/** Waits for WebFig to finish connecting after a login or a reload. */
async function waitReady() {
	await page.waitForFunction(
		() => {
			const s = document.getElementById("startup");
			return s && getComputedStyle(s).display === "none";
		},
		null,
		{ timeout: 90_000 },
	);
	await page.waitForTimeout(500);
}

async function login(stemFirst) {
	const { user, password } = credentials();
	await page.goto(`${webfigURL}/`, { waitUntil: "networkidle" });
	await page.fill("#name", user);
	// The login page is photographed here, with the password field still
	// empty: what a reader sees before typing, and no secret in the picture.
	if (stemFirst) {
		const box = await page.locator(".login section").boundingBox();
		const pad = 24;
		await shot("login", {
			clip: {
				x: Math.max(0, box.x - pad),
				y: Math.max(0, box.y - pad),
				width: Math.min(VIEWPORT.width, box.width + 2 * pad),
				height: Math.min(VIEWPORT.height, box.height + 2 * pad),
			},
		});
	}
	await page.fill("#password", password);
	await page.click('#login input[type="submit"]');
	await waitReady();
	// WebFig opens on Quick Set, whose side menu is hidden; every step here
	// is in the full menu tree, which is what Winbox shows too.
	await page.click("#id_WebFig");
	await page.waitForTimeout(800);
}

/** Opens a menu entry of the side menu: "Files", or "IP:Addresses". */
async function menu(entry) {
	const [group, item] = entry.split(":");
	await page.click("#id_WebFig");
	if (item) {
		const link = page.locator(`#nav-menu-list a[href="#${entry}"]`);
		if (!(await link.isVisible())) {
			await page
				.locator("#nav-menu-list .text", { hasText: new RegExp(`^${group}$`) })
				.first()
				.click();
		}
		await link.click();
	} else {
		await page.locator(`#nav-menu-list a[href="#${group}"]`).click();
	}
	await page.waitForTimeout(1200);
}

/** Opens a tab of the page on screen, by its hash: "Interfaces.VETH". */
async function tab(hash) {
	await page.locator(`a[href="#${hash}"]:visible`).first().click();
	await page.waitForTimeout(1200);
}

/** The New of the page on screen; `option` for a New that is a menu. */
async function newItem(option) {
	const select = page.locator("#new-button select:visible");
	if (option) {
		await page
			.locator("#new-button select")
			.first()
			.selectOption({ label: option });
	} else if (await select.count()) {
		throw new Error("this New is a menu: name the option");
	} else {
		await page.locator("a[href$='.new']:visible").first().click();
	}
	await page.waitForTimeout(1200);
	if (!/\.new/.test(page.url()))
		throw new Error(`New did not open a form (${page.url()})`);
}

/** A form row, by its label as WebFig prints it. */
function row(label) {
	return page
		.locator("div.f-item:visible")
		.filter({
			has: page.locator("label", {
				hasText: new RegExp(
					`^${label.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`,
				),
			}),
		})
		.first();
}

/** Opens a row whose value starts unset behind a + button. */
async function reveal(r) {
	const plus = r.locator(".extra button.btn-plus");
	if (await plus.count()) {
		await plus.click();
		await page.waitForTimeout(250);
	}
}

/** Types into a row's text field (or its only textarea). */
async function text(label, value) {
	const r = row(label);
	await reveal(r);
	const field = r.locator("textarea, input[type=text]").first();
	await field.fill(value);
	await field.press("Tab");
	await page.waitForTimeout(150);
}

/** Picks an option of a row's select, by its text. */
async function choose(label, option) {
	const r = row(label);
	await reveal(r);
	await r.locator("select").first().selectOption({ label: option });
	await page.waitForTimeout(150);
}

/** Ticks or clears a row's checkbox. */
async function tick(label, on) {
	const box = row(label).locator("input[type=checkbox]").first();
	if ((await box.isChecked()) !== on) await box.click();
	await page.waitForTimeout(150);
}

/**
 * Adds one value to a row that holds a list (Interface, Envlists, Address):
 * its + opens a field, a select or a text box.
 */
async function add(label, value) {
	const r = row(label);
	await r.locator("button.btn-plus").last().click();
	await page.waitForTimeout(250);
	const select = r.locator("select").last();
	if (await select.count()) {
		await select.selectOption({ label: value });
	} else {
		const field = r.locator("input[type=text]").last();
		await field.fill(value);
		await field.press("Tab");
	}
	await page.waitForTimeout(150);
}

/** Scrolls the form to put a row at the top of the panel. */
async function scrollTo(label) {
	await row(label).evaluate((el) => {
		const panel = el.closest(".form--panel-scroll");
		if (panel)
			panel.scrollTop +=
				el.getBoundingClientRect().top - panel.getBoundingClientRect().top - 8;
	});
	await page.waitForTimeout(300);
}

/**
 * OK, and the form must close: WebFig keeps a refused form open with the
 * router's reason under its buttons, which is a failure here, in its words.
 */
async function ok() {
	await page.getByRole("button", { name: "OK", exact: true }).click();
	for (let i = 0; i < 40; i += 1) {
		await page.waitForTimeout(250);
		if (!/\.new|\.\d+$/.test(page.url())) return;
	}
	// The router's refusal is printed under the buttons ("no interface (6)").
	const footer = await page
		.getByRole("button", { name: "OK", exact: true })
		.locator("xpath=ancestor::*[3]")
		.innerText()
		.catch(() => "");
	throw new Error(
		`the form did not close after OK (${page.url()}): ${footer.replace(/\s+/g, " ").trim() || "no message"}`,
	);
}

/** The visible table rows whose text contains `needle`. */
function rows(needle) {
	return page.locator("table tbody tr:visible").filter({ hasText: needle });
}

/** Ticks the select box of every row containing `needle`. */
async function select(needle, count) {
	const r = rows(needle);
	const n = await r.count();
	if (count !== undefined && n !== count) {
		throw new Error(`${n} rows hold "${needle}", not ${count}`);
	}
	for (let i = 0; i < n; i += 1) {
		await r.nth(i).locator("td input[type=checkbox]").first().check();
	}
	await page.waitForTimeout(300);
	return n;
}

/** The toolbar's Remove, for the rows selected. */
async function remove() {
	await page
		.getByRole("button", { name: "Remove", exact: true })
		.first()
		.click();
	await page.waitForTimeout(1500);
}

/** An entry of the Actions panel of the page on screen (Start, Stop). */
async function action(name) {
	await page
		.locator("button:visible", { hasText: new RegExp(`^${name}$`) })
		.last()
		.click();
	await page.waitForTimeout(800);
}

/** Polls the router until `test` holds on the output of `command`. */
async function until(what, command, test, seconds = 120) {
	for (let i = 0; i < seconds; i += 1) {
		const out = lab("ssh", command);
		if (test(out)) return out;
		await new Promise((r) => setTimeout(r, 1000));
	}
	throw new Error(`${what}: not within ${seconds} s`);
}

/* ------------------------------------------------------------ Terminal */

const term = () => page.locator("#terminal");
const termText = () => term().evaluate((e) => e.innerText);

async function terminal() {
	await page.click("#id_Terminal");
	await page.waitForTimeout(1500);
	await term().click();
	// The first session after a reset asks whether to show the licence, and
	// anything typed or pasted before the prompt `] >` answers that question
	// instead of running: a pasted script loses its first line or more.
	// Answer it, and type only at the prompt.
	for (let i = 0; i < 60; i += 1) {
		const t = (await termText()).trimEnd();
		if (/\[Y\/n\]:$/.test(t)) {
			await page.keyboard.type("n");
		} else if (/\] >$/.test(t)) {
			return;
		}
		await page.waitForTimeout(250);
	}
	throw new Error("the WebFig terminal never showed a prompt");
}

/**
 * Types one command at the prompt, waits for the next prompt, and returns
 * what the command printed. The terminal is a screen of fixed rows, not a
 * log, so the output is found after the command's echo.
 */
async function run(command) {
	await page.keyboard.type(command);
	await page.keyboard.press("Enter");
	const echo = command.slice(0, 40);
	for (let i = 0; i < 160; i += 1) {
		await page.waitForTimeout(250);
		const lines = (await termText()).trimEnd().split("\n");
		const at = lines.findLastIndex((l) => l.includes(echo));
		if (at >= 0 && at < lines.length - 1 && /\] >$/.test(lines.at(-1))) {
			return lines.slice(at + 1, -1).join("\n");
		}
	}
	throw new Error(`no prompt after ${command}`);
}

/**
 * The terminal's rows from the one holding `echo` (a command as typed) to
 * the prompt after it, photographed. The banner at the top of a session
 * names the RouterOS release, and a page shows commands, not releases, so
 * the picture is these rows only. `rows` stops it after that many rows,
 * and `width` keeps that fraction of the terminal's width, for output that
 * runs on past what the page needs to show.
 */
async function shotTerminal(stem, echo, { rows = 0, width = 1 } = {}) {
	const area = await term().evaluate(
		(e, [needle, count]) => {
			const lines = [...e.children].filter((c) => c.tagName === "SPAN");
			const first = lines.findLastIndex((l) => l.textContent.includes(needle));
			if (first < 0) return null;
			let last = lines.length - 1;
			while (last > first && !/\S/.test(lines[last].textContent)) last -= 1;
			if (count) last = Math.min(last, first + count - 1);
			// A row's box is taller than its glyphs and overlaps the next
			// one: the picture ends where the next row begins.
			const next = lines[last + 1];
			return {
				top: lines[first].getBoundingClientRect().top,
				bottom: next
					? next.getBoundingClientRect().top
					: lines[last].getBoundingClientRect().bottom,
			};
		},
		[echo, rows],
	);
	if (!area) throw new Error(`the terminal shows no "${echo}"`);
	const box = await term().boundingBox();
	await shot(stem, {
		clip: {
			x: box.x,
			y: Math.max(0, area.top - 6),
			width: Math.round(box.width * width),
			height: Math.floor(area.bottom - area.top + 6),
		},
	});
}

/* ----------------------------------------------------------- the steps */

async function writeManifest(g, capture) {
	await menu("Files");
	await newItem("Text File");
	await text("Name", g.manifestFile);
	await text("Contents", g.manifest);
	if (capture) await shot("manifest-new");
	await ok();
	if (capture) await shot("manifest-list", { bottom: LIST });
}

async function createVeth(g, capture) {
	await menu("Interfaces");
	await tab("Interfaces.VETH");
	await page.locator("a[href='#Interfaces.VETH.new']:visible").first().click();
	await page.waitForTimeout(1200);
	await text("Comment", g.veth.comment);
	await text("Name", g.veth.name);
	await add("Address", g.veth.address);
	await text("Gateway", g.veth.gateway);
	if (capture) await shot("veth-new");
	await ok();
	if (capture) await shot("veth-list", { bottom: LIST });
}

async function createAddress(g, capture) {
	await menu("IP:Addresses");
	await newItem();
	await text("Comment", g.address.comment);
	await text("Address", g.address.address);
	await choose("Interface", g.address.interface);
	if (capture) await shot("address-new");
	await ok();
	if (capture) await shot("address-list", { bottom: LIST });
}

async function createIfaceMember(g, capture) {
	if (!g.ifaceMember) return;
	await menu("Interfaces");
	await tab("Interfaces.Interface_List");
	await page
		.locator("a[href='#Interfaces.Interface_List.new']:visible")
		.first()
		.click();
	await page.waitForTimeout(1200);
	await text("Comment", g.ifaceMember.comment);
	await choose("List", g.ifaceMember.list);
	await choose("Interface", g.ifaceMember.interface);
	if (capture) await shot("iface-member-new");
	await ok();
	if (capture) await shot("iface-member-list", { bottom: LIST });
}

async function createAddrMember(g, capture) {
	if (!g.addrMember) return;
	await menu("IP:Firewall");
	await tab("IP:Firewall.Address_Lists");
	await newItem();
	await text("Comment", g.addrMember.comment);
	await text("List", g.addrMember.list);
	await text("Address", g.addrMember.address);
	if (capture) await shot("addr-list-new");
	await ok();
	if (capture) await shot("addr-list-list", { bottom: LIST });
}

async function createEnvs(g, capture) {
	await menu("Container");
	await tab("Container.Envs");
	for (const [i, e] of g.envs.entries()) {
		await newItem();
		await text("List", e.list);
		await text("Key", e.key);
		await text("Value", e.value);
		if (capture && i === 0) await shot("env-new");
		await ok();
	}
	if (capture) await shot("env-list", { bottom: LIST });
}

/** WebFig's Restart Interval is a duration field that shows hh:mm:ss. */
const hms = (d) => {
	const m = /^(\d+)([smh])$/.exec(d);
	if (!m) throw new Error(`restart-interval ${d}`);
	const s = Number(m[1]) * { s: 1, m: 60, h: 3600 }[m[2]];
	const p = (n) => String(n).padStart(2, "0");
	return `${p(Math.floor(s / 3600))}:${p(Math.floor((s % 3600) / 60))}:${p(s % 60)}`;
};

const RESTART_POLICY = {
	"on-failure": "on failure (non 0 exit code)",
	always: "always",
	no: "no",
};

/**
 * The tar route's upload: Files › Upload…, with the manifest's directory
 * already there, as the page orders it (the tar is uploaded in its
 * "Create the container" step).
 */
async function uploadTar(g) {
	await menu("Files");
	const [chooser] = await Promise.all([
		page.waitForEvent("filechooser"),
		page
			.locator("#toolbar-item-10, button:visible:has-text('Upload...')")
			.first()
			.click(),
	]);
	// The asset is mikroscope-agent-<arch>.tar; the router's copy is
	// named as the manifest and the container's File name it.
	await chooser.setFiles({
		name: g.container.file,
		mimeType: "application/x-tar",
		buffer: readFileSync(TAR),
	});
	await until(
		"the upload listed",
		`:put [:len [/file/find name="${g.container.file}"]]`,
		(out) => out.trim() === "1",
		60,
	);
	await menu("Files");
	await shot("tar-upload", { bottom: LIST });
}

async function createContainer(g, route, capture) {
	const c = g.container;
	await menu("Container");
	await tab("Container.Container");
	await newItem();
	await text("Comment", c.comment);
	if (route === "pull") await text("Remote Image", c["remote-image"]);
	else await text("File", c.file);
	await text("Root Dir", c["root-dir"]);
	if (capture) await shot(`container-new-${route}-1`);
	await tick("Privileged", c.privileged === "yes");
	await add("Interface", c.interface);
	await add("Envlists", c.envlist);
	if (capture && route === "pull") {
		await scrollTo("Privileged");
		await shot("container-new-pull-2");
	}
	await text("Memory Max", c["memory-max"]);
	await tick("Logging", c.logging === "yes");
	await tick("Start On Boot", c["start-on-boot"] === "yes");
	await choose("Restart Policy", RESTART_POLICY[c["restart-policy"]]);
	await text("Restart Interval", hms(c["restart-interval"]));
	await text("Restart Max Count", c["restart-max-count"]);
	if (capture && route === "pull") {
		await scrollTo("Memory Max");
		await shot("container-new-pull-3");
	}
	await ok();
	await checkContainer(g, route);
}

/**
 * The container the form made, read back from the router, against the
 * golden script's /container/add: every property the form set. The one the
 * form cannot set, ignore-remote-image-change, is the terminal's.
 */
async function checkContainer(g, route, { afterTerminal = false } = {}) {
	const c = g.container;
	const want = {
		...(afterTerminal
			? { "ignore-remote-image-change": c["ignore-remote-image-change"] }
			: {}),
		comment: c.comment,
		"root-dir": `/${c["root-dir"]}`,
		interface: c.interface,
		envlists: c.envlist,
		logging: c.logging,
		"start-on-boot": c["start-on-boot"],
		"restart-policy": c["restart-policy"],
		"restart-max-count": c["restart-max-count"],
		"restart-interval": c["restart-interval"],
		privileged: c.privileged,
		...(route === "pull"
			? { "remote-image": c["remote-image"] }
			: { file: c.file }),
	};
	const find = `[/container/find comment="${g.tag}"]`;
	const got = {};
	for (const key of Object.keys(want)) {
		got[key] = lab("ssh", `:put [/container/get ${find} ${key}]`).trim();
	}
	// memory-max prints in MiB; the golden writes 64M.
	got["memory-max"] = lab(
		"ssh",
		`:put [/container/get ${find} memory-max]`,
	).trim();
	const bytes = (m) => {
		const x = /^(\d+(?:\.\d+)?)\s*([KMG]?)(?:i?B)?$/i.exec(m);
		return x
			? Number(x[1]) *
					{ "": 1, K: 1024, M: 1024 ** 2, G: 1024 ** 3 }[x[2].toUpperCase()]
			: NaN;
	};
	// Durations print as hh:mm:ss; the golden writes 10s.
	const seconds = (d) => {
		const hms = /^(\d+):(\d\d):(\d\d)$/.exec(d);
		if (hms)
			return Number(hms[1]) * 3600 + Number(hms[2]) * 60 + Number(hms[3]);
		const unit = /^(\d+)([smh])$/.exec(d);
		return unit ? Number(unit[1]) * { s: 1, m: 60, h: 3600 }[unit[2]] : NaN;
	};
	const bad = Object.entries(want).filter(([k, v]) => {
		const have = got[k];
		if (k === "restart-interval") return seconds(have) !== seconds(v);
		if (v === "yes") return !/^(yes|true)$/.test(have);
		if (v === "no") return !/^(no|false)$/.test(have);
		return have !== v;
	});
	if (bytes(got["memory-max"]) !== bytes(c["memory-max"]))
		bad.push(["memory-max", c["memory-max"]]);
	if (bad.length) {
		throw new Error(
			`the container WebFig made differs from the golden script:\n${bad.map(([k, v]) => `  ${k}: want ${v}, router has ${got[k]}`).join("\n")}`,
		);
	}
	log("the container's properties equal the golden script's");
}

const tagFind = (g) => `[find comment="${g.tag}"]`;

async function waitStopped(g, route, capture) {
	// Extraction is over when the container carries the stopped flag (the
	// steps spec's wait); a pull shows downloading/extracting until then.
	await until(
		"the image extracted",
		`:put [:len [/container/find comment="${g.tag}" stopped]]`,
		(out) => out.trim() === "1",
		180,
	);
	await menu("Container");
	await tab("Container.Container");
	if (capture) {
		await shot(
			route === "pull" ? "container-list-pulled" : "container-list-extracted",
			{
				bottom: LIST,
			},
		);
	}
}

async function ignoreRemoteImageChange(g, capture) {
	// WebFig's container forms have no field for it: neither New nor the
	// edit form shows ignore-remote-image-change, whether File or Remote
	// Image is set or not (virtual lab, CHR x86_64, RouterOS 7.24.4,
	// 2026-09-27). The terminal sets it, and names restart-policy in the same
	// command: in the same lab on the same day, a /container/set that left
	// restart-policy out (of ignore-remote-image-change, of comment, of
	// logging) put it from on-failure back to `always`, and changed nothing
	// else /container/print detail shows, while a set that named it kept it,
	// and so did an Apply or OK in WebFig's edit form, which sends it. The
	// CLI never runs /container/set, so its installs are not affected.
	const policy = g.container["restart-policy"];
	await terminal();
	await run(
		`/container/set ${tagFind(g)} ignore-remote-image-change=yes restart-policy=${policy}`,
	);
	const ignore = await run(
		`:put [/container/get ${tagFind(g)} ignore-remote-image-change]`,
	);
	const kept = await run(`:put [/container/get ${tagFind(g)} restart-policy]`);
	if (!/^(yes|true)$/.test(ignore.trim()) || kept.trim() !== policy) {
		throw new Error(
			`after the terminal's /container/set: ignore-remote-image-change ${ignore.trim()}, restart-policy ${kept.trim()}`,
		);
	}
	if (capture) await shotTerminal("terminal-ignore", "/container/set");
}

async function removeTar(g, capture) {
	await menu("Files");
	await select(g.container.file, 1);
	if (capture) await shot("tar-remove", { bottom: LIST });
	await remove();
	await until(
		"the tar removed",
		`:put [:len [/file/find name="${g.container.file}"]]`,
		(out) => out.trim() === "0",
		30,
	);
}

async function start(g, capture) {
	await menu("Container");
	await tab("Container.Container");
	await select(g.container.comment, 1);
	if (capture) await shot("container-start", { bottom: LIST });
	await action("Start");
	await until(
		"the container running",
		`:put [:len [/container/find comment="${g.tag}" running]]`,
		(out) => out.trim() === "1",
		60,
	);
	await menu("Container");
	await tab("Container.Container");
	if (capture) await shot("container-running", { bottom: LIST });
}

/** GET /healthz through the lab's loopback port, until it answers 200. */
async function healthz() {
	const url = `http://127.0.0.1:${agentPort}/healthz`;
	for (let i = 0; i < 60; i += 1) {
		try {
			const r = await fetch(url, { signal: AbortSignal.timeout(2000) });
			if (r.status === 200) {
				log(`GET ${url}: 200`);
				return;
			}
		} catch {
			// not answering yet
		}
		await new Promise((r) => setTimeout(r, 1000));
	}
	throw new Error(`${url} never answered 200`);
}

async function verify(g, capture) {
	await healthz();
	if (capture) {
		await menu("Log");
		// Filter › Topics contains container: the pull, the extraction and
		// the start, without the lab's own ssh logins around them.
		await page.locator("label[for=table-filter]").click();
		const f = page.locator("#filters .filter-row").first();
		await f.locator("select.attribute").selectOption({ label: "Topics" });
		await f.locator("select.option").selectOption("contains");
		const value = f.locator("input.fixed-value:visible");
		if (await value.count()) await value.fill("container");
		else
			await f
				.locator("select.fixed-value")
				.selectOption({ label: "container" });
		await page.keyboard.press("Enter");
		await page.waitForTimeout(1500);
		await shot("log-container", { bottom: LIST });
	}
	await terminal();
	const url = `http://${g.values.containerIP}:${g.values.port}/healthz`;
	const out = await run(
		`:put ([/tool/fetch url="${url}" output=user as-value]->"data")`,
	);
	if (!/"ok":true/.test(out))
		throw new Error(`the router's fetch of ${url} printed:\n${out}`);
	// The answer runs on to the agent's version and build date, which a
	// picture on the page would carry into every later release: the
	// picture stops at the first row of the answer, short of them.
	if (capture)
		await shotTerminal("terminal-healthz", ":put ([/tool/fetch", {
			rows: 2,
			width: 0.62,
		});
}

async function removeThroughGUI(g, capture) {
	await menu("Container");
	await tab("Container.Container");
	await select(g.container.comment, 1);
	await action("Stop");
	await until(
		"the container stopped",
		`:put [:len [/container/find comment="${g.tag}" stopped]]`,
		(out) => out.trim() === "1",
		60,
	);
	await menu("Container");
	await tab("Container.Container");
	await select(g.container.comment, 1);
	if (capture) await shot("remove-container", { bottom: LIST });
	await remove();
	await until(
		"the container removed",
		`:put [:len [/container/find comment="${g.tag}"]]`,
		(out) => out.trim() === "0",
		60,
	);

	await tab("Container.Envs");
	await select(g.container.envlist, g.envs.length);
	if (capture) await shot("remove-envs", { bottom: LIST });
	await remove();

	if (g.addrMember) {
		await menu("IP:Firewall");
		await tab("IP:Firewall.Address_Lists");
		await select(g.addrMember.address, 1);
		await remove();
	}
	if (g.ifaceMember) {
		await menu("Interfaces");
		await tab("Interfaces.Interface_List");
		await select(g.ifaceMember.interface, 1);
		await remove();
	}
	await menu("IP:Addresses");
	await select(g.address.address, 1);
	await remove();
	await menu("Interfaces");
	await tab("Interfaces.VETH");
	await select(g.veth.name, 1);
	await remove();

	// RouterOS removes a container's root dir with the container; the
	// directory left holds the manifest, which goes last.
	await until(
		"the root dir removed",
		`:put [:len [/file/find name="${g.container["root-dir"]}"]]`,
		(out) => out.trim() === "0",
		30,
	);
	await menu("Files");
	await select(g.manifestFile, 1);
	if (capture) await shot("remove-files", { bottom: LIST });
	await remove();
	// The directory goes once it is empty: a mikroscope/ that holds
	// anything else was not made by this install.
	await until(
		"the manifest removed",
		`:put [:len [/file/find name~"^${g.manifestDir}/"]]`,
		(out) => out.trim() === "0",
		30,
	);
	await menu("Files");
	await page
		.locator("table tbody tr:visible")
		.filter({
			has: page.locator("td", { hasText: new RegExp(`^${g.manifestDir}$`) }),
		})
		.locator("td input[type=checkbox]")
		.first()
		.check();
	await remove();
}

/* ------------------------------------------------------------- the run */

// The lists the two membership steps join: `LAN` and `LANs`, as a router
// with MikroTik's default configuration has them. The clean lab has none.
lab("profile", "doctor-lists");
// The header shows the router's identity: a neutral one, not the lab's name.
lab("ssh", "/system/identity/set name=router");
const baseline = lab("export");
const residue = lab("residue");
log(
	"baseline taken: the export and the residue after the doctor-lists profile",
);

function assertClean(when) {
	const now = lab("export");
	if (now !== baseline) {
		throw new Error(
			`${when}: the export differs from the baseline\n--- before\n${baseline}\n--- after\n${now}`,
		);
	}
	const left = lab("residue");
	if (left !== residue) {
		throw new Error(
			`${when}: the residue differs\n--- before\n${residue}\n--- after\n${left}`,
		);
	}
	log(`${when}: the export and the residue equal the baseline`);
}

let failed = null;
try {
	await login(true);
	for (const route of ROUTES) {
		const g = INSTALLS[route];
		const capture = route === "pull";
		log(
			`route ${route}: ${route === "pull" ? g.container["remote-image"] : g.container.file}`,
		);
		await writeManifest(g, capture);
		await createVeth(g, capture);
		await createAddress(g, capture);
		await createIfaceMember(g, capture);
		await createAddrMember(g, capture);
		await createEnvs(g, capture);
		if (route === "tar") await uploadTar(g);
		await createContainer(g, route, route === "pull" || route === "tar");
		await waitStopped(g, route, true);
		await ignoreRemoteImageChange(g, capture);
		if (route === "tar") await removeTar(g, true);
		await start(g, capture);
		await verify(g, capture);
		// Still what the form set, after the terminal step and the start.
		await checkContainer(g, route, { afterTerminal: true });
		// What the CLI makes of a GUI install: status names every object.
		const st = lab("cli", "status");
		log(`mikroscope status:\n${st}`);
		if (KEEP) {
			log(`--keep: the ${route} install stays on the router`);
			break;
		}
		if (route === "pull") {
			await removeThroughGUI(g, capture);
		} else {
			log(lab("cli", "uninstall", "--yes"));
		}
		assertClean(`after the ${route} route`);
	}
} catch (error) {
	failed = error;
	await page
		.screenshot({ path: path.join(REPO, "build", "webfig-failure.png") })
		.catch(() => {});
} finally {
	await browser.close();
}
if (failed)
	die(
		`${failed.stack ?? failed}\n(the screen at the failure: build/webfig-failure.png)`,
	);

if (WRITE && ROUTES.length === 2 && !KEEP) {
	const order = Object.keys(SHOTS).filter((s) => taken.includes(s));
	writeFileSync(
		path.join(OUT, "captures.json"),
		`${JSON.stringify(
			Object.fromEntries(
				order.map((s) => [
					s,
					{
						file: `${s}.webp`,
						menu: SHOTS[s].menu,
						alt: { en: SHOTS[s].en, es: SHOTS[s].es },
					},
				]),
			),
			null,
			2,
		)}\n`,
	);
	log(
		`${order.length} captures and captures.json in ${path.relative(REPO, OUT)}`,
	);
}
if (!KEEP) {
	log(
		`done: ${ROUTES.join(" and ")} installed through WebFig, answered /healthz, and left the router as they found it`,
	);
}
