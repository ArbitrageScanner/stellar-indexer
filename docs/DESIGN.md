# Explorer design system

The design system of the explorer UI. Every screen follows it; change it on purpose.

## System
- Genre: modern-minimal
- Shape: app screen (25 hash routes in one shell; `web/static`)
- Theme: Dusk: the app scope keeps only its slate, smoked glass, bone day mode and two signals
- Axes: dark/grotesk-sans/cool · posture: committed (slate carries the surface; periwinkle stays a signal)

## Tokens
`web/static/css/tokens.css` (theme) and the token blocks at the top of `web/static/css/app.css` (explorer additions, day mode, the legacy aliases the scripts use) are the source of truth.
```css
:root {
  --color-paper: oklch(17.5% 0.014 268);  --color-paper-2: oklch(21% 0.016 268);  --color-paper-3: oklch(25% 0.017 268);
  --color-ink: oklch(96% 0.006 75);  --color-ink-2: oklch(85% 0.010 262);  --color-muted: oklch(70% 0.016 262);  --color-rule: oklch(30% 0.016 266);
  --color-accent: oklch(77% 0.10 274);  --color-accent-ink: oklch(19% 0.035 274);  --color-focus: oklch(80% 0.10 274);
  --color-accent-2: oklch(83% 0.095 62);   /* apricot: open, failed, owed */
  --font-display: "Wix Madefor Display";  --font-body: "Wix Madefor Text";  --font-mono: ui-monospace (code, hashes, addresses only);
  --radius-card: 18px;  --radius-inner: 10px;  --radius-control: 10px;  --radius-pill: 999px;
}
```
Series `--s1`…`--s8` are mixed for slate (and re-mixed for day); `--s1` periwinkle and `--s2` apricot keep the signal meanings (done/ok, failed/open). Up is `--color-up` (sage), down `--color-down` (apricot-clay): never a red debit. `--color-error` is for request errors only.

## Surfaces
- Page: slate. Sidebar: `--color-shade`. Top bar: slate glass with a hairline foot.
- A panel is `.card`: smoked glass with its lit edge (`--glass-hi`) and `--shadow-card`. Never a card in a card, never glass on glass.
- Figures live in one ruled tally (`.tiles` / `.tile`: vertical hairlines, no boxes), in rows and in lists. Never a grid of boxed stat tiles.
- Tables: horizontal rules only, sentence-case heads in muted, rows split on `--color-row`.

## Type
Display 500 for h1/h2/h3 and statement figures; Text 400/500 everywhere else (600 only on series initials). Labels, table heads, tags, nav: Text, a step smaller, sentence case, untracked; never uppercase, never mono. Figures `tabular-nums`; units and decimals that are not the point in `--color-muted`.

## Controls
- Primary: periwinkle pill (`.btn.primary`), one per screen, 36px.
- Secondary: `.btn` ghost pill on `--color-hover` with an `--color-edge` outline; `.btn.sm` 32px; every button carries a 44px hit area.
- Fields: 36px, 1px `--color-edge`, focus is an outline, disabled is opacity + not-allowed + the attribute.
- Choices (`.seg`) and tabs (`.tabs`) are a ruled row of words with a periwinkle dot on the chosen one, never boxed segments.

## Copy
No middle-dot meta strings (`A · B`), no arrows glued to labels, no tracked capitals. Join facts with a comma, a line break or separate elements in a flex row. Sentence case everywhere.

## Motion
Character: calm. State only: `--dur-micro` / `--dur-short`, `transform` and `opacity`. New ledgers arrive with one 420ms rise; skeletons shimmer; under reduced motion everything ends at once (base.css).

## Exports
`web/static/css/tokens.css` is the only token export.
