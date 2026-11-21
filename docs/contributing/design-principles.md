# Design principles for the interface

The rules the web interface's kit follows, and that every page built from it keeps. They exist so that the interface is usable by everyone, and each one is checked by a test or a scan, so a change that breaks it fails the build. This page is for people who write or change the interface. The part for people who use it is [Accessibility](../guide/accessibility.md).

Build pages from the kit (`web/src/kit` and the shell in `web/src/shell`) and the design tokens (`web/src/styles/tokens.css`). A page that reaches for a raw color, a fixed size or its own dialog is where accessibility problems start.

## Contrast

- Use only the tokens. Text uses `--ink`, `--ink-2` or `--ink-3` on a surface (`--bg`, `--sheet`, `--sheet-2`, `--sheet-3`); the colored `-ink` tokens go on their matching `-wash`.
- Text needs 4.5 to 1. Large text, icons, the border of a control and the focus ring need 3 to 1. Use `--control-line` for the border of anything a person can operate; `--line` and `--line-2` are for decoration only.
- Text or an icon on a solid status color (`--ok`, `--bad`, `--warn`) uses `--on-solid`; on the accent it uses `--on-signal`. Never write `#fff`.
- The test `web/src/styles/contrast.test.ts` computes every pair in both themes. If you add a pair, add it to `web/scripts/contrast-lib.mjs`.

## Focus

- Every interactive element shows a 2 pixel ring that differs from its surroundings by 3 to 1: `:focus-visible { outline: 2px solid var(--signal) }` is global. Do not remove an outline. If a field is drawn inside a frame with no outline of its own, the frame shows `:focus-within`.
- Tab order is reading order: do not use positive `tabindex`.
- A dialog takes focus, keeps Tab inside, closes on Escape and gives focus back. Use `Dialog` from the kit. A panel that opens beside the page does not take focus, and closing it gives focus back to its button.
- A region that scrolls and holds nothing focusable must be reachable by keyboard. The shell does that for tables, code and drawings; do not hide overflow without a way to scroll it.

## Status is never by color alone

A state is a shape, a color and a word. Use `Status`, `StateChip` or `Chip` with an icon or text. A colored dot with no text, a red number with no label and a green row with no word are all wrong. If space is short, the word is still in the accessible name. Test: `src/a11y/kit.a11y.test.tsx`.

## Names, roles and states

- An icon-only button has an accessible name (`aria-label`) that says what it does. Icons themselves are `aria-hidden`.
- A toggle exposes `aria-pressed`; a disclosure exposes `aria-expanded`; the current page in a list of links has `aria-current="page"`; the chosen tab has `aria-selected`.
- A name starts with the words the eye sees, so a person who speaks the label can say the control's name (WCAG 2.5.3).
- Tabs, menus, comboboxes and dialogs follow the WAI-ARIA Authoring Practices: use `Tabs`, `PageTabs`, `Dialog` and the search palette as the pattern.
- A heading is a heading: one `h1` per page (the `PageHead`), and levels go down one at a time.

## Forms

Every field has a visible label, its help and its error tied to it with `aria-describedby`, `aria-invalid` on a field that is wrong, and the error text near it in a live region. The server's answer says what to do (`next`); show it. Do not move focus to an error unless the person just submitted; then move it to the first field that is wrong.

## Announcements

Changes that happen on their own are announced without taking focus: `announce("text")` from `web/src/kit/announce.tsx` (polite for most things; assertive for a failure or a lost session). Page changes are announced by the shell. Do not announce what the person just did themselves unless the outcome is not otherwise visible and findable.

## Target size

At least 24 by 24 CSS pixels (WCAG 2.5.8), 44 on a touch screen (`pointer: coarse`). Check boxes and radio buttons are 24. A list item that is only a link gets padding.

## Motion

Nothing essential is in an animation: a running step is a shape and a word, and the spinner stops under `prefers-reduced-motion`. Nothing flashes more than three times a second. Do not start anything that moves for more than five seconds without a way to stop it.

## Contrast mode and forced colors

`prefers-contrast: more` strengthens text and lines (`web/src/styles/a11y.css`). Under `forced-colors: active` the system replaces your colors, so a state that was drawn with a fill also needs an edge or a shape. Check a new component in both.

## Plain language

Say what happened and what to do, in short sentences and the words in the glossary. No jargon in an error. A button says what it does ("Start a run", not "OK"). A date or a time is written so a screen reader reads it well.

## The scans

`make check` runs the contrast test and axe-core over every kit part and routed page. `make web-a11y` runs the real-browser scan and the keyboard journeys. A finding is fixed, not added to a baseline; the two baselines (`web/src/a11y/baseline.json` and `web/a11y-browser-baseline.json`) may only shrink.
