# Accessibility

What Launchway does so that everyone can use it, how that is checked on every change, and where it still falls short. It is for people who use the web interface with a keyboard, a screen reader, a magnifier or high contrast, for the people who buy and review software for them, and for anyone who builds on the API, the command line or the MCP server.

## The standard

The web interface is built to meet the Web Content Accessibility Guidelines (WCAG) 2.2, level AA. That is also the level that Section 508 in the United States and EN 301 549 in Europe point to for web content. The page [Accessibility conformance statement](../reference/accessibility-conformance.md) lists every criterion with what is tested and what is not.

Launchway does not claim to be certified. Describing what is tested, and what is known to fall short, is the honest alternative. A person who uses assistive technology every day will find problems an automated scan cannot, and the section [Report a problem](#report-a-problem) says what to send.

The API, the command line and the MCP server have a written quality bar of their own: errors that say what to do, help for every command, descriptions for every tool. It is described in the development notes (`docs/development/interface-quality.md`) and tested.

## What you can rely on

- **Keyboard.** Everything can be done without a mouse. The first Tab stop is a link that skips to the page. The slash key opens the search, and you can turn that shortcut off under your user menu. Dialogs keep focus inside them, close with Escape and give focus back to the button that opened them. Menus, tabs and lists follow the keyboard patterns of the WAI-ARIA Authoring Practices: arrow keys move, Home and End jump.
- **Screen readers.** Every page has one main heading, landmarks (banner, navigation, main) and a title that is read when you move between pages. Buttons and links have names. Status is always a word as well as a shape: a step is Passed, Failed, Waiting for a person or Running, never only a colored dot. The launch line, which is a drawing, is also a list that reads station by station with each state. The diagrams in Learn come with their text. Changes that happen on their own, such as a Sortie (run) that fails, a decision that needs you, a command that finished or a session that ended, are announced without moving your focus.
- **Color and contrast.** Text meets 4.5 to 1 against its background and icons, control borders and the focus ring meet 3 to 1, in the light and the dark theme. Links inside sentences are underlined. Nothing depends on color alone.
- **Size and zoom.** The page reflows down to 320 pixels wide and at 400 percent zoom without sideways scrolling. Targets are at least 24 by 24 pixels, and 44 on a touch screen. Text spacing overrides do not cut text off.
- **Motion.** If your system asks for reduced motion, nothing animates; running work is still a shape and a word. Nothing flashes.
- **High contrast.** If your system asks for more contrast, quieter text and lines become stronger. In forced colors mode (Windows high contrast) every state keeps a shape and an edge.
- **Sign-in.** Launchway signs you in through your organization's single sign-on. It never asks you to solve a puzzle, remember a character sequence or retype something it already knows.

## What is tested, and when

| Check | What it catches | When it runs |
|---|---|---|
| Contrast of the design tokens | A color pair that drops under 4.5 to 1 (text) or 3 to 1 (icons, borders, focus ring), in both themes | Every `make check` |
| axe-core over every kit part and routed page, in light and dark | Missing names, roles and labels, bad heading order, landmarks, tables, forms, duplicate IDs, ARIA mistakes | Every `make check` |
| Status never by color alone | A state without an accessible name or a word | Every `make check` |
| axe-core in a real browser over 26 pages at 1440 and 390 wide, light and dark | Everything above, plus color contrast, target size, scrolling regions and links that look like text | `make web-a11y` |
| Reflow, text spacing and the focus ring in a real browser | Sideways scrolling at 320 pixels, text cut off, controls with no visible focus | `make web-a11y` |
| Keyboard-only journeys, with the accessibility tree and the announcements checked | A journey that needs a mouse, focus that is lost, an outcome that is not announced | `make web-a11y` |
| A baseline that may only shrink | A new violation, and a fixed one left in the list | Both of the above |

The journeys cover: skipping to the page, searching and opening a failed run to read why it failed, deciding an approval with a reason, starting a Sortie, adding a variable, opening Learn and searching it, closing overlays with Escape, and a session that ends.

## What is checked by hand

An automated scan finds about a third of the problems that matter. These are checked by a person before a release, with a screen reader and the keyboard: reading a Sortie page from top to bottom, deciding an approval, using the search, Learn and the settings with NVDA on Windows and VoiceOver on macOS; zooming to 400 percent; Windows high contrast. The results are not yet written down for each release.

## Known gaps

These are known and listed, not hidden.

- **Charts.** The test and coverage trend charts have a text summary, but not every one has a table with the same numbers.
- **Hover help.** The one-line help that appears when you hover or focus an area in the top bar cannot be dismissed with Escape. The same text is available to screen readers at all times.
- **Session timeout.** A session ends after it has been idle for the configured time and after its lifetime. The end is announced, and signing in brings you back to the page, but there is no warning before it ends and no button to extend it.
- **Truncated words.** Some lists shorten a long name with an ellipsis. The full text is available to screen readers and in the tooltip.
- **Autocomplete hints.** Fields do not yet carry the autocomplete attribute that helps browsers fill in personal details. Launchway asks for few of them.
- **A decision card's list.** On the page for deciding approvals, the two lines that say what each answer does are marked up as a list of terms in a way a screen reader may read oddly.
- **Manual testing is not yet repeated for every release.**

## Report a problem

If something in Launchway is hard or impossible to use, tell us. Open an issue in the project's repository and say which page, what you tried to do, what happened, and which browser and assistive technology you use. If you prefer not to use a public issue, follow the contact steps in the security policy (`SECURITY.md`); say that it is an accessibility report. Reports are read, and a problem that stops someone from using a page is treated as a bug that blocks a release.

> **Verified** on 2026-11-21 against the branch that added the gates: `make web-test` (axe-core in jsdom over every kit part and routed page, and the token contrast test) and `make web-a11y` (26 pages at two widths and two themes, and ten keyboard journeys) passed, run from a build made with `make all`. **Not verified:** a screen reader on a physical device, Windows high contrast on Windows, Safari and Firefox.
