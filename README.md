<div align="center">

<img src="assets/icon.png" width="96" alt="Fylane">

# Fylane

**Let ChatGPT, Claude and Grok on the web read and edit a project on your computer or your VPS. Every edit and command waits for your OK first.**

English | [简体中文](README.zh-CN.md)

[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.25-00ADD8)](go.mod)
[![MCP](https://img.shields.io/badge/protocol-MCP-6E56CF)](https://modelcontextprotocol.io)

</div>

<br>

## What it is

The AI in your browser cannot see the project on your computer. To get a file
changed, you paste code in and paste the answer back.

Fylane runs on your computer. Pick a folder, and the AI in your browser can
connect to it: read files, edit code, run tests.

The AI can only send requests. Fylane makes the actual change or runs the
command on your machine, and shows it to you in its window first. Nothing
happens until you approve.

![A write waiting for approval](assets/approval.png)

What you can do with it:

- Ask ChatGPT "where is this error thrown" and let it read the project. No pasting.
- Let Claude edit three files, check the changes in Fylane, then approve.
- Let Grok run `npm test` and read the results back.
- Works for a project on a VPS too, and you still approve on this computer.
  See [Remote machines](#remote-machines).
- Open a new chat tomorrow and carry on where you left off. See [Memory](#memory).
- Close your laptop and the AI can no longer reach it.

## How it works

```
AI in the browser  ──►  public address (tunnel or relay)  ──►  Fylane on your computer  ──►  the folder you chose
                                                                        │
                                                                approval happens here
```

Only Fylane on your computer touches your files. The public address in the
middle just passes messages along and stores nothing.

When the project is on a VPS, there is one more hop over ssh:

```
AI in the browser  ──►  public address  ──►  Fylane on your computer  ──ssh──►  Fylane on the VPS  ──►  the folder on the server
                                                      │
                                              approval still happens here
```

The Fylane on the VPS is not open to the internet. Only your computer can reach
it, over ssh. Nothing changes on the AI platform: it connects to the same
address.

## Install

Download from [Releases](https://github.com/leazoot/fylane/releases/latest):

| System | Download |
| --- | --- |
| macOS | `fylane-desktop-macos.dmg`, drag into Applications |
| Windows | `fylane-desktop-windows-amd64.zip`, unzip and run `Fylane.exe` |
| Linux / servers | command line only for now, see [Command line](#command-line) |

The packages are not signed yet. On first open, macOS says the developer cannot
be verified: go to System Settings → Privacy & Security and click Open Anyway.
On Windows, when SmartScreen appears, click More info → Run anyway. To check
the files first, see `SHA256SUMS` on the release page.

## First run

Fylane walks you through four steps in its window. No terminal needed.

![Step 1: choose a folder](assets/first-run.png)

1. **Choose a folder.** The AI can see this folder and nothing above it. You
   can change it or take it back at any time.
2. **Try one write.** Fylane writes a sample file into the folder so you can
   see what approving looks like.
3. **Decide what needs asking.** By default Fylane asks once per folder before
   running commands, then ordinary commands just run. File writes and risky
   commands still ask. You can change this later in Settings.
4. **Connect an AI.** This happens on the AI platform, see the next section.

Then you reach the main screen. On the left is the lane, where requests waiting
for you appear. On the right are the current folder and the connected AIs.

![The lane](assets/lane.png)

## Connecting Fylane to an AI platform

Every platform takes three steps: copy the address from Fylane, paste it into
the platform's connector settings, then approve the connection in Fylane.

### Step 1: get the address from Fylane

Open **Settings → Connection**.

![Connection](assets/connection.png)

The first time, choose **Cloudflare quick tunnel** and click Set up. No account
or domain needed. After a few seconds an address like
`https://xxx.trycloudflare.com/mcp` appears. Click Copy.

This address changes every time Fylane restarts, so you will need to paste it
into the platform again. For an address that stays the same, see
[A fixed address](#a-fixed-address).

### Step 2: paste it into the platform

<details open>
<summary><b>ChatGPT</b></summary>

Needs a paid plan (Plus, Pro or Team).

1. Avatar → **Settings → Security and login** → turn on **Developer mode**.
   In older versions it is under Apps & Connectors → Advanced.

   ![Developer mode](assets/setup/chatgpt-developer-mode.png)

2. Go to **Plugins** (called Apps & Connectors in older versions) and click
   **Create**. Fill in:
   - Name: anything, for example `Fylane`
   - Connection: keep **Server URL** and paste the address
   - Authentication: **OAuth**. "No authentication" fails with
     `Error creating connector`.
   - Tick "I understand and want to continue".

   ![New Plugin form](assets/setup/chatgpt-create-connector.png)

3. Click Create. A browser page opens, see step 3.
4. In a chat, click **+** next to the input → **More**, and tick `Fylane`.

</details>

<details>
<summary><b>Claude</b></summary>

1. Avatar at the bottom left → **Settings → Connectors** → **Add custom
   connector**.
2. Name it `Fylane` and paste the address as the URL. **Leave Client ID and
   Client Secret under Advanced empty.** Filling them in causes an error.
3. Click Add, then click **Connect** next to Fylane in the list. An
   authorization page opens, see step 3.
4. In a chat, open the **tools** button on the input and make sure Fylane is on.

In Claude each tool can be set to "ask every time" or "always allow". That is
only Claude's setting. Fylane still asks what it needs to ask.

<!-- screenshot: assets/setup/claude-add-connector.png (the add custom connector form) -->

</details>

<details>
<summary><b>Grok</b></summary>

1. grok.com → **Settings → Connectors** → add a custom MCP connector and paste
   the address.
2. Click connect. An authorization page opens, see step 3.

A few things are different on Grok:

- **Grok does not confirm writes itself.** When using Grok, keep write approval
  on in Fylane.
- Grok has its own cloud sandbox, so "run the tests" may run them there. Say it
  plainly: "use Fylane's run_command to run the tests".
- Grok waits only 60 seconds per call. If you have not approved by then, it is
  told the request is pending. Approve, then ask it to try again.

<!-- screenshot: assets/setup/grok-add-connector.png -->

</details>

### Step 3: approve the connection in Fylane

The platform opens an authorization page with a short code. The Fylane window
shows the same code. Check they match and click **Approve connection**.

![Approve connection](assets/pairing.png)

If you are using a browser on another computer, the page asks for a pairing
code instead. In Fylane, go to **Settings → Connection**, click **Show a code**,
and type it in. A code lasts 10 minutes and works once.

### Try it

Back in the chat, type:

> List the files in the root of this project.

The AI lists the folder. Reading needs no approval. Then try:

> Create hello.txt in the project with the content "hello".

The Fylane window lights up and shows what the AI wants to write. Approve and
the file appears. Reject and the AI is told no.

## Approval and the security boundary

**Writes need your approval.** When the AI wants to create, edit, delete or
move a file, Fylane shows you the change first. Nothing is written until you
approve.

**Mistakes can be undone.** An approved write can be undone from the Tasks
page for 7 days. If the file has changed since, the undo stops instead of
overwriting it.

**Some commands are always refused.** The AI can only run a single program,
not a shell script. Running as `sudo`, deleting files outside the folder,
changing `.git` and writing straight to a disk are refused whatever your
settings.

**Risky commands ask first.** For example `rm -r`, `git push`, throwing away
uncommitted changes, changing file permissions, installing software globally
or listing processes. With commands set to "Allowed inside the workspace",
these no longer ask.

**Anything outside the folder asks separately.** Reading environment variables
or the keychain, or using a path outside the folder, asks every time, at every
setting.

**Sensitive files are hidden.** `.env`, `*.pem`, `id_rsa`, `credentials*` and
similar files are left out of listings and search. If the AI asks for one by
name, you are asked.

**Programs stay inside the folder.** On macOS and Linux, a program Fylane runs
for the AI, such as `npm test`, can only read the folder and the caches its
tools need. Windows does not have this yet.

**The AI does not know where the folder is.** It only sees paths inside the
folder.

**You choose how often you are asked.** Commands have three settings: "Ask
every time", "Ask once per workspace" and "Allowed inside the workspace".
Writes have three: "Ask before every write", "New files write straight
through" and "Write without asking". Deletes and sensitive files ask at every
setting. Whatever you choose, the checks above stay on.

Everything is recorded on this machine. The Tasks page shows each request, who
sent it and how it went.

![Task history](assets/tasks.png)

## Memory

Start a new chat and carry on where you left off.

For each folder, Fylane remembers two things:

- **Where things stand**: what is done, what comes next, what is decided, what is still open.
- **What happened**: the work done and the important decisions along the way.

In a new chat there is no need to explain the background again. Just say
"continue where we left off".

- If it does not pick up, say "check this folder's memory first".
- When you finish a piece of work or make an important decision, say "note this down".

Memory does not keep growing. The current state is kept up to date, and old
notes can be folded into a summary, with the originals still there when you
need them.

On the Memory page you can view, edit, delete, export or clear all of it.
Everything is stored in Fylane, never in your project's files.

## Remote machines

Your project is on a VPS, you are at your Mac, and you want the AI to edit and
test over there. Add that machine to Fylane. Nothing changes on the AI
platform.

**Before you start**: from a terminal on this computer, `ssh user@host` already
logs in without a password. Fylane uses your system ssh, so your keys and
`~/.ssh/config` work as usual. It never asks for a password.

1. On the lane, under **Machine**, click **Switch machine → Add a remote
   machine…** and type what you would type after `ssh`, such as an alias or
   `user@host`.
2. If Fylane is not on that machine yet, click **Install Fylane** in the rail.
   It installs the same version as this app.
3. When it says **Connected**, click **Choose a folder** under Workspace and
   pick a folder on that machine, or type a path such as `~/project`.

After that it works like a local folder. Reads, writes and commands happen on
the VPS, and you approve on your Mac. On the Tasks page, records from a remote
machine show the machine's name.

Good to know:

- **The Fylane on the VPS is not open to the internet.** The AI reaches it
  through your computer, so when your computer is off, that VPS is unavailable
  too.
- **You approve on this computer only.** Commands on a remote machine ask every
  time by default.
- **Switching machines only changes which machine the lane shows.** The Tasks
  page still shows all machines, and pending requests are never hidden.
- The remote machine keeps its data in `~/.fylane/` there. **Remove** only
  makes this computer forget the machine. Nothing on it is deleted.
- Remote settings cannot be changed from the window yet. The Windows app needs
  the built-in OpenSSH client.

## A fixed address

The Cloudflare quick tunnel gets a new address on every restart. For one that
stays the same, pick another option under **Settings → Connection**:

| Way | What it needs | Address |
| --- | --- | --- |
| Cloudflare quick tunnel | nothing | changes on restart |
| Tailscale Funnel | install Tailscale, sign in once (free) | fixed, `xxx.ts.net` |
| Cloudflare named tunnel | a domain hosted on Cloudflare | fixed, your own domain |
| ngrok | an ngrok account | free tier changes, paid stays |
| Your own relay | a server with a public domain | fixed, your own domain |

Fylane starts and manages the first four for you. Click Set up in the window.

Your own relay suits a team, or several computers sharing one address. It runs
on your server and stores no file content. See [`deploy/`](deploy/):

```bash
FYLANE_RELAY_HOST=relay.example.com docker compose -f deploy/docker-compose.yml up -d
```

## Command line

On a machine without the desktop app, such as a Linux server, use
`fylane-companion`. It does the same job, with approvals in the terminal: press
`y` to approve, any other key to reject. Deleting a whole folder needs you to
type `yes`.

Install (macOS and Linux):

```bash
curl -fsSL https://raw.githubusercontent.com/leazoot/fylane/main/scripts/install.sh | sh
```

Then, inside a project:

```bash
cd ~/projects/my-app
fylane-companion share
```

It prints the address and a pairing code:

```
sharing my-app — starting a tunnel, this takes a few seconds

  Connector URL   https://swift-lane-9f2c.trycloudflare.com/mcp
  Pairing code    7K4M-2QB9   (valid for 10m0s)
```

Then it is the same as the desktop app: paste the address into the platform
and enter the pairing code on the authorization page. Press Ctrl-C to stop. On
a server over ssh, run it inside `tmux` or `screen` so it keeps running when
you disconnect.

With your own relay:

```bash
fylane-companion pair -relay wss://relay.example.com/tunnel -register
fylane-companion serve -workspace ~/projects/my-app
```

Building from source needs Go 1.25:

```bash
git clone https://github.com/leazoot/fylane
cd fylane
go build -o bin/fylane-companion ./companion/cmd/companion
```

## The tools the AI gets

| | Tools |
| --- | --- |
| Read | `list_directory` `read_file` `read_files` `search_files` `stat_path` `git_query` |
| Write | `write_file` `edit_file` `apply_patch` `change_manage` |
| Run | `run_command` `task_status` `code_task` |
| Remember | `memory_recall` `memory_note` `memory_search` `memory_read` `memory_compact` |
| Navigate | `code_navigate`, finds definitions and references |
| Extend | `mcp_gateway`, passes calls to another MCP server on your computer |

Writes and commands that need approval return `pending_approval` until you
decide in Fylane. `change_manage` handles moves, deletes and undo.

## Questions people ask

| | |
| --- | --- |
| Does my code go to a server? | What the AI reads is sent to the AI platform, just like pasting it. It goes nowhere else, and the address in the middle stores nothing. |
| Can the AI delete my project? | Deleted files go to a local recycle area first. The folder itself and `.git` cannot be deleted. Deleting a folder that has files in it asks twice. |
| What if I am away from the computer? | The request waits in the window. Nothing happens until you respond. If that is too slow, ask less often in Settings. |
| Can it run anything? | No. It runs single programs only. `sudo` and deleting files outside the folder are always refused, and `rm -r` or `git push` ask first by default. |
| Does the AI remember the project between chats? | Yes, see [Memory](#memory). |
| Does it work with a project on my VPS? | Yes. It connects over the ssh you already use, and you still approve on this computer. |
| Which AIs? | ChatGPT, Claude and Grok, set up with the three steps above. Other apps that support remote MCP can connect the same way. |

## Development

```bash
go test ./...

cd desktop/frontend
npm install
npm test          # vitest
npm run harness   # previews every screen with sample data on :5199
```

The desktop app needs the [Wails v2](https://wails.io) CLI and Node 22+:

```bash
cd desktop
wails dev
```

## Security

Only your approval on this machine counts. Confirmations on the AI platform are
just hints. The relay never stores file content, changes, folder listings or
sensitive file names.

Known limits and how to report a vulnerability: [SECURITY.md](SECURITY.md).

## License

[Apache 2.0](LICENSE)
