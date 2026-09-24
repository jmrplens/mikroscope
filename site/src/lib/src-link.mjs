// @ts-check
/**
 * Where a repository path is read on GitHub, for <Src path="…" />.
 *
 * The pages name the code they describe — `internal/expo/expo.go`,
 * `internal/router/options.go` — as inline code a reader cannot follow. <Src>
 * makes the same text a link, and this is the one place its URL is built:
 * Src.astro renders it, src/lib/page-markdown.mjs reduces it for the twins,
 * llms-full.txt and docs/, and scripts/check-src.mjs holds every path to a
 * file tracked in this repository.
 *
 * The link is to `main`, not to the release tag: docs.yml deploys the site
 * when a release commit reaches main, and the tag is pushed after that merge,
 * so a tag link could name a tree that does not exist yet. A file that moves
 * on main fails check-src.mjs on the pull request that moves it.
 *
 * A path ends in `/` when it names a directory, which GitHub serves under
 * `tree/` rather than `blob/`; check-src.mjs holds that spelling to what is on
 * disk. No line anchors: a line number is out of date the first time the file
 * above it changes, and nothing would notice.
 */
import { REPO_URL } from "./release.mjs";

/** A repository-relative path: no leading slash, no `..`, `.`, fragment, query or space. */
const PATH =
	/^(?!\/)(?!.*(?:^|\/)\.{1,2}(?:\/|$))[\w@+.[\]-]+(?:\/[\w@+.[\]-]+)*\/?$/;

/**
 * @param {string} path the path as a page writes it, `internal/expo/expo.go`
 * @returns {boolean} whether it is spelled as a repository path
 */
export const isRepoPath = (path) => typeof path === "string" && PATH.test(path);

/**
 * @param {string} path a repository path, a directory ending in `/`
 * @param {string} where the page or component naming it, for the failure
 * @returns {string} its URL on main
 */
export function srcUrl(path, where) {
	if (!isRepoPath(path)) {
		throw new Error(
			`${where}: <Src path="${path}" /> is not a repository path. Write it from the repository root, with no leading slash, "..", "#" or query, and end a directory with "/".`,
		);
	}
	// encodeURI, for the one route file whose name has brackets in it.
	return path.endsWith("/")
		? `${REPO_URL}/tree/main/${encodeURI(path.slice(0, -1))}`
		: `${REPO_URL}/blob/main/${encodeURI(path)}`;
}
