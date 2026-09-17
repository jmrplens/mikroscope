/**
 * A redirect stub is not a document.
 *
 * `astro.config.mjs` declares `movedPages`: addresses that have moved since the
 * site was published and still answer, so nothing that was bookmarked or linked
 * 404s. Astro writes one file per entry — a `noindex` one-liner whose whole
 * body is a link to the page that replaced it, with no `lang`, no `main` and a
 * lowercase doctype, none of which is this site's markup to fix.
 *
 * Every gate that walks dist/ therefore has to skip them, and they are
 * recognised by what they are rather than by a list each script would have to
 * keep: a `<meta http-equiv="refresh">` in a file under 1 KiB. The size test is
 * what keeps a real page that happens to refresh itself from being skipped.
 */
export function isRedirectStub(html) {
	return html.length < 1024 && html.includes('<meta http-equiv="refresh"');
}
