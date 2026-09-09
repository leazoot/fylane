# Fylane Desktop UI

Wails v2 + React UI shell for the Fylane Companion.

Architecture: the resident Core process (`fylane-companion serve`) owns the
tunnel, MCP server, approvals, and database. This shell is a thin,
restartable process that renders state and forwards approval decisions over
the Core's local control API (`control.json` in the Fylane data directory;
override with `FYLANE_DATA_DIR`). Closing the window hides it to the tray;
killing the shell never interrupts the Core.

Build: `wails build` (requires the Wails v2 CLI and Node).
