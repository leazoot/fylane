<div align="center">

<img src="assets/icon.png" width="96" alt="Fylane">

# Fylane

**让网页版 ChatGPT、Claude、Grok 直接读写你电脑上的项目目录。改文件、跑命令之前,先在本机经你确认。**

[English](README.md) | 简体中文

[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.25-00ADD8)](go.mod)
[![MCP](https://img.shields.io/badge/protocol-MCP-6E56CF)](https://modelcontextprotocol.io)

</div>

<br>

## 这是什么

你在浏览器里用 ChatGPT、Claude 或 Grok,它们看不到你电脑上的项目。想让它改一个文件,
只能把代码贴过去、把结果贴回来,一天下来复制粘贴几十次。

Fylane 是装在你电脑上的一个小程序。你指定一个目录,它把这个目录变成一个 MCP 服务,
网页版的 AI 就能像用插件一样连上来:读目录里的文件、改代码、在里面跑测试。

它和「把电脑交给 AI」有一个本质区别:AI 只能**请求**,真正的写入和执行发生在你电脑上,
而且每一次都要先经过 Fylane 窗口里的确认。你看到它要改什么,点一下批准,才会落盘。

典型用法:

- 让 ChatGPT 直接读你的项目,回答「这个报错在哪儿抛出来的」,不用贴代码。
- 让 Claude 改三个文件,在 Fylane 里看完 diff 再批准。
- 让 Grok 在你的项目里跑 `npm test`,把结果读回去接着分析。
- 电脑合上盖子,AI 就连不上了。什么都没有传到云端。

## 它是怎么工作的

```
浏览器里的 AI  ──►  公网地址(隧道或中继)  ──►  你电脑上的 Fylane  ──►  你指定的目录
                                                       │
                                                  审批在这里发生
```

只有 Fylane 这一个进程能碰磁盘。中间的隧道或中继只负责转发加密帧,不存文件、不存 diff、
不存路径。AI 看到的路径都是相对路径,它不知道这个目录在你电脑上的绝对位置。

## 安装

从 [Releases](https://github.com/leazoot/fylane/releases/latest) 下载:

| 系统 | 下载 |
| --- | --- |
| macOS | `fylane-desktop-macos.dmg`,拖进「应用程序」 |
| Windows | `fylane-desktop-windows-amd64.zip`,解压后运行 `Fylane.exe` |
| Linux / 服务器 | 目前只有命令行版,一条命令安装,见下面的[命令行](#命令行) |

安装包目前没有签名。macOS 第一次打开会提示「无法验证开发者」,在「系统设置 → 隐私与安全性」
里点「仍要打开」;Windows 的 SmartScreen 点「更多信息 → 仍要运行」。每个文件的 SHA-256
都在 Release 页的 `SHA256SUMS` 里,可以先核对再打开。

## 第一次打开

Fylane 会带你走四步,整个过程在窗口里完成,不需要开终端。

![第一步:选一个目录](assets/first-run.png)

1. **选一个目录。** 这是 AI 唯一能看到的地方,上层目录对它不存在。以后随时可以换,也可以收回。
2. **送一个文件走一遍。** Fylane 会往这个目录里写一个示例文件,让你看一次审批是什么样:
   先看到内容,点批准,文件才出现。
3. **决定什么必须问你。** 默认是「每个目录问一次」:第一条普通命令要你点头,之后同一个目录
   里的普通命令直接跑,但每一次写文件、每一条危险命令照样要问。可以改成每次都问,或者普通
   命令都不问。
4. **接入一个 AI。** 这一步在 AI 平台那边完成,见下一节。

完成后就是主界面。左边是通道:等你批准的请求会出现在这里。右边是当前目录和已连接的 AI。

![通道](assets/lane.png)

## 把 Fylane 接到 AI 平台

不管接哪个平台,都是同一件事:在 Fylane 里拿到一个地址,把它填到平台的连接器设置里,
然后在 Fylane 窗口里批准这次连接。

### 第一步:在 Fylane 里拿到地址

打开「设置 → 接入方式」。

![接入方式](assets/connection.png)

第一次用,选「Cloudflare 快速隧道」点「设置」。不用注册账号、不用域名,几秒钟后这里
会出现一个 `https://xxx.trycloudflare.com/mcp` 的地址,点「复制」。

这个地址每次重启 Fylane 都会变,平台那边要重新填。试用够了,想要固定地址,看后面的
[固定地址](#固定地址)。

### 第二步:填到平台里

<details open>
<summary><b>ChatGPT</b></summary>

需要付费套餐(Plus / Pro / Team)。

1. 头像 → **设置 → 应用与连接器**(新版界面叫「插件」)→ **高级** → 打开 **开发者模式**。
2. 回到连接器页,点 **创建**。
   - 名称:随便填,比如 `Fylane`
   - MCP 服务器 URL:粘贴刚才复制的地址
   - 身份验证:选 **OAuth**。选「无身份验证」会报 `Error creating connector`,因为
     Fylane 的地址要求登录。
3. 点创建。ChatGPT 会在后台完成握手,然后弹出一个浏览器页面,见第三步。
4. 在对话里点输入框旁边的 **+** → **更多**,勾上 `Fylane`,这个对话就能用了。

<!-- 截图:assets/setup/chatgpt-developer-mode.png(设置 → 应用与连接器 → 高级 → 开发者模式) -->
<!-- 截图:assets/setup/chatgpt-create-connector.png(创建连接器的表单,身份验证选 OAuth) -->

</details>

<details>
<summary><b>Claude</b></summary>

1. 左下角头像 → **设置 → 连接器** → **添加自定义连接器**。
2. 名称填 `Fylane`,远程 MCP 服务器 URL 粘贴地址。**高级设置里的 Client ID 和 Client Secret
   两格都留空**,Fylane 会自动注册客户端;填了反而会报错。
3. 点添加。列表里出现 Fylane 后,再点一次它旁边的 **连接**,浏览器会打开授权页,见第三步。
4. 在对话里点输入框的 **工具** 按钮,确认 Fylane 已开启。

Claude 会按「只读 / 写入」把工具分组,每个工具可以单独设成「每次询问」或「总是允许」。
这些是平台侧的提示,不影响 Fylane 本机的审批。

<!-- 截图:assets/setup/claude-add-connector.png(添加自定义连接器表单) -->

</details>

<details>
<summary><b>Grok</b></summary>

1. grok.com → **设置 → 连接器** → 添加自定义 MCP 连接器,URL 粘贴地址。
2. 点连接,浏览器打开授权页,见第三步。

两点和其他平台不一样,提前知道少踩坑:

- **Grok 平台侧不弹写入确认**,AI 说改就发请求。所有保护都靠 Fylane 本机的审批,所以在
  Grok 上不要把审批档位调到「都不问」。
- Grok 自带一个云端 Linux 沙箱。你说「在项目里跑测试」,它可能在自己的沙箱里跑,而不是
  你的电脑。要点名:「用 Fylane 连接器的 run_command 跑测试」。
- Grok 单次工具调用只等 60 秒。写入需要你批准,如果 60 秒内没批,它会收到「待批准」,
  批完让它再试一次即可。

<!-- 截图:assets/setup/grok-add-connector.png -->

</details>

### 第三步:在 Fylane 里批准这次连接

平台会打开一个 Fylane 的授权页,页面上显示一个短码。同时你电脑上的 Fylane 窗口会弹出
同样的码,对一下,点「批准连接」。

![批准连接](assets/pairing.png)

如果浏览器页面找不到本机的 Fylane(比如你在另一台电脑的浏览器里操作),页面上会让你
输入配对码。回到 Fylane「设置 → 接入方式」点「显示配对码」,把那串码填进去。配对码
10 分钟内有效,用一次就作废。

### 试一下

回到对话,输入:

> 列一下这个项目根目录有哪些文件。

AI 会调用 Fylane 把目录列回来。读取不需要批准。再试:

> 在项目里新建一个 hello.txt,内容写 hello。

这时 Fylane 窗口会亮起来,显示 AI 要写什么。点批准,文件才会出现在目录里;点拒绝,
AI 会收到被拒绝的消息。

## 审批和安全边界

下面是 Fylane 具体做了什么,以及你会在窗口里看到什么。

**写文件要批准。** AI 要写入、修改、删除、移动文件时,请求先到 Fylane 窗口。你看到的是
完整 diff,不是一句「AI 想改文件」。批准后才落盘。

**批准过的写入 7 天内可以撤销。** 每次写入前 Fylane 都留一份原文件的副本。在「任务」
页里可以回滚。如果文件在这期间又被别的东西改过,回滚会拒绝,不会盖掉新内容。

**命令有边界。** AI 只能传「程序 + 参数」,不能传一整段 shell,没有管道、没有 `sh -c`。
`rm -rf`、`sudo`、杀进程、往系统目录写这一类命令,不管审批档位怎么设都会被拒绝。

**危险的读取要单独问。** 读进程列表、读环境变量、读工作区以外的文件,这些会暴露机器
信息的操作,在任何档位下都会停下来问你。

**敏感文件默认看不见。** `.env`、`*.pem`、`id_rsa`、`credentials*` 这类文件在列目录时
不显示、搜索时跳过。AI 点名要读,会单独弹一次确认。

**子进程被系统锁在目录里。** Fylane 替 AI 启动的程序(比如 `npm test`),macOS 上用
`sandbox-exec`、Linux 上用 Landlock 限制,只能读这个工作区和工具链缓存,读别的地方会被
操作系统直接拒绝。Windows 没有等价机制,那里靠上面的其他几层。

**AI 不知道目录在哪。** 工具只接受相对路径,绝对路径不会出现在任何发给 AI 的内容里。

**审批频率可调,检查不可关。** 你可以选每次都问、每个目录问一次、或者普通命令不问。
不管选哪档,路径检查、危险命令规则表、审计记录都在跑。

所有记录都在本机。「任务」页能看到每一条请求、谁发的、结果是什么。

![任务记录](assets/tasks.png)

## 远程机器

项目在 VPS 上,人在 Mac 前面,想让 AI 直接在 VPS 上改代码、跑测试。Fylane 可以把远程机器
接进来,AI 平台那边什么都不用改:还是这一个连接器,`workspace_info` 里会多出那台机器上
的目录,带着机器名。

**前提**:在这台电脑的终端里,`ssh 用户@主机` 已经能用密钥免密登录。Fylane 用的就是系统
自带的 ssh,密钥、known_hosts、`~/.ssh/config` 里的别名全都照常生效;它不会问密码。

1. 打开通道页,右栏「机器」下点「切换机器 → 添加远程机器…」,照着 `ssh` 后面的写法
   填一行:别名、`user@host`、`host:2222` 都行。Fylane 会立刻去敲门,门开了就告诉你对面
   有什么;名字默认取主机名,随便改,以后在右栏点「修改」还能改。
2. 如果那台机器上还没有 Fylane,右栏会显示「没有安装 Fylane」和一个「安装 Fylane」
   按钮。点一下,Fylane 通过 ssh 在那台机器上跑 `install.sh`,装的版本和你本机一样,
   校验 SHA256SUMS。装好后它会自动把远端的 Fylane 拉起来,以后每次连接都会检查它在跑。
3. 「已连接」之后,在下面的「工作区」里点「选择目录」。打开的是那台机器的主目录,
   一层层点进去,带 `git` 标记的就是仓库;也可以直接输路径(`~/project` 就行),但要
   机器确认它是个目录才授得出去,打错的路径不会被授权。

之后和本机一样用。AI 在那个目录里的读写和命令都在 VPS 上执行,审批回到你的 Mac 窗口
来问;「任务」页里,来自远程机器的记录在命令前面带一个机器名标签,本机的不带。

几件事要知道:

- **VPS 上的 Fylane 不对外发布。** 它不开隧道、不配对,只在那台机器的回环地址上听,
  本机通过 ssh 端口转发接过去。本机就是它的隧道,所以 Mac 关机时 VPS 也用不了。
- **审批只在你这里。** 远程机器的命令档位默认「每次都问」,每条命令都会回到 Mac 来问。
- **切换机器只切换通道页站在哪台机器上**,不过滤任务,也不会藏起任何一台机器的待审批。
- 远端的数据在那台机器的 `~/.fylane/`(程序、数据库、审计记录、`serve.log`)。
  「移除」只是本机忘掉这台机器,不动那边的任何东西。
- 目前不支持从窗口改远程机器的设置;远端用自己的默认值。Windows 桌面端需要系统自带的
  OpenSSH 客户端。

## 固定地址

Cloudflare 快速隧道每次重启换地址。要一个不变的地址,在「设置 → 接入方式」里换一种:

| 方式 | 需要什么 | 地址 |
| --- | --- | --- |
| Cloudflare 快速隧道 | 什么都不需要 | 每次重启变 |
| Tailscale Funnel | 装 Tailscale,登录一次(免费) | 固定,`xxx.ts.net` |
| Cloudflare 命名隧道 | 一个托管在 Cloudflare 的域名 | 固定,你自己的域名 |
| ngrok | ngrok 账号 | 免费版每次变,付费版固定 |
| 自建中继 | 一台有公网域名的服务器 | 固定,你自己的域名 |

前四种由 Fylane 替你拉起和管理,在窗口里点「设置」就行。

自建中继适合团队或者多台电脑共用一个入口:中继跑在你的服务器上,处理 OAuth、在内存里
转发,是唯一暴露在公网的部件,不存任何文件内容。部署方法在 [`deploy/`](deploy/):

```bash
FYLANE_RELAY_HOST=relay.example.com docker compose -f deploy/docker-compose.yml up -d
```

## 命令行

没有桌面端的机器(Linux、服务器)可以直接用 `fylane-companion`。它和桌面端是同一个
核心,只是审批在终端里做:需要批准的写入会把 diff 打在终端里,命令会打出完整的命令行,
按 `y` 批准,其他任何键拒绝。删除整个目录要输入完整的 `yes`。

安装(macOS 和 Linux,会核对 SHA-256):

```bash
curl -fsSL https://raw.githubusercontent.com/leazoot/fylane/main/scripts/install.sh | sh
```

然后进到项目目录:

```bash
cd ~/projects/my-app
fylane-companion share
```

它会打印地址和配对码:

```
sharing my-app — starting a tunnel, this takes a few seconds

  Connector URL   https://swift-lane-9f2c.trycloudflare.com/mcp
  Pairing code    7K4M-2QB9   (valid for 10m0s)
```

后面的步骤和桌面端一样:地址填到平台,授权页输入配对码。Ctrl-C 退出,隧道和配对码一起失效。
在服务器上用 SSH 跑的话,放进 `tmux` 或 `screen` 里,断开连接也能继续。

接自建中继:

```bash
fylane-companion pair -relay wss://relay.example.com/tunnel -register
fylane-companion serve -workspace ~/projects/my-app
```

从源码构建需要 Go 1.25:

```bash
git clone https://github.com/leazoot/fylane
cd fylane
go build -o bin/fylane-companion ./companion/cmd/companion
```

## AI 拿到的工具

| | 工具 |
| --- | --- |
| 读 | `list_directory` `read_file` `read_files` `search_files` `stat_path` |
| 写 | `write_file` `edit_file` `apply_patch` `change_manage` |
| 跑 | `run_command` `task_status` `code_task` |
| 导航 | `code_navigate`,由 language server 给出真实的定义与引用 |
| 扩展 | `mcp_gateway`,转发到你机器上的另一个 MCP 服务 |

写入和有后果的命令会先返回 `pending_approval`,等你在 Fylane 里做决定。
`change_manage` 负责移动、删除到回收区,以及回滚。

## 开发

```bash
go test ./...

cd desktop/frontend
npm install
npm test          # vitest
npm run harness   # 用固定数据渲染每个界面,在 :5199
```

桌面端需要 [Wails v2](https://wails.io) 命令行和 Node 22+:

```bash
cd desktop
wails dev
```

## 安全

本地审批是唯一的权威。平台侧的确认只是提示,从来不是安全层。中继被视为不可信的内容
中转,从不持久化文件内容、diff、目录列表或敏感文件名。

已知的边界和报告漏洞的方式见 [SECURITY.md](SECURITY.md)。

## 许可证

[Apache 2.0](LICENSE)
