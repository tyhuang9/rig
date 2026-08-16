# hostd dashboard design system

The provided mockups are the visual contract. This product is a restrained operational dashboard, not a generic glass/card admin template.

## Tokens

- Navigation rail: `#151922`; active navigation: `#252c39`; muted navigation text: `#9ea6b4`.
- Page background: `#f5f6f8`; surfaces: `#ffffff` and `#fafbfc`; borders: `#e3e6ea`.
- Primary: `#315ee8` (hover `#274ec9`), success `#16825d`, warning `#a76713`, danger `#b43d44`.
- System sans font with `ui-monospace` for IDs, releases, ports, and technical metadata.
- 8–10px restrained radius; subtle shadows only for elevated dialogs; no gradients, emoji icons, or ornamental status badges.

## Layout and interaction

- Desktop uses a 216px dark rail and max-width content; the rail collapses at 1024px and the main content becomes a single column below 768px.
- Application and service rows use CSS Grid. Long values ellipsize inside their assigned cell and must not move columns.
- Status is a dot plus plain text; color is never the sole meaning.
- The only prominent action in a panel is the primary action. Future features are omitted instead of appearing enabled but inert.
- Loading UI reserves final row geometry with skeletons after 300ms.

## Accessibility

- Provide a skip link, semantic headings/landmarks, labels, associated field errors, visible `:focus-visible` rings, and keyboard-operable controls.
- Dialogs trap focus and restore focus to their launcher. Mutating controls disable while pending.
- Meet WCAG AA contrast; use `aria-live` only for concise job state summaries, not a stream of logs.
- Respect `prefers-reduced-motion`. Coarse-pointer targets use 44px dimensions where the compact desktop design permits.
