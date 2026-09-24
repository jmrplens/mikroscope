/**
 * Every figure the site quotes in more than one place, each written once.
 *
 * The landing, `start`, `cost` and `cost/rate-ceiling` repeat the same numbers
 * in two languages. Typed into eight files, one of them goes stale the day a
 * run is repeated; here, a new run changes every page and both locales in the
 * same commit. Components read from this module (`<Measured>`, `<RunsTable>`,
 * `<Provenance>`), and so does src/data/home.ts, which types no figure.
 *
 * Each value names the campaign it came from, and each campaign names the
 * device, the RouterOS version, the date and the conditions, plus the site
 * page or Go symbol that states it, which is for the reviewer and never rendered.
 *
 * The busybox baseline is the only comparison the project has between the
 * agent and the naive approach.
 */
import { formatNumber, unbreakable } from "../lib/format";

export type Lang = "en" | "es";
export type Unit =
	| "%"
	| "MiB"
	| "µs"
	| "Hz"
	| "ms"
	| "s"
	| "kB"
	| "B"
	| "Mbit/s"
	| "/s"
	| "/h"
	| "°C"
	| "";

export interface Campaign {
	device: "RB5009UG+S+";
	cpu: { cores: 4; ghz: 1.4; core: "Cortex-A72" };
	routeros: string;
	kernel?: string;
	/** ISO date; null where the source does not record one, rendered as "date not recorded". */
	date: string | null;
	/** The window, the load and the sinks, as a reader needs them to reuse the number. */
	conditions: Record<Lang, string>;
	/** The length of each measurement window, in seconds, where the campaign used one. */
	windowS?: number;
	/** The sinks the collector forwarded to during the campaign, where it ran one. */
	sinks?: readonly string[];
	/** Where this is stated, a site page or a Go file and symbol, for review. Never rendered. */
	source: string;
}

export const RB5009 = {
	device: "RB5009UG+S+",
	cpu: { cores: 4, ghz: 1.4, core: "Cortex-A72" },
	routeros: "7.24.2",
} as const;

/**
 * The RouterOS the reference device runs now, which is not the one most
 * campaigns were measured on: `RB5009.routeros` is what every campaign
 * inherits, and a campaign measured later names its own version. The device
 * is first recorded on 7.24.4 on 2026-09-21 (campaign stream-2026-09-21, and
 * the `uninstall --expose` reading in CHANGELOG [1.1.0]). The upgrade itself
 * was not written down, but it can be bounded: 7.24.4's build-time is
 * 2026-09-16 11:32:21, and on 2026-09-24 the router reported 7.24.4 with an
 * uptime of 5d15h49m12s, a boot at about 2026-09-18 22:43 UTC, and the API
 * tier's uptime in the reference store grows at clock rate from 2026-09-19
 * 11:13:35 UTC on (src/data/figures/data/*.json). An upgrade needs a reboot,
 * so the router has run 7.24.4 since that boot at the latest. Campaigns dated
 * 2026-09-16 to 2026-09-18 still inherit 7.24.2 from `RB5009`; this bound
 * neither confirms nor refutes that. The landing's "has run on" names both,
 * and takes this one from here rather than from a campaign, so the next
 * campaign on 7.24.4 cannot change what the sentence says.
 */
export const RB5009_NOW: { readonly routeros: string } = {
	routeros: "7.24.4",
};

export const campaigns = {
	"rates-2026-09-18": {
		...RB5009,
		date: "2026-09-18",
		windowS: 300,
		conditions: {
			en: "300 s windows at steady state (ring full), full source set, the shipped configuration — a 60 s ring, the memory limit derived from it and the default 64M container cap — with the collector forwarding to InfluxDB 3",
			es: "ventanas de 300 s en régimen estacionario (con el anillo ya lleno), conjunto completo de fuentes, la configuración que se envía —anillo de 60 s, límite de memoria derivado de él y el tope de contenedor de serie de 64M— con el colector reenviando a InfluxDB 3",
		},
		sinks: ["InfluxDB 3"],
		source: "site/src/content/docs/cost/rate-ceiling.mdx",
	},
	"rates-2026-09-15": {
		...RB5009,
		date: "2026-09-15",
		windowS: 60,
		conditions: {
			en: "60 s windows at steady state (ring full), full source set, collector forwarding to a file, a Prometheus exposition and InfluxDB 3 at once",
			es: "ventanas de 60 s en régimen estacionario (con el anillo ya lleno), conjunto completo de fuentes, colector reenviando a la vez a fichero, a una exposición Prometheus y a InfluxDB 3",
		},
		sinks: ["file", "Prometheus", "InfluxDB 3"],
		source: "site/src/content/docs/cost/rate-ceiling.mdx",
	},
	"kernel-2026-09-11": {
		...RB5009,
		kernel: "5.6.3",
		date: "2026-09-11",
		conditions: {
			en: "`/proc/pressure` and `/proc/schedstat` absent",
			es: "`/proc/pressure` y `/proc/schedstat` ausentes",
		},
		source: "site/src/content/docs/limits/index.mdx",
	},
	// Three `/stream` connections against the production agent, timed to the
	// centisecond from the operator host on the same LAN. The point of the run
	// was the server's own 30 s WriteTimeout, so nothing else was varied.
	"stream-2026-09-21": {
		...RB5009,
		routeros: "7.24.4",
		date: "2026-09-21",
		conditions: {
			en: "three `GET /stream` connections against the installed agent at 10 Hz, opened from the operator host on the LAN and held until the server closed them",
			es: "tres conexiones `GET /stream` contra el agente instalado a 10 Hz, abiertas desde el equipo del operador en la LAN y mantenidas hasta que el servidor las cerró",
		},
		source: "site/src/content/docs/reference/http.mdx",
	},
	// The naive approach, measured once so the agent's cost has something to be
	// compared against: a busybox shell loop reading the agent's file set of
	// that date (seven files; the agent reads far more now) at the same rate,
	// which pays a fork per iteration where the Go agent pays none. Not
	// re-measured against today's source set. The read cost beside it is the
	// same campaign's ≈ 0.77 ms per sample.
	"busybox-2026-09-11": {
		...RB5009,
		kernel: "5.6.3",
		date: "2026-09-11",
		windowS: 60,
		conditions: {
			en: "a busybox shell loop reading the agent's file set of that date, seven files, at 10 Hz, one fork per iteration, in a container on the router; two 60 s runs",
			es: "un bucle de shell de busybox leyendo a 10 Hz el conjunto de ficheros que leía entonces el agente, siete ficheros, con un fork por iteración, en un contenedor del router; dos ejecuciones de 60 s",
		},
		source:
			"site/src/content/docs/cost/index.mdx; measured on the RB5009, RouterOS 7.24.2, kernel 5.6.3, 2026-09-11: 7 files at 10 Hz, 2.40 and 2.48 % of one core over two 60 s runs, cgroup cpu.stat over /proc/uptime",
	},
	"netns-2026-09-12": {
		...RB5009,
		date: "2026-09-12",
		conditions: {
			en: "`privileged=yes` does not change the network namespace",
			es: "`privileged=yes` no cambia el espacio de nombres de red",
		},
		source: "site/src/content/docs/limits/namespaces.mdx",
	},
	// The privileged discovery round: a container reading the host's /proc
	// once with privileged=yes, no agent. Its slabinfo is the one in
	// testdata/proc/rb5009, and internal/procfs/slabinfo.go cites its
	// nf_conntrack row.
	"discovery-2026-09-12": {
		...RB5009,
		kernel: "5.6.3",
		date: "2026-09-12",
		conditions: {
			en: "a privileged discovery container reading the host's `/proc` once, before the agent read slabinfo",
			es: "un contenedor de descubrimiento privilegiado leyendo una vez el `/proc` del host, antes de que el agente leyera slabinfo",
		},
		source:
			"site/src/content/docs/limits/namespaces.mdx; testdata/proc/rb5009/slabinfo (nf_conntrack 6582 active of 8075); the comment on Slab, internal/procfs/slabinfo.go",
	},
	"playbooks-2026-09-12": {
		...RB5009,
		kernel: "5.6.3",
		date: "2026-09-12",
		conditions: {
			en: "agent at 10 Hz in an ephemeral privileged container",
			es: "agente a 10 Hz en un contenedor privilegiado efímero",
		},
		source: "site/src/content/docs/playbooks/index.mdx",
	},
	// One recording, the one the walkthrough's chart is drawn from. The sample
	// count and span are the chart's own subtitle; the gaps and the skew have no
	// source outside the record pages' own prose, so they stay there and out of
	// this line.
	"record-2026-09-12": {
		...RB5009,
		date: "2026-09-12",
		conditions: {
			en: "a 60 s `record` at 10 Hz, 600 samples over 59.9 s, a RouterOS script loop started over ssh and the router log added as markers",
			es: "un `record` de 60 s a 10 Hz, 600 muestras en 59,9 s, un bucle de script de RouterOS lanzado por ssh y el log del router añadido como marcadores",
		},
		source:
			"site/src/content/docs/record/index.mdx; the recording's own .meta.json, which is not published",
	},
	// The recording the walkthrough's chart is drawn from: the router at rest,
	// with three notes typed into `record`'s own terminal. The sample count and
	// the span are the chart's subtitle; the per-panel readings the page quotes
	// are read off that chart and say so.
	"record-2026-09-16": {
		...RB5009,
		date: "2026-09-16",
		conditions: {
			en: "a 70 s `record` at 10 Hz, 700 samples over 69.9 s, the router otherwise at rest, three notes typed into `record`'s terminal",
			es: "un `record` de 70 s a 10 Hz, 700 muestras en 69,9 s, el router por lo demás en reposo, tres notas escritas en el terminal de `record`",
		},
		source:
			"site/src/content/docs/start/walkthrough.mdx; site/src/assets/walkthrough/rb5009-walkthrough.svg subtitle",
	},
	// The next three predate the project's own measurement pages, and the date
	// each carries is the one recorded beside it when it was measured. The ssh
	// cost is older than the agent: it was measured on 2026-08-26, while the
	// router still ran 7.24.1 (the 7.24.2 upgrade reboot was 2026-09-10), so it
	// names its own version.
	"ssh-connect": {
		...RB5009,
		routeros: "7.24.1",
		date: "2026-08-26",
		conditions: {
			en: "one ssh connect, for its duration",
			es: "una conexión ssh, mientras dura",
		},
		source:
			"site/src/content/docs/install/index.mdx; measured on the RB5009 on 2026-08-26 under RouterOS 7.24.1, before the 7.24.2 upgrade of 2026-09-10; the same cost showed in /tool profile on 7.24.2, 2026-09-11 (17–33 % for 1–2 snapshots)",
	},
	"relay-fetch": {
		...RB5009,
		kernel: "5.6.3",
		date: "2026-09-11",
		conditions: {
			en: "`/tool fetch output=user` called over the binary API",
			es: "`/tool fetch output=user` llamado por la API binaria",
		},
		source:
			"site/src/content/docs/install/reaching-the-agent.mdx; measured on the RB5009, RouterOS 7.24.2, kernel 5.6.3, 2026-09-11: truncates silently at 64 512 B for 64 K, 256 K, 1 M and 4 M bodies",
	},
	// The count and its latency are one set of ten calls. The pages place it
	// "about a day before" the slab reading of 2026-09-12; the day is the one recorded
	// with the ten calls.
	"api-conntrack": {
		...RB5009,
		kernel: "5.6.3",
		date: "2026-09-11",
		conditions: {
			en: "the connection table counted over the RouterOS binary API, `/ip/firewall/connection/print count-only`, ten calls",
			es: "la tabla de conexiones contada por la API binaria de RouterOS, `/ip/firewall/connection/print count-only`, diez llamadas",
		},
		source:
			"measured on the RB5009, RouterOS 7.24.2, kernel 5.6.3, 2026-09-11: 6 212 entries, median 1.3 ms, n = 10, min 1.1, first call 71 ms cold; site/src/content/docs/sinks/api-tier.mdx, playbooks/conntrack.mdx and reference/cli.mdx give that date; the time of day is not recorded",
	},
	// No date beside it in the source cited, so it renders "date not recorded"
	// wherever a page gives it provenance.
	"floors-overnight": {
		...RB5009,
		date: null,
		conditions: {
			en: "a 10.5 h capture at 50 Hz over one idle night, clock pinned",
			es: "una captura de 10,5 h a 50 Hz durante una noche en reposo, con el reloj fijo",
		},
		source: "site/src/content/docs/limits/source-floors.mdx (undated there)",
	},
	"squeeze-2026-09-16": {
		...RB5009,
		date: "2026-09-16",
		conditions: {
			en: "3 738 704 per-CPU samples over 24 h at 10 Hz, softnet `time_squeeze` per sample, 0 drops in the whole window",
			es: "3 738 704 muestras por CPU en 24 h a 10 Hz, `time_squeeze` de softnet por muestra, 0 descartes en toda la ventana",
		},
		source:
			"site/src/content/docs/sinks/detections.mdx; the same distribution is the comment on minSqueeze, internal/derive/derive.go",
	},
	"microburst-replay-2026-09-16": {
		...RB5009,
		date: "2026-09-16",
		conditions: {
			en: "the rule replayed over 6 h of stored samples, 863 944 rows, 4 CPUs",
			es: "la regla reejecutada sobre 6 h de muestras almacenadas, 863 944 filas, 4 CPU",
		},
		source:
			"site/src/content/docs/sinks/detections.mdx; the comment on minSqueeze, internal/derive/derive.go",
	},
	// The line size site/src/content/docs/install/layout.mdx rounds to 2.4 kB. The exact 2 439 B is
	// the comment on ApproxLineBytes, internal/agent/agent.go, which carries the
	// device and K=8 but not the date; site/src/content/docs/limits/index.mdx
	// gives the date, and the rest of the conditions were checked on the
	// reference device and are written down nowhere that publishes. It predates the PMU, buddyinfo and MTD sources and
	// the per-source floors.
	// 7.24.4, not the inherited 7.24.2: the router has not rebooted since
	// about 2026-09-18 22:43 UTC and ran 7.24.4 when that was read (see
	// RB5009_NOW), so it ran 7.24.4 all of 2026-09-19.
	"port-overflow-2026-09-19": {
		...RB5009,
		routeros: "7.24.4",
		date: "2026-09-19",
		conditions: {
			en: "ether1, the 2.5 GbE port to the NAS, over 10 s counter intervals; the before figures are the three hours preceding the fix and the after figures the 39 minutes following it, at the same load",
			es: "ether1, el puerto de 2,5 GbE del NAS, en intervalos de contador de 10 s; las cifras de antes son las tres horas previas al arreglo y las de después los 39 minutos siguientes, con la misma carga",
		},
		source:
			"a per-port audit of the RB5009 on 2026-09-19; the counters are RouterOS /interface/ethernet/print stats on ether1, read by the API tier every 10 s",
	},
	"line-size-2026-09-17": {
		...RB5009,
		date: "2026-09-17",
		conditions: {
			en: "one ring line with every source on, privileged, 10 Hz, 4 cores, `IRQ_TOP_K=8` — including the PMU and the sampler's own timing, which the 2026-09-12 measurement predates",
			es: "una línea del anillo con todas las fuentes activas, privileged, 10 Hz, 4 núcleos, `IRQ_TOP_K=8` —incluidos el PMU y la propia temporización del muestreador, que la medición del 2026-09-12 no tenía",
		},
		source:
			"internal/agent/agent.go, ApproxLineBytes: 3 230 B measured, charged from Go's 3 456 B size class. The 2 439 B of 2026-09-12 understated the ring by 35 %, which is why the budget check never bound.",
	},
	"line-size": {
		...RB5009,
		date: "2026-09-12",
		conditions: {
			en: "the mean pre-encoded ring line with every source of that date, the slow sources refreshed at 1 Hz, privileged, 10 Hz, 4 cores, `IRQ_TOP_K=8`",
			es: "la línea media precodificada del anillo con todas las fuentes de esa fecha, las lentas refrescadas a 1 Hz, privileged, 10 Hz, 4 núcleos, `IRQ_TOP_K=8`",
		},
		source:
			"internal/agent/agent.go, ApproxLineBytes (the exact 2 439 B, the device and K=8); site/src/content/docs/install/layout.mdx (rounded to about 2.4 kB); site/src/content/docs/limits/index.mdx (the date); the remaining conditions were checked on the reference device, no published document records them",
	},
	// The garbage-collector pair: the same agent, ring and sources, first under
	// a 14 MiB soft limit and then with room. The figures are
	// site/src/content/docs/about/status.mdx's; that page gives the date too.
	gc: {
		...RB5009,
		date: "2026-09-12",
		conditions: {
			en: "10 Hz, 300 s ring full, the same sources in both runs; only the memory limits differ",
			es: "10 Hz, anillo de 300 s lleno, las mismas fuentes en las dos ejecuciones; solo cambian los límites de memoria",
		},
		source: "site/src/content/docs/about/status.mdx; date from the same page",
	},
	// The question this campaign answers: is cpu-load a one-second average, as
	// this project says everywhere, or a longer one? Settled by correlating the
	// API series against the kernel tier's own busy ratio at 10 Hz.
	"cpu-load-window-2026-09-15": {
		...RB5009,
		kernel: "5.6.3",
		date: "2026-09-15",
		conditions: {
			en: "`/system/resource` polled at 1 Hz against the agent's per-core busy ratio at 10 Hz, two separate hours",
			es: "`/system/resource` consultado a 1 Hz frente a la proporción de ocupación por núcleo del agente a 10 Hz, en dos horas distintas",
		},
		source: "measured from the collector's own InfluxDB series",
	},
	// The image tar's size is a property of the build, not of the router: the
	// device fields are here only because every campaign has them, and
	// Provenance refuses both image campaigns so no page can print "Measured
	// on RB5009UG+S+" beside them.
	//
	// `image` is the published v1.2.1 release's arm64 image tar, the file the
	// router loads: 6 690 304 B, measured with `ls -l` on the release asset
	// (checksum verified against the signed checksums.txt) on 2026-09-24. The
	// agent binary inside it is 6 684 832 B; the image is the binary and ~5 KB
	// of manifest and tar headers. The other architectures' tars are
	// 7 214 592 B (armv5, armv7) and 7 222 784 B (amd64).
	image: {
		...RB5009,
		date: "2026-09-24",
		conditions: {
			en: "arm64 image tar of the published v1.2.1 release, the file the router loads",
			es: "tar de la imagen arm64 de la versión v1.2.1 publicada, el fichero que carga el router",
		},
		source:
			"release asset mikroscope-agent-arm64.tar of v1.2.1, 6 690 304 B, checked against the release's signed checksums.txt",
	},
	// `image-v100` is history: 6.1 MiB is the figure the 1.0.0 release notes
	// give; v1.0.0 was tagged 2026-09-16, which is a release date, not a
	// measurement date, so the date stays null.
	"image-v100": {
		...RB5009,
		date: null,
		conditions: {
			en: "agent image size of the 1.0.0 release",
			es: "tamaño de la imagen del agente de la versión 1.0.0",
		},
		source:
			'CHANGELOG.md [1.0.0], "Measured on the reference device" (v1.0.0 tagged 2026-09-16, a release date)',
	},
} satisfies Record<string, Campaign>;

export type CampaignId = keyof typeof campaigns;

/**
 * The bound `read.under2ms` is a share under: a whole tick's due sources read
 * in under this long (site/src/content/docs/cost/rate-ceiling.mdx). Kept beside the share, so the
 * copy quoting the share takes its bound from the same place.
 */
export const readBound = { value: 2, unit: "ms", digits: 0 } as const;

/**
 * The burst the landing's arithmetic is about: one core saturated for `onMs`
 * of a `windowMs` window, which is how often the RouterOS API reports
 * `cpu-load`. `burst.100msOn4Cores` is computed from it and the core count.
 */
export const burst = { onMs: 100, windowMs: 1000 } as const;

export const isCampaignId = (id: string): id is CampaignId =>
	Object.hasOwn(campaigns, id);

/** "4 × 1.4 GHz Cortex-A72", with the locale's decimal and no break inside. */
export function describeCpu(c: Campaign, lang: Lang): string {
	return unbreakable(
		`${c.cpu.cores} × ${formatNumber(c.cpu.ghz, lang, 1)} GHz ${c.cpu.core}`,
	);
}

/**
 * What a figure is, which decides what may be said beside it.
 *
 * - `reading`: taken off the device, in the named campaign.
 * - `derived`: arithmetic from a constant that campaign established (USER_HZ),
 *   so the copy around it must say "arithmetic", not "measured".
 * - `budget`: a design target. Nobody measured it, so it has no
 *   campaign, and nothing may render provenance for it.
 */
export type Measurement = {
	value: number;
	/**
	 * The top of a range, where the source gives one ("20–27 %"). `value` is
	 * then its bottom. A spread is part of the figure, so it is never averaged
	 * into one number here.
	 */
	max?: number;
	unit: Unit;
	/** Decimal places shown, which is the precision the source reports. */
	digits: number;
} & (
	| { kind: "reading" | "derived"; campaign: CampaignId }
	| { kind: "budget"; campaign: null }
);

export interface Run {
	key: "10hz" | "20hz" | "50hz" | "100hz" | "50hz-floor" | "100hz-floor";
	rateHz: number;
	/** 0 is the default per-source floors; otherwise every source at FLOOR_HZ. */
	floorHz: 0 | number;
	installDefault: boolean;
	cpuPct: number;
	usPerSample: number;
	rssMiB: number;
	slipped: number;
	/** Samples the window actually delivered — slippedPct's denominator. */
	samples: number;
	slippedPct: number;
	gaps: number;
	drops: number;
	/** The memory flags the run used, verbatim (site/src/content/docs/cost/rate-ceiling.mdx); `<RunFlags>` renders them. */
	flags: string;
}

// Every row of the 2026-09-18 campaign ran the shipped configuration and
// passed no memory flag at all: the 60 s ring is the default and the limit is
// derived from it (16 MiB at 10 and 20 Hz, 25 at 50, 48 at 100). The container
// cap stayed at the default 64M for all six, which the previous campaign could
// not do — it needed --memory-max 96M at 50 Hz and 128M at 100.
const FLAGS_DEFAULT =
	"(the defaults: --buffer 60, the derived --mem-limit-mb, --memory-max 64M)";

/** site/src/content/docs/cost/rate-ceiling.mdx, all from campaign rates-2026-09-18. */
export const runs: readonly Run[] = [
	{
		key: "10hz",
		rateHz: 10,
		floorHz: 0,
		installDefault: true,
		cpuPct: 2.69,
		usPerSample: 2685,
		rssMiB: 13.2,
		slipped: 0,
		samples: 3000,
		slippedPct: 0.0,
		gaps: 0,
		drops: 0,
		flags: FLAGS_DEFAULT,
	},
	{
		key: "20hz",
		rateHz: 20,
		floorHz: 0,
		installDefault: false,
		cpuPct: 4.61,
		usPerSample: 2303,
		rssMiB: 15.4,
		slipped: 0,
		samples: 6000,
		slippedPct: 0.0,
		gaps: 0,
		drops: 0,
		flags: FLAGS_DEFAULT,
	},
	{
		key: "50hz",
		rateHz: 50,
		floorHz: 0,
		installDefault: false,
		cpuPct: 9.63,
		usPerSample: 1926,
		rssMiB: 23.3,
		slipped: 0,
		samples: 14999,
		slippedPct: 0.0,
		gaps: 0,
		drops: 0,
		flags: FLAGS_DEFAULT,
	},
	{
		key: "100hz",
		rateHz: 100,
		floorHz: 0,
		installDefault: false,
		cpuPct: 16.83,
		usPerSample: 1684,
		rssMiB: 45.7,
		slipped: 5,
		samples: 30000,
		slippedPct: 0.017,
		gaps: 0,
		drops: 0,
		flags: FLAGS_DEFAULT,
	},
	{
		key: "50hz-floor",
		rateHz: 50,
		floorHz: 50,
		installDefault: false,
		cpuPct: 22.56,
		usPerSample: 4511,
		rssMiB: 25.1,
		slipped: 4,
		samples: 15000,
		slippedPct: 0.027,
		gaps: 0,
		drops: 0,
		flags: FLAGS_DEFAULT,
	},
	{
		key: "100hz-floor",
		rateHz: 100,
		floorHz: 100,
		installDefault: false,
		cpuPct: 42.7,
		usPerSample: 4270,
		rssMiB: 49.5,
		slipped: 178,
		samples: 29996,
		slippedPct: 0.593,
		gaps: 0,
		drops: 0,
		flags: FLAGS_DEFAULT,
	},
];

export type RunKey = Run["key"];

export const isRunKey = (key: string): key is RunKey =>
	runs.some((r) => r.key === key);

const fixed = {
	// The design budget (site/src/content/docs/cost/index.mdx): a target, not a reading.
	"budget.cpu": {
		kind: "budget",
		value: 2,
		unit: "%",
		digits: 0,
		campaign: null,
	},
	"budget.rss": {
		kind: "budget",
		value: 16,
		unit: "MiB",
		digits: 0,
		campaign: null,
	},
	"budget.image": {
		kind: "budget",
		value: 8,
		unit: "MiB",
		digits: 0,
		campaign: null,
	},
	"image.size": {
		kind: "reading",
		value: 6.38,
		unit: "MiB",
		digits: 2,
		campaign: "image",
	},
	"image.size.v100": {
		kind: "reading",
		value: 6.1,
		unit: "MiB",
		digits: 1,
		campaign: "image-v100",
	},
	// site/src/content/docs/cost/index.mdx. The shell loop's own cost, and the cost of the reads it
	// makes: the difference between the two is the fork, the pipes and the
	// `sh` arithmetic, not the /proc reads, which both approaches pay.
	// The two 60 s runs of 2026-09-11, 2.40 and 2.48 %.
	"busybox.cpu": {
		kind: "reading",
		value: 2.4,
		max: 2.48,
		unit: "%",
		digits: 2,
		campaign: "busybox-2026-09-11",
	},
	"procread.ms": {
		kind: "reading",
		value: 0.77,
		unit: "ms",
		digits: 2,
		campaign: "busybox-2026-09-11",
	},
	// site/src/content/docs/cost/rate-ceiling.mdx: at the DEFAULT floors a tick reads only its due
	// sources, so the copy must not say "every source".
	"read.under2ms": {
		kind: "reading",
		value: 97.7,
		unit: "%",
		digits: 1,
		campaign: "rates-2026-09-18",
	},
	// site/src/content/docs/cost/rate-ceiling.mdx: the same 100 Hz window with
	// FLOOR_HZ, where not one read of 29 994 came in under 2 ms.
	"read.floorOver5ms": {
		kind: "reading",
		value: 1.45,
		unit: "%",
		digits: 2,
		campaign: "rates-2026-09-18",
	},
	// site/src/content/docs/cost/rate-ceiling.mdx: the same 100 Hz window at the
	// DEFAULT floors, the mean read against the worst. Both were first published
	// with this campaign's table in 0e5321c (1.0.8), and the page is the only
	// record: one window, no spread beyond the pair itself.
	"read.100hz.meanMs": {
		kind: "reading",
		value: 1.2,
		unit: "ms",
		digits: 1,
		campaign: "rates-2026-09-18",
	},
	"read.100hz.worstMs": {
		kind: "reading",
		value: 17.9,
		unit: "ms",
		digits: 1,
		campaign: "rates-2026-09-18",
	},
	// The traffic the measurement campaigns ran under, so a reader knows what
	// "ordinary" meant here: the mean WAN receive rate over the 2026-09-18
	// campaign's 44 minutes, peaks to 933 Mbit/s. The 2026-09-15 campaign ran
	// in the evening at about 30 Mbit/s; the name lost the time of day when
	// the campaign moved to the morning. The source says "about", and so must
	// the copy around it.
	"load.ordinary": {
		kind: "reading",
		value: 19,
		unit: "Mbit/s",
		digits: 0,
		campaign: "rates-2026-09-18",
	},
	// Arithmetic from USER_HZ = 100 (site/src/content/docs/limits/index.mdx): one tick is 10 ms, so a
	// 100 ms sample holds 10 ticks per core and resolves one core in 10 % steps.
	"tick.stepOneCore": {
		kind: "derived",
		value: 10,
		unit: "%",
		digits: 0,
		campaign: "kernel-2026-09-11",
	},
	"tick.ms": {
		kind: "derived",
		value: 10,
		unit: "ms",
		digits: 0,
		campaign: "kernel-2026-09-11",
	},
	// A different quantity that happens to share the 2.5 % of a four-core step:
	// one core busy for 100 ms of a 1 s window is 100 / 1000 of that core, and
	// that core is one of four, so the four-core, one-second average moves by
	// 0.1 / 4 = 2.5 %. Kept separate so a change to either definition cannot
	// silently change the other.
	"burst.100msOn4Cores": {
		kind: "derived",
		value: (burst.onMs / burst.windowMs / RB5009.cpu.cores) * 100,
		unit: "%",
		digits: 1,
		campaign: "kernel-2026-09-11",
	},
	// site/src/content/docs/about/status.mdx: MEM_LIMIT_MB 14, then --mem-limit-mb 40 --memory-max 64M.
	"gc.tight.cpu": {
		kind: "reading",
		value: 9.38,
		unit: "%",
		digits: 2,
		campaign: "gc",
	},
	"gc.tight.us": {
		kind: "reading",
		value: 9374,
		unit: "µs",
		digits: 0,
		campaign: "gc",
	},
	"gc.roomy.cpu": {
		kind: "reading",
		value: 1.39,
		unit: "%",
		digits: 2,
		campaign: "gc",
	},
	"gc.roomy.us": {
		kind: "reading",
		value: 1388,
		unit: "µs",
		digits: 0,
		campaign: "gc",
	},
	// site/src/content/docs/limits/index.mdx, the same arithmetic as tick.stepOneCore: ten ticks per
	// core in 100 ms, four cores, so the four-core average moves by 2.5 %; and
	// one core over a whole second holds 100 ticks, so 1 %.
	"tick.stepFourCores": {
		kind: "derived",
		value: 2.5,
		unit: "%",
		digits: 1,
		campaign: "kernel-2026-09-11",
	},
	"tick.stepOneSecond": {
		kind: "derived",
		value: 1,
		unit: "%",
		digits: 0,
		campaign: "kernel-2026-09-11",
	},
	// site/src/content/docs/install/index.mdx.
	"ssh.connectCpu": {
		kind: "reading",
		value: 20,
		max: 27,
		unit: "%",
		digits: 0,
		campaign: "ssh-connect",
	},
	// site/src/content/docs/reference/http.mdx: how long a /stream connection
	// lives before the server's WriteTimeout closes it, and how many lines it
	// carried in that time at 10 Hz.
	"stream.lifetime": {
		kind: "reading",
		value: 30.01,
		max: 30.06,
		unit: "s",
		digits: 2,
		campaign: "stream-2026-09-21",
	},
	"stream.lines": {
		kind: "reading",
		value: 304,
		max: 306,
		unit: "",
		digits: 0,
		campaign: "stream-2026-09-21",
	},
	// site/src/content/docs/install/reaching-the-agent.mdx: a reply is truncated there, silently.
	"relay.replyMaxBytes": {
		kind: "reading",
		value: 64512,
		unit: "B",
		digits: 0,
		campaign: "relay-fetch",
	},
	// site/src/content/docs/playbooks/loop.mdx (the rate before the fix, the rate after it,
	// and the gap between reflected frames, the STP hello interval).
	"loop.eventsBefore": {
		kind: "reading",
		value: 1.49,
		unit: "/s",
		digits: 2,
		campaign: "playbooks-2026-09-12",
	},
	"loop.eventsAfter": {
		kind: "reading",
		value: 0.03,
		unit: "/s",
		digits: 2,
		campaign: "playbooks-2026-09-12",
	},
	"loop.helloGap": {
		kind: "reading",
		value: 2.0,
		max: 2.01,
		unit: "s",
		digits: 2,
		campaign: "playbooks-2026-09-12",
	},
	// site/src/content/docs/limits/namespaces.mdx: the nf_conntrack slab's active
	// objects read from inside the container while its own namespace said 0.
	"conntrack.slab": {
		kind: "reading",
		value: 6287,
		unit: "",
		digits: 0,
		campaign: "playbooks-2026-09-12",
	},
	// The same cache read the same day by the privileged discovery container,
	// before the agent: a different moment from conntrack.slab, not a second
	// value for it.
	"conntrack.slabDiscovery": {
		kind: "reading",
		value: 6582,
		unit: "",
		digits: 0,
		campaign: "discovery-2026-09-12",
	},
	// site/src/content/docs/sinks/api-tier.mdx ("the day before"); the
	// latency is the median of ten calls, min 1.1 ms, the first (cold) 71 ms.
	"conntrack.api": {
		kind: "reading",
		value: 6212,
		unit: "",
		digits: 0,
		campaign: "api-conntrack",
	},
	"api.conntrackMs": {
		kind: "reading",
		value: 1.3,
		unit: "ms",
		digits: 1,
		campaign: "api-conntrack",
	},
	// site/src/content/docs/sinks/derive.mdx; the source says "about", and so must the copy.
	"squeeze.backgroundOne": {
		kind: "reading",
		value: 11.2,
		unit: "%",
		digits: 1,
		campaign: "squeeze-2026-09-16",
	},
	"squeeze.backgroundTwo": {
		kind: "reading",
		value: 1.2,
		unit: "%",
		digits: 1,
		campaign: "squeeze-2026-09-16",
	},
	// Exactly three squeezes: 0.212 % in the minSqueeze comment. Three or more
	// would be 0.33 % (3: 0.212, 4: 0.063, 5 or more: 0.055).
	"squeeze.exactlyThree": {
		kind: "reading",
		value: 0.21,
		unit: "%",
		digits: 2,
		campaign: "squeeze-2026-09-16",
	},
	"microburst.firesPerHourAtTwo": {
		kind: "reading",
		value: 77.7,
		unit: "/h",
		digits: 1,
		campaign: "microburst-replay-2026-09-16",
	},
	"microburst.firesPerHourAtThree": {
		kind: "reading",
		value: 0.5,
		unit: "/h",
		digits: 1,
		campaign: "microburst-replay-2026-09-16",
	},
	// site/src/content/docs/limits/source-floors.mdx; internal/agent/source.go, the per-source floors comment in ProcSource; the source says "~", and so must the copy.
	"thermal.quantumC": {
		kind: "reading",
		value: 0.42,
		unit: "°C",
		digits: 2,
		campaign: "floors-overnight",
	},
	// What the ring BUDGET charges per line, rounded for prose: the allocator
	// size class, not the measured line. site/src/content/docs/install/layout.mdx.
	"ring.lineKB": {
		kind: "reading",
		value: 3.5,
		unit: "kB",
		digits: 1,
		campaign: "line-size-2026-09-17",
	},
	// The measured line. It is NO LONGER the same number as ApproxLineBytes:
	// since 2026-09-17 the budget charges the 3 456 B size class the allocator
	// rounds this up to, because that is what the heap actually pays.
	"ring.lineBytes": {
		kind: "reading",
		value: 3230,
		unit: "B",
		digits: 0,
		campaign: "line-size-2026-09-17",
	},
	// The NAS port's receive overflow, before and after the shaper. See the
	// port-errors playbook.
	"overflow.shareBefore": {
		kind: "reading",
		value: 0.502,
		unit: "%",
		digits: 3,
		campaign: "port-overflow-2026-09-19",
	},
	"overflow.perHourBefore": {
		kind: "reading",
		value: 5399,
		unit: "",
		digits: 0,
		campaign: "port-overflow-2026-09-19",
	},
	// What makes it a microburst rather than a load problem: the link was this
	// full, on average, in the 10 s intervals that overflowed.
	"overflow.occupancy": {
		kind: "reading",
		value: 0.36,
		unit: "%",
		digits: 2,
		campaign: "port-overflow-2026-09-19",
	},
	// The same 11.2 MB per 10 s interval, as a rate: what makes it legible is
	// that it sits against a 2 500 Mbit/s link.
	"overflow.burstRate": {
		kind: "reading",
		value: 8.96,
		unit: "Mbit/s",
		digits: 2,
		campaign: "port-overflow-2026-09-19",
	},
	// internal/agent/agent.go, ApproxLineBytes — the charged figure exactly.
	"ring.lineChargedBytes": {
		kind: "reading",
		value: 3456,
		unit: "B",
		digits: 0,
		campaign: "line-size-2026-09-17",
	},
	// How many samples one relay pull may carry. NOT measured: it is
	// RelayMax x 100 / (line x headroom), and it moves whenever the line does.
	// It is here, with the assertion below, because it drifted silently once:
	// the line grew from 2 560 B to 3 456 B in 1.0.5, the binary began printing
	// 13, and six pages went on saying 18 until the 2026-09-19 audit.
	"relay.maxBatch": {
		kind: "reading",
		value: 13,
		unit: "",
		digits: 0,
		campaign: "line-size-2026-09-17",
	},
	// The trailing mean RouterOS `cpu-load` fits best in the first hour, and the
	// delay it reaches the API with; sinks/api-tier states the answer from these.
	"cpuLoad.window1": {
		kind: "reading",
		value: 1.0,
		unit: "s",
		digits: 1,
		campaign: "cpu-load-window-2026-09-15",
	},
	"cpuLoad.delay1": {
		kind: "reading",
		value: 0.6,
		unit: "s",
		digits: 1,
		campaign: "cpu-load-window-2026-09-15",
	},
	// Pearson r of that fit against the kernel's busy ratio, over this many 1 Hz
	// API samples.
	"cpuLoad.r1": {
		kind: "reading",
		value: 0.9825,
		unit: "",
		digits: 4,
		campaign: "cpu-load-window-2026-09-15",
	},
	"cpuLoad.samples1": {
		kind: "reading",
		value: 3499,
		unit: "",
		digits: 0,
		campaign: "cpu-load-window-2026-09-15",
	},
	// The same fit in a second, separate hour: the spread between the two is the
	// figure's spread.
	"cpuLoad.window2": {
		kind: "reading",
		value: 1.1,
		unit: "s",
		digits: 1,
		campaign: "cpu-load-window-2026-09-15",
	},
	"cpuLoad.delay2": {
		kind: "reading",
		value: 0.1,
		unit: "s",
		digits: 1,
		campaign: "cpu-load-window-2026-09-15",
	},
	"cpuLoad.r2": {
		kind: "reading",
		value: 0.9734,
		unit: "",
		digits: 4,
		campaign: "cpu-load-window-2026-09-15",
	},
	"cpuLoad.samples2": {
		kind: "reading",
		value: 3594,
		unit: "",
		digits: 0,
		campaign: "cpu-load-window-2026-09-15",
	},
} satisfies Record<string, Measurement>;

type RunMetric = "cpu" | "rss" | "usPerSample" | "slippedPct";

export type MeasurementId = keyof typeof fixed | `run.${RunKey}.${RunMetric}`;

const fromRuns = Object.fromEntries(
	runs.flatMap((r) => {
		const base = { kind: "reading", campaign: "rates-2026-09-18" } as const;
		return [
			[`run.${r.key}.cpu`, { ...base, value: r.cpuPct, unit: "%", digits: 2 }],
			[
				`run.${r.key}.rss`,
				{ ...base, value: r.rssMiB, unit: "MiB", digits: 1 },
			],
			[
				`run.${r.key}.usPerSample`,
				{ ...base, value: r.usPerSample, unit: "µs", digits: 0 },
			],
			// Rendered rather than typed into prose: the three published
			// percentages were a consistent x0.75 out until 2026-09-19.
			[
				`run.${r.key}.slippedPct`,
				{ ...base, value: r.slippedPct, unit: "%", digits: 3 },
			],
		];
	}),
);

export const measurements = { ...fixed, ...fromRuns } as Record<
	MeasurementId,
	Measurement
>;

export const isMeasurementId = (id: string): id is MeasurementId =>
	Object.hasOwn(measurements, id);

/*
 * Sentences the site states in prose that are true only while the data says
 * so. Each throws at build time the day a repeated run makes it false, rather
 * than shipping a page that contradicts its own table.
 */
const installDefault = runs.find((r) => r.installDefault);
if (installDefault === undefined)
	throw new Error("measurements.ts: no run is the install default");
// "inside on memory, over on CPU": cost/index.mdx, start/questions.mdx and
// about/status.mdx (both locales), src/data/home.ts readout.claim, and the
// README's cost paragraph, which is typed by hand. It was over on both until the 60 s ring and
// the derived memory limit of 1.0.6, and the assertion that guarded the older
// sentence is what caught the day the prose stopped being true.
if (!(installDefault.cpuPct > measurements["budget.cpu"].value)) {
	throw new Error(
		"measurements.ts: the install-default run is no longer above the CPU budget; rewrite cost/index.mdx, start/questions.mdx and about/status.mdx (both locales), home.ts and README.md, which say it is",
	);
}
if (!(installDefault.rssMiB <= measurements["budget.rss"].value)) {
	throw new Error(
		"measurements.ts: the install-default run no longer fits the memory budget; rewrite cost/index.mdx, start/questions.mdx and about/status.mdx (both locales), home.ts and README.md, which say it does",
	);
}
// Every slipped percentage is its own count over its own window. Stated because
// it was wrong: 0e5321c published three percentages computed against a 400 s
// denominator for 300 s windows, a consistent x0.75, and the page printed a
// count and a percentage that did not divide to each other.
for (const r of runs) {
	const pct = Math.round((r.slipped / r.samples) * 100 * 1000) / 1000;
	if (Math.abs(pct - r.slippedPct) > 0.001) {
		throw new Error(
			`measurements.ts: run ${r.key} reports ${r.slippedPct} % slipped but ${r.slipped}/${r.samples} is ${pct} %`,
		);
	}
}
// The relay cap follows the line size, and the pages quote it. internal/transport
// /transport.go: RelayMax 64 512 B, relayHeadroomPercent 134. Recomputed here so
// that growing the line cannot leave the prose behind a second time.
{
	const relayMax = 64512;
	const headroomPercent = 134;
	const cap = Math.floor(
		(relayMax * 100) /
			(measurements["ring.lineChargedBytes"].value * headroomPercent),
	);
	if (cap !== measurements["relay.maxBatch"].value) {
		throw new Error(
			`measurements.ts: the relay cap is now ${cap}, not ${measurements["relay.maxBatch"].value}. Update relay.maxBatch and the pages that quote it: install/reaching-the-agent, record/index, reference/cli, reference/http and sinks/index, in both locales.`,
		);
	}
}
// "on RouterOS 7.24.2 and later 7.24.4": src/data/home.ts notClaimed.body
// names both versions, which says nothing if they are the same.
if (RB5009_NOW.routeros === RB5009.routeros) {
	throw new Error(
		"measurements.ts: RB5009_NOW.routeros equals RB5009.routeros; rewrite home.ts notClaimed.body, which names both",
	);
}
// "nothing was lost": cost/rate-ceiling.mdx (both locales), src/data/home.ts cost.after.
if (runs.some((r) => r.gaps !== 0 || r.drops !== 0)) {
	throw new Error(
		"measurements.ts: a run reports gaps or drops; rewrite cost/rate-ceiling.mdx and home.ts, which say none did",
	);
}
