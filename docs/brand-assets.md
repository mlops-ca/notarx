# notarx brand assets

[Documentation index](README.md)

notarx uses the mlops.ca brand system (Kharkevich Engineering Lab): signal-lime
on ink, Bricolage Grotesque for headings, JetBrains Mono for labels, a 2px
radius and 1px hairlines. Files live in `docs/assets/brand/`. The licence is in
[`LICENSE-ASSETS`](assets/brand/LICENSE-ASSETS) and the name and logo policy in
[`TRADEMARKS.md`](../TRADEMARKS.md). The decision is recorded in
[ADR 0013](adr/0013-notarx-fork-charter.md).

## Mark

The mlops.ca open ring (the loop) and its lime signal node, with a check inside:
*the loop verifies*. Keep one ring-stroke of clear space on every side, never
recolour the node and never place the mark on lime.

| File | Use |
| --- | --- |
| `notarx-mark.svg` | App icon on ink (default) |
| `notarx-mark-light.svg` | App icon on paper |
| `notarx-glyph-dark.svg` / `notarx-glyph-light.svg` | Transparent glyph for dark / light surfaces |
| `notarx-lockup-dark.svg` / `notarx-lockup-light.svg` | Glyph and wordmark for dark / light surfaces |
| `favicon.svg`, `favicon-32.png`, `apple-touch-icon.png` | Console favicon and Apple touch icon (copied to `console/public/`) |
| `notarx-icon-{16,48,192,512}.png` | Additional raster icons |
| `og-image.svg` / `og-image.png` | 1200×630 social card |

In the wordmark, `notar` uses the text colour and `x` the accent:
`#C8F135` on dark, `#3F5C00` on light (lime text fails contrast on paper).

## Colour tokens

| Token | Dark | Light |
| --- | --- | --- |
| background | `#0B0C0A` | `#EDECE3` |
| surface | `#161812` | `#F6F5EE` |
| text / muted | `#ECEBE3` / `#9CA091` | `#12140E` / `#555A4A` |
| signal (fills only) | `#C8F135` | `#C8F135` |
| signal text | `#C8F135` | `#3F5C00` |
| danger | `#FF7A52` | `#B0310F` |

## Regenerating

All text in the SVGs is outlined, so they render without the fonts installed.
`generate.py` rebuilds them from the variable fonts (fontTools with brotli):

```sh
python3 docs/assets/brand/generate.py <dir-with-bricolage-and-jetbrains-woff2> docs/assets/brand
inkscape docs/assets/brand/notarx-mark.svg --export-type=png -w 512 -h 512 \
  --export-filename=docs/assets/brand/notarx-icon-512.png
```
