import { createContext, useContext } from "react";

// One language at a time. The window used to mix an English frame with
// Chinese explanatory lines (the design package's own pattern); the product
// a later decision replaced it with a real switch,
// so every visible string lives here in both languages and nowhere else.
//
// Keys are flat and read like paths. `desktop/frontend/src/lib/i18n.test.ts`
// fails if a key is missing on either side, if an English string carries
// Chinese characters, or if any other source file does — that last check is
// what keeps a stray hard-coded sentence from creeping back in.

export type Lang = "en" | "zh";

export const LANGS: { key: Lang; label: string }[] = [
  { key: "en", label: "English" },
  { key: "zh", label: "中文" },
];

const STORAGE_KEY = "fylane.lang";

/** storedLang is the user's choice, or the system's language on first run. */
export function storedLang(): Lang {
  try {
    const saved = window.localStorage.getItem(STORAGE_KEY);
    if (saved === "en" || saved === "zh") {
      return saved;
    }
  } catch {
    // Private mode or a locked-down profile: fall through to the system.
  }
  return /^zh/i.test(window.navigator.language ?? "") ? "zh" : "en";
}

export function storeLang(lang: Lang): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, lang);
  } catch {
    // Not being able to remember the choice is not a reason to refuse it.
  }
}

export const DICT = {
  // ── shell ──────────────────────────────────────────────────────────
  // Desktop v2 §1: three top-level words, and the window has no fourth
  // screen for a fourth one to name.
  "nav.lane": { en: "Lane", zh: "通道" },
  "nav.settings": { en: "Settings", zh: "设置" },
  "nav.tasks": { en: "Tasks", zh: "任务" },
  "shell.actions": { en: "Actions", zh: "操作" },
  // The Node dock (Fylane-V3 board 03) and the one line the title bar keeps.
  "shell.navOpen": { en: "Open navigation", zh: "展开导航" },
  "shell.navClose": { en: "Close navigation", zh: "收起导航" },
  "shell.titleCalm": { en: "Fylane · lane clear", zh: "Fylane · 通道畅通" },
  "shell.titlePaused": { en: "Fylane · paused", zh: "Fylane · 已暂停" },
  "shell.titleRunning": { en: "Fylane · running a command", zh: "Fylane · 正在执行" },
  "shell.titleHeld_one": { en: "Fylane · 1 request waiting", zh: "Fylane · 1 个请求等待批准" },
  "shell.titleHeld_other": {
    en: "Fylane · {n} requests waiting",
    zh: "Fylane · {n} 个请求等待批准",
  },
  "shell.titleOffline": { en: "Fylane · not running", zh: "Fylane · 未运行" },
  "shell.resume": { en: "Resume lane", zh: "恢复通道" },
  "shell.pause": { en: "Pause lane", zh: "暂停通道" },
  // Windows caption buttons. macOS draws its own, so these are never read
  // there — they exist for the screen reader, not for the surface.
  "shell.minimise": { en: "Minimise", zh: "最小化" },
  "shell.maximise": { en: "Maximise", zh: "最大化" },
  "shell.close": { en: "Close window", zh: "关闭窗口" },
  "shell.noWorkspace": { en: "not set", zh: "未设置" },
  "shell.dismiss": { en: "Dismiss", zh: "知道了" },
  "shell.errAddFolder": { en: "Could not add the folder.", zh: "没能添加这个目录。" },
  "shell.errLaneState": { en: "Could not change the lane state.", zh: "没能改变通道状态。" },
  "shell.errApprove": { en: "Could not approve.", zh: "没能放行。" },
  "shell.errReject": { en: "Could not reject.", zh: "没能拒绝。" },
  "shell.errRollback": { en: "Could not roll back.", zh: "没能回滚。" },
  "shell.errAccept": { en: "Could not record the acceptance.", zh: "没能记下验收。" },
  "shell.errRollbackIncomplete": { en: "Rollback did not complete.", zh: "回滚没有完成。" },
  "shell.errSwitchWorkspace": { en: "Could not switch workspace.", zh: "没能切换工作区。" },
  "shell.errClearRecords": { en: "Could not clear the records.", zh: "没能清理记录。" },
  "shell.errClearBackups": { en: "Could not clear the undo copies.", zh: "没能清除撤销副本。" },
  "shell.errPrefs": { en: "Could not change the setting.", zh: "没能修改这项设置。" },
  "shell.errGate": { en: "Could not change the gate.", zh: "没能修改审批设置。" },
  // Opening the page reads; it does not change anything. Saying "could not
  // change" there names an edit the user never made.
  "shell.errPrefsRead": { en: "Could not read the settings.", zh: "没能读取设置。" },
  "shell.errGateRead": { en: "Could not read the approval settings.", zh: "没能读取审批设置。" },
  "shell.errOpenDir": { en: "Could not open the folder.", zh: "没能打开这个目录。" },
  "shell.errStartCore": { en: "Could not start the core.", zh: "没能启动 Core。" },
  "shell.errPairing": { en: "Could not answer the pairing request.", zh: "没能回应这次配对请求。" },

  // ── lane (Desktop v2 §5) ───────────────────────────────────────────
  "laneV2.subCalm": { en: "Nothing is waiting.", zh: "没有等待处理的请求。" },
  "laneV2.subRunning": { en: "A command is running.", zh: "有命令正在执行。" },
  "laneV2.subWaiting_one": { en: "One request is waiting for you.", zh: "有 1 个请求等你批准。" },
  "laneV2.subWaiting_other": {
    en: "{n} requests are waiting for you.",
    zh: "有 {n} 个请求等你批准。",
  },
  "laneV2.subPaused": { en: "Paused — nothing gets through.", zh: "已暂停，什么都过不去。" },
  "laneV2.subNoWorkspace": { en: "No folder yet.", zh: "还没有选目录。" },
  "laneV2.subOffline": { en: "Fylane is not running.", zh: "Fylane 没有在运行。" },
  "laneV2.asksToRun": { en: "asks to run", zh: "请求执行" },
  "laneV2.asksToWrite": { en: "asks to write", zh: "请求写入" },
  "laneV2.asksToSend": { en: "wants to send this out", zh: "请求发送出去" },
  "laneV2.asksToDelegate": { en: "asks to take over", zh: "请求接管工作区" },
  "laneV2.asksToForward": { en: "asks to use a tool Fylane cannot check", zh: "请求调用 Fylane 无法核验的工具" },
  "laneV2.runsIn": { en: "Runs in", zh: "在" },
  "laneV2.allow": { en: "Allow", zh: "放行" },
  "laneV2.allowHere": { en: "Allow in this folder", zh: "授权此工作区" },
  "laneV2.details": { en: "Details", zh: "查看详情" },
  "laneV2.hideDetails": { en: "Hide details", zh: "收起详情" },
  "laneV2.reject": { en: "Reject", zh: "拒绝" },
  "laneV2.mSource": { en: "SOURCE", zh: "来源" },
  "laneV2.mAsked": { en: "ASKED AT", zh: "请求时间" },
  "laneV2.mDir": { en: "WORKING DIRECTORY", zh: "工作目录" },
  "laneV2.mID": { en: "REQUEST ID", zh: "请求 ID" },
  "laneV2.mSafety": { en: "SAFETY", zh: "安全提示" },
  "laneV2.mChanges": { en: "CHANGES", zh: "写入内容" },
  "laneV2.viaBrowser": { en: "{who} · browser session", zh: "{who} · 浏览器会话" },
  "laneV2.safetyDefault": {
    en: "It runs inside the granted folder, with a timeout and an output cap, and it is recorded either way.",
    zh: "它在授权目录里执行，有超时和输出上限，无论结果如何都会被记录。",
  },
  "laneV2.calmTitle": { en: "The lane is clear", zh: "通道畅通" },
  "laneV2.calmBody": {
    en: "Nothing is waiting and nothing is running. When a platform asks for something, it stops here first.",
    zh: "没有等待批准的请求，也没有正在执行的命令。平台下次发起请求时，会先停在这里。",
  },
  "laneV2.offlineTitle": { en: "Fylane is not running", zh: "Fylane 没有在运行" },
  "laneV2.offlineBody": {
    en: "Nothing can reach this computer until it is back. Starting it changes nothing else.",
    zh: "在它回来之前，什么都到不了这台电脑。启动它不会改变别的东西。",
  },
  "laneV2.startCore": { en: "Start Fylane", zh: "启动 Fylane" },
  "laneV2.runningMeta": { en: "{who} · running for {time}", zh: "{who} · 已执行 {time}" },
  "laneV2.stop": { en: "Stop", zh: "停止" },
  "laneV2.recent": { en: "RECENT", zh: "最近任务" },
  "laneV2.seeAll": { en: "See all →", zh: "查看全部 →" },
  "laneV2.workspace": { en: "WORKSPACE", zh: "工作区" },
  "laneV2.localConnected": { en: "Local · connected", zh: "本地 · 已连接" },
  "laneV2.pausedShort": { en: "Paused", zh: "已暂停" },
  "laneV2.noFolder": { en: "No folder granted", zh: "还没有授权目录" },
  "laneV2.switch": { en: "Switch", zh: "切换" },
  "laneV2.chooseFolder": { en: "Choose a folder", zh: "选择目录" },
  "laneV2.openDir": { en: "Open folder", zh: "打开目录" },
  "laneV2.grantedFolders": { en: "GRANTED FOLDERS", zh: "已授权的工作区" },
  "laneV2.addFolder": { en: "Add a folder…", zh: "添加目录…" },
  "laneV2.connectedAI": { en: "CONNECTED AI", zh: "已连接的 AI" },
  "laneV2.asking": { en: "Asking", zh: "请求中" },
  "laneV2.connected": { en: "Connected", zh: "已连接" },
  "laneV2.notConnected": { en: "Not connected", zh: "未连接" },
  "laneV2.pausedTitle": { en: "Fylane is paused", zh: "Fylane 已暂停" },
  "laneV2.pausedBody": {
    en: "New commands stop at the gate until you resume.",
    zh: "新的命令会挡在闸门前，直到你恢复。",
  },

  // ── lane (Fylane-V3, boards 01–04) ─────────────────────────────────
  // The v3 scene states its own headline in the serif, so the words below
  // are statements rather than the labels v2 put above a card.
  "laneV3.allow": { en: "Approve", zh: "批准执行" },
  "laneV3.ordinary": { en: "Ordinary command", zh: "普通命令" },
  "laneV3.mBoundary": { en: "BOUNDARY", zh: "边界" },
  "laneV3.runningHeadline": { en: "Running on this machine", zh: "正在本机执行" },
  "laneV3.calmHeadline": { en: "Nothing is waiting for approval", zh: "没有等待批准的请求" },
  "laneV3.offlineTag": { en: "Not running", zh: "未运行" },
  "laneV3.noFolderHeadline": { en: "No folder has been granted", zh: "还没有授权目录" },
  "laneV3.noFolderBody": {
    en: "Fylane can reach nothing until one folder is chosen. Only that folder, and nothing above it.",
    zh: "在选定一个目录之前，Fylane 碰不到任何东西。只有那个目录，它的上层不算。",
  },
  // Said on the row itself, because that is where the difference lives: one
  // operation covers a whole tree, and one path in the list may be a file
  // whose contents are the reason to stop.
  "laneV3.opRecursive": {
    en: "the whole directory and everything in it",
    zh: "整个目录及其中的全部内容",
  },
  "laneV3.opRecursiveCount": {
    en: "the whole directory and everything in it — {files}, {size}",
    zh: "整个目录及其中的全部内容 —— {files},{size}",
  },
  "laneV3.opRecursiveAtLeast": {
    en: "the whole directory and everything in it — at least {files}, {size}",
    zh: "整个目录及其中的全部内容 —— 至少 {files},{size}",
  },
  "laneV3.opFiles_one": { en: "{n} file", zh: "{n} 个文件" },
  "laneV3.opFiles_other": { en: "{n} files", zh: "{n} 个文件" },
  "laneV3.opBeyondUndo": {
    en: "too large for the recycle area — this one may not come back",
    zh: "超出回收区容量 —— 这一次可能撤不回来",
  },
  "laneV3.confirmDelete": { en: "Delete it all", zh: "全部删除" },
  "laneV3.confirmAsk": { en: "Sure?", zh: "确定?" },
  "laneV3.opSensitive": { en: "sensitive file", zh: "敏感文件" },
  // Reach. Zero is said rather than hidden: "no other callers" is a
  // reason to be less careful, and a blank row would mean both that and
  // "nobody asked", which are opposite kinds of nothing.
  "laneV3.opNoCallers": { en: "no other callers", zh: "无其他调用方" },
  "laneV3.opCallers_one": { en: "{n} caller elsewhere", zh: "{n} 处其他调用方" },
  "laneV3.opCallers_other": { en: "{n} callers elsewhere", zh: "{n} 处其他调用方" },
  "laneV3.opCallersAtLeast": {
    en: "at least {n} callers elsewhere",
    zh: "至少 {n} 处其他调用方",
  },
  "laneV3.passedToday": { en: "PASSED TODAY", zh: "今日已通过" },
  "laneV3.rejectedToday": { en: "REJECTED", zh: "已拒绝" },

  // ── settings (Desktop v2 §7) ───────────────────────────────────────
  "set.title": { en: "Settings", zh: "设置" },
  "set.sub": {
    en: "Which commands have to ask you, and how Fylane reaches and protects this machine.",
    zh: "哪些命令需要问你,以及 Fylane 如何连接并保护这台设备。",
  },
  "set.basics": { en: "Basics", zh: "基础" },
  "set.basicsNote": { en: "Language, appearance and startup.", zh: "语言、外观与启动方式。" },
  "set.autostart": { en: "Start at login", zh: "开机启动" },
  // The cell holds two switches now — how the app lives on this machine —
  // so its title names that, and each switch carries its own label.
  "set.presence": { en: "Startup & presence", zh: "启动与常驻" },
  "set.dock": { en: "Hide Dock icon", zh: "隐藏 Dock 图标" },
  "set.dockNote": { en: "Menu bar only", zh: "只留菜单栏" },
  // Beside the switch in a third-width cell. The design puts a short state
  // label there, not a sentence: the cell's own title already says what the
  // setting is, and an explanation wraps to three lines at this width.
  "set.autostartNote": { en: "Runs after you log in", zh: "登录后自动运行" },
  "set.autostartUnsupported": {
    en: "This build cannot register a login item on this system.",
    zh: "这个版本无法在当前系统上注册开机启动项。",
  },
  "set.execution": { en: "Execution & approval", zh: "执行与审批" },
  "set.executionNote": {
    en: "Decides when Fylane has to stop and ask you.",
    zh: "决定 Fylane 什么时候必须停下来问你。",
  },
  "set.ordinary": { en: "ORDINARY COMMANDS", zh: "普通命令" },
  "set.rungStrict": { en: "Ask every time", zh: "每次询问" },
  "set.rungStrictNote": {
    en: "Every command stops at the gate and waits to be let through.",
    zh: "每条命令都在 Gate 停下等待放行。",
  },
  "set.rungWorkspace": { en: "Ask once per workspace", zh: "每个工作区首次询问" },
  "set.rungWorkspaceNote": {
    en: "After the first yes, ordinary commands run in that workspace.",
    zh: "同一工作区内之后直接执行。",
  },
  "set.rungOpen": { en: "Allowed inside the workspace", zh: "工作区内允许" },
  "set.writes": { en: "FILE WRITES", zh: "文件写入" },
  "set.writeSafe": { en: "Ask before every write", zh: "每次写入都询问" },
  "set.writeSafeNote": {
    en: "Every change set stops at the gate, whatever it holds.",
    zh: "无论内容是什么,每一批写入都在 Gate 停下。",
  },
  "set.writeBalanced": { en: "New files write straight through", zh: "新建文件直接写入" },
  // The exclusions, not the rule. Someone flipping this is deciding what they
  // are giving up, and what they keep is the part that answers that.
  "set.writeBalancedNote": {
    en: "Only when every file in the set is new and not sensitive. Edits, deletes and sensitive paths still ask.",
    zh: "仅当这一批全是新建的非敏感文件。改写、删除、敏感路径照旧询问。",
  },
  "set.rungOpenNote": {
    en: "Ordinary commands run in the current workspace without asking.",
    zh: "普通命令在当前工作区内直接执行。",
  },
  "set.cancel": { en: "Cancel", zh: "取消" },
  "set.openConfirmTitle": {
    en: "Ordinary commands will run without asking",
    zh: "普通命令将不再询问你",
  },
  "set.openConfirmBody": {
    en: "In the current workspace, ordinary commands run the moment a model asks. High-risk commands and anything that reads this machine still stop and ask, and every command is still recorded.",
    zh: "在当前工作区里,模型一提出请求,普通命令就直接执行。高风险命令、以及读取这台机器的命令仍会停下来问你,每条命令也仍会记录。",
  },
  "set.openConfirmYes": { en: "I understand, turn it on", zh: "我明白,启用" },
  "set.openWarning": { en: "Ordinary commands run without asking", zh: "普通命令正在无询问执行" },
  "set.openWarningBody": {
    en: "High-risk commands and anything that reads this machine still ask, and everything is still recorded on the tasks page.",
    zh: "高风险命令、以及读取这台机器的命令仍会询问,所有执行仍记录在任务页。",
  },
  "set.risky": { en: "High-risk commands", zh: "高风险命令" },
  "set.riskyNote": {
    en: "Deleting files, killing processes, escalating privilege, changing system directories: asked every time, whatever the setting above.",
    zh: "删除文件、结束进程、提权、修改系统目录:不管上面选哪一档,每次都会先问你。",
  },
  "set.riskyValue": { en: "Always asks", zh: "始终询问" },
  "set.fixed": { en: "Fixed", zh: "固定" },
  "set.riskyTip": {
    en: "Safety principle: high-risk commands cannot be set to always allow. Fylane asks you every time.",
    zh: "安全原则:高风险命令不能设为始终允许,Fylane 每次都会请求你的确认。",
  },
  "set.timeout": { en: "Task timeout", zh: "任务超时" },
  "set.timeoutNote": {
    en: "A command still running after this long is ended and recorded as timed out.",
    zh: "运行超过这个时间的命令会被结束,并记为超时。",
  },
  "set.secs": { en: "{n}s", zh: "{n} 秒" },
  "set.mins": { en: "{n} min", zh: "{n} 分钟" },
  "set.allowStop": { en: "Allow stopping a task", zh: "允许随时停止任务" },
  "set.allowStopNote": {
    en: "Shows a Stop button on the task list so a running command can be interrupted.",
    zh: "任务列表里显示「停止」按钮,可以中断正在运行的命令。",
  },
  "laneV3.mNetwork": { en: "NETWORK", zh: "网络" },
  "set.network": { en: "Outbound network", zh: "出站网络" },
  "set.networkNote": {
    en: "Lets commands in each folder reach the network. Turning it off makes builds that download dependencies fail. On by default.",
    zh: "允许每个目录里的命令联网。关闭后,需要下载依赖的构建会失败。默认允许。",
  },
  "set.netAllowed": { en: "can reach the network", zh: "可以联网" },
  "set.netDenied": { en: "no network", zh: "不能联网" },
  "set.netPartial": { en: "no TCP; DNS and QUIC still leave", zh: "已拒 TCP;DNS 与 QUIC 仍会外发" },
  "set.netUnbounded": { en: "asked for, this machine cannot", zh: "已要求,这台机器做不到" },
  "set.netPartialWhy": {
    en: "This kernel's Landlock denies outbound TCP and has no rule for UDP, so DNS and QUIC are not stopped. macOS denies every socket.",
    zh: "这个内核的 Landlock 只能拒绝出站 TCP,没有 UDP 规则,因此 DNS 与 QUIC 拦不住。macOS 上是全部拒绝。",
  },
  "set.netUnboundedWhy": {
    en: "These folders ask for no network and this machine has no way to deny it. Nothing else changed: approval, the rule table, the path sandbox and the record are as they were.",
    zh: "这些文件夹要求不联网,而这台机器没有办法拒绝。其他一切不变:审批、规则表、路径沙箱与记录都照旧。",
  },
  "record.density": { en: "Density", zh: "密度" },
  "record.roomy": { en: "Roomy", zh: "舒适" },
  "record.compact": { en: "Compact", zh: "紧凑" },
  "set.servers": { en: "Code navigation", zh: "代码导航" },
  "set.serversNote": {
    en: "The AI can look up where a function or variable is defined and where it is used. Read-only; nothing is written.",
    zh: "AI 可以查到函数或变量在哪定义、在哪被使用。只读,不会改文件。",
  },
  "set.serverFiles": { en: "{files} files", zh: "{files} 文件" },
  "set.serverMore": { en: "{shown} and {rest} more", zh: "{shown} 等 {rest} 种" },
  "set.serverRunning": { en: "running", zh: "运行中" },
  // Not "not running". It is installed and it works; it simply has not been
  // asked anything yet, and starts when it is. "Not running" reads as a fault
  // report for the normal state.
  "set.serverIdle": { en: "starts when needed", zh: "用到时启动" },
  "set.readBoundary": { en: "Subprocess read boundary", zh: "子进程读边界" },
  "set.readBoundaryNote": {
    en: "Programs the AI runs can read only this folder and the toolchain caches. The operating system blocks everything else.",
    zh: "AI 运行的程序只能读取这个目录和工具链缓存,电脑上的其他文件由系统拦下。",
  },
  "set.readBoundaryAbsent": {
    en: "This system offers no such boundary, so subprocess reads are not bounded. Every other check runs exactly as it does elsewhere.",
    zh: "当前系统没有这种边界,子进程的读取不受限制。其他所有检查与在别处完全一样。",
  },
  "set.readBoundaryOff": { en: "Subprocess reads are not bounded", zh: "子进程读取不受限制" },
  "set.readBoundaryOffBody": {
    en: "Programs Fylane starts can read anything your account can. Approval, the rule table, the path sandbox and the record are unchanged.",
    zh: "Fylane 启动的程序能读取你的账户能读取的一切。审批、规则表、路径沙箱与记录一律不变。",
  },
  "set.proxies": { en: "Forwarded MCP tools", zh: "转发的 MCP 工具" },
  "set.proxiesNote": {
    en: "Local MCP servers Fylane will pass calls to. Fylane cannot check what their tools do, so they are added by editing the config file on this machine and nowhere else.",
    zh: "Fylane 会向其转发调用的本机 MCP 服务。Fylane 无法核验这些工具做什么,因此只能在本机改配置文件添加,别处都不行。",
  },
  "set.proxyAsks": { en: "asks every time", zh: "每次都问" },
  "set.proxyFollows": { en: "follows the rung", zh: "跟随档位" },
  "set.proxyWarn": {
    en: "These tools run under the workspace grant instead of asking. Fylane cannot tell what they do — the rule table and the path sandbox do not apply to them.",
    zh: "这些工具按工作区授权直接运行,不再逐次询问。Fylane 无法判断它们做什么 —— 规则表与路径沙箱对它们不生效。",
  },
  "set.connection": { en: "Connection", zh: "连接" },
  "set.connectionNote": {
    en: "Which link the AI in your browser uses to find this machine.",
    zh: "浏览器中的 AI 通过哪条链路找到这台设备。",
  },
  "set.hosted": { en: "Fylane hosted · nothing to install", zh: "Fylane 托管 · 无需安装" },
  "set.otherWays": { en: "OTHER WAYS", zh: "其他方式" },
  "set.setup": { en: "Set up →", zh: "设置 →" },
  "set.setupClose": { en: "Close", zh: "收起" },
  "set.useThis": { en: "Use this", zh: "使用此方式" },
  // No command to copy: the panel above opens the vendor's download page,
  // because the Core never downloads an executable itself.
  "set.installFirst": {
    en: "Install it from the download page above first; once it is there, Fylane starts it for you.",
    zh: "先从上面的下载页安装,装好后 Fylane 会替你启动。",
  },
  "set.privacy": { en: "Privacy & this machine", zh: "隐私与本地" },
  "set.privacyNote": { en: "A few things Fylane will not do.", zh: "Fylane 的几条硬性原则。" },
  // The undo copies. Removing them is the one action on this page that takes
  // a capability away rather than a record, so it says so and asks twice.
  "set.undo": { en: "Writes can be taken back", zh: "写入可以撤销" },
  "set.undoNone": {
    en: "Nothing is within its undo window right now.",
    zh: "当前没有处于撤销窗口内的写入。",
  },
  "set.undoSome_one": {
    en: "One write can still be undone. Its copy is kept here for 7 days.",
    zh: "有 1 次写入还能撤销，副本在本机保留 7 天。",
  },
  "set.undoSome_other": {
    en: "{n} writes can still be undone. Their copies are kept here for 7 days.",
    zh: "有 {n} 次写入还能撤销，副本在本机保留 7 天。",
  },
  "set.grants": { en: "Folders already authorized", zh: "已授权的目录" },
  "set.grantsNoteWorkspace": {
    en: "After one command is approved in a folder, ordinary commands there no longer ask. Withdraw to be asked again.",
    zh: "在一个目录里批准过一条命令后,那里的普通命令不再询问。收回后会重新询问。",
  },
  "set.grantsNoteStrict": {
    en: "Set to ask every time, so these authorizations are not in effect. Folders authorized earlier are listed so you can withdraw them.",
    zh: "当前设为每次都问,这些授权暂不生效。之前授权过的目录列在这里,可以收回。",
  },
  "set.grantsNoteOpen": {
    en: "Set to run ordinary commands without asking, so these authorizations make no difference. They apply again when you choose a stricter setting.",
    zh: "当前设为普通命令不询问,这些授权暂不起作用。改回更严格的设置时会重新生效。",
  },
  "set.grantsNone": {
    en: "No folder is authorized. Every command is asked about.",
    zh: "没有目录被授权。每条命令都会询问。",
  },
  "set.grantSince": { en: "Authorized {ago}.", zh: "{ago}授权。" },
  "set.grantStale": { en: "Authorized {ago} · not in effect", zh: "{ago}授权 · 暂不生效" },
  "set.grantStaleDetail": {
    en: "Given under “{rung}”. The setting is stricter now, so this folder is asked about again.",
    zh: "当时在「{rung}」下授权。现在的设置更严格,这个目录会重新询问。",
  },
  "set.grantInert": { en: "Authorized {ago} · not in effect", zh: "{ago}授权 · 暂不生效" },
  "set.grantInertDetail": {
    en: "Applies again when you choose a stricter setting. Withdrawing now changes nothing today.",
    zh: "改回更严格的设置时会重新生效。现在收回不影响任何事。",
  },
  "set.grantWithdraw": { en: "Withdraw", zh: "收回" },
  "set.undoClear": { en: "Clear the undo copies", zh: "清除撤销副本" },
  "set.undoAsk": {
    en: "Clearing them cannot be reversed: the writes stay on disk and stop being undoable. The records of what was written remain.",
    zh: "清除之后无法恢复：文件保持原样，但不能再撤销。写入记录本身仍然保留。",
  },
  "set.undoConfirm": { en: "Clear them", zh: "确认清除" },
  "set.undoDone_one": {
    en: "Cleared. One write can no longer be taken back.",
    zh: "已清除。有 1 次写入不能再撤销了。",
  },
  "set.undoDone_other": {
    en: "Cleared. {n} writes can no longer be taken back.",
    zh: "已清除。有 {n} 次写入不能再撤销了。",
  },
  "set.undoDoneNone": {
    en: "Nothing was there to clear.",
    zh: "没有可清除的副本。",
  },
  "set.undoDoneLeft": {
    en: "{n} copies could not be deleted and are still taking space on disk. They no longer undo anything.",
    zh: "有 {n} 份副本没能删掉，仍占着磁盘空间，但已经撤销不了任何东西。",
  },
  "set.undoCancel": { en: "Keep them", zh: "取消" },
  "set.p1": { en: "Runs on this machine", zh: "本机执行" },
  "set.p1d": {
    en: "Commands are executed by this device. There is no cloud runner.",
    zh: "命令最终由这台设备执行,不存在云端执行环境。",
  },
  "set.p1tag": { en: "Local", zh: "本地" },
  "set.p2": { en: "Workspace boundary", zh: "工作区边界" },
  "set.p2d": {
    en: "Only inside the folders you granted, and it never changes folder by itself.",
    zh: "只在你授权的目录内工作,不会自行更换目录。",
  },
  "set.p3": { en: "Task content is not uploaded", zh: "不上传任务内容" },
  "set.p3d": {
    en: "Commands and their output travel the connection and are not kept in the cloud.",
    zh: "命令与输出只在连接通道内传输,不做云端留存。",
  },
  "set.p3tag": { en: "No upload", zh: "无上传" },
  "set.p4": { en: "Records stay on this machine", zh: "任务记录留在本机" },
  "set.p4d": {
    en: "{count} kept here, clearable at any time.",
    zh: "{count}留在本机,可以随时清理。",
  },

  // ── settings (Fylane-V3, boards 08–12) ─────────────────────────────
  "setV3.current": { en: "CURRENT", zh: "当前" },
  "setV3.rules": { en: "EXECUTION RULES", zh: "执行规则" },
  "setV3.inEffect": { en: "IN EFFECT", zh: "当前生效" },
  "setV3.endpoint": { en: "ENDPOINT", zh: "地址" },
  "setV3.p2tag": { en: "Protected", zh: "边界" },
  // The second line of the page head, revealed on hover. It names the build
  // and where it runs — the design shows the OS release and Node version, and
  // this window collects neither, so it says the two things it does know
  // rather than a version number it would have to invent.
  "set.buildOn": { en: "{os} · desktop build", zh: "{os} · 桌面版" },
  "set.updateHere": { en: "{version} is out", zh: "新版本 {version} 已发布" },
  "set.updateGet": { en: "Get it", zh: "去下载" },
  "settings.language": { en: "Language", zh: "语言" },
  "settings.appearance": { en: "Appearance", zh: "外观" },
  "settings.themeAuto": { en: "Auto", zh: "自动" },
  "settings.themeLight": { en: "Light", zh: "亮色" },
  "settings.themeDark": { en: "Dark", zh: "深色" },

  // ── connection ─────────────────────────────────────────────────────
  "conn.title": { en: "Connection", zh: "接入方式" },
  "conn.state.stopped": { en: "no tunnel running", zh: "未运行" },
  "conn.state.starting": { en: "starting", zh: "启动中" },
  "conn.state.running": { en: "running", zh: "运行中" },
  "conn.state.failed": { en: "not running", zh: "未能运行" },
  "conn.noAddress": {
    en: "No address yet — start one of the tunnels below.",
    zh: "还没有地址 —— 先启动下面任意一条隧道。",
  },
  "conn.copy": { en: "Copy", zh: "复制" },
  "conn.copied": { en: "Copied", zh: "已复制" },
  "conn.starting": { en: "Starting…", zh: "启动中…" },
  "conn.inUse": { en: "IN USE", zh: "使用中" },
  "conn.stable": { en: "FIXED ADDRESS", zh: "地址固定" },
  "conn.temporary": { en: "NEW ADDRESS EACH START", zh: "每次启动换地址" },
  "conn.fieldHostname": { en: "HOSTNAME", zh: "域名" },
  "conn.fieldToken": { en: "TUNNEL TOKEN", zh: "隧道 Token" },
  "conn.cfQuick": { en: "Cloudflare quick tunnel", zh: "Cloudflare 快速隧道" },
  "conn.cfQuickNote": {
    en: "No account, no domain. The address changes on every restart, so the platform must be reconnected.",
    zh: "无需账号与域名。每次重启地址变化，平台需重新接入。",
  },
  "conn.cfNamed": { en: "Cloudflare named tunnel", zh: "Cloudflare 命名隧道" },
  "conn.cfNamedNote": {
    en: "Your own domain, one address that stays. Sign in to Cloudflare once.",
    zh: "使用自有域名，地址固定。需在浏览器登录一次 Cloudflare。",
  },
  "conn.tailscale": { en: "Tailscale Funnel", zh: "Tailscale Funnel" },
  "conn.tailscaleNote": {
    en: "Publishes on your tailnet name. No domain needed, and the address stays.",
    zh: "以 tailnet 名称对外发布。无需域名，地址固定。",
  },
  "conn.ngrok": { en: "ngrok", zh: "ngrok" },
  "conn.ngrokNote": {
    en: "Paste an authtoken once. A free account gets a new address each start.",
    zh: "需粘贴一次 authtoken。免费账号每次启动更换地址。",
  },
  "conn.relay": { en: "Relay", zh: "中继" },

  // Getting a way in ready. Nothing here asks for a terminal: either the
  // vendor's own browser sign-in runs, or its token page is opened for the
  // user to copy from.
  "conn.needInstall": { en: "{name} is not installed", zh: "未安装 {name}" },
  "conn.openDownload": { en: "Open the download page", zh: "打开下载页" },

  // Fetching the program itself. The offer states what would arrive
  // before it asks, because these three facts are what the download is
  // actually bound by — the source is fixed, the digest is checked byte for
  // byte, and a mismatch is deleted rather than warned about.
  "conn.offerLead": {
    en: "{name} is not on this machine. Fylane can download this one build — only this one.",
    zh: "这台机器上没有 {name}。Fylane 可以下载这一份 —— 只有这一份。",
  },
  "conn.offerProgram": { en: "PROGRAM", zh: "程序" },
  "conn.offerSource": { en: "SOURCE", zh: "来源" },
  "conn.offerDigest": { en: "SHA-256", zh: "SHA-256" },
  "conn.offerNote": {
    en: "It lands in Fylane's own data directory, never on PATH. Before it runs, every byte is checked against the digest above; a mismatch is deleted.",
    zh: "它落在 Fylane 自己的数据目录，不进 PATH；运行前逐字节核对上面这个摘要，不一致就删掉。",
  },
  "conn.offerGet": { en: "Download and verify", zh: "下载并校验" },
  "conn.offerSelf": { en: "Install it yourself", zh: "自己安装" },
  "conn.downloading": { en: "Downloading and checking the digest.", zh: "正在下载并核对摘要。" },
  "conn.downloadingShort": { en: "Downloading", zh: "下载中" },
  // Saying what was discarded is the difference between "it failed" and
  // "something unverified is now on your disk". Every failure path in
  // tunnelget removes what it fetched, so this is true of all of them.
  "conn.downloadFailed": {
    en: "The download did not finish. It was discarded and left nothing on this machine.",
    zh: "下载没有完成，已经删除，没有在这台机器上留下任何东西。",
  },
  "conn.downloadRetry": { en: "Try again", zh: "重试" },
  // No pin for this platform: there is nothing to offer, and an offer that
  // cannot be honoured is worse than no offer. Only the way out is shown.
  "conn.unpinned": {
    en: "{name} is not installed on this machine.",
    zh: "这台机器上没有安装 {name}。",
  },
  "conn.signIn": { en: "Sign in", zh: "连接账号" },
  "conn.signInAgain": { en: "Sign in again", zh: "重新连接账号" },
  "conn.signedIn": { en: "Account connected", zh: "账号已连接" },
  "conn.signingIn": { en: "Opening the sign-in page…", zh: "正在打开登录页…" },
  "conn.signInWaiting": { en: "Waiting for browser authorization…", zh: "正在等待浏览器授权…" },
  "conn.signInOpen": { en: "Reopen the page", zh: "重新打开登录页" },
  "conn.signInCancel": { en: "Cancel", zh: "取消" },
  "conn.signInFailed": { en: "Sign-in did not complete", zh: "登录未完成" },
  "conn.signInFirst": { en: "SIGN IN FIRST", zh: "先连接账号" },
  "conn.tokenHow": {
    en: "Paste the authtoken from your dashboard. It is kept in the OS keychain.",
    zh: "粘贴后台的 authtoken，存入系统钥匙串。",
  },
  "conn.openDashboard": { en: "Open the token page", zh: "打开 Token 页面" },
  "conn.tokenStored": {
    en: "Token stored. Leave empty to keep it.",
    zh: "已存有 Token，留空即沿用。",
  },
  // Undoing a sign-in. What Fylane removes is what Fylane's sign-in put on
  // this machine — never anything on the vendor's account.
  "conn.signOut": { en: "Disconnect", zh: "断开连接" },
  "conn.forgetToken": { en: "Clear the token", zh: "清除 Token" },
  "conn.signOutFailed": { en: "Could not disconnect.", zh: "没能断开连接。" },
  // The typed-code way in. The platform's connect page offers it when its
  // loopback claim finds no Companion — a browser on another machine — and
  // this row is where the code it asks for comes from.
  "pairv3.label": { en: "PAIRING CODE", zh: "配对码" },
  "pairv3.none": { en: "not shown", zh: "未生成" },
  "pairv3.show": { en: "Show a code", zh: "显示配对码" },
  "pairv3.again": { en: "New code", zh: "换一个" },
  "pairv3.minting": { en: "Requesting a code…", zh: "正在生成配对码…" },
  "pairv3.left": { en: "{time} left", zh: "剩 {time}" },
  "conn.signOutNotOurs": {
    en: "{name} signs in the whole machine; sign out from its own app.",
    zh: "{name} 登录的是整台机器，请在它自己的应用中退出。",
  },
  // The hostname arrives from the sign-in a few polls later; until it does the
  // field is waiting on the Core, not on the user.
  "conn.hostnameWait": { en: "Reading the domain from your account…", zh: "正在从账号读取域名…" },
  "conn.needHostname": { en: "A HOSTNAME IS NEEDED", zh: "需要填写域名" },
  "conn.hostnameHow": {
    en: "A hostname under a domain on your Cloudflare account. The tunnel and DNS record are created for you.",
    zh: "你 Cloudflare 账号下某个域名的主机名。隧道与 DNS 记录由 Fylane 创建。",
  },
  "conn.switchTimeout": {
    en: "Fylane did not come back on its own. Start it from the Lane screen.",
    zh: "Fylane 没有自己回来。到「通道」屏启动它。",
  },

  // ── command palette ────────────────────────────────────────────────
  // ⌘K items (Desktop v2 §8). Nothing here opens a screen the product no
  // longer has.
  "cmd.approve": { en: "Allow the current request", zh: "放行当前请求" },
  "cmd.workspace": { en: "Switch workspace", zh: "切换工作区" },
  "cmd.resume": { en: "Resume Fylane", zh: "恢复 Fylane" },
  "cmd.pause": { en: "Pause Fylane", zh: "暂停 Fylane" },
  "cmd.openLane": { en: "Open the lane", zh: "打开通道" },
  "cmd.openTasks": { en: "Open tasks", zh: "打开任务" },
  "cmd.openSettings": { en: "Open settings", zh: "打开设置" },
  "cmd.stopTask": { en: "Stop the running task", zh: "停止当前任务" },
  "cmd.start": { en: "Start the Fylane core", zh: "启动 Fylane Core" },
  "cmd.hintOffline": { en: "offline", zh: "离线" },
  "cmd.placeholder": { en: "Run an action\u2026", zh: "快捷操作\u2026" },
  "cmd.search": { en: "Search actions", zh: "搜索操作" },
  "cmd.close": { en: "Close actions", zh: "关闭操作面板" },
  "cmd.title": { en: "Actions", zh: "操作" },
  "cmd.esc": { en: "esc", zh: "esc" },
  "cmd.empty": {
    en: "Nothing matches \u201c{query}\u201d.",
    zh: "没有匹配\u201c{query}\u201d的项。",
  },

  // ── workspace counts ───────────────────────────────────────────────
  "privacy.folders_one": { en: "{n} folder", zh: "{n} 个目录" },
  "privacy.folders_other": { en: "{n} folders", zh: "{n} 个目录" },

  // ── tasks ──────────────────────────────────────────────────────────
  // The task words are the six the design names, plus one:
  // a timeout is not a failure and the fix for it is different, so it keeps
  // its own word (recorded deviation).
  "task.running": { en: "Running", zh: "执行中" },
  "task.done": { en: "Done", zh: "已完成" },
  "task.failed": { en: "Failed", zh: "失败" },
  "task.timedOut": { en: "Timed out", zh: "超时" },
  "task.canceled": { en: "Stopped", zh: "已取消" },
  "task.interrupted": { en: "Interrupted", zh: "已中断" },
  "task.rejected": { en: "Rejected", zh: "已拒绝" },
  "task.waiting": { en: "Waiting for approval", zh: "等待批准" },
  "task.unnamed": { en: "command", zh: "命令" },

  "tasks.title": { en: "Tasks", zh: "任务" },

  // ── tasks (Fylane-V3, boards 05–07) ────────────────────────────────
  // The filter row is four words, not a segmented control. "Not passed"
  // gathers everything that ended without running to completion, which is
  // the one thing a person scans this list for.
  "tasksV3.all": { en: "All", zh: "全部" },
  "tasksV3.notPassed": { en: "Not passed", zh: "未通过" },
  "tasksV3.toReview": { en: "To review", zh: "待评审" },
  "tasksV3.mStatus": { en: "STATUS", zh: "状态" },
  "tasksV3.mDuration": { en: "DURATION", zh: "耗时" },
  "tasksV3.mExit": { en: "EXIT", zh: "退出码" },
  "tasksV3.result": { en: "RESULT", zh: "结果" },
  "tasksV3.backToLane": { en: "Back to the lane", zh: "回到通道" },
  "tasksV3.emptyFilterTitle": { en: "Nothing matches this filter", zh: "没有符合的记录" },
  "tasksV3.emptyFilterBody": {
    en: "Nothing in this folder ended that way. The other filters still have records.",
    zh: "这个工作区里没有这样结束的记录。其他筛选下还有内容。",
  },
  "tasksV3.showAll": { en: "Show everything", zh: "查看全部" },

  // ── the record (Desktop v2 §6) ─────────────────────────────────────
  "record.subtitle": {
    en: "Commands run in {name} · {count}",
    zh: "在 {name} 中执行的命令 · 共 {count}",
  },
  "record.subtitleNoFolder": { en: "No folder granted yet", zh: "还没有授权目录" },
  "record.entries_one": { en: "{n} entry", zh: "{n} 条" },
  "record.entries_other": { en: "{n} entries", zh: "{n} 条" },
  "record.clear": { en: "Clear records", zh: "清理记录" },
  "record.recent": { en: "RECENT", zh: "最近" },
  "record.earlier": { en: "EARLIER", zh: "更早" },
  "record.emptyTitle": { en: "Nothing has run yet", zh: "还没有任务" },
  "record.emptyBody": {
    en: "Commands the connected AI asks to run in this folder appear here, with their state, how long they took and their full output.",
    zh: "已连接的 AI 在这个工作区请求执行的命令会出现在这里,包含状态、耗时和完整输出。",
  },
  "record.written": { en: "Written", zh: "已写入" },
  "record.rolledBack": { en: "Undone", zh: "已撤销" },
  "record.files_one": { en: "{n} file", zh: "{n} 个文件" },
  "record.files_other": { en: "{n} files", zh: "{n} 个文件" },
  "record.more_one": { en: "{n} more", zh: "{n} 个" },
  "record.more_other": { en: "{n} more", zh: "{n} 个" },
  "record.inFlight": { en: "running", zh: "正在执行" },
  "record.exit": { en: "exit {code}", zh: "退出码 {code}" },
  "record.justNow": { en: "just now", zh: "刚刚" },
  "record.minsAgo": { en: "{n} min ago", zh: "{n} 分钟前" },
  "record.hoursAgo": { en: "{n} h ago", zh: "{n} 小时前" },
  "record.daysAgo": { en: "{n} d ago", zh: "{n} 天前" },
  "record.today": { en: "Today {time}", zh: "今天 {time}" },
  "record.yesterday": { en: "Yesterday {time}", zh: "昨天 {time}" },
  "record.expand": { en: "Show details", zh: "展开详情" },
  "record.collapse": { en: "Hide details", zh: "收起详情" },
  "record.copyCommand": { en: "Copy command", zh: "复制命令" },
  "record.copyOutput": { en: "Copy output", zh: "复制输出" },
  "record.copied": { en: "Copied", zh: "已复制" },
  "record.hCommand": { en: "COMMAND", zh: "命令" },
  "record.hSource": { en: "SOURCE", zh: "来源" },
  "record.hStarted": { en: "STARTED", zh: "开始" },
  "record.hDuration": { en: "DURATION · EXIT", zh: "耗时 · 退出码" },
  "record.hDir": { en: "WORKING DIRECTORY", zh: "工作目录" },
  "record.hOutput": { en: "OUTPUT", zh: "输出" },
  "record.hFiles": { en: "FILES", zh: "文件" },
  "record.noOutput": { en: "This command printed nothing.", zh: "这条命令没有任何输出。" },
  // The write half. It is not in the Desktop v2 design; it is here because
  // undoing a write must stay reachable now that the trace screen is gone.
  "record.undo": { en: "Undo this write", zh: "撤销这次写入" },
  "record.undoGone": {
    en: "The undo window for this write has passed.",
    zh: "这次写入的撤销时限已经过了。",
  },
  "record.undoUntil": { en: "Can be undone until {time}", zh: "{time} 前可以撤销" },
  // Acceptance. The point of these strings is the difference between
  // the last two: a write somebody read and approved of, and one whose undo
  // window simply ran out with nobody looking.
  "record.accepted": { en: "Accepted", zh: "已验收" },
  "record.hAccepted": { en: "REVIEW", zh: "验收" },
  "record.accept": { en: "Accept this write", zh: "验收这次写入" },
  "record.acceptedAt": { en: "Accepted {time}", zh: "{time} 已验收" },
  "record.acceptAwaiting": { en: "Not reviewed yet", zh: "还没有验收" },
  "record.acceptLapsed": { en: "Never reviewed", zh: "一直没有验收" },
  "tasks.sentTo": { en: "{size} sent to {provider}", zh: "已发送 {size} 给 {provider}" },
  "tasks.sent": { en: "{size} sent", zh: "已发送 {size}" },

  // ── pairing sheet ─────────────────────────────────────────────────
  // The browser is waiting on this device. The verify code is the whole
  // point: approving without checking it links whatever asked, so it is the
  // largest thing on the layer and the guidance says what to do when it does
  // not match.
  "pair.aria": { en: "Pairing request", zh: "配对请求" },
  "pair.waiting": { en: "waiting", zh: "等待确认" },
  "pair.platform": { en: "A platform", zh: "某个平台" },
  "pair.title": { en: "wants to connect to this device", zh: "正在请求连接这台设备" },
  "pair.guide": {
    en: "Make sure this matches the code shown in your browser.",
    zh: "确认与浏览器中显示的代码一致。",
  },
  "pair.guide2": {
    en: "If it doesn't match, reject the request.",
    zh: "代码不一致时请拒绝。",
  },
  "pair.reject": { en: "Reject", zh: "拒绝" },
  "pair.approve": { en: "Approve connection", zh: "批准连接" },
  "pair.keys": { en: "Esc to reject · Enter to approve", zh: "Esc 拒绝 · Enter 批准" },
  "pair.copy": { en: "Copy", zh: "复制" },
  "pair.copied": { en: "Copied", zh: "已复制" },
  "pair.codeAria": { en: "Verification code {code}", zh: "校验短码 {code}" },
  "pair.approved": {
    en: "This browser can now reach this machine through Fylane",
    zh: "这个浏览器已经可以通过 Fylane 到达本机",
  },
  "pair.rejected": { en: "The connection request was rejected", zh: "连接请求已被拒绝" },
  "pair.doneApproved": { en: "Connected", zh: "已连接" },
  "pair.doneRejected": { en: "Rejected", zh: "已拒绝" },
  "pair.close": { en: "Close", zh: "收起" },

  // ── first write ────────────────────────────────────────────────────
  "firstwrite.held": {
    en: "This write is still waiting at the gate for your approval — you can deal with it on the Lane screen once the walkthrough is done.",
    zh: "这次写入还在等你审批 —— 引导走完之后,可以在「通道」里处理它。",
  },
  "firstwrite.conflict": {
    en: "{file} is already in this folder. Fylane will not quietly overwrite it.",
    zh: "{file} 已经在这个文件夹里了。Fylane 不会悄悄覆盖它。",
  },
  "firstwrite.failed": { en: "The write did not go through.", zh: "这次写入没有成功。" },

  // ── the request ────────────────────────────────────────────────────
  // Kept for the i18n test's own fixture and for a request whose summary the
  // Core did not fill in.
  "lane.heldSubMany": {
    en: "{n} change sets are held at the gate. Review them before they reach disk.",
    zh: "有 {n} 个变更集在等你审批。落盘之前先看一眼。",
  },
  "laneScreen.defaultWhat": { en: "Write to the workspace", zh: "写入工作区" },

  // ── first run (Fylane-V3) ──────────────────────────────────────────
  // Four steps, drawn in the lane's own skeleton. The v1 keys below are kept
  // until the old screen is deleted.
  "fr.setup": { en: "SETTING UP", zh: "正在设置" },
  "fr.step": { en: "Step {n} of 4", zh: "第 {n} / 4 步" },
  "fr.gateNote": {
    en: "Everything an AI asks for stops here first.",
    zh: "AI 要的每一件事都先停在这里。",
  },
  "fr.s1": { en: "Choose a folder", zh: "选一个目录" },
  "fr.s2": { en: "Send one file through", zh: "送一个文件走一遍" },
  "fr.s3": { en: "Decide what has to ask", zh: "决定什么必须问你" },
  "fr.s4": { en: "Connect an AI", zh: "接入一个 AI" },
  "fr.t1": { en: "Give one folder, and nothing else.", zh: "只给一个目录,别的都不给。" },
  "fr.b1": {
    en: "This is the only folder an AI can reach. Nothing above it is visible, and you can change it later or take it back entirely.",
    zh: "这是 AI 唯一能碰到的目录。它的上层看不见,之后你可以换,也可以整个收回。",
  },
  "fr.choose": { en: "Choose a folder…", zh: "选择目录…" },
  "fr.choosing": { en: "Choosing…", zh: "正在选择…" },
  "fr.picker": { en: "Opens your own file picker", zh: "打开系统的目录选择器" },
  "fr.t2": { en: "Watch one file arrive.", zh: "看一个文件到达。" },
  "fr.b2": {
    en: "Fylane writes a real file so you can see the whole path work, approval included. You can undo it the moment it lands.",
    zh: "Fylane 会真的写一个文件,让你看完整条路怎么走,包括审批。落地之后随时可以撤销。",
  },
  "fr.write": { en: "Write the file", zh: "写入这个文件" },
  "fr.writing": { en: "Writing…", zh: "正在写入…" },
  "fr.skipWrite": { en: "Skip this", zh: "跳过" },
  "fr.wrote": { en: "Written to {path}", zh: "已写入 {path}" },
  "fr.t3": { en: "Decide what has to stop and ask.", zh: "决定什么必须停下来问你。" },
  "fr.b3": {
    en: "Commands run inside the folder, with a timeout and an output cap, and every one is recorded. This only sets when you get asked first.",
    zh: "命令在这个目录内执行,有超时和输出上限,每一条都会被记录。这里只决定什么时候先问你。",
  },
  // The open rung is deliberately not offered here. It needs an explicit
  // acknowledgement and a standing warning, and first run is not
  // where someone should be nudged into turning approvals off.
  "fr.rungLater": {
    en: "A third setting, which stops asking altogether inside the folder, is in Settings once you are set up.",
    zh: "还有第三档 —— 在目录内完全不再询问 —— 设置好之后可以在「设置」里找到。",
  },
  "fr.t4": { en: "Your lane is open.", zh: "你的通道打开了。" },
  "fr.b4": {
    en: "You connect an AI from the platform's own connector settings, using the address in Settings. This window will say so when one arrives — you do not have to wait here.",
    zh: "接入 AI 是在平台自己的连接器设置里完成的,用「设置」里的那个地址。接上之后这个窗口会告诉你 —— 你不用守在这儿等。",
  },
  "fr.open": { en: "Open my lane", zh: "打开我的通道" },
  "fr.howTo": { en: "Show me how to connect one", zh: "教我怎么接" },
  "fr.connected_one": { en: "{n} AI connected", zh: "已连接 {n} 个 AI" },
  "fr.connected_other": { en: "{n} AIs connected", zh: "已连接 {n} 个 AI" },
  "fr.skip": { en: "Skip the rest", zh: "跳过其余步骤" },
  "fr.nothingElse": { en: "Nothing else is needed from you here.", zh: "这里不再需要你做什么了。" },
} as const;

export type Key = keyof typeof DICT;

/** interpolate fills {name} placeholders; a missing value is left visible
 * rather than silently blanked, so a wiring mistake shows up on screen. */
function interpolate(text: string, vars?: Record<string, string | number>): string {
  if (!vars) {
    return text;
  }
  return text.replace(/\{(\w+)\}/g, (whole, name: string) =>
    name in vars ? String(vars[name]) : whole,
  );
}

export function translate(lang: Lang, key: Key, vars?: Record<string, string | number>): string {
  return interpolate(DICT[key][lang], vars);
}

export interface LangValue {
  lang: Lang;
  setLang(lang: Lang): void;
}

export const LangContext = createContext<LangValue>({ lang: "en", setLang: () => {} });

export interface Translator {
  lang: Lang;
  t(key: Key, vars?: Record<string, string | number>): string;
  /** tn picks the singular or plural key; Chinese resolves to the same
   * string, which is why the count always travels as a variable. */
  tn(base: string, n: number, vars?: Record<string, string | number>): string;
}

/** translatorFor is the hook-free form, for modules outside React and for
 * tests that pin the English wording. */
export function translatorFor(lang: Lang): Translator {
  return {
    lang,
    t: (key, vars) => translate(lang, key, vars),
    tn: (base, n, vars) =>
      translate(lang, `${base}_${n === 1 ? "one" : "other"}` as Key, { n, ...vars }),
  };
}

export function useT(): Translator {
  return translatorFor(useContext(LangContext).lang);
}
