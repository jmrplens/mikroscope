/**
 * The page's locale, as the `Lang` every data record is keyed by.
 *
 * One place, and it throws: a third locale added to astro.config.mjs without
 * its strings in src/data/ fails the build naming what to add, instead of
 * rendering English numbers and English router objects on that locale's pages.
 */
import type { Lang } from "../data/measurements";

export function langOf(route: { lang: string }): Lang {
	if (route.lang === "en" || route.lang === "es") return route.lang;
	throw new Error(
		`Unknown locale "${route.lang}": add it to Lang in src/data/measurements.ts and fill every Record<Lang, …> in src/data/`,
	);
}
