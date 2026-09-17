// @ts-check
import { defineConfig } from "astro/config";
import starlight from "@astrojs/starlight";
import sitemap from "@astrojs/sitemap";
import starlightLinksValidator from "starlight-links-validator";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { decodedFragments } from "./src/lib/decoded-fragments.mjs";
import { inlineCodeNowrap } from "./src/lib/inline-code.mjs";

const siteRoot = fileURLToPath(new URL(".", import.meta.url));
const siteBase = "/mikroscope";

// Where the documentation is advertised, which is not where it is served.
//
// `site` below has to name GitHub Pages, because that is where the bytes are
// and it is what the canonical link, the sitemap and the hreflang pairs must
// agree with. What a reader is handed is the other one: jmrp.io 301s
// /docs/mikroscope and everything under it to the Pages URL, so a link that
// carries it reaches the same page and puts the canonical domain in the places
// a mention counts. Every absolute link written OUTSIDE the site — the README,
// the generated docs/, the dashboards — uses this one; the site's own internal
// links stay relative and never see it.
export const publicDocs = "https://jmrp.io/docs/mikroscope";

/**
 * The newest git commit date for the file behind a sitemap URL, so
 * `sitemap-0.xml` carries a real per-page `<lastmod>`. Starlight omits it.
 * @param {string} pathname
 * @returns {string | undefined}
 */
function getLastmod(pathname) {
	const slug = pathname
		.replace(new RegExp(`^${siteBase}/?`), "")
		.replace(/\/$/, "");
	const candidates =
		slug === ""
			? ["src/content/docs/index.mdx"]
			: [`src/content/docs/${slug}.mdx`, `src/content/docs/${slug}/index.mdx`];
	for (const relativePath of candidates) {
		try {
			const out = execFileSync(
				"git",
				["log", "-1", "--format=%cI", "--", relativePath],
				{
					cwd: siteRoot,
					encoding: "utf-8",
					stdio: ["ignore", "pipe", "ignore"],
				},
			).trim();
			if (out) return out;
		} catch {
			// Not a git checkout, or the file has no history yet.
		}
	}
	return undefined;
}

/**
 * Adds the site's own hast plugins to the Sätteri processor Starlight uses,
 * the same way Starlight adds its own (it pushes onto
 * `config.markdown.processor.options.hastPlugins` at config setup).
 * @returns {import("astro").AstroIntegration}
 */
function siteMarkdownPlugins() {
	return {
		name: "mikroscope-markdown-plugins",
		hooks: {
			"astro:config:setup": ({ config, logger }) => {
				const processor = /** @type {any} */ (config.markdown)?.processor;
				const plugins = processor?.options?.hastPlugins;
				if (!Array.isArray(plugins)) {
					logger.warn(
						"markdown processor has no hastPlugins list; inline code is not kept together and encoded fragments stay encoded",
					);
					return;
				}
				plugins.push(inlineCodeNowrap(), decodedFragments());
			},
		},
	};
}

export default defineConfig({
	site: "https://jmrplens.github.io/mikroscope",
	base: siteBase,
	trailingSlash: "always",
	integrations: [
		siteMarkdownPlugins(),
		starlight({
			title: "mikroscope",
			description:
				"Sub-second kernel telemetry from inside a MikroTik router, with the cost of the observer measured rather than claimed.",
			plugins: [
				starlightLinksValidator({
					errorOnRelativeLinks: false,
					errorOnFallbackPages: false,
				}),
			],
			defaultLocale: "root",
			locales: {
				root: { label: "English", lang: "en" },
				es: { label: "Español", lang: "es" },
			},
			favicon: "/favicon.svg",
			components: {
				// The mark inlined so the palette can paint its two tones.
				SiteTitle: "./src/components/overrides/SiteTitle.astro",
				// Theme and language as header buttons below md, where Starlight
				// hides them in the drawer.
				Header: "./src/components/overrides/Header.astro",
				// The drawer and its button on every page, the splash landing included.
				PageFrame: "./src/components/overrides/PageFrame.astro",
				// The JSON-LD graph, and the links to the markdown twin and llms.txt.
				Head: "./src/components/overrides/Head.astro",
			},
			// The mark in the hero's image slot, on every page with a hero.
			routeMiddleware: "./src/routeMiddleware.ts",
			head: [
				// Starlight sets og:title and og:description itself; the image is
				// the one thing it cannot know.
				{
					tag: "meta",
					attrs: {
						property: "og:image",
						content: "https://jmrplens.github.io/mikroscope/og.png",
					},
				},
				{ tag: "meta", attrs: { property: "og:image:width", content: "1200" } },
				{ tag: "meta", attrs: { property: "og:image:height", content: "630" } },
				{
					tag: "meta",
					attrs: { name: "twitter:card", content: "summary_large_image" },
				},
				{
					tag: "meta",
					attrs: {
						name: "twitter:image",
						content: "https://jmrplens.github.io/mikroscope/og.png",
					},
				},
				{
					tag: "link",
					attrs: {
						rel: "icon",
						href: "/mikroscope/favicon.ico",
						sizes: "32x32",
					},
				},
				{
					tag: "link",
					attrs: {
						rel: "apple-touch-icon",
						href: "/mikroscope/apple-touch-icon.png",
					},
				},
			],
			social: [
				{
					icon: "github",
					label: "GitHub",
					href: "https://github.com/jmrplens/mikroscope",
				},
			],
			editLink: {
				baseUrl: "https://github.com/jmrplens/mikroscope/edit/main/site/",
			},
			lastUpdated: true,
			pagination: true,
			customCss: [
				// theme.css first and unlayered, so its `:root` beats the tokens
				// Starlight declares inside `@layer starlight.base` without an
				// `!important`, and so every sheet after it can read them.
				"./src/styles/theme.css",
				"./src/styles/typography.css",
				"./src/styles/chrome.css",
				// The content components' panels; `.ms-scroll` lives here now.
				"./src/styles/components.css",
				// The landing's blocks, which no other page renders.
				"./src/styles/home.css",
				// Last: focus rings and the skip link must win.
				"./src/styles/a11y.css",
			],
			// External, not inline. Inline, Expressive Code puts a <style> inside
			// each MDX page's first code fence, and @astrojs/markdown-satteri 0.4.1
			// collapses that style's text into a `set:html` property that nothing
			// renders: the built /cost/ page shipped `<style set:html="…">`, its
			// fence lost `overflow-x: auto`, and the page scrolled sideways to
			// 845 px at a 390 px viewport (measured 2026-09-15). The `<Code>`
			// component path was unaffected.
			expressiveCode: { emitExternalStylesheet: true },
			// The information architecture, in reading order. Every English label
			// has its Spanish translation beside it, so a page added here without
			// one is visible in review.
			sidebar: [
				{
					label: "Start here",
					translations: { es: "Empezar aquí" },
					items: [
						{
							label: "What it is",
							translations: { es: "Qué es" },
							slug: "start",
						},
						{
							label: "Five minutes with a router",
							translations: { es: "Cinco minutos con un router" },
							slug: "start/walkthrough",
						},
					],
				},
				{
					label: "What it costs",
					translations: { es: "Lo que cuesta" },
					items: [
						{
							label: "The cost of the observer",
							translations: { es: "El coste del observador" },
							slug: "cost",
						},
						{
							label: "The rate ceiling",
							translations: { es: "El techo de muestreo" },
							slug: "cost/rate-ceiling",
						},
						{
							label: "What the numbers do not say",
							translations: { es: "Lo que los números no dicen" },
							slug: "cost/limits",
						},
					],
				},
				{
					label: "Install",
					translations: { es: "Instalar" },
					items: [
						{
							label: "Installing the agent",
							translations: { es: "Instalar el agente" },
							slug: "install",
						},
						{
							label: "Getting the CLI",
							translations: { es: "Tener la CLI" },
							slug: "install/cli",
						},
						{
							label: "What the router needs",
							translations: { es: "Lo que necesita el router" },
							slug: "install/prerequisites",
						},
						{
							label: "Four ways to install",
							translations: { es: "Cuatro formas de instalar" },
							slug: "install/routes",
						},
						{
							label: "The two firewall traps",
							translations: { es: "Las dos trampas del cortafuegos" },
							slug: "install/firewall",
						},
						{
							label: "Where things go",
							translations: { es: "Dónde va cada cosa" },
							slug: "install/layout",
						},
						{
							label: "Reaching the agent",
							translations: { es: "Llegar al agente" },
							slug: "install/reaching-the-agent",
						},
					],
				},
				{
					label: "Record and capture",
					translations: { es: "Grabar y capturar" },
					items: [
						{
							label: "Record, mark, plot",
							translations: { es: "Grabar, marcar, dibujar" },
							slug: "record",
						},
						{
							label: "Triggered capture",
							translations: { es: "Captura por disparo" },
							slug: "record/triggers",
						},
					],
				},
				{
					label: "Collector and sinks",
					translations: { es: "Colector y destinos" },
					items: [
						{
							label: "The collector",
							translations: { es: "El colector" },
							slug: "sinks",
						},
						{ label: "Prometheus", slug: "sinks/prometheus" },
						{ label: "InfluxDB 3", slug: "sinks/influxdb" },
						{
							label: "The file and the other sinks",
							translations: { es: "El fichero y los demás destinos" },
							slug: "sinks/other",
						},
						{
							label: "The RouterOS API tier",
							translations: { es: "La capa de la API de RouterOS" },
							slug: "sinks/api-tier",
						},
						{
							label: "What the collector derives",
							translations: { es: "Lo que deriva el colector" },
							slug: "sinks/derive",
						},
						{
							label: "Detections",
							translations: { es: "Detecciones" },
							slug: "sinks/detections",
						},
						{
							label: "The device-info stream",
							translations: { es: "El flujo de datos del equipo" },
							slug: "sinks/device-info",
						},
					],
				},
				{
					label: "Dashboards",
					translations: { es: "Paneles" },
					items: [
						{
							label: "Two dashboards, one panel list",
							translations: { es: "Dos paneles, una sola lista" },
							slug: "dashboards",
						},
						{
							label: "Import and check",
							translations: { es: "Importar y comprobar" },
							slug: "dashboards/import-and-check",
						},
						{
							label: "Alert rules",
							translations: { es: "Reglas de alerta" },
							slug: "dashboards/alerts",
						},
					],
				},
				{
					label: "Reading the data",
					translations: { es: "Leer los datos" },
					items: [
						{
							label: "How to read what it shows",
							translations: { es: "Cómo leer lo que muestra" },
							slug: "playbooks",
						},
						{
							label: "The shape of an idle router",
							translations: { es: "La forma de un router en reposo" },
							slug: "playbooks/idle",
						},
						{
							label: "A loop only the kernel could see",
							translations: { es: "Un bucle que solo veía el kernel" },
							slug: "playbooks/loop",
						},
						{
							label: "RouterOS ports and kernel names",
							translations: { es: "Puertos de RouterOS y nombres del kernel" },
							slug: "playbooks/port-names",
						},
						{
							label: "A CPU-bound workload",
							translations: { es: "Una carga limitada por CPU" },
							slug: "playbooks/cpu",
						},
						{
							label: "A packet flood",
							translations: { es: "Una inundación de paquetes" },
							slug: "playbooks/packet-flood",
						},
						{
							label: "Flash wear",
							translations: { es: "Desgaste de la flash" },
							slug: "playbooks/flash-wear",
						},
						{
							label: "Conntrack without the API",
							translations: { es: "Conntrack sin la API" },
							slug: "playbooks/conntrack",
						},
					],
				},
				{
					label: "Limits",
					translations: { es: "Límites" },
					items: [
						{
							label: "The resolution floor is the kernel's",
							translations: { es: "El suelo de resolución es del kernel" },
							slug: "limits",
						},
						{
							label: "The router's CPU, the container's network",
							translations: { es: "La CPU del router, la red del contenedor" },
							slug: "limits/namespaces",
						},
						{
							label: "What privileged buys",
							translations: { es: "Lo que aporta privileged" },
							slug: "limits/privileged",
						},
						{
							label: "Each source at its own floor",
							translations: { es: "Cada fuente a su propio suelo" },
							slug: "limits/source-floors",
						},
					],
				},
				{
					label: "Security",
					translations: { es: "Seguridad" },
					items: [
						{
							label: "What runs where",
							translations: { es: "Qué se ejecuta dónde" },
							slug: "security",
						},
						{
							label: "The API user",
							translations: { es: "El usuario de la API" },
							slug: "security/api-user",
						},
						{
							label: "What --expose opens",
							translations: { es: "Lo que abre --expose" },
							slug: "security/expose",
						},
						{
							label: "What the installer refuses",
							translations: { es: "Lo que el instalador rechaza" },
							slug: "security/installer",
						},
					],
				},
				{
					label: "Reference",
					translations: { es: "Referencia" },
					collapsed: true,
					items: [
						{
							label: "Commands and flags",
							translations: { es: "Órdenes y opciones" },
							slug: "reference/cli",
						},
						{
							label: "Environment variables",
							translations: { es: "Variables de entorno" },
							slug: "reference/environment",
						},
						{
							label: "The agent's HTTP endpoints",
							translations: { es: "Los endpoints HTTP del agente" },
							slug: "reference/http",
						},
						{
							label: "Prometheus metric families",
							translations: { es: "Familias de métricas de Prometheus" },
							slug: "reference/metrics",
						},
						{
							label: "InfluxDB and SQL measurements",
							translations: { es: "Medidas de InfluxDB y SQL" },
							slug: "reference/measurements",
						},
						{
							label: "How the project tests itself",
							translations: { es: "Cómo se prueba el proyecto" },
							slug: "reference/testing",
						},
					],
				},
				{
					label: "About",
					translations: { es: "Acerca de" },
					collapsed: true,
					items: [
						{
							label: "Where the project stands",
							translations: { es: "En qué punto está" },
							slug: "about/status",
						},
						{
							label: "The mark",
							translations: { es: "La marca" },
							slug: "about/brand",
						},
						{
							label: "Lineage and licence",
							translations: { es: "Linaje y licencia" },
							slug: "about/lineage",
						},
					],
				},
			],
		}),
		sitemap({
			// The not-found pages are for a mistyped address, not for an index:
			// /404/ is what GitHub Pages serves, /es/404/ is its twin for the
			// language menu.
			filter: (page) => !/\/404\/?$/.test(new URL(page).pathname),
			serialize: (item) => ({
				...item,
				lastmod: getLastmod(new URL(item.url).pathname),
			}),
		}),
	],
});
