// The documentation's diagrams, generated rather than drawn.
//
// A diagram in a bilingual site is four files — two languages by two layouts,
// because a row of boxes that reads well on a laptop is unreadable at 390 px —
// and four files drawn by hand drift apart the first time a label changes. So
// the labels live in one object here, the geometry is computed, and
// `pnpm run figures:check` fails when the committed SVGs disagree with this
// script.
//
// The output is inline SVG, not an <img>: inline, it inherits the site's own
// colour tokens, so a figure follows the theme toggle instead of guessing from
// prefers-color-scheme. Every fill and stroke below is a var(--ms-…) with a
// literal fallback, which is what keeps the file legible on its own.
//
// The playbooks' charts are the other kind: real rows from the reference
// InfluxDB store, committed in src/data/figures/data/ with the SQL that read
// them. See "The charts" below.
//
// Usage: node scripts/gen-figures.mjs           # write src/data/figures/
//        node scripts/gen-figures.mjs --check   # fail if they are stale
//        node scripts/gen-figures.mjs --fetch   # re-read the charts' rows
//                                               # from the store, then write
import { mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

import { formatNumber } from "../src/lib/format.ts";

const SITE = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const OUT = path.join(SITE, "src/data/figures");
const DATA = path.join(OUT, "data");

/** The one diagram, in both languages. Geometry is below; this is the words. */
const dataPath = {
	en: {
		title: "Where the data comes from and where it goes",
		desc: "The router runs the agent in a container that reads the shared kernel and serves it over a veth. The collector on your machine pulls that, merges the RouterOS API tier into it, derives, and writes to every sink you named.",
		router: "The router",
		kernel: "Shared kernel",
		kernelDetail: "/proc · /sys · /dev/kmsg · PMU",
		agent: "mikroscope-agent",
		agentDetail: "1–100 Hz · 300 s ring · HTTP",
		api: "RouterOS API",
		apiDetail: "1 Hz, what the kernel cannot see",
		host: "Your machine",
		collector: "mikroscope forward",
		collectorDetail: "pull · merge · derive · fan out",
		sinks: "Sinks",
		sinkList: [
			"InfluxDB 3",
			"Prometheus",
			"file · SQL",
			"Loki · OTLP",
			"Graphite · Elastic",
			"Telegraf · stdout",
		],
		veth: "veth /30",
		pull: "pull every 500 ms",
	},
	es: {
		title: "De dónde vienen los datos y a dónde van",
		desc: "El router ejecuta el agente en un contenedor que lee el kernel compartido y lo sirve por un veth. El colector de tu máquina tira de ahí, le une la capa de la API de RouterOS, deriva y escribe en cada destino que hayas nombrado.",
		router: "El router",
		kernel: "Kernel compartido",
		kernelDetail: "/proc · /sys · /dev/kmsg · PMU",
		agent: "mikroscope-agent",
		agentDetail: "1–100 Hz · anillo 300 s · HTTP",
		api: "API de RouterOS",
		apiDetail: "1 Hz, lo que el kernel no ve",
		host: "Tu máquina",
		collector: "mikroscope forward",
		collectorDetail: "tirar · unir · derivar · repartir",
		sinks: "Destinos",
		sinkList: [
			"InfluxDB 3",
			"Prometheus",
			"fichero · SQL",
			"Loki · OTLP",
			"Graphite · Elastic",
			"Telegraf · stdout",
		],
		veth: "veth /30",
		pull: "cada 500 ms",
	},
};

const C = {
	surface: "var(--ms-surface, #f5f7f8)",
	raised: "var(--ms-surface-raised, #ffffff)",
	border: "var(--ms-border, #d4dade)",
	strong: "var(--ms-border-strong, #aab4ba)",
	heading: "var(--ms-heading, #10171b)",
	body: "var(--ms-body, #2d383f)",
	muted: "var(--ms-muted, #5a666d)",
	accent: "var(--ms-mark-quiet, #c2740a)",
	// The charts' series: one blue and one orange, and the second is also
	// dashed, so a reader who cannot tell the two hues apart still can.
	received: "var(--ms-info, #17558f)",
	sent: "var(--ms-mark-quiet, #b45309)",
	fault: "var(--ms-status-bad, #b3352f)",
	faultSoft: "var(--ms-status-bad-soft, #fbe7e6)",
};
const FONT = "ui-sans-serif,system-ui,sans-serif";
const MONO = "ui-monospace,SFMono-Regular,Menlo,monospace";

const esc = (s) =>
	String(s)
		.replaceAll("&", "&amp;")
		.replaceAll("<", "&lt;")
		.replaceAll(">", "&gt;");

/** A titled box with an optional second line. */
function box({ x, y, w, h, title, detail, mono = false, dashed = false }) {
	const parts = [
		`<rect x="${x}" y="${y}" width="${w}" height="${h}" rx="8" fill="${C.raised}" stroke="${dashed ? C.strong : C.border}"${dashed ? ' stroke-dasharray="4 3"' : ""}/>`,
		`<text x="${x + w / 2}" y="${y + (detail ? h / 2 - 4 : h / 2 + 5)}" text-anchor="middle" font-family="${mono ? MONO : FONT}" font-size="14" font-weight="600" fill="${C.heading}">${esc(title)}</text>`,
	];
	if (detail) {
		parts.push(
			`<text x="${x + w / 2}" y="${y + h / 2 + 15}" text-anchor="middle" font-family="${FONT}" font-size="11.5" fill="${C.muted}">${esc(detail)}</text>`,
		);
	}
	return parts.join("\n\t");
}

/** A straight arrow with a label above it. */
function arrow({ x1, y1, x2, y2, label, marker }) {
	const mid = { x: (x1 + x2) / 2, y: (y1 + y2) / 2 };
	const horizontal = y1 === y2;
	return [
		`<line x1="${x1}" y1="${y1}" x2="${x2}" y2="${y2}" stroke="${C.strong}" stroke-width="1.5" marker-end="url(#${marker})"/>`,
		label
			? `<text x="${horizontal ? mid.x : mid.x + 8}" y="${horizontal ? mid.y - 7 : mid.y}" text-anchor="${horizontal ? "middle" : "start"}" font-family="${FONT}" font-size="11" fill="${C.muted}">${esc(label)}</text>`
			: "",
	]
		.filter(Boolean)
		.join("\n\t");
}

/** A group label over a dashed container. */
function group({ x, y, w, h, label }) {
	return [
		`<rect x="${x}" y="${y}" width="${w}" height="${h}" rx="12" fill="${C.surface}" stroke="${C.border}" stroke-dasharray="6 4"/>`,
		`<text x="${x + 14}" y="${y + 20}" font-family="${FONT}" font-size="12" font-weight="600" fill="${C.body}" letter-spacing="0.03em">${esc(label).toUpperCase()}</text>`,
	].join("\n\t");
}

// Both layouts of a figure are in the page at once — CSS picks one — so every
// id has to be unique across them, or the document has two `#t` and the arrow
// marker of one resolves to the other's.
function svg({ id, width, height, title, desc, body }) {
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${width} ${height}" width="100%" role="img" aria-labelledby="${id}-t ${id}-d">
	<title id="${id}-t">${esc(title)}</title>
	<desc id="${id}-d">${esc(desc)}</desc>
	<defs>
		<marker id="${id}-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
			<path d="M 0 0 L 10 5 L 0 10 z" fill="${C.strong}"/>
		</marker>
	</defs>
	${body}
</svg>
`;
}

/** Laptop layout: the router on the left, the collector and sinks to the right. */
function wide(t, id) {
	const parts = [];
	const marker = `${id}-arrow`;
	parts.push(group({ x: 8, y: 8, w: 300, h: 250, label: t.router }));
	parts.push(
		box({
			x: 28,
			y: 40,
			w: 260,
			h: 56,
			title: t.kernel,
			detail: t.kernelDetail,
		}),
	);
	parts.push(arrow({ x1: 158, y1: 96, x2: 158, y2: 124, marker }));
	parts.push(
		box({
			x: 28,
			y: 126,
			w: 260,
			h: 56,
			title: t.agent,
			detail: t.agentDetail,
			mono: true,
		}),
	);
	parts.push(
		box({
			x: 28,
			y: 192,
			w: 260,
			h: 50,
			title: t.api,
			detail: t.apiDetail,
			dashed: true,
		}),
	);

	parts.push(group({ x: 340, y: 8, w: 280, h: 250, label: t.host }));
	parts.push(
		box({
			x: 360,
			y: 96,
			w: 240,
			h: 66,
			title: t.collector,
			detail: t.collectorDetail,
			mono: true,
		}),
	);
	parts.push(
		arrow({ x1: 288, y1: 150, x2: 356, y2: 124, label: t.veth, marker }),
	);
	parts.push(arrow({ x1: 288, y1: 217, x2: 356, y2: 148, marker }));

	parts.push(group({ x: 652, y: 8, w: 230, h: 250, label: t.sinks }));
	t.sinkList.forEach((name, i) => {
		const y = 40 + i * 34;
		parts.push(
			`<rect x="672" y="${y}" width="190" height="26" rx="6" fill="${C.raised}" stroke="${C.border}"/>`,
			`<text x="767" y="${y + 17}" text-anchor="middle" font-family="${FONT}" font-size="12" fill="${C.body}">${esc(name)}</text>`,
		);
	});
	parts.push(
		arrow({ x1: 600, y1: 129, x2: 670, y2: 129, label: t.pull, marker }),
	);
	return svg({
		id,
		width: 890,
		height: 266,
		title: t.title,
		desc: t.desc,
		body: parts.join("\n\t"),
	});
}

/** Phone layout: the same path, stacked. */
function narrow(t, id) {
	const parts = [];
	const marker = `${id}-arrow`;
	const w = 300;
	parts.push(group({ x: 8, y: 8, w, h: 204, label: t.router }));
	parts.push(
		box({
			x: 24,
			y: 36,
			w: w - 32,
			h: 50,
			title: t.kernel,
			detail: t.kernelDetail,
		}),
	);
	parts.push(arrow({ x1: 158, y1: 86, x2: 158, y2: 106, marker }));
	parts.push(
		box({
			x: 24,
			y: 108,
			w: w - 32,
			h: 50,
			title: t.agent,
			detail: t.agentDetail,
			mono: true,
		}),
	);
	parts.push(
		box({ x: 24, y: 164, w: w - 32, h: 32, title: t.api, dashed: true }),
	);
	parts.push(
		arrow({ x1: 158, y1: 204, x2: 158, y2: 232, label: t.veth, marker }),
	);

	parts.push(
		box({
			x: 24,
			y: 240,
			w: w - 32,
			h: 52,
			title: t.collector,
			detail: t.collectorDetail,
			mono: true,
		}),
	);
	parts.push(arrow({ x1: 158, y1: 292, x2: 158, y2: 316, marker }));

	parts.push(group({ x: 8, y: 318, w, h: 128, label: t.sinks }));
	t.sinkList.forEach((name, i) => {
		const col = i % 2;
		const row = Math.floor(i / 2);
		const x = 22 + col * 140;
		const y = 346 + row * 30;
		parts.push(
			`<rect x="${x}" y="${y}" width="132" height="24" rx="6" fill="${C.raised}" stroke="${C.border}"/>`,
			`<text x="${x + 66}" y="${y + 16}" text-anchor="middle" font-family="${FONT}" font-size="11" fill="${C.body}">${esc(name)}</text>`,
		);
	});
	return svg({
		id,
		width: 316,
		height: 454,
		title: t.title,
		desc: t.desc,
		body: parts.join("\n\t"),
	});
}

/* ------------------------------------------------------------- the charts */

// The playbooks' charts are drawn from rows read out of the reference
// InfluxDB store, the one `mikroscope forward` writes to from the owner's
// RB5009. Each dataset in src/data/figures/data/ carries the SQL that read it,
// the window, the day it was read and where the RouterOS version comes from,
// so a number in a chart can be traced to a query without trusting this file.
//
// Drawing never touches the network. `--fetch` is the one mode that does: run
// by hand with MIKROSCOPE_INFLUX_URL and MIKROSCOPE_INFLUX_TOKEN (and
// MIKROSCOPE_INFLUX_DB, unless the URL is a write URL carrying `db=`) in the
// environment, as .env.example names them, it runs the queries below and
// rewrites the datasets before drawing. `--check` fails when a dataset's
// recorded SQL is not the SQL written here, so a query edited without a fetch
// cannot pass. A store with a shorter retention than these windows will return
// fewer rows on a later fetch, and the assertions in each chart then fail
// rather than draw a different story.
//
// The store holds nothing before 2026-09-19 11:13 UTC (read 2026-09-24). The
// playbooks' readings of 2026-09-12 predate it, which is why those pages have
// tables and no chart.

const EPOCH = "2026-09-19T00:00:00Z";
const HOUR = 3_600_000;
const inWindow = (w) => `time >= '${w.from}' AND time < '${w.to}'`;
const binOf = (every) =>
	`date_bin(INTERVAL '${every}', time, TIMESTAMP '${EPOCH}')`;

// An upgrade needs a reboot, so a window in which the router's uptime grows
// exactly as fast as the clock is a window on one RouterOS version.
const uptimeSql = (w) =>
	`SELECT min(time) AS first, max(time) AS last, min(uptime_s) AS up_first, max(uptime_s) AS up_last FROM mikroscope_api_system WHERE ${inWindow(w)}`;
const deviceSql = (w) =>
	`SELECT DISTINCT board, kernel, cores FROM mikroscope_device WHERE ${inWindow(w)}`;

// The one fact the store does not record. Read on the router with a single
// read-only `/system/resource/print` on 2026-09-24: version 7.24.4 (stable),
// uptime 5d15h49m12s. Every window below lies inside that uptime, which the
// uptime query of each dataset re-checks against the store's own series.
const ROUTEROS = {
	version: "7.24.4",
	source:
		"/system/resource/print on the router, 2026-09-24: version 7.24.4 (stable), uptime 5d15h49m12s; the uptime query shows no reboot inside the window",
};

const FIVE_DAYS = { from: "2026-09-19T11:00:00Z", to: "2026-09-24T11:00:00Z" };
const OVERFLOW_DAY = {
	from: "2026-09-19T11:10:00Z",
	to: "2026-09-20T00:00:00Z",
};

const DATASETS = {
	"loop-2026-09-19": {
		window: FIVE_DAYS,
		every: "1 hour",
		queries: {
			records: `SELECT ${binOf("1 hour")} AS bin, port, sum(count) AS records FROM mikroscope_kmsg WHERE kind = 'own-address' AND ${inWindow(FIVE_DAYS)} GROUP BY bin, port ORDER BY bin, port`,
			last: `SELECT max(time) AS last FROM mikroscope_kmsg WHERE kind = 'own-address' AND ${inWindow(FIVE_DAYS)}`,
			ports: `SELECT ${binOf("1 hour")} AS bin, interface, round(avg(rx_pps), 2) AS rx_pps, round(avg(tx_pps), 2) AS tx_pps, count(*) AS readings FROM mikroscope_api_iface WHERE interface IN ('ether2', 'sfp-sfpplus1') AND ${inWindow(FIVE_DAYS)} GROUP BY bin, interface ORDER BY bin, interface`,
			samples: `SELECT ${binOf("1 hour")} AS bin, count(*) AS samples FROM mikroscope_sample WHERE ${inWindow(FIVE_DAYS)} GROUP BY bin ORDER BY bin`,
			uptime: uptimeSql(FIVE_DAYS),
			device: deviceSql(FIVE_DAYS),
		},
	},
	"conntrack-2026-09-19": {
		window: FIVE_DAYS,
		every: "1 hour",
		queries: {
			api: `SELECT ${binOf("1 hour")} AS bin, round(avg(entries), 1) AS api, count(*) AS readings FROM mikroscope_api_conntrack WHERE ${inWindow(FIVE_DAYS)} GROUP BY bin ORDER BY bin`,
			slab: `SELECT ${binOf("1 hour")} AS bin, round(avg(active), 1) AS slab, min("limit") AS ceiling_min, max("limit") AS ceiling_max, count(*) AS readings FROM mikroscope_slab WHERE cache = 'nf_conntrack' AND ${inWindow(FIVE_DAYS)} GROUP BY bin ORDER BY bin`,
			uptime: uptimeSql(FIVE_DAYS),
			device: deviceSql(FIVE_DAYS),
		},
	},
	"port-overflow-2026-09-19": {
		window: OVERFLOW_DAY,
		every: "10 minutes",
		queries: {
			// The counters are cumulative, so each reading's increment is taken
			// from the one before it; the first reading of the window has none.
			counters: `SELECT ${binOf("10 minutes")} AS bin, sum(d_overflow) AS overflows, sum(d_packets) AS packets, count(*) AS readings FROM (SELECT time, rx_overflow - lag(rx_overflow) OVER (ORDER BY time) AS d_overflow, rx_packet - lag(rx_packet) OVER (ORDER BY time) AS d_packets FROM mikroscope_api_ifcounters WHERE interface = 'ether1' AND ${inWindow(OVERFLOW_DAY)}) GROUP BY bin ORDER BY bin`,
			uptime: uptimeSql(OVERFLOW_DAY),
			device: deviceSql(OVERFLOW_DAY),
		},
	},
};

/** The store answers in local-less ISO; every time in it is UTC. */
const utc = (s) => Date.parse(/[Zz]|[+-]\d\d:?\d\d$/.test(s) ? s : `${s}Z`);
const isoMinute = (t) =>
	new Date(t).toISOString().slice(0, 16).replace("T", " ");
const isoSecond = (t) =>
	new Date(t).toISOString().slice(0, 19).replace("T", " ");

function fail(message) {
	throw new Error(`[figures] ${message}`);
}

/** A query result as objects, from the { columns, rows } a dataset stores. */
function table(ds, key) {
	const result = ds.results[key];
	if (!result) fail(`${ds.figure}: no result for "${key}". Run --fetch.`);
	return result.rows.map((row) =>
		Object.fromEntries(result.columns.map((c, i) => [c, row[i]])),
	);
}

/** Every bin start in the window, as epoch milliseconds. */
function binsOf(window, stepMs) {
	const out = [];
	for (let t = utc(window.from); t < utc(window.to); t += stepMs) out.push(t);
	return out;
}

/** One value per bin, null where the rows have none. */
function aligned(bins, rows, pick) {
	const byBin = new Map(bins.map((t) => [t, null]));
	for (const row of rows) {
		const t = utc(row.bin);
		if (!byBin.has(t)) fail(`a row at ${row.bin} is outside the bins`);
		byBin.set(t, pick(row, byBin.get(t)));
	}
	return bins.map((t) => byBin.get(t));
}

const present = (values) => values.filter((v) => v !== null);
function median(values) {
	const v = present(values).sort((a, b) => a - b);
	if (v.length === 0) fail("a median of nothing");
	const mid = Math.floor(v.length / 2);
	return v.length % 2 ? v[mid] : (v[mid - 1] + v[mid]) / 2;
}
function pearson(xs, ys) {
	const pairs = xs
		.map((x, i) => [x, ys[i]])
		.filter(([x, y]) => x !== null && y !== null);
	const n = pairs.length;
	const mx = pairs.reduce((s, [x]) => s + x, 0) / n;
	const my = pairs.reduce((s, [, y]) => s + y, 0) / n;
	let sxy = 0;
	let sxx = 0;
	let syy = 0;
	for (const [x, y] of pairs) {
		sxy += (x - mx) * (y - my);
		sxx += (x - mx) ** 2;
		syy += (y - my) ** 2;
	}
	return sxy / Math.sqrt(sxx * syy);
}

/**
 * The device, the kernel and the RouterOS version a dataset was read on, with
 * the reboot check that ties the version to the whole window.
 */
function provenanceOf(ds) {
	const [up] = table(ds, "uptime");
	const clock = (utc(up.last) - utc(up.first)) / 1000;
	const grew = up.up_last - up.up_first;
	if (Math.abs(clock - grew) > 120) {
		fail(
			`${ds.figure}: the router's uptime grew ${grew} s over ${Math.round(clock)} s of clock, so it rebooted inside the window and RouterOS ${ds.routeros.version} cannot be assumed for all of it`,
		);
	}
	const devices = table(ds, "device");
	if (devices.length !== 1)
		fail(`${ds.figure}: ${devices.length} device rows, expected one`);
	const [d] = devices;
	if (d.board !== "RB5009")
		fail(`${ds.figure}: board ${d.board}, expected RB5009`);
	return {
		device: "RB5009UG+S+",
		routeros: ds.routeros.version,
		kernel: d.kernel,
		booted: utc(up.first) - up.up_first * 1000,
	};
}

/* ----- drawing */

const r1 = (v) => Math.round(v * 10) / 10;
/** A width estimate for wrapping: the average glyph of a sans at this size. */
const textWidth = (s, size) => [...s].length * size * 0.56;

function wrapWords(s, width, size, separator = " ") {
	const lines = [];
	let current = "";
	for (const word of s.split(separator)) {
		const next = current ? `${current}${separator}${word}` : word;
		if (current && textWidth(next, size) > width) {
			lines.push(current);
			current = word;
		} else current = next;
	}
	if (current) lines.push(current);
	return lines;
}

function label(x, y, s, { size, fill = C.body, weight, anchor = "start" }) {
	return `<text x="${r1(x)}" y="${r1(y)}"${anchor === "start" ? "" : ` text-anchor="${anchor}"`} font-family="${FONT}" font-size="${size}"${weight ? ` font-weight="${weight}"` : ""} fill="${fill}">${esc(s)}</text>`;
}

// The wide layout's width is in user units, and the SVG is drawn at the
// column's width: 640 units renders the 11 and 12.5 unit labels at about 10.5
// and 12 px in the 616 px column a 1280 px window leaves between the two
// sidebars, and larger in the 50rem column of a wider one. At 890 units, the
// width the data-path diagram uses, they came out at 7.6 and 8.7 px there
// (the column's width measured in Chromium on 2026-09-24, the sizes scaled
// from it).
const CHART = {
	wide: {
		width: 640,
		left: 58,
		right: 14,
		font: 12.5,
		small: 11,
		panel: 116,
		stroke: 1.6,
	},
	// Drawn 360 wide and shown in a column of 328-358 px, so it is scaled by
	// 0.91-0.99 on a phone: at `small: 10` the ticks, legends and notes
	// rendered at 9.1-9.8 px (conntrack, loop and port-errors, WebKit 390 px
	// and Chromium 360 px, 2026-09-24). 11 renders at 10-10.9 px.
	narrow: {
		width: 360,
		left: 44,
		right: 10,
		font: 12,
		small: 11,
		panel: 100,
		stroke: 1.4,
	},
};

/**
 * A stack of panels on one time axis. Each panel is bars or lines, on a
 * linear or a log scale; bands shade a stretch of time across every panel and
 * carry a label in a strip above the first one; markers are dashed verticals.
 */
function chart({
	id,
	layout,
	title,
	desc,
	subtitle,
	bins,
	step,
	panels,
	bands = [],
	markers = [],
	xTicks,
}) {
	const L = CHART[layout];
	const x0 = L.left;
	const x1 = L.width - L.right;
	const t0 = bins[0];
	const t1 = bins[bins.length - 1] + step;
	const xOf = (t) => x0 + ((t - t0) / (t1 - t0)) * (x1 - x0);
	const back = [];
	const front = [];
	let y = 2;

	// What it was measured on, first, as Provenance puts it beside a figure.
	for (const line of wrapWords(subtitle, L.width - 4, L.small, " · ")) {
		y += L.small + 4;
		front.push(label(2, y, line, { size: L.small, fill: C.muted }));
	}
	y += 8;

	// The band labels, in their own strip so no bar or line runs under them.
	const bandTop = y;
	if (bands.length > 0) {
		const lines = bands.map((b) =>
			wrapWords(b.label, Math.max(xOf(b.to) - xOf(b.from) - 6, 40), L.small),
		);
		const rows = Math.max(...lines.map((l) => l.length));
		bands.forEach((b, i) => {
			lines[i].forEach((line, j) => {
				front.push(
					label(xOf(b.from) + 4, y + (j + 1) * (L.small + 3), line, {
						size: L.small,
						fill: C.body,
						weight: 600,
					}),
				);
			});
		});
		y += rows * (L.small + 3) + 8;
	}

	const stripBottom = y;
	let lastBottom = y;
	// Where each plot area is. The shading, the markers and the time grid are
	// drawn inside these and not from the band strip to the last axis: drawn
	// through, they crossed every panel's title, legend and note ("a se¦cond",
	// "recibidos en"), with the markers on top of the words (the three charts,
	// both layouts, 2026-09-24).
	const plots = [];
	for (const p of panels) {
		y += 6;
		for (const line of wrapWords(p.label, L.width - 4, L.font)) {
			y += L.font + 3;
			front.push(
				label(2, y, line, { size: L.font, weight: 600, fill: C.heading }),
			);
		}
		// The legend, then any note, below the label; an entry that does not
		// fit on the row starts the next one.
		const rows = [[]];
		let lx = 2;
		for (const s of p.series.filter((s) => s.legend)) {
			const w = 16 + textWidth(s.legend, L.small) + 14;
			if (lx > 2 && lx + w > L.width - 2) {
				rows.push([]);
				lx = 2;
			}
			rows[rows.length - 1].push({ s, x: lx });
			lx += w;
		}
		for (const row of rows.filter((r) => r.length > 0)) {
			y += L.small + 6;
			const ly = y - L.small / 2 + 1;
			for (const { s, x } of row) {
				front.push(
					s.kind === "bars"
						? `<rect x="${x}" y="${r1(ly - 5)}" width="12" height="10" fill="${s.color}"/>`
						: `<line x1="${x}" y1="${r1(ly)}" x2="${x + 14}" y2="${r1(ly)}" stroke="${s.color}" stroke-width="${L.stroke + 0.6}"${s.dash ? ` stroke-dasharray="${s.dash}"` : ""}/>`,
					label(x + 18, y, s.legend, { size: L.small, fill: C.body }),
				);
			}
		}
		if (p.note) {
			for (const line of wrapWords(p.note, L.width - 4, L.small)) {
				y += L.small + 4;
				front.push(label(2, y, line, { size: L.small, fill: C.muted }));
			}
		}
		// Room for the top tick label, which is centred on the top gridline and
		// so reaches above it: with 8 alone a wrapped note sat 3 px over "3 000"
		// (port-errors, narrow, 2026-09-24).
		y += 6 + Math.ceil(L.small * 0.6);
		const top = y;
		const bottom = y + L.panel;
		plots.push({ top, bottom });
		const yOf =
			p.scale === "log"
				? (v) => {
						const lo = Math.log10(p.min);
						const hi = Math.log10(p.max);
						const f = (Math.log10(Math.max(v, p.min)) - lo) / (hi - lo);
						return bottom - Math.min(f, 1) * (bottom - top);
					}
				: (v) =>
						bottom -
						((Math.min(v, p.max) - p.min) / (p.max - p.min)) * (bottom - top);

		for (const tick of p.ticks) {
			const ty = yOf(tick.value);
			back.push(
				`<line x1="${x0}" y1="${r1(ty)}" x2="${x1}" y2="${r1(ty)}" stroke="${C.border}" stroke-width="1"/>`,
			);
			front.push(
				label(x0 - 5, ty + L.small / 3, tick.label, {
					size: L.small,
					fill: C.muted,
					anchor: "end",
				}),
			);
		}
		for (const s of p.series) {
			if (s.kind === "bars") {
				const w = xOf(t0 + step) - xOf(t0);
				const gap = w > 4 ? 1 : 0;
				s.values.forEach((v, i) => {
					if (v === null || v <= p.min) return;
					const bx = xOf(bins[i]);
					const by = yOf(v);
					front.push(
						`<rect x="${r1(bx + gap / 2)}" y="${r1(by)}" width="${r1(w - gap)}" height="${r1(bottom - by)}" fill="${s.color}"/>`,
					);
				});
			} else {
				let d = "";
				let pen = false;
				s.values.forEach((v, i) => {
					if (v === null) {
						pen = false;
						return;
					}
					const px = xOf(bins[i] + step / 2);
					d += `${pen ? "L" : "M"}${r1(px)} ${r1(yOf(v))}`;
					pen = true;
				});
				front.push(
					`<path d="${d}" fill="none" stroke="${s.color}" stroke-width="${L.stroke}" stroke-linejoin="round"${s.dash ? ` stroke-dasharray="${s.dash}"` : ""}/>`,
				);
			}
		}
		front.push(
			`<line x1="${x0}" y1="${bottom}" x2="${x1}" y2="${bottom}" stroke="${C.strong}" stroke-width="1"/>`,
		);
		y = bottom + 4;
		lastBottom = bottom;
	}

	// A band is shaded behind its own label strip and inside each plot.
	const shaded = [
		...(stripBottom > bandTop ? [{ top: bandTop, bottom: stripBottom }] : []),
		...plots,
	];
	for (const b of bands) {
		back.unshift(
			...shaded.map(
				({ top, bottom }) =>
					`<rect x="${r1(xOf(b.from))}" y="${r1(top)}" width="${r1(xOf(b.to) - xOf(b.from))}" height="${r1(bottom - top)}" fill="${b.fill}"/>`,
			),
		);
	}
	for (const t of markers) {
		for (const { top, bottom } of plots) {
			front.push(
				`<line x1="${r1(xOf(t))}" y1="${r1(top)}" x2="${r1(xOf(t))}" y2="${r1(bottom)}" stroke="${C.strong}" stroke-width="1" stroke-dasharray="4 3"/>`,
			);
		}
	}

	// The time axis, labelled once under the last panel.
	y = lastBottom + L.small + 6;
	for (const t of xTicks(t0, t1)) {
		const tx = xOf(t.at);
		for (const { top, bottom } of plots) {
			back.push(
				`<line x1="${r1(tx)}" y1="${r1(top)}" x2="${r1(tx)}" y2="${r1(bottom)}" stroke="${C.border}" stroke-width="1" stroke-dasharray="2 3"/>`,
			);
		}
		front.push(
			label(tx, y, t.label, { size: L.small, fill: C.muted, anchor: "middle" }),
		);
	}
	const height = Math.ceil(y + 6);
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${L.width} ${height}" width="100%" role="img" aria-labelledby="${id}-t ${id}-d">
	<title id="${id}-t">${esc(title)}</title>
	<desc id="${id}-d">${esc(desc)}</desc>
	${[...back, ...front].join("\n\t")}
</svg>
`;
}

/** One tick per UTC midnight, labelled MM-DD: the year is in the subtitle. */
const dayTicks = (t0, t1) => {
	const out = [];
	for (let t = Math.ceil(t0 / 86_400_000) * 86_400_000; t < t1; t += 86_400_000)
		out.push({ at: t, label: new Date(t).toISOString().slice(5, 10) });
	return out;
};
/** One tick every `hours`, on the hour, labelled HH:MM. */
const hourTicks = (hours) => (t0, t1) => {
	const out = [];
	const every = hours * HOUR;
	for (let t = Math.ceil(t0 / every) * every; t < t1; t += every)
		out.push({ at: t, label: new Date(t).toISOString().slice(11, 16) });
	return out;
};

/** Linear ticks from 0 to `max` every `every`, formatted for the language. */
const linearTicks = (max, every, lang, digits = 0) => {
	const out = [];
	for (let v = 0; v <= max + 1e-9; v += every)
		out.push({ value: v, label: formatNumber(v, lang, digits) });
	return out;
};
/** Decades from 0.1 to 1000; the floor reads "≤ 0.1", because 0 has no log. */
const decadeTicks = (lang) =>
	[0.1, 1, 10, 100, 1000].map((v, i) => ({
		value: v,
		label: i === 0 ? `≤${formatNumber(v, lang, 1)}` : formatNumber(v, lang, 0),
	}));

/** The number as the prose writes it: one decimal under 10, none above. */
const n = (v, lang) => formatNumber(v, lang, Math.abs(v) < 10 ? 1 : 0);
/** A span of hours as "4 h 20 min", in both languages. */
const span = (hours) => {
	const minutes = Math.round(hours * 60);
	const h = Math.floor(minutes / 60);
	const m = minutes % 60;
	// No-break spaces: "8 h" and "30 min" must not end up on two lines.
	return m === 0 ? `${h}\u00a0h` : `${h}\u00a0h\u00a0${m}\u00a0min`;
};

/* ----- the words, and the three charts */

const SUBTITLE = {
	en: (p, from, to, every) =>
		`${p.device} · RouterOS ${p.routeros} · Linux ${p.kernel} · ${from} to ${to} UTC · ${every}, from the reference InfluxDB store`,
	es: (p, from, to, every) =>
		`${p.device} · RouterOS ${p.routeros} · Linux ${p.kernel} · del ${from} al ${to} UTC · ${every}, del almacén InfluxDB de referencia`,
};

/**
 * The loop's return, 2026-09-19..23: the kernel's own-address records, and
 * what the bridge sent each of the two ports it cut off in turn.
 */
function loopFigure(ds) {
	const p = provenanceOf(ds);
	const bins = binsOf(ds.window, HOUR);
	const samples = aligned(bins, table(ds, "samples"), (r) => r.samples);
	// An hour with no row is zero records only if the agent was writing.
	const records = aligned(
		bins,
		table(ds, "records"),
		(r, acc) => (acc ?? 0) + r.records,
	).map((v, i) => (samples[i] ? (v ?? 0) : null));
	const ports = table(ds, "ports");
	const series = (iface, col) =>
		aligned(
			bins,
			ports.filter((r) => r.interface === iface),
			(r) => r[col],
		);
	const e2rx = series("ether2", "rx_pps");
	const e2tx = series("ether2", "tx_pps");
	const sfprx = series("sfp-sfpplus1", "rx_pps");
	const sfptx = series("sfp-sfpplus1", "tx_pps");
	const last = utc(table(ds, "last")[0].last);

	// The hour the steady stream started, which is also the hour the cut-off
	// port changed from sfp-sfpplus1 to ether2. The assertions are the claims
	// the words below make, checked against the rows rather than trusted.
	const steady = utc("2026-09-20T21:00:00Z");
	const phase = (from, to) => (_, i) => bins[i] >= from && bins[i] + HOUR <= to;
	const inA = phase(bins[0], steady);
	const inB = phase(steady + HOUR, last);
	const inC = phase(last + HOUR, bins[bins.length - 1] + HOUR);
	const pick = (values, inPhase) =>
		values.map((v, i) => (inPhase(v, i) ? v : null));
	const maxA = Math.max(...present(pick(records, inA)));
	const medB = median(pick(records, inB));
	if (maxA >= 100)
		fail(`loop: ${maxA} records in an hour before the steady stream`);
	if (medB < 1700)
		fail(`loop: a median of ${medB} records an hour in the steady stream`);
	if (Math.max(...present(pick(sfptx, inA))) >= 0.5)
		fail("loop: the bridge sent sfp-sfpplus1 traffic while it was cut off");
	if (Math.max(...present(pick(e2tx, inB))) >= 2)
		fail("loop: the bridge sent ether2 traffic while it was cut off");
	if (present(pick(records, inC)).some((v) => v > 0))
		fail("loop: own-address records after the last one");

	const sfpSilent = Math.max(...present(pick(sfptx, inA))) === 0;
	const s = {
		maxA,
		medB,
		every: 3600 / medB,
		sfpRxA: median(pick(sfprx, inA)),
		sfpTxA: median(pick(sfptx, inA)),
		e2RxB: median(pick(e2rx, inB)),
		e2TxB: median(pick(e2tx, inB)),
		e2RxC: median(pick(e2rx, inC)),
		e2TxC: median(pick(e2tx, inC)),
	};
	const from = isoMinute(bins[0]);
	const to = isoMinute(bins[bins.length - 1] + HOUR);
	const words = {
		en: {
			title: "The loop's return, hour by hour",
			desc: `Hourly, ${from} to ${to} UTC, from the reference InfluxDB store (${p.device}, RouterOS ${p.routeros}, Linux ${p.kernel}). Until ${isoMinute(steady)} the kernel logged at most ${maxA} own-address records an hour, while the bridge ${sfpSilent ? "sent sfp-sfpplus1 nothing in any hour" : `sent sfp-sfpplus1 ${n(s.sfpTxA, "en")} packets a second`} and received ${n(s.sfpRxA, "en")} a second from it. From then until the last record, at ${isoSecond(last)}, it logged a median of ${formatNumber(medB, "en", 0)} an hour, one every ${formatNumber(s.every, "en", 1)} s, and the bridge sent ether2 ${n(s.e2TxB, "en")} packets a second against ${n(s.e2RxB, "en")} received. Afterwards ether2 received ${n(s.e2RxC, "en")} and was sent ${n(s.e2TxC, "en")}. Rates are medians of the hourly means.`,
			every: "hourly",
			records: "Own-address records in the kernel log, per hour",
			recordsNote: `at most ${maxA} an hour before ${isoMinute(steady).slice(5)}, then a median of ${formatNumber(medB, "en", 0)}`,
			port: (name) => `${name}: packets a second, hourly mean, log scale`,
			received: "received",
			sent: "sent by the bridge",
			bands: ["sfp-sfpplus1 cut off", "ether2 cut off", "no loop"],
		},
		es: {
			title: "La vuelta del bucle, hora a hora",
			desc: `Por horas, del ${from} al ${to} UTC, del almacén InfluxDB de referencia (${p.device}, RouterOS ${p.routeros}, Linux ${p.kernel}). Hasta el ${isoMinute(steady)} el kernel anotó como mucho ${maxA} registros de dirección propia por hora, mientras el bridge ${sfpSilent ? "no enviaba nada a sfp-sfpplus1 en ninguna hora" : `enviaba a sfp-sfpplus1 ${n(s.sfpTxA, "es")} paquetes por segundo`} y recibía ${n(s.sfpRxA, "es")} por segundo de él. Desde entonces y hasta el último registro, el ${isoSecond(last)}, registró una mediana de ${formatNumber(medB, "es", 0)} por hora, uno cada ${formatNumber(s.every, "es", 1)} s, y el bridge envió a ether2 ${n(s.e2TxB, "es")} paquetes por segundo frente a ${n(s.e2RxB, "es")} recibidos. Después, ether2 recibió ${n(s.e2RxC, "es")} y se le enviaron ${n(s.e2TxC, "es")}. Las tasas son medianas de las medias horarias.`,
			every: "por horas",
			records: "Registros de dirección propia en el log del kernel, por hora",
			recordsNote: `como mucho ${maxA} por hora antes del ${isoMinute(steady).slice(5)}, después una mediana de ${formatNumber(medB, "es", 0)}`,
			port: (name) =>
				`${name}: paquetes por segundo, media horaria, escala logarítmica`,
			received: "recibidos",
			sent: "enviados por el bridge",
			bands: ["sfp-sfpplus1 aislado", "ether2 aislado", "sin bucle"],
		},
	};
	return (lang, layout, id) => {
		const w = words[lang];
		const rates = (name, rx, tx) => ({
			label: w.port(name),
			scale: "log",
			min: 0.1,
			max: 1000,
			ticks: decadeTicks(lang),
			series: [
				{ kind: "line", values: rx, color: C.received, legend: w.received },
				{
					kind: "line",
					values: tx,
					color: C.sent,
					dash: "5 3",
					legend: w.sent,
				},
			],
		});
		return {
			title: w.title,
			desc: w.desc,
			svg: chart({
				id,
				layout,
				lang,
				title: w.title,
				desc: w.desc,
				subtitle: SUBTITLE[lang](p, from, to, w.every),
				bins,
				step: HOUR,
				xTicks: dayTicks,
				bands: [
					{ from: bins[0], to: steady, label: w.bands[0], fill: C.faultSoft },
					{ from: steady, to: last, label: w.bands[1], fill: C.faultSoft },
					{
						from: last,
						to: bins[bins.length - 1] + HOUR,
						label: w.bands[2],
						fill: "none",
					},
				],
				markers: [steady, last],
				panels: [
					{
						label: w.records,
						note: w.recordsNote,
						scale: "linear",
						min: 0,
						max: 2000,
						ticks: linearTicks(2000, 1000, lang),
						series: [{ kind: "bars", values: records, color: C.fault }],
					},
					rates("ether2", e2rx, e2tx),
					rates("sfp-sfpplus1", sfprx, sfptx),
				],
			}),
		};
	};
}

/**
 * The conntrack cross-check the page asks the reader to make, made for five
 * days: the API's count against the slab cache's active objects.
 */
function conntrackFigure(ds) {
	const p = provenanceOf(ds);
	const bins = binsOf(ds.window, HOUR);
	const apiRows = table(ds, "api");
	const slabRows = table(ds, "slab");
	const api = aligned(bins, apiRows, (r) => r.api);
	const slab = aligned(bins, slabRows, (r) => r.slab);
	const ratio = api.map((a, i) =>
		a !== null && slab[i] !== null ? slab[i] / a : null,
	);
	const ceilings = new Set(
		slabRows.flatMap((r) => [r.ceiling_min, r.ceiling_max]),
	);
	if (ceilings.size !== 1)
		fail("conntrack: nf_conntrack_max changed inside the window");
	const [ceiling] = ceilings;
	const s = {
		apiMin: Math.min(...present(api)),
		apiMax: Math.max(...present(api)),
		ratioMin: Math.min(...present(ratio)),
		ratioMax: Math.max(...present(ratio)),
		r: pearson(api, slab),
		readings: median(apiRows.map((r) => r.readings)),
	};
	if (s.ratioMin < 1)
		fail("conntrack: an hour where the slab count was below the API's");
	const yMax = Math.ceil(Math.max(...present(slab)) / 2500) * 2500;
	const rMax = Math.ceil(s.ratioMax * 20) / 20;
	const from = isoMinute(bins[0]);
	const to = isoMinute(bins[bins.length - 1] + HOUR);
	const f = (v, lang, d) => formatNumber(v, lang, d);
	const words = {
		en: {
			title: "The slab count against the API's, five days",
			desc: `Hourly means, ${from} to ${to} UTC, from the reference InfluxDB store (${p.device}, RouterOS ${p.routeros}, Linux ${p.kernel}): the RouterOS API's /ip/firewall/connection/print count-only, a median of ${f(s.readings, "en", 0)} readings an hour, against the active objects of the nf_conntrack slab cache the agent read. The API count ran from ${f(s.apiMin, "en", 0)} to ${f(s.apiMax, "en", 0)}; in every hour the slab count was ${f(s.ratioMin, "en", 2)} to ${f(s.ratioMax, "en", 2)} times it, and the two hourly series correlate at r = ${f(s.r, "en", 4)}. nf_conntrack_max read ${f(ceiling, "en", 0)} throughout.`,
			every: "hourly means",
			counts: "Tracked connections, hourly mean",
			api: "API count-only",
			slab: "nf_conntrack slab objects",
			ratio: "Slab objects ÷ API count",
			ratioNote: `${f(s.ratioMin, "en", 2)}–${f(s.ratioMax, "en", 2)} in every hour; r = ${f(s.r, "en", 4)}`,
		},
		es: {
			title: "El recuento del slab frente al de la API, cinco días",
			desc: `Medias horarias, del ${from} al ${to} UTC, del almacén InfluxDB de referencia (${p.device}, RouterOS ${p.routeros}, Linux ${p.kernel}): el /ip/firewall/connection/print count-only de la API de RouterOS, una mediana de ${f(s.readings, "es", 0)} lecturas por hora, frente a los objetos activos de la caché de slab nf_conntrack que leyó el agente. El recuento de la API fue de ${f(s.apiMin, "es", 0)} a ${f(s.apiMax, "es", 0)}; en todas las horas el del slab fue entre ${f(s.ratioMin, "es", 2)} y ${f(s.ratioMax, "es", 2)} veces el de la API, y las dos series horarias correlacionan con r = ${f(s.r, "es", 4)}. nf_conntrack_max marcó ${f(ceiling, "es", 0)} todo el tiempo.`,
			every: "medias horarias",
			counts: "Conexiones en seguimiento, media horaria",
			api: "count-only de la API",
			slab: "objetos del slab nf_conntrack",
			ratio: "Objetos del slab ÷ recuento de la API",
			ratioNote: `${f(s.ratioMin, "es", 2)}–${f(s.ratioMax, "es", 2)} en todas las horas; r = ${f(s.r, "es", 4)}`,
		},
	};
	return (lang, layout, id) => {
		const w = words[lang];
		const ratioTicks = [];
		for (let v = 1; v <= rMax + 1e-9; v += 0.05)
			ratioTicks.push({ value: v, label: formatNumber(v, lang, 2) });
		return {
			title: w.title,
			desc: w.desc,
			svg: chart({
				id,
				layout,
				lang,
				title: w.title,
				desc: w.desc,
				subtitle: SUBTITLE[lang](p, from, to, w.every),
				bins,
				step: HOUR,
				xTicks: dayTicks,
				panels: [
					{
						label: w.counts,
						scale: "linear",
						min: 0,
						max: yMax,
						ticks: linearTicks(yMax, 2500, lang),
						series: [
							{ kind: "line", values: api, color: C.received, legend: w.api },
							{
								kind: "line",
								values: slab,
								color: C.sent,
								dash: "5 3",
								legend: w.slab,
							},
						],
					},
					{
						label: w.ratio,
						note: w.ratioNote,
						scale: "linear",
						min: 1,
						max: rMax,
						ticks: ratioTicks,
						series: [{ kind: "line", values: ratio, color: C.body }],
					},
				],
			}),
		};
	};
}

/**
 * The port losing frames, 2026-09-19: ether1's rx-overflow per ten minutes,
 * and what it received, before and after the overflow stopped.
 */
function overflowFigure(ds) {
	const p = provenanceOf(ds);
	const step = 10 * 60_000;
	const bins = binsOf(ds.window, step);
	const rows = table(ds, "counters");
	const overflows = aligned(bins, rows, (r) => r.overflows);
	const pps = aligned(bins, rows, (r) =>
		r.packets === null ? null : r.packets / 600,
	);
	const readings = median(rows.map((r) => r.readings));
	// The first ten minutes with none, after which two hours had none either.
	const stop = utc("2026-09-19T15:30:00Z");
	const at = (t) => bins.indexOf(t);
	for (let i = 0; i < at(stop); i += 1)
		if (!(overflows[i] > 0))
			fail(
				`overflow: none in the bin at ${isoMinute(bins[i])}, before the stop`,
			);
	for (let i = at(stop); i < at(stop) + 12; i += 1)
		if (overflows[i] !== 0)
			fail(
				`overflow: ${overflows[i]} in the bin at ${isoMinute(bins[i])}, inside the two quiet hours`,
			);
	const sum = (values) => present(values).reduce((a, b) => a + b, 0);
	const s = {
		before: sum(overflows.slice(0, at(stop))),
		after: sum(overflows.slice(at(stop))),
		ppsBefore: median(pps.slice(0, at(stop))),
		ppsAfter: median(pps.slice(at(stop))),
		hoursBefore: (stop - bins[0]) / HOUR,
		hoursAfter: (bins[bins.length - 1] + step - stop) / HOUR,
	};
	const from = isoMinute(bins[0]);
	const to = isoMinute(bins[bins.length - 1] + step);
	const f = (v, lang, d = 0) => formatNumber(v, lang, d);
	const words = {
		en: {
			title: "ether1's receive overflow, before and after it stopped",
			desc: `Per ten minutes, ${from} to ${to} UTC, from the reference InfluxDB store (${p.device}, RouterOS ${p.routeros}, Linux ${p.kernel}): the increments of ether1's rx-overflow and received-packet counters, read by the API tier a median of ${f(readings, "en")} times per ten minutes. Every ten minutes from ${from.slice(11)} to ${isoMinute(stop).slice(11)} overflowed, ${f(s.before, "en")} in the ${span(s.hoursBefore)} before ${isoMinute(stop).slice(11)}; the two hours after it had none, and the whole ${span(s.hoursAfter)} after it ${f(s.after, "en")}. The port received a median of ${f(s.ppsBefore, "en")} packets a second before and ${f(s.ppsAfter, "en")} after.`,
			every: "per 10 minutes",
			overflows: "ether1 rx-overflow, per 10 minutes",
			note: `${f(s.before, "en")} in the ${span(s.hoursBefore)} before ${isoMinute(stop).slice(11)}, ${f(s.after, "en")} in the ${span(s.hoursAfter)} after`,
			packets: "ether1 received packets a second, 10-minute mean",
		},
		es: {
			title:
				"El desbordamiento de recepción de ether1, antes y después de pararse",
			desc: `Cada diez minutos, del ${from} al ${to} UTC, del almacén InfluxDB de referencia (${p.device}, RouterOS ${p.routeros}, Linux ${p.kernel}): los incrementos de los contadores rx-overflow y de paquetes recibidos de ether1, leídos por la capa de la API una mediana de ${f(readings, "es")} veces cada diez minutos. Todos los tramos de diez minutos desde las ${from.slice(11)} hasta las ${isoMinute(stop).slice(11)} desbordaron, ${f(s.before, "es")} en las ${span(s.hoursBefore)} anteriores a las ${isoMinute(stop).slice(11)}; las dos horas siguientes no tuvieron ninguno, y las ${span(s.hoursAfter)} posteriores enteras, ${f(s.after, "es")}. El puerto recibió una mediana de ${f(s.ppsBefore, "es")} paquetes por segundo antes y ${f(s.ppsAfter, "es")} después.`,
			every: "cada 10 minutos",
			overflows: "rx-overflow de ether1, cada 10 minutos",
			note: `${f(s.before, "es")} en las ${span(s.hoursBefore)} anteriores a las ${isoMinute(stop).slice(11)}, ${f(s.after, "es")} en las ${span(s.hoursAfter)} posteriores`,
			packets: "Paquetes por segundo recibidos en ether1, media de 10 minutos",
		},
	};
	const oMax = Math.ceil(Math.max(...present(overflows)) / 1000) * 1000;
	const pMax = Math.ceil(Math.max(...present(pps)) / 500) * 500;
	return (lang, layout, id) => {
		const w = words[lang];
		return {
			title: w.title,
			desc: w.desc,
			svg: chart({
				id,
				layout,
				lang,
				title: w.title,
				desc: w.desc,
				subtitle: SUBTITLE[lang](p, from, to, w.every),
				bins,
				step,
				xTicks: hourTicks(layout === "wide" ? 1 : 2),
				markers: [stop],
				panels: [
					{
						label: w.overflows,
						note: w.note,
						scale: "linear",
						min: 0,
						max: oMax,
						ticks: linearTicks(oMax, 1000, lang),
						series: [{ kind: "bars", values: overflows, color: C.fault }],
					},
					{
						label: w.packets,
						scale: "linear",
						min: 0,
						max: pMax,
						ticks: linearTicks(pMax, 500, lang),
						series: [{ kind: "line", values: pps, color: C.received }],
					},
				],
			}),
		};
	};
}

const CHARTS = {
	"loop-2026-09-19": loopFigure,
	"conntrack-2026-09-19": conntrackFigure,
	"port-overflow-2026-09-19": overflowFigure,
};

/* ----- the store */

/** One SQL query against the store, as { columns, rows }. */
async function query(sql) {
	const url = process.env.MIKROSCOPE_INFLUX_URL;
	const token = process.env.MIKROSCOPE_INFLUX_TOKEN;
	if (!url || !token)
		fail(
			"--fetch needs MIKROSCOPE_INFLUX_URL and MIKROSCOPE_INFLUX_TOKEN in the environment",
		);
	const u = new URL(url);
	const db = process.env.MIKROSCOPE_INFLUX_DB || u.searchParams.get("db");
	if (!db)
		fail("--fetch needs MIKROSCOPE_INFLUX_DB, or a write URL carrying db=");
	const base =
		`${u.origin}${u.pathname.replace(/\/api\/v[23]\/write(_lp)?\/?$/, "")}`.replace(
			/\/$/,
			"",
		);
	const res = await fetch(`${base}/api/v3/query_sql`, {
		method: "POST",
		headers: {
			Authorization: `Bearer ${token}`,
			"Content-Type": "application/json",
		},
		body: JSON.stringify({ db, q: sql, format: "json" }),
	});
	if (!res.ok)
		fail(
			`the store answered ${res.status}: ${(await res.text()).slice(0, 300)}`,
		);
	const objects = await res.json();
	// The store leaves a null column out of its row, so the columns are the
	// union in first-seen order and a missing one is written as null.
	const columns = [];
	for (const o of objects)
		for (const k of Object.keys(o)) if (!columns.includes(k)) columns.push(k);
	return {
		columns,
		rows: objects.map((o) => columns.map((c) => o[c] ?? null)),
	};
}

async function fetchDatasets() {
	const prettier = await import("prettier");
	mkdirSync(DATA, { recursive: true });
	for (const [figure, spec] of Object.entries(DATASETS)) {
		const results = {};
		for (const [key, sql] of Object.entries(spec.queries))
			results[key] = await query(sql);
		const ds = {
			figure,
			about:
				"Rows read from the reference InfluxDB store by site/scripts/gen-figures.mjs --fetch. Each result is the answer to the query of the same name, verbatim; the chart is drawn from these rows and nothing else.",
			fetched: new Date().toISOString().slice(0, 10),
			window: spec.window,
			every: spec.every,
			routeros: ROUTEROS,
			queries: spec.queries,
			results,
		};
		const file = path.join(DATA, `${figure}.json`);
		const options = (await prettier.resolveConfig(file)) ?? {};
		writeFileSync(
			file,
			await prettier.format(JSON.stringify(ds), { ...options, filepath: file }),
		);
		console.log(
			`[figures] fetched ${figure}: ${Object.values(results).reduce((a, r) => a + r.rows.length, 0)} rows`,
		);
	}
}

/** The committed datasets, each checked against the SQL written above. */
function loadDatasets(problems) {
	const out = {};
	for (const [figure, spec] of Object.entries(DATASETS)) {
		let ds;
		try {
			ds = JSON.parse(readFileSync(path.join(DATA, `${figure}.json`), "utf8"));
		} catch {
			problems.push(`data/${figure}.json is missing; run with --fetch`);
			continue;
		}
		if (JSON.stringify(ds.window) !== JSON.stringify(spec.window))
			problems.push(
				`data/${figure}.json: its window is not the one in this script; run with --fetch`,
			);
		for (const [key, sql] of Object.entries(spec.queries)) {
			if (ds.queries?.[key] !== sql)
				problems.push(
					`data/${figure}.json: query "${key}" is not the one in this script; run with --fetch`,
				);
		}
		out[figure] = ds;
	}
	return out;
}

const figures = {};
// The title and description of each figure, as a module rather than only
// inside the SVGs: page-markdown.mjs reduces <Figure> to that sentence for
// docs/, and it is loaded both by node from site/ and by Vite from a build
// directory, so it has to be an import rather than a path on disk.
const meta = {};
for (const [lang, words] of Object.entries(dataPath)) {
	figures[`data-path.${lang}.wide.svg`] = wide(words, `data-path-${lang}-wide`);
	figures[`data-path.${lang}.narrow.svg`] = narrow(
		words,
		`data-path-${lang}-narrow`,
	);
	meta["data-path"] ??= {};
	meta["data-path"][lang] = { title: words.title, desc: words.desc };
}

if (process.argv.includes("--fetch")) await fetchDatasets();
const problems = [];
const datasets = loadDatasets(problems);
for (const [name, build] of Object.entries(CHARTS)) {
	if (!datasets[name]) continue;
	const draw = build(datasets[name]);
	for (const lang of ["en", "es"]) {
		for (const layout of ["wide", "narrow"]) {
			const { title, desc, svg } = draw(
				lang,
				layout,
				`${name}-${lang}-${layout}`,
			);
			figures[`${name}.${lang}.${layout}.svg`] = svg;
			meta[name] ??= {};
			meta[name][lang] = { title, desc };
		}
	}
}
figures["figures.json"] = `${JSON.stringify(meta, null, 2)}\n`;

mkdirSync(OUT, { recursive: true });
if (process.argv.includes("--check")) {
	const onDisk = new Set(
		readdirSync(OUT).filter((f) => f.endsWith(".svg") || f.endsWith(".json")),
	);
	for (const [name, body] of Object.entries(figures)) {
		onDisk.delete(name);
		let current = null;
		try {
			current = readFileSync(path.join(OUT, name), "utf8");
		} catch {
			problems.push(`${name} is missing`);
			continue;
		}
		if (current !== body) problems.push(`${name} is stale`);
	}
	for (const extra of onDisk)
		problems.push(`${extra} is not generated by this script`);
	if (problems.length > 0) {
		console.error(
			`[figures] ${problems.length} problem(s). Run \`pnpm run figures\`:`,
		);
		for (const p of problems) console.error(`  ${p}`);
		process.exit(1);
	}
	console.log(
		`[figures] ${Object.keys(figures).length} files, all up to date.`,
	);
} else {
	if (problems.length > 0) {
		for (const p of problems) console.error(`[figures] ${p}`);
		process.exit(1);
	}
	for (const [name, body] of Object.entries(figures)) {
		writeFileSync(path.join(OUT, name), body);
	}
	console.log(
		`[figures] wrote ${Object.keys(figures).length} files into src/data/figures.`,
	);
}
