/// <reference path="../.astro/types.d.ts" />

// Starlight 0.42.0 resolves `virtual:starlight/components/*` to the component a
// user override names, or to its own, but ships no type declaration for those
// modules, so `astro check` failed on them (ts2307). Declared here so overrides
// can import through them and a later override of LanguageSelect,
// MobileMenuToggle or Sidebar reaches every place that renders one.
declare module "virtual:starlight/components/*" {
	const Component: (props: Record<string, unknown>) => unknown;
	export default Component;
}

// The per-page "Last updated" table, served by the Vite plugin in
// astro.config.mjs from src/lib/lastmod.mjs: a site-relative source path
// (a route entry's `filePath`) to an ISO 8601 date.
declare module "virtual:mikroscope/lastmod" {
	const dates: Record<string, string>;
	export default dates;
}
