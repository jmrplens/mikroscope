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

## Where things are

| Path                        | What it is                                                                                                                                                                                                              |
| --------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `src/content/docs/`         | English pages at the top level, Spanish under `es/`, same relative paths                                                                                                                                                |
| `src/content/i18n/`         | UI strings this project adds to Starlight's own                                                                                                                                                                         |
| `src/components/`           | The content components pages import: `Measured`, `Provenance`, `RouterWrites`, `EnvlistKeys`, `DoctorChecks`, `AlertRules`, `DashboardPanels` and the rest                                                              |
| `src/data/`                 | What those components render: measurements and campaigns, verified facts, router objects, envlist keys, doctor checks, cadence reasons, the dashboards read from `dashboards/*.json`, and the landing copy in `home.ts` |
| `src/lib/`                  | Locale-aware number formatting, per-page ids for `aria-labelledby`, the locale guard, and `page-markdown.mjs`, the MDX-to-Markdown reduction `docs/` is generated through                                               |
| `src/styles/theme.css`      | The palette, and the only place a colour is written                                                                                                                                                                     |
| `src/components/overrides/` | The Starlight components this site replaces                                                                                                                                                                             |
| `public/`                   | Generated by `go run ./cmd/gen_brand icons -out site/public` — do not edit by hand                                                                                                                                      |
| `scripts/`                  | The gates, and `gen-docs.mjs`, which writes the repository's `docs/`                                                                                                                                                    |

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
under `es/`, and add it to the `sidebar` array in `astro.config.mjs` with its
`translations: { es: … }` label. `pnpm i18n:check` will tell you if you forgot
the twin, and the build will tell you if the sidebar names a slug that does not
exist.

Adding a whole locale is two edits: the `locales` map in `astro.config.mjs`, and
`LOCALES` at the top of `scripts/check-i18n-parity.mjs`.
