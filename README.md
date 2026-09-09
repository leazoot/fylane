<div align="center">

<img src="assets/icon.png" width="96" alt="Fylane">

# Fylane

**Let ChatGPT, Claude and Grok work in a folder on your machine. Every write stops and asks you first.**

English | [简体中文](README.zh-CN.md)

[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.25-00ADD8)](go.mod)
[![MCP](https://img.shields.io/badge/protocol-MCP-6E56CF)](https://modelcontextprotocol.io)

</div>

<br>

![The approval gate](assets/lane.png)

Fylane is an MCP server that runs on your own computer. You point it at one
folder, connect a chat that speaks MCP, and the model can read, edit, and run
commands there — through a gate you control.

Nothing is uploaded. The relay that carries the connection sees encrypted
frames and never stores a file, a diff, or a path. When you close the lid, the
AI simply cannot reach the machine.

## Features

- **Every write waits for approval.** You see the diff before it lands, and a
  local decision is the only thing that applies it.
- **Undo a write for 7 days.** Each change set keeps a backup and a rollback
  window; rolling back refuses if the file changed since.
- **The AI never learns where anything is.** Tools take an opaque workspace ID
  and relative paths. Absolute paths never leave the machine.
- **Commands are bounded, not shelled.** Program and arguments only — no
  pipes, no `sh -c`. Destructive ones are refused by a rule table at every
  approval level.
- **Secrets stay out of reach.** `.env`, `*.pem`, `id_rsa` and friends are
  hidden from listings, skipped in search, and need a separate yes to read.
- **A kernel boundary, not a promise.** Programs Fylane starts can read the
  workspace and the toolchain caches. Everything else is denied by the OS
  (`sandbox-exec` on macOS, Landlock on Linux).
- **Choose how often you are asked.** Ask every time, ask once per folder, or
  don't ask for ordinary commands. The checks and the audit log stay on either
  way.

## Quick start

Go 1.25 or newer.

```bash
git clone https://github.com/leazoot/fylane
cd fylane
go build -o bin/fylane-companion ./companion/cmd/companion
```

Point it at a folder. No account, no config file.

```bash
cd ~/projects/my-app
fylane-companion share
```

It prints a public URL and a pairing code:

```
sharing my-app — starting a tunnel, this takes a few seconds

  Connector URL   https://swift-lane-9f2c.trycloudflare.com/mcp
  Pairing code    7K4M-2QB9   (valid for 10m0s)
```

Add that URL as an MCP server in ChatGPT, Claude or Grok, then approve the
pairing code when the chat first connects. Ask the model to list the folder
and it will come back with your files.

The tunnel and the code die when you press Ctrl-C.

## What the AI can do

| | Tools |
| --- | --- |
| Read | `list_directory` `read_file` `read_files` `search_files` `stat_path` |
| Write | `write_file` `edit_file` `apply_patch` `change_manage` |
| Run | `run_command` `task_status` `code_task` |
| Navigate | `code_navigate` — real definitions and references from a language server |
| Extend | `mcp_gateway` — forward to another MCP server on your machine |

Writes and consequential commands return `pending_approval` until you decide.
`change_manage` also covers moves, deletes into a recycle area, and rollback.

## Desktop app

The companion runs headless, but the desktop shell is where approvals live: a
lane for what is waiting, a log of what ran, and the settings that decide when
you get asked.

<table>
<tr>
<td width="50%"><img src="assets/tasks.png" alt="Task history"></td>
<td width="50%"><img src="assets/settings.png" alt="Execution and approval settings"></td>
</tr>
</table>

Currently packaged for macOS. The companion itself builds for macOS, Linux and
Windows.

## Running your own relay

`share` uses a throwaway tunnel. For a fixed address, run the relay on a server
you own — it handles OAuth and forwards frames in memory, and is the only piece
that is ever public.

```bash
FYLANE_RELAY_HOST=relay.example.com docker compose -f deploy/docker-compose.yml up -d
```

Caddy in the same file gets the certificate. The relay never terminates TLS.

Then pair the companion to it once:

```bash
fylane-companion pair -relay wss://relay.example.com/tunnel -register
fylane-companion serve -workspace ~/projects/my-app
```

## Development

```bash
go test ./...

cd desktop/frontend
npm install
npm test          # vitest
npm run harness   # renders each screen against fixtures on :5199
```

The desktop shell needs the [Wails v2](https://wails.io) CLI and Node 22+:

```bash
cd desktop
wails dev
```

## Security

Local approval is the only authority. Platform-side confirmations are hints,
never a security layer. The relay is treated as untrusted with content and
never persists file bodies, diffs, listings, or sensitive file names.

To report a vulnerability, see [SECURITY.md](SECURITY.md).

## License

[Apache 2.0](LICENSE)
