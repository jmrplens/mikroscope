# The documentation site

Astro + Starlight, bilingual, built from `site/` and served from GitHub Pages at
<https://jmrplens.github.io/mikroscope/>. It is advertised as
<https://jmrp.io/docs/mikroscope>, which 301s to the Pages URL — that split is
explained where it is configured, at the top of `astro.config.mjs`.

```sh
pnpm install
pnpm dev          # localhost:4321/mikroscope/
pnpm build        # dist/
pnpm docs         # regenerate ../docs/ from the English pages
pnpm lint         # every gate below, in the order CI runs them
```

## `docs/` is generated from these pages — do not edit it

The repository's `docs/*.md` are output. They are written by
`scripts/gen-docs.mjs` from the English pages of this site, through the same
MDX-to-Markdown reduction (`src/lib/page-markdown.mjs`) that reads the same
`src/data/` modules the components do — so a figure on a page and the same
figure in `docs/` are one number, formatted once.

To change what `docs/` says, change the page and its Spanish twin, then run
`pnpm docs`. `pnpm docs:check` fails when `docs/` no longer matches the pages,
and it runs in `pnpm lint` and in `.github/workflows/docs.yml`.

The mapping is the `MANIFEST` array at the top of `scripts/gen-docs.mjs`: each
entry names one file of `docs/` and the pages it holds, in reading order. Every
English page must be claimed by exactly one entry or named in `NOT_IN_DOCS`
with the reason it is not documentation, so a page added to the site fails the
check until somebody decides where it belongs. The reduction throws on a
component it does not know, naming the page and the tag, so adding a component
to the content fails the build rather than quietly deleting a section from
`docs/`.

## The two gates worth knowing about

**`pnpm contrast:check`** measures the palette against WCAG 2.2 AA, in both
themes, from the sheets the site actually loads. Nothing in it restates a
colour: the stylesheet list comes out of `astro.config.mjs`, every value comes
out of those sheets by token name, and the ratios are computed — so a token
renamed or deleted makes the gate measure whatever the page really resolves to
and fail, rather than keep printing the old number. It also refuses a colour
token declared for one theme only, checked against the _declared_ blocks rather
than the resolved palettes, because a light page inherits every dark token it
does not override and comparing resolved maps would miss exactly that.

**`pnpm i18n:check`** keeps the Spanish tree structurally identical to the
English one: the set of pages, the shape of the frontmatter, the ladder of
headings, the component tags each page invokes. It never compares prose. A
translated paragraph that has gone stale passes this gate and always will — no
checker can tell a deliberate rewording from a forgotten one, and pretending
otherwise would make the gate something people route around.

## The release version, and links to the source

Nothing under `site/` writes the release number by hand. It is read from
`VERSION` when the site is built, with its date from the matching
`## [x.y.z] - YYYY-MM-DD` heading of `CHANGELOG.md`, and the build fails when
that heading is missing (`src/lib/release.mjs`). Where a page needs it:

- In prose, `<Version />` renders the number; `show="tag"` gives the tag, with
  its `v`, `show="date"` the release date, and `link` links it to the release
  page.
- In a fenced code block or inline code, where no component can go, write
  `{{MIKROSCOPE_VERSION}}`:
  `--remote-image jmrplens/mikroscope-agent:{{MIKROSCOPE_VERSION}}`. The HTML,
  the markdown twins, `llms-full.txt` and `docs/` all get the version in its
  place. In prose MDX reads the braces as an expression and the build fails,
  and in frontmatter nothing replaces it.
- In a component or `src/lib/*.mjs`, `release` from `src/data/release.ts`
  (`version`, `tag`, `date`, `notesUrl`, `releasesUrl`, `changelogUrl`); in a
  script under `scripts/`, `readRelease()` from `src/lib/release.mjs`.

A sentence about a past release keeps that release's number: "since 1.1.0" is
history, not a pin. `pnpm version:check` fails when a page writes a literal
version in an install command (`mikroscope-agent:1.2.0`, `mikroscope_1.2.0_…`,
`VERSION=1.2.0`, `--version 1.2.0`, a `releases/download/v…` URL), unless the
sentence is about that release on purpose and is listed in `HISTORICAL` in
`scripts/check-version.mjs` with its reason. It also fails when the placeholder
reaches `dist/` or `docs/`.

A file of this repository named on a page is `<Src path="internal/expo/expo.go" />`:
the path as inline code, linked to it on GitHub's `main` (a directory ends in
`/`). `pnpm src:check` fails when a path is not something git tracks.

## Figures and charts

`scripts/gen-figures.mjs` writes `src/data/figures/`: the diagrams, drawn from
labels in the script, and the playbooks' charts, drawn from real rows of the
reference InfluxDB store committed in `src/data/figures/data/*.json` beside the
SQL, window and date that read them. Drawing never touches the network, and
`pnpm figures:check` fails when a committed SVG, or a dataset's SQL or window,
disagrees with the script. To re-read the rows, run it by hand with the store's
address in the environment:

```sh
MIKROSCOPE_INFLUX_URL=… MIKROSCOPE_INFLUX_TOKEN=… MIKROSCOPE_INFLUX_DB=… \
  node scripts/gen-figures.mjs --fetch
```

(`MIKROSCOPE_INFLUX_DB` can be left out when the URL is a write URL carrying
`db=`.) It runs read-only queries, rewrites the datasets and redraws. Each chart
checks its rows against what its page says about them and fails rather than
draw a different story.

## What the build writes for machines

- **Markdown twins.** Every page also builds as `index.md` beside its HTML,
  through the same `page-markdown.mjs` reduction as `docs/`, with every link
  made absolute so a twin read on its own still resolves.
- **`llms.txt`**, per language, indexes the bundles with their size and an
  estimated token count: `llms-full.txt` (every page), `llms-core.txt` (the
  pages a first answer needs) and `llms/<section>.txt`, one per sidebar group,
  named after the group's English label. `pnpm llms:check` walks all of them.
- **One JSON-LD graph per page**, written by `src/components/overrides/Head.astro`
  and checked by `pnpm schema:check`. A page's `datePublished` is the commit
  that first added its file, so the build and the check need the full git
  history: `fetch-depth: 0` in CI, and a build run from `site/`.
- **One date per page.** `src/lib/lastmod.mjs` dates a page by the newest
  commit among its own file and the data it renders (`src/data`, images,
  `VERSION` where the page writes the placeholder), and `astro.config.mjs`
  hands that one table to the sitemap's `<lastmod>` and, through
  `src/lastUpdated.mjs`, to Starlight's "Last updated" line, which Head.astro's
  `dateModified` reads.
- **A Content-Security-Policy** in a `<meta>` element of every page, written
  after the build by `src/lib/meta-csp.mjs`, which hashes each inline script
  from the final bytes. `META_CSP` in `astro.config.mjs` switches it off, and
  the comment above it lists what to click in a browser after a Starlight,
  Pagefind or inline-script change.

`src/components/overrides/Footer.astro` adds the author line under Starlight's
footer, reading the name and links from the canonical Person node that
`src/lib/identity.mjs` fetches, so nothing about the person is typed here.

The repository's own `README.md` quotes the install default's cost, which no
build renders; `pnpm readme:check` fails when those figures stop matching
`src/data/measurements.ts`.

## Where things are

| Path                        | What it is                                                                                                                                                                                                                                                               |
| --------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `src/content/docs/`         | English pages at the top level, Spanish under `es/`, same relative paths                                                                                                                                                                                                 |
| `src/content/i18n/`         | UI strings this project adds to Starlight's own                                                                                                                                                                                                                          |
| `src/components/`           | The content components pages import: `Measured`, `Provenance`, `Version`, `Src`, `RouterWrites`, `EnvlistKeys`, `DoctorChecks`, `AlertRules`, `DashboardPanels` and the rest                                                                                             |
| `src/data/`                 | What those components render: measurements and campaigns, verified facts, router objects, envlist keys, doctor checks, cadence reasons, the dashboards read from `dashboards/*.json`, the release read from `VERSION` in `release.ts`, and the landing copy in `home.ts` |
| `src/lib/`                  | Locale-aware number formatting, per-page ids for `aria-labelledby`, the locale guard, the release parsing and version placeholder, and `page-markdown.mjs`, the MDX-to-Markdown reduction `docs/` is generated through                                                   |
| `src/styles/theme.css`      | The palette, and the only place a colour is written                                                                                                                                                                                                                      |
| `src/components/overrides/` | The Starlight components this site replaces                                                                                                                                                                                                                              |
| `public/`                   | Generated by `go run ./cmd/gen_brand icons -out site/public` — do not edit by hand                                                                                                                                                                                       |
| `scripts/`                  | The gates, and `gen-docs.mjs`, which writes the repository's `docs/`                                                                                                                                                                                                     |

## The one expected build warning

```text
[WARN] [build] Could not render `/404` from route `/[...slug]` as it conflicts with higher priority route `/404`.
```

`src/content/docs/404.mdx` is Starlight's documented way to replace its
not-found page: the injected `/404` route renders that entry. Starlight 0.42.0
also lists every docs entry, `404` included, in its `[...slug]` route, and
Astro reports the overlap and keeps the higher-priority one. The result is what
was wanted: `dist/404.html` carries the custom hero, and `/es/404/` is built as
an ordinary page that the sitemap filter in `astro.config.mjs` leaves out and
its own frontmatter marks `noindex`. Silencing the warning would mean
`disable404Route` and a hand-written route, which is more to maintain than a
line of build output. Any other routing warning is new and worth reading.

## Adding a page

Write it under `src/content/docs/`, write its Spanish twin at the same path
under `es/`, and add `{ slug: "…" }` to its group in the `sidebar` array of
`astro.config.mjs`. Starlight labels the entry with each language's page title,
so the menu cannot drift from the heading; give an explicit `label` with
`translations: { es: … }` only where the menu should say less than the title.
Then claim the page in `MANIFEST` or `NOT_IN_DOCS` in `scripts/gen-docs.mjs`.
`pnpm i18n:check` will tell you if you forgot the twin, `pnpm docs:check` if the
page is unclaimed, and the build if the sidebar names a slug that does not
exist or a page is in no sidebar group (`src/lib/llms.mjs` refuses one).

Adding a whole locale is two edits: the `locales` map in `astro.config.mjs`, and
`LOCALES` at the top of `scripts/check-i18n-parity.mjs`.
