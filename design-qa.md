# Design QA

final result: passed

## Scope

- Target: DLAPI default homepage redesign.
- Visual direction: match the provided one-page Liquid Glass reference image.
- Implementation: `web/default/src/features/home/index.tsx`, `web/default/src/features/home/liquid-glass.css`, `web/default/src/assets/home/liquid-gateway-core.png`, and `PublicLayout` header hiding support.

## Checks

- Desktop 1342 x 900: passed. The homepage now follows the supplied reference composition: a single rounded glass panel, internal nav, left copy and CTAs, application rail, centered liquid gateway core, provider rail, and bottom feature strip. Public header is hidden. No page scroll or horizontal overflow.
- Mobile 390 x 844: passed. The one-page composition adapts without horizontal overflow; brand, headline, CTAs, core visual and provider cards remain visible.
- Performance: passed for the current scope. The previous long multi-section page was reduced to one screen, animation count was reduced, and the homepage CSS chunk is about 10.8 kB in production build.
- Motion: passed. Only subtle core float and flow pulse remain, with reduced-motion fallback.
- Internationalization: passed. New homepage keys were added to all frontend locale files and `bun run i18n:sync` reports no missing keys.

## Notes

- Running `web/default` alone without the Go backend produces a local `/api/status` 404 toast from existing system-config loading. This is an environment artifact and not caused by the homepage UI.
