# Design-fidelity harness

Renders one desktop screen with fixture data so a screenshot can be diffed
against its design board. Development only: it is a
separate vite root and never enters the shipped bundle.

    npm run harness            # serve on :5199
    open 'http://localhost:5199/?board=rules'
    open 'http://localhost:5199/?board=rules&narrow=1'

Boards: `lane`, `lane-held`, `lane-command`, `lane-disclosure`, `lane-empty`,
`lane-paused`, `lane-offline`, `lane-remote`, `lane-remote-held`,
`lane-remote-missing`, `machine-add`, `machine-edit`, `machine-folder`, `tasks`,
`tasks-empty`,
`tasks-remote`, `memory`, `memory-empty`, `memory-remote`, `settings`,
`settings-update`, `commands`, `pairing`, `first-run`, `first-run-write`,
`first-run-rung`, `first-run-done`.

That list was wrong until 2026-09-02: it still named `safety`,
`safety-balanced`, `sources`, `privacy`, `changeset`, `rules*`, `workspaces`,
`trace*`, the two `confirm-*` sheets and five `onboarding-*` frames, none of
which have existed since the V3 rewrite folded those screens into three
pages. A board list nobody can open is worse than none — it sends a reader
looking for a screen that was deleted.

No proposal boards are open. `write-policy-a` and `write-policy-b` (where the file-write approval policy lives) were drawn because `Fylane-V3`
has no board for that axis at all; the left-column placement was approved and
implemented as the second `PolicyGroup` in `src/screens/Settings.tsx`, drawn
from the Core's own answer instead of a fixture and pinned by seven tests in
`screens.test.tsx`. The `settings` board shows the real block. Before them,
`grants`, `grants-empty` and `grants-stale` went the same way, and
four `tunnel-get` boards before those — all now live in the real
screen. A second copy here would only drift from the one people see. `grants`, `grants-empty` and `grants-stale`
(standing workspace grants) were approved and implemented as
`GrantRows` in `src/screens/Settings.tsx`, drawn from the Core's own answer
instead of a fixture and pinned by seven tests in `screens.test.tsx`; the
`settings` board shows the real block. Before them, four — `tunnel-get`,
`-busy`, `-failed`, `-unpinned`, what opens under the Cloudflare row when the
program is missing and Fylane could fetch the pinned one — were
approved and implemented. They live in `src/screens/Settings.tsx` as `Missing` now, drawn from
the Core's own snapshot instead of a fixture, and their four states are pinned
by tests in `screens.test.tsx`. A second copy here would only drift from the
one people actually see.

First run advances by real clicks only, so its boards set the opening frame
and the remaining ones are reached by pressing the button on screen: the
`transit` and `arrived` frames come from `onboarding-connected`. Its canvas
is 1440 × 900 and is scaled down to fit smaller windows, so a 1:1 diff
against `FylaneOnboard.dc.html` wants a 1440 × 900 viewport.
