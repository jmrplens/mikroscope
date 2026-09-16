/**
 * How a measured number is written, in one place for both locales.
 *
 * The site's house style predates this file: "2 856 µs" in English and
 * "2,85 %" in Spanish, a thin space between thousands in both, a space before
 * every unit. Intl gets the digits right and the separators wrong for that
 * style (en-GB groups with a comma, es-ES with a full stop, and es-ES does not
 * group four-digit numbers at all unless told "always"), so the parts are
 * taken from Intl and the separators replaced.
 */
import type { Lang, Measurement } from "../data/measurements";

/** U+202F NARROW NO-BREAK SPACE: groups thousands and never breaks a number. */
const GROUP = " ";
/** U+00A0 NO-BREAK SPACE: "2.85 %" never splits across lines. */
const NBSP = " ";

export function formatNumber(v: number, lang: Lang, digits: number): string {
	return new Intl.NumberFormat(lang === "es" ? "es-ES" : "en-GB", {
		minimumFractionDigits: digits,
		maximumFractionDigits: digits,
		useGrouping: "always",
	})
		.formatToParts(v)
		.map((part) => {
			if (part.type === "group") return GROUP;
			if (part.type === "decimal") return lang === "es" ? "," : ".";
			return part.value;
		})
		.join("");
}

/** U+2013 EN DASH, the range sign in both locales: "20–27 %", "2,00–2,01 s". */
const RANGE = "–";

export function formatQuantity(
	m: Pick<Measurement, "value" | "unit" | "digits" | "max">,
	lang: Lang,
): string {
	let n = formatNumber(m.value, lang, m.digits);
	if (m.max !== undefined) n += RANGE + formatNumber(m.max, lang, m.digits);
	return m.unit === "" ? n : `${n}${NBSP}${m.unit}`;
}

const WORDS: Record<Lang, readonly string[]> = {
	en: [
		"zero",
		"one",
		"two",
		"three",
		"four",
		"five",
		"six",
		"seven",
		"eight",
		"nine",
		"ten",
	],
	es: [
		"cero",
		"uno",
		"dos",
		"tres",
		"cuatro",
		"cinco",
		"seis",
		"siete",
		"ocho",
		"nueve",
		"diez",
	],
};

/**
 * A small count as a word ("five runs", "cinco ejecuciones"), which is how the
 * prose around it was written; past ten, digits. For a count that comes from
 * the data, so the word changes when the data does. `capital` starts a sentence.
 * Spanish "uno" is not agreed in gender; no count on the site is 1 today.
 */
export function spellCount(n: number, lang: Lang, capital = false): string {
	const word =
		Number.isInteger(n) && n >= 0 && n < WORDS[lang].length
			? WORDS[lang][n]
			: undefined;
	if (word === undefined) return formatNumber(n, lang, 0);
	return capital ? word[0].toUpperCase() + word.slice(1) : word;
}

/** Joins words with U+00A0, so a short fact like "RouterOS 7.24.2" wraps as one. */
export const unbreakable = (s: string): string => s.replace(/ /g, NBSP);

/**
 * Splits a translation on backticks into text and code runs, so a string such
 * as "What `install` writes" renders a real `<code>` without `set:html` on
 * translated text.
 */
export function codeSpans(s: string): { code: boolean; text: string }[] {
	return s
		.split("`")
		.map((text, i) => ({ code: i % 2 === 1, text }))
		.filter((part) => part.text !== "");
}
