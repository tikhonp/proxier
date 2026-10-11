# Phone test: UI and functional findings (2026-10-11)

A walk through every UI page on a real Android phone, with the problems seen. Every finding is fixed (commits 6b0c651, 27ef8e3, 937a87d, d92ac68 and a header follow-up); the table at the end says how. The fixes were checked in headless Chrome at 360 px with touch emulation, then rechecked on the same phone (the "On the phone" column).

## Setup

- Device: vivo V2027, Android 12, Chrome 154, 720×1600 at 320 dpi (360 CSS px wide, 2× scale).
- Build: current `main`, run locally (`dev` version) against an empty SQLite file, reached over `adb reverse tcp:8080`.
- Driven through Chrome's DevTools protocol (navigation, DOM checks, console and network errors, screenshots) and real `adb input` taps wherever the soft keyboard mattered.
- Both languages (English, Russian) were swept on every page. No console error and no failed request (other than the expected 422 on an empty form and a `/favicon.ico` 404) appeared on any page.

## Coverage

Checked on all of: Dashboard, Servers, Templates, Subscriptions, Links, Lists, Services, Search, Discover, Routers, Shadowrocket, Router scripts, Jobs, Activity, all 7 Settings tabs, the New/detail pages of templates, subscriptions, links, services, lists, scripts, routers and Shadowrocket configs, a public `/s/` link, the menu drawer, the search pop-up and landscape orientation.

Flows that worked: sign-in (by session cookie), language switch, creating a subscription, a link (Reveal, Copy → "Copied", QR), a custom service (paste, preview, refusal of an invalid line, save, snapshots), adding a service to a list, creating a router script draft, the template editor (draft, full screen, typing, unsaved-changes prompt).

**Not tested** (needs real accounts or hardware): provisioning a server, Cloudflare, Telegram, MikroTik sync, Shadowrocket on an iPhone, Discover (no Chromium sidecar). The New server and Add router forms were checked for layout only.

## Findings

"Verified" means seen directly (DOM measurement or real-phone screenshot). "Unverified" means reported by a review of screenshots and not rechecked.

### Layout and navigation

1. **Settings tab strip hides the current tab** (verified). The strip is 899 px wide in a 360 px viewport. Four of seven tabs (Subscriptions, Routing, Integrations, Notifications) are off-screen. The active tab is not scrolled into view, so on those pages the strip shows General / Security / Servers with no sign of where you are, and nothing hints that more tabs exist.
2. **Header is cramped** (verified). The search field is a 63 px stub reading "/ S…". The account name "admin" is cut to "a…" although there is room. Every page has it.
3. **Search pop-up shows keyboard hints on a touch device** (verified): "↑ ↓ ⌃N ⌃P move", "esc closes".
4. **Template editor: line starts hidden** (verified on the phone). After tapping in the code, the editor scrolls sideways by the gutter width (54.5 px), so the first characters of each line sit under the line numbers (")ath:" instead of "path:").
5. **Template editor toolbar fills a screen.** Save draft, Validate, Preview, Publish, Hand off, Discard draft stack full width (about 650 px) before the file list and the code.
6. **Time field clipped on New link** (verified): the expiry time input shows "--:-".
7. **Small tap targets** (verified by measurement): filter chips 26 px tall (Servers, Templates, Search, Jobs, Activity, Links), the per-event toggle buttons on Settings → Notifications 36×18 px, "Repair drift" 36×18 px, breadcrumb links 22 px, the actor links on Activity 19 px. Radios and checkboxes are 16 px; whether their labels widen the touch area was not checked.
8. **Filter rows misaligned** (verified): selects are 48 px tall next to 40 px Apply/Find buttons; Activity's two selects differ in width.
9. **Settings → Notifications is about 14,000 px tall** on a phone, one long scroll.
10. **Settings → Servers: "Allow a zone" wraps word by word** in a narrow column (unverified).

### Behaviour and consistency

11. **Name handling differs between forms.** A custom service named "My Test Service" is silently turned into `my-test-service` (and the service page then headlines its tag, `mytest`, while the Name row shows "My Test Service"). The Shadowrocket form shows "Also the file name: iphone.conf" for "iPhone Mom" yet rejects it as invalid.
12. **Integrations has no way to set things up.** Telegram shows "not configured" with no link or button, while the Dashboard has "Set up Telegram".
13. **Settings → Subscriptions** labels the link language "en | ru" (codes) while the link form says "English" / "Russian".

### Text

14. Script page warning reads "the last values read are ." (empty list) when the PARAMETERS block has no END line.
15. Activity repeats the object's name after the sentence ("Created router script Fresh MikroTik" then a link "Fresh MikroTik"), and "Changed routing list Main" does not say what changed.
16. Add services shows "custom" twice for each service.
17. Placeholder "Filter by tag or se…" is cut off on Add services.
18. "Chromium: not configured: set PROXIER_CHROMIUM_URL" is an environment-variable instruction on user-facing screens (Discover, Integrations).
19. Russian (unverified): "Новый свой сервис" and the hint repeating "кнопкой" are clumsy; "сервер пока не построить" is awkward; "Вход с адреса ::1 администратор" has no verb. The "0 0" counts on the Lists page have no labels.

## What was done

| # | Fix | On the phone |
|---|---|---|
| 1 | The current tab is scrolled into the strip on load; an end with more tabs fades. | shows Notifications whole, the left end fades |
| 2 | On a phone the search is a 48 px magnifier cell beside the name, which has the rest of the row; the logo's sides are 12 px. | "adm…" at first: the row was 3 px short (the phone's font is wider than headless Chrome's); the logo's sides went 16 → 12 px and "admin" shows whole |
| 3 | A touch screen with no hover hides the key hints (`.pc-keys-only`). | no hints in the pop-up; the touch query matches |
| 4 | The editor's scroller is padded by the gutters' width (`scroll-padding-left`), so aligning the code into view stops beside the line numbers. Headless Chrome reproduced the 56 px shift by aligning the code, not by a tap. | three `adb input` taps in the code, keyboard open: `scrollLeft` stays 0, lines start at "name" |
| 5 | The editor's actions go two to a row at 40 px, Publish first (202 px). | |
| 6 | The time field is 120 px on a phone. | "--:--" whole at 120 px; the clock icon opens Android's picker |
| 7 | Chips and segments 40 px; switches keep their look with a 48×46 tap area; breadcrumbs and Activity's links get 40 px tap areas. Radios and checkboxes sit inside their labels; the row ticks on Import and Discover, which had none, are in a 40 px `.tick` label (the Servers table's bulk ticks are desktop only). | chips 40 px; a tap 8 px below a switch turns it on; Activity's links 39 px. The Import and Discover ticks were not reached (no import file, no Chromium) |
| 8 | A filter row's selects share its width; its button is 48 px like them. | |
| 9 | Each module's rules fold under a summary with counts; on a phone they start folded, and switching a rule comes back with its group open. | 730 px folded; a switched rule comes back at its `#rule-` with its group open |
| 10 | The alert's sentence and link are one span. | |
| 11 | A custom service's tag field shows the tag the name gives as it's typed, and its page headlines the name with the tag under it. A Shadowrocket name is made a file name the same way ("iPhone Mom" → `iphone-mom`), shown as "File name: iphone-mom.conf" while typing. | |
| 12 | Unconfigured Telegram and Cloudflare rows say **Set up**. | |
| 13 | The link language reads English / Russian. | |
| 14 | A block with no `:local` line says so. | |
| 15 | A sentence that names its object links that name; routing list changes say what changed; the sign-in has a verb in Russian. | |
| 16 | "custom" once; "1 domain". | |
| 17 | Placeholder "Tag or selector". | |
| 18 | Chromium's state names no environment variable. | |
| 19 | The Russian wording is fixed; the Lists page's counts carry their words on a phone. | |

## Not defects

- Sticky header repeated in full-page screenshots: a capture artifact.
- A first "Nothing matches" in search: the screenshot was taken before the results arrived; search works.
- Chrome's saved-password sheet blocked typing on the sign-in form: a browser feature, not the app.
- Two language settings on General (default for new accounts; the admin's own): by design.
- A public `/s/<token>` link opened in a browser shows plain text: correct for a subscription feed.
- Missing `/favicon.ico` on public pages (404): trivial.
- The unsaved-changes prompt when leaving the template editor: expected.
