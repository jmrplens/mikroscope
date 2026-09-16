/**
 * A per-page id for a component that names itself with `aria-labelledby`.
 *
 * The count lives on `Astro.locals`, one object per page render, for the
 * reason NotClaimed.astro gives: a counter in a component's frontmatter is
 * recreated on every render and would hand every instance on the page the
 * same id. The colon keeps it apart from every heading slug, which cannot
 * contain one.
 */
export function nextId(locals: object, prefix: string): string {
	const counts = locals as Record<string, unknown>;
	const key = `msIdCount:${prefix}`;
	const n = (typeof counts[key] === "number" ? counts[key] : 0) + 1;
	counts[key] = n;
	return `${prefix}:${n}`;
}
