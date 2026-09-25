# Brand

The mark is geometry, not a drawing: nine sample bars whose envelope is a
burst, crossed by the flat line of their own mean. That is the whole claim of
the project in one shape — a one-second average reports the line, and
sub-second sampling is what resolves the spike standing over it. It lives as a
generator rather than as a folder of hand-drawn files, because changing the
palette or the bar count is then one edit in `cmd/gen_brand` instead of nine in
each of a dozen files.

Run from the root of the repository, writing into this directory:

```sh
go run ./cmd/gen_brand mark -out brand         # the mark and the favicon, per theme
go run ./cmd/gen_brand compose -out brand      # the banner, the social image and the og:image
go run ./cmd/gen_brand icons -out site/public  # the favicon, the touch icons and the web app manifest a web page asks for
```

`compose` reads the three `bg-*.png` backgrounds from the same directory it
writes to, and shells out to `rsvg-convert` for the rasters.

## The line is the mean, and it is computed

`bars()` draws the line at the arithmetic mean of the heights it was given
rather than at a number chosen to look right, and a bar takes the loud tone
exactly when it stands above that mean. So the sentence the mark makes is one the code
enforces: three of the nine samples are above their own average, and they are
the three the eye goes to. `TestTheLineSitsAtTheMeanOfTheBars` fails the build
if the drawing and the arithmetic ever drift apart.

## Four tones, two per theme, and no opacity anywhere

Measured against the background each is drawn on:

| Theme | Background | Above the mean | At or below it, and the line | Apart |
|---|---|---|---|---|
| Dark | `#0e1316` | `#fbbf24` **11.20:1** | `#c2740a` **5.16:1** | 2.17:1 |
| Light | `#ffffff` | `#633009` **10.73:1** | `#b45309` **5.02:1** | 2.14:1 |

One palette per theme is not a refinement, it is the only way the mark is
legible in both: `#fbbf24`, the amber light enough to read on the near-black
page, is **1.67:1** on white, and one dark enough for white disappears into
the page.

The quiet/loud split is two solid tones rather than one tone at two alphas
because a quiet bar drawn as the theme's own amber at a low opacity cannot clear
AA: 0.28 of the dark theme's `#f59e0b` composites over the page to **1.73:1**,
and 0.42 of the light theme's `#a16207` over white to **1.80:1**, where AA asks
4.5:1, and reaching 4.5 by raising the alpha takes the light theme to 0.96 — at
which point a quiet bar is a loud bar and the one distinction the mark exists to
make is gone.

That costs punch. Solid-against-solid gives about **2.1:1** between the two
groups, against roughly 5:1 for an alpha split, so the spike shouts less.
What it buys is that nothing in the mark composites against a background this
repository does not control — a README on GitHub, an og:image in a chat client,
a favicon over browser chrome — so every number above is the number the reader
actually gets.

Worth saying plainly: WCAG 1.4.11 asks **3:1** of a graphical object, and 1.4.3
exempts logotypes from any minimum at all. 4.5:1 on every bar is a house rule
stricter than the standard, chosen by the owner on 2026-09-15.
`TestEveryToneClearsAAInItsOwnTheme` recomputes all six ratios from the hex on
every run and asserts the **thresholds** — each tone at or above 4.5:1 on its
own background, the two tones at least 2.0:1 apart. It does not assert the
figures printed in the table above, so a tone edited to another value that
still clears 4.5:1 passes while this table goes stale. Re-read the numbers from
the test output when a hex changes.

**A tension worth stating.** Amber also means *threshold* on this project's
dashboards, where a panel turns orange before it turns red. The two do not
collide literally — the panels use Grafana's own named colours and never these
hexes — but a reader who has learnt "amber means look at this" on a dashboard is
being asked to read the same hue as the project's own mark. That is the cost of
the choice, made knowingly (owner, 2026-09-15).

## The favicon is a different drawing

Nine bars at sixteen pixels is mush, so the favicon drops to five and keeps the
spike standing over the line, which is the part that carries the meaning.

## Files

| File | What it is |
|---|---|
| `mark-dark.svg`, `mark-light.svg` | The mark, one per theme |
| `mark-inline.svg` | The mark as one element a stylesheet can paint, for embedding |
| `favicon-dark.svg`, `favicon-light.svg` | The five-bar variant |
| `banner.svg` and `.png` | 1280x320, for the README |
| `social.svg` and `.png` | 1280x640, the repository social preview |
| `og.svg` and `.png` | 1200x630, the documentation `og:image` (the site's copy is re-encoded: see [below](#the-sites-ogimage)) |
| `background.png` | The generated field the three compositions crop from |
| `bg-banner.png`, `bg-social.png`, `bg-og.png` | Those crops |

## The background

Generated once with inference.sh (`openai/gpt-image-2`, 1536x1024, `quality:
high`, $0.16) and kept as a raster; everything drawn over it is vector, so the
type stays crisp at whatever size the raster is produced. The prompt asked for
a near-black field of faint vertical sample bars growing denser and warmer
toward the right, and for the left third to stay empty — which is where the
mark and the type sit, so the composition never fights its own background.

The three crops keep the whole left-to-right gradient rather than taking a
window out of the middle of it:

```sh
magick background.png -resize 1280x -gravity center -crop 1280x320+0+0 +repage bg-banner.png
magick background.png -resize 1280x -gravity center -crop 1280x640+0+0 +repage bg-social.png
magick background.png -resize 1200x -gravity center -crop 1200x630+0+0 +repage bg-og.png
```

Type over the field's own darkest ground (#020608, sampled from the left edge):
the heading `#f6f3ee` reads 18.38:1, the tagline `#cfc6b8` 12.04:1, and the
mark's two tones 12.19:1 and 5.62:1 — higher than on the page, the field being
darker than it.

## The site's og:image

`site/public/og.png` is not a copy of `brand/og.png`. Every page of the
documentation names it, and at 605 194 B it was the heaviest file a link
preview fetched, most of it the ±1-level noise along the background's vertical
lines, which a lossless encoder cannot compress. On 2026-09-24 the site's copy
was rebuilt from `og.svg` over a denoised background: a 1x11 vertical median of
`bg-og.png`, applied only where it moves a pixel by 2 levels or less so the bar
tips and highlights stay, then every channel rounded up to an even value. The
mark and the type are still drawn as vectors by `rsvg-convert`, as `compose`
draws them. The result is 158 857 B, 1200x630 RGB, at most 3/255 from the
original in any channel (PSNR 48.8 dB, SSIM 0.9867), and no difference was
visible side by side at full size or at 3x zoom.

`compose` does not do this, so after it runs, rebuild the site's copy from the
repository root with this (ImageMagick, `rsvg-convert`, and Python with Pillow
and NumPy), which gives the same bytes again:

```python
import base64, subprocess, tempfile, os
import numpy as np
from PIL import Image

with tempfile.TemporaryDirectory() as d:
    med = os.path.join(d, "med.png")
    subprocess.run(["magick", "brand/bg-og.png", "-statistic", "median", "1x11", med], check=True)
    bg = np.asarray(Image.open("brand/bg-og.png").convert("RGB")).astype(int)
    m = np.asarray(Image.open(med).convert("RGB")).astype(int)
    keep = np.abs(bg - m).max(axis=2, keepdims=True) > 2
    out = np.clip(((np.where(keep, bg, m) + 1) // 2) * 2, 0, 255).astype("uint8")
    proc = os.path.join(d, "bg.png")
    Image.fromarray(out).save(proc)
    svg = open("brand/og.svg").read()
    orig_b64 = base64.b64encode(open("brand/bg-og.png", "rb").read()).decode()
    assert orig_b64 in svg, "brand/og.svg no longer embeds brand/bg-og.png verbatim"
    svg = svg.replace(orig_b64, base64.b64encode(open(proc, "rb").read()).decode())
    open(os.path.join(d, "og.svg"), "w").write(svg)
    raw = os.path.join(d, "og.png")
    subprocess.run(["rsvg-convert", os.path.join(d, "og.svg"), "-o", raw], check=True)
    Image.open(raw).save("site/public/og.png", optimize=True)
```

## Setting the social preview

Manual: Settings, then Social preview, then upload `social.png`. GitHub offers
no API for it.
