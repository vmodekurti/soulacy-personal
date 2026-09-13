# Blue Living Core

Selected by the owner on September 12, 2026: the Living Core seed/S silhouette
in blue-violet, with a white mark and a lowercase `soulacy` wordmark.

## Production assets

- `icon-source.png`: selected built-in image-generation output (1254 × 1254).
- `icon-1024.png`: opaque 1024 × 1024 App Store master, resized with macOS `sips`.
- `../../../gui/public/brand/living-core-blue-v1-*.png`: 32, 128, 180, 192 and 512px web derivatives.
- The iOS AppIcon and 1×/2×/3× BrandMark assets use this same master.
- Website and documentation PNGs are byte-identical copies of the web assets.

The app-icon source has square, full-bleed corners. iOS applies its own icon
mask; in-app and web components round the tile at presentation time. The
wordmark remains real text, lowercase, bold, tightly tracked, white on dark
surfaces and navy on light surfaces. Do not rasterize the wordmark or recolor
the mark as a template image. Functional SF Symbols and agent avatars are not
product logos and are unchanged.

Transparent mark and wordmark exports were rejected: the generator returned
RGB files with checkerboards baked into the backgrounds, not actual alpha.
They are not included in the application or website. Production uses the
approved opaque blue tile plus live text instead. No fallback API or external
image service was used.

## Final generation prompt

Built-in image generation, editing the approved blue Living Core comparison:

Use case: precise-object-edit. Input image is the approved brand reference. Extract ONLY the LEFT blue-violet Living Core app icon as one production iOS app-icon asset. Preserve the exact white seed silhouette with two asymmetric curved lobes and the broad S-shaped negative-space opening; no redesign or extra detail. Output one 1024x1024 square. Fill the ENTIRE canvas edge to edge with a completely opaque uniform saturated blue-violet #514BFF. There must be NO rounded outer corners, NO outer padding/background/margins, NO transparent pixels: the operating system applies its own app-icon mask. Center the white Living Core mark on this full-bleed square, same geometry and proportional sizing as the large blue app icon in the reference: mark approximately 56% canvas width and 69% canvas height. The white mark is flat pure white, the S-shaped opening shows the same blue background. No headings, labels, wordmark, miniatures, presentation board, shadows, gradients, glow, texture, bevels, watermarks, outlines or other elements. Flat clean two-color production art. ENTIRE CANVAS IS OPAQUE.

## Checks

`gui/src/lib/BrandMark.test.js` verifies accessible/decorative labeling and
square presentation at every used web size. `brandAssets.test.js` checks PNG
dimensions, opaque RGB format, all manifest/shortcut references, touch icons,
service-worker versioning, and cross-site asset identity. The iOS
`BrandingTests` check display-scale loading and light/dark rendering at small,
workspace and welcome sizes, including original colors and rounded corners.

Release and live-deployment evidence is recorded separately; this file does
not imply that every website using the repository has been published.
