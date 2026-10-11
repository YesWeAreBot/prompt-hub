<div align="center">

# prompt-hub

**像管理代码一样管理 prompt：可版本化、可评审、可校验的 prompt pack 与本地渲染器。**

`ph` 是一个本机 daemon + CLI：把带版本的 **prompt pack** 安装、锁定依赖、放进沙箱渲染；
`ph hub` 则是一个用于分发这些 pack 的注册表。

[English](README.md) · [快速开始](#快速开始) · [pack 格式](#pack-格式) · [CLI](#cli-参考) · [HTTP API](#http-api) · [注册表](#注册表ph-hub) · [安全模型](#安全模型)

</div>

---

## prompt-hub 是什么

生产环境里的 prompt，其实是**一套没有构建系统的软件**：它躺在某个文本框里，改了没有评审，
读了什么数据没人声明，还会在不同环境之间悄悄漂移。`prompt-hub` 把它缺的那套机制补上。

一个 **pack** 是一个目录（也可以是单文件，或 `.pack` 归档），里面有 manifest、文本或代码形式的入口、
可选的 `pack.lock`，以及存放黄金样例的 `fixtures/`。`ph` 把 pack 装进本地 **store**，解析依赖，
然后在沙箱里把它渲染成模型真正收到的文本 —— 带审计日志，带可以用于缓存的 `ETag`。

| prompt 的常见问题 | prompt-hub 的做法 |
| --- | --- |
| "那条坏回复当时用的是哪个版本？" | 每个 pack 都有内容寻址的 **tree hash**，每次渲染都写进 JSONL **ledger** |
| "两个 pack 分叉之后各自漂移了" | `kind: library` pack + `includes` 范围（`^`/`~`），由 `pack.lock` 钉死 |
| "没人告诉我们这个 prompt 会读用户数据" | 每个 pack 必须声明 `requires`；**profile** 给每个字段标注稳定性与隐私等级 |
| "prompt 在渲染时偷偷出网" | `egress` 既要 pack 声明，又要宿主机授权，否则**拒绝渲染** |
| "生产 prompt 把 bot 宿主搞崩了" | 单次渲染限制**超时 / 内存 / 进程数**；运行时失败绝不静默降级 |
| "两种语言的宿主渲染结果不一致" | 一份规范两套实现 —— Go daemon 与 JS SDK 逐字节对拍 |
| "升级 prompt 顺手扩大了权限，没人批准" | `ph upgrade` 打印**能力差异**，扩大能力的升级必须 `--yes` |
| "重发 1.0.0 悄悄改变了所有人的行为" | 同版本不同内容 = **硬性拒绝**，store 与注册表都拒 |

### 设计准则

1. **失败要响，绝不静默降级。** 缺少必需 context 字段、运行时档位未启用、出网未授权、
   范围依赖没有锁定 —— 全都是错误。daemon 不会渲染出一份「略微不同」的 prompt 然后当作成功：
   静默降级等于渲染出一份你不知道自己错了的 prompt。
2. **契约是声明出来的，不是猜出来的。** manifest 里的 `requires` 是宿主必须兑现的承诺。
   如果 pack 用了自己没声明的字段，`ph check` 会在它进生产之前就指出来。
3. **内容是寻址的，不是假设的。** tree hash 在安装时、首次渲染时、从注册表下载后都会重算并比对。
   不一致 = 拒绝，没有「仍然安装」这个选项。
4. **能力变化需要被评审。** 升级会做差异对比；新增 context 字段或新增出网目标都是权限变化，
   必须明确同意。

---

## 目录

- [快速开始](#快速开始)
- [核心概念](#核心概念)
- [pack 格式](#pack-格式)
- [运行时档位](#运行时档位)
- [沙箱与限制](#沙箱与限制)
- [CLI 参考](#cli-参考)
- [HTTP API](#http-api)
- [注册表（`ph hub`）](#注册表ph-hub)
- [profiles（字段元数据）](#profiles字段元数据)
- [JavaScript SDK](#javascript-sdk)
- [安全模型](#安全模型)
- [仓库结构](#仓库结构)
- [开发](#开发)
- [示例](#示例)
- [常见问题](#常见问题)
- [许可](#许可)

---

## 快速开始

需要 **Go 1.26.4+**。`template` 与 `wasm` 是内置档位，不需要任何外部程序；`php` 档按需启用。

```console
$ git clone https://github.com/YesWeAreBot/prompt-hub.git
$ cd prompt-hub
$ go build -o bin/ph ./cmd/ph
```

用 `PH_HOME` 把状态放到仓库之外（默认 `~/.prompt-hub`）：

```console
$ export PH_HOME="$PWD/.ph"        # PowerShell: $env:PH_HOME="$PWD\.ph"
```

### 1. 检查并测试一个 pack

```console
$ ./bin/ph check examples/nekochan
packId: nekochan
version: 1.0.0
profile: chat/1
kind: content
entry: index.prompt
runtime: template
requires:
    - chat.user.nickname
    - chat.channel.name
outputMax: 4000
license: MIT
---
tree hash: sha256-481522e231f8f753137f51026aaf95868a8fe859d12e09dcd0ed8bc3c92c8115
文件数:    3
manifest:  合法
字段画像:  frozen-session，最高隐私 internal
  chat.user.nickname           session      internal
  chat.channel.name            session      internal
  → 依赖 session 字段，只能按会话冻结（换会话 / 换群要重算）

$ ./bin/ph test examples/nekochan
PASS  基本渲染

1/1 通过
```

`ph check` 会校验 manifest、打印规范化结果、遍历资产闭包，并按 profile 给每个声明字段定性；
`ph test` 回放 `fixtures/*.json`，把渲染结果与期望**逐字节**比对。

### 2. 安装与渲染

```console
$ ./bin/ph install examples/cats-role
已安装 cats-role@1.2.0
  tree hash  sha256-8c4b0f69c860996fc27572ada8632e7b568ecddd63b229eab358622d4caf77ed
  目录       ~/.prompt-hub/store/cats-role/1.2.0/sha256-8c4b0f69c…

$ ./bin/ph install examples/nekochan-asset
已安装 nekochan-asset@1.2.0
  tree hash  sha256-e8ea3bb70fde5f3d10e288a9272ad22426611b5edd86b4ab659a765a0ec917a3
  目录       ~/.prompt-hub/store/nekochan-asset/1.2.0/sha256-e8ea3bb70fde5f3d…

$ ./bin/ph list
cats-role@1.2.0
nekochan@1.0.0
nekochan-asset@1.2.0
```

启动 daemon 并经由它渲染 —— 这也是宿主应用走的路径：

```console
$ ./bin/ph serve
prompt-hub daemon  listening on http://127.0.0.1:18400
  store   /home/you/.prompt-hub/store
  ledger  /home/you/.prompt-hub/ledger.jsonl
  token   /home/you/.prompt-hub/token
  views   /home/you/.prompt-hub/views
  档位    template, wasm, php(未启用)
  沙箱    timeout=5s  memory=256MB  procs=8
  出网    全禁（pack 声明 egress 会被拒）
  已装 pack: 3 个

$ ./bin/ph render nekochan@1.0.0 --ctx chat.user.nickname=小兔 --ctx chat.channel.name=辩论队 --turn ev_1
HTTP 200 OK
ETag:           "sha256:ebb6f57f5611156d3448ef36ebfeb0f94cb852c73efb452bca99a257eb2d4eb0"
X-Pack-Revs:    nekochan@1.0.0
X-Tree-Hash:    sha256-481522e231f8f753137f51026aaf95868a8fe859d12e09dcd0ed8bc3c92c8115
X-Render-Ms:    2
---
你是傲娇的猫娘助手「胡梨」，正在「辩论队」里活动。说话带喵，高冷但心软。

【用户偏好】
- 目标称呼：小兔

【语气】
- 群里人多的时候回复要短，别连续刷屏。
```

漏给字段时，daemon 明确告诉你缺什么，而不是猜：

```console
$ ./bin/ph render nekochan@1.0.0
HTTP 422 Unprocessable Entity
---
{"code":"missing-context","error":"缺少 pack 声明的必需 context 字段: chat.channel.name, chat.user.nickname","partial":false,"retryable":false,"missing":["chat.channel.name","chat.user.nickname"]}
```

### 3. 分发它

```console
$ ./bin/ph hub serve --addr 127.0.0.1:18500 --publish-token s3cret &
$ ./bin/ph publish examples/nekochan --hub http://127.0.0.1:18500 --token s3cret
已发布 nekochan@1.0.0
  tree hash  sha256-481522e231f8f753137f51026aaf95868a8fe859d12e09dcd0ed8bc3c92c8115
  大小       676 字节
  声明读     chat.user.nickname, chat.channel.name

$ ./bin/ph index --hub http://127.0.0.1:18500
cats-role@1.2.0            template chat/1       0.7KB
nekochan@1.0.0             template chat/1       0.7KB  读 2 字段

$ ./bin/ph install nekochan@1.0.0 --hub http://127.0.0.1:18500
$ ./bin/ph upgrade nekochan --hub http://127.0.0.1:18500
nekochan 已是最新（本地 1.0.0，hub 1.0.0）
```

---

## 核心概念

| 术语 | 含义 |
| --- | --- |
| **pack** | 可安装单元：带 `pack.yaml` 的目录、头部注释即 manifest 的单文件、或 `.pack` 归档；用 `packId@version` 标识 |
| **manifest** | `pack.yaml` —— 身份、运行时档位、入口，以及被声明的能力面：`requires` / `egress` / `includes` |
| **tree hash** | 覆盖 pack 内每个文件的 `sha256-…`，既是 pack 的内容地址，也是依赖版本的内容地址 |
| **store** | 本地内容寻址安装根目录：`<store>/<packId>/<version>/<treeHash>/` |
| **resolver** | 先查 store，再查只读的**内嵌层**（`serve --embed`） |
| **library pack** | `kind: library` —— 自己不出文本，只提供可复用资产/函数；也是唯一能被 `include` 的 kind |
| **范围 / 锁定** | `includes: cats-role@^1.2` 是范围；`pack.lock` 把精确版本**及其 tree hash** 钉死 |
| **profile** | 宿主 context 的具名 schema（`chat/1`）：每个字段带 `stability` 与 `privacy` |
| **placement** | profile 推导出的 prompt 缓存位置：`frozen-global` / `frozen-session` / `append-only` |
| **运行时档位** | 产出文本的引擎：`template`、`wasm`（内置）、`php`（可选） |
| **ledger** | 追加式 JSONL 审计流水：每次渲染尝试（含失败）一行 |

---

## pack 格式

### 目录结构

```
nekochan-asset/
├── pack.yaml             # manifest（必需）
├── pack.lock             # 由 `ph lock` 生成；钉死范围依赖
├── index.prompt          # 入口
├── partials/
│   └── intro.md
└── fixtures/
    └── basic.json        # 黄金样例：context 进，期望文本出
```

### `pack.yaml`

```yaml
packId: nekochan-asset          # 必需；/^[a-z0-9][a-z0-9._-]*$/
version: 1.2.0                  # 必需；semver，可带 -prerelease
profile: chat/1                 # 默认 chat/1
kind: content                   # content | library         默认 content
entry: index.prompt             # content 必需
runtime: template               # template | wasm | php     默认 template
includes:                       # 依赖；范围只支持 ^ 和 ~
  - cats-role@^1.2
requires:                       # 本 pack 会读的 context 字段
  - chat.user.nickname
  - chat.channel.name
egress:                         # 运行时允许访问的 host（默认全禁）
  - api.example.com
exports:                        # kind: library 必需
  - cats_role
outputMax: 4000                 # 输出字节上限           默认 8000
targets: []                     # 自由标签
license: MIT
```

`pack.Validate` 强制的规则：

- `packId` 匹配 `^[a-z0-9][a-z0-9._-]*$`；`version` 匹配 `^<major>.<minor>.<patch>[-prerelease]?$`；
- `kind: content` **必须**声明 `entry`；`kind: library` **不得**声明 `entry`，且**必须**声明 `exports`；
- `runtime` 匹配 `^[a-z][a-z0-9-]*$`；
- 每条 `includes` 都必须是 `<id>@<精确版本 | ^范围 | ~范围>`，没有别的范围写法。

范围只接受 `^` 与 `~`；没有锁条目时，依赖必须写精确版本：

```console
$ ./bin/ph lock examples/nekochan-asset          # 把范围钉成精确版本，写 pack.lock
  cats-role      1.2.0      sha256-8c4b0f69c860996fc27572ada8632e7b568ecddd63b229eab358622d4caf77ed（范围 ^1.2）
已写入 examples/nekochan-asset/pack.lock（1 条）

$ ./bin/ph lock examples/nekochan-asset --update # 把范围升到当前可用的最高版本
```

> **为什么非要锁文件？** 范围让写 pack 方便，让生产不确定。`prompt-hub` 在 `ph lock` 时解析范围，
> 记录的是 **tree hash**（不只是版本号），而渲染路径只消费精确版本。带范围却没有锁条目的 pack，
> 会在安装、检查、发布三个环节被拒。

### 单文件 pack

小到不值得单独建目录的 prompt，可以把 manifest 写在头部注释里。hash 是对规范化后的虚拟树计算的，
所以单文件 pack 与等价目录 pack 的 tree hash 一致：

```text
/* packId: single-demo
 * version: 2.0.0
 * runtime: template
 * requires: chat.user.nickname
 */
你好 {{chat.user.nickname}}，这一轮是 {{turn}}。
```

```console
$ ./bin/ph install ./neko.prompt
已安装 single-demo@2.0.0
  tree hash  sha256-05f3a48dca1257b75460fa208c32a16d15a6f3f8899faa3f160e510857a86ccd
  目录       ~/.prompt-hub/store/single-demo/2.0.0/sha256-05f3a48dca1257b75460fa208c32a16d15a6f3f8899faa3f160e510857a86ccd
  形态       单文件（manifest 已从头部注释规范化为 pack.yaml）
```

头部键名会被归一化（`Pack-Id`/`id`/`name` → `packId`，`dependencies` → `includes`，`context` → `requires` 等）。
`entry` 只能是 `self` 或该文件自身的名字。文件开头的 `<?php` 会被容忍，因此单个 `.php` pack 同样可用。

### `.pack` 归档

注册表用确定性 gzip+tar 搬运 pack：

- 路径排序、模式 `0644`、`mtime` 固定为零时刻、uid/gid 归零、PAX 格式 —— **内容相同则字节相同**；
- 读取时拒绝：越界路径（`..`）、绝对路径、NUL 字节、重复条目、非普通文件类型、长度不符、以及超过 64 MiB 的条目。

### tree hash

pack 的内容地址是跨语言可复现的：

```
for each path p in byte-order sort(paths):
    fileHash = sha256(content[p])
    h.update(p); h.update("\x000644\x00"); h.update(hex(fileHash) + "\n")
treeHash = "sha256-" + hex(sha256_final(h))
```

路径排序用的是 **UTF-8 字节序**，不是宿主语言的排序，所以同一个 pack 在任何平台、
以及在 [JavaScript SDK](#javascript-sdk) 里都算出同一个 hash —— 这一点由覆盖 ASCII、中文、
补充平面文件名的跨实现对拍测试保证（见 [`interop_test.go`](internal/render/interop_test.go)）。

---

## 运行时档位

`pack.yaml` 的 `runtime` 决定用哪个档位。档位按名字解析；档位不可用是**带修复提示的硬错误**，
绝不会回退到别的档位。

```console
$ ./bin/ph runtime
档位        状态
  template  ✅ 内置（纯文本 + {{field}}）
  wasm      ✅ 内置（wazero + WASI 沙箱）
  php       ❌ 未找到 php（可放在 ~/.prompt-hub/tools/php 或加入 PATH）
            ph runtime enable php（下载便携 PHP 到 $PH_HOME/tools/php）
```

### `template` —— 内置

纯文本 + `{{field}}` 替换，以及资产内联。

**内置变量**（不需要写进 `requires`）：

| 变量 | 值 |
| --- | --- |
| `{{packId}}`、`{{version}}` | pack 身份 |
| `{{turn}}` | 宿主传入的轮次标识 |
| `{{now}}` | 当前 UTC 时间，`YYYY-MM-DD HH:MM:SS` |
| `{{nowUnix}}` | 当前 Unix 时间戳 |

**`{{asset '<ref>'}}`** 递归内联另一份文本：

- `{{asset './partials/intro.md'}}` —— 本 pack 内的文件；
- `{{asset 'cats-role/tone.md'}}` —— 某个被 *include* 的 pack 里的文件；该 pack 必须在 `includes` 里，否则渲染失败。

限制：单文件 256 KiB、嵌套 8 层、展开后总量 1 MiB。循环引用会打印完整链路（`a → b → a`）；二进制资产被拒。

**引用未提供的变量是错误**，并会指出它来自哪个资产：

```
模板引用了未提供的变量: chat.user.nickname（来自 cats-role/tone.md）
```

### `wasm` —— 内置

`entry` 是编译到 WASI 的 `.wasm` 模块（`GOOS=wasip1 GOARCH=wasm`）。daemon 用
[wazero](https://github.com/tetratelabs/wazero) 运行它：

- pack 视图以**只读**方式挂载到 `/`（`WithReadOnlyDirMount`）；
- 内存受渲染限制约束，墙钟超时会中断模块；编译结果按 `treeHash` 缓存，`ph serve` 启动时**预热**，
  首次请求不必付编译开销；
- stdout 即 prompt，stderr 会变成渲染告警。

```console
$ cd examples/nekochan-wasm && GOOS=wasip1 GOARCH=wasm go build -o index.wasm . && cd ../..
$ ./bin/ph install examples/nekochan-wasm && ./bin/ph render nekochan-wasm@1.0.0 \
    --ctx chat.user.nickname=小兔 --ctx chat.channel.name=辩论队 --ctx chat.user.affinity=86
```

### `php` —— 可选

`entry` 是 `.php` 文件；daemon 用 `-n`（不读 php.ini）调用解释器，并注入
[`_ph/sdk.php`](internal/render/sdk/php/sdk.php)，由它从 stdin 解析渲染信封：

```php
<?php
require __DIR__ . '/_ph/sdk.php';

$nickname = ph_required('chat.user.nickname');
$channel  = ph_required('chat.channel.name');

echo "你是傲娇的猫娘助手「胡梨」，正在「{$channel}」里活动。\n";
echo "- 目标称呼：{$nickname}\n";
```

| 助手函数 | 用途 |
| --- | --- |
| `ph($key, $default = null)` | 读一个 context 值 |
| `ph_required($key)` | 读不到就以非零码退出（缺字段即契约违例） |
| `ph_turn()`、`ph_now()`、`ph_pack()`、`ph_output_max()`、`ph_view()`、`ph_env()` | 信封元数据 |

被 include 的 library pack 会被铺到 `vendor/<packId>/`，所以 PHP pack 用普通 `require` 就能组合：

```php
require __DIR__ . '/vendor/cats-role/role.php';
```

安装一个经过校验的便携 PHP 到 `$PH_HOME/tools/php`（Windows；其它平台请用发行版包管理器或 `static-php-cli`）：

```console
$ ./bin/ph runtime enable php
把便携 PHP 装到 ~/.prompt-hub/tools/php
读取发行版索引 https://downloads.php.net/~windows/releases/releases.json
选中 PHP 8.x.y（nts-vs17-x64，…）
sha256 校验通过
已安装 PHP 8.x.y
php 档现在可用：~/.prompt-hub/tools/php/php.exe
```

---

## 沙箱与限制

进程型档位（`php`，以及适用时的 wasm）按次受约束：

| 限制 | 开关 | 默认 |
| --- | --- | --- |
| 墙钟超时 | `--render-timeout` | `5s` |
| 内存 | `--max-render-memory-mb` | `256`（0 = 不限） |
| 进程数 | `--max-render-procs` | `8`（0 = 不限） |
| 输出 | manifest 的 `outputMax` | `8000` 字节 |
| 出网 | `--allow-egress host1,host2` | 全禁 |
| 资产 | 代码内固定 | 单文件 256 KiB、8 层、展开 1 MiB |

- **Windows**：Job Object + `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`，并设置进程/作业内存上限与活动进程数上限；
  整棵进程树随 Job 一起结束。沙箱建不起来时渲染**直接失败**，除非进程本来就是显式以非沙箱方式启动的。
- **Unix**：独立进程组，超时按组杀。
- **截断不静默**：超长输出仍会渲染，但会设置 `X-Ph-Warnings` 头、在 ledger 里记
  `status: "truncated", degraded: true`，并在 `ph ledger` 里显示。
- **运行时失败不降级**：非零退出或超时都是错误响应，不是"半份 prompt"。超时信息里会带上
  最后一行 stderr 与最后一行 stdout 作为诊断线索。

---

## CLI 参考

```console
$ ./bin/ph --help
```

| 命令 | 用途 |
| --- | --- |
| `ph serve` | 启动本机渲染 daemon（默认 `127.0.0.1:18400`） |
| `ph install <pack \| id@version --hub URL>` | 从路径安装，或从注册表安装（`--no-deps` 跳过依赖） |
| `ph check <pack>` | 校验 manifest、依赖、资产与字段落位 |
| `ph test <pack>` | 回放 `fixtures/*.json`，`expect` 逐字节比对 |
| `ph list` | 列出已安装 pack |
| `ph render <id@version> [--ctx k=v]… [--turn ID] [--etag E]` | 通过 daemon 渲染 |
| `ph hash <pack>` | 打印 tree hash |
| `ph ledger [--n 20]` | 查看渲染审计流水 |
| `ph lock <pack> [--update]` | 把范围依赖钉进 `pack.lock` |
| `ph remove <id@version>` / `ph gc` | 删除单个版本 / 清理不完整安装残留 |
| `ph export <id@version> --to DIR` | 导出成内嵌层布局，供 `ph serve --embed` 使用 |
| `ph profile [name/version]` | 打印字段元数据表 |
| `ph runtime [enable php]` | 查看档位 / 按需装 PHP |
| `ph hub serve [--root DIR] [--publish-token T]` | 启动注册表（默认 `127.0.0.1:18500`） |
| `ph publish <pack> --hub URL [--token T]` | 发布 pack |
| `ph index --hub URL` | 列出注册表内容及其声明的能力 |
| `ph upgrade <packId> --hub URL [--yes]` | 升级前先展示能力差异 |

`ph serve` 参数：`--addr`、`--store`、`--token-file`、`--ledger`、`--no-token`、`--php`、`--views`、
`--render-timeout`、`--max-render-memory-mb`、`--max-render-procs`、`--allow-egress`、`--embed`、
`--context-endpoint`、`--context-token`、`--profiles`。

`--no-token` 与 `--publish-token ""` 只用于本机试验；绑定非 `127.0.0.1` 地址时 daemon 会给出警告。

---

## HTTP API

基地址 `http://127.0.0.1:18400`。除 `/v1/health` 外，所有路由都要求 `$PH_HOME/token` 里的 token，
通过 `X-Ph-Token: <token>` 或 `Authorization: Bearer <token>` 传递；响应统一 `Cache-Control: no-store`。

| 方法与路径 | 说明 |
| --- | --- |
| `GET /v1/health` | 存活状态与已装 pack 数 |
| `GET /v1/render?pack=<id@version>&turn=<id>&ctx.<field>=<value>` | 渲染一个 pack |
| `GET /v1/validate?pack=<id@version>[&ctx.<field>=<value>…]` | 预检：声明能力、依赖解析、字段元数据、落位，以及（给了 context 时）还缺哪些字段 |
| `GET /v1/packs` | 已安装 pack（含内嵌层） |
| `GET /v1/packs/{id}/{version}/describe` | 单个 pack 的完整描述：档位可用性、出网授权、依赖闭包、profile 定性 |
| `GET /v1/renderers` | 运行时档位及其可用性与启用提示 |
| `GET /v1/ledger?n=20` | 最近的 ledger 条目 |
| `POST /v1/initialize` | 握手：登记应用名与它的 context 回调地址 |
| `GET /v1/profiles`、`GET /v1/profiles/{name}/{version}` | 字段 schema |

### 渲染响应

成功时返回 `text/plain; charset=utf-8`，正文即 prompt：

| 响应头 | 含义 |
| --- | --- |
| `ETag` | `"sha256:<treeHash 与正文的联合 hash>"`，内容相同则稳定 |
| `X-Pack-Revs` | 解析到的 `id@version` |
| `X-Tree-Hash` | pack 树的内容地址 |
| `X-Ph-Source` | `store` 或 `embed` |
| `X-Render-Ms` | 渲染耗时 |
| `X-Ph-Warnings` | 截断与运行时诊断（若有） |

把 `ETag` 放进 `If-None-Match` 会得到 `304 Not Modified` —— 渲染照常执行、ledger 照常记录
（`"notModified": true`），但不再传正文。宿主就是靠这个在多轮之间缓存冻结前缀的。

### 错误

错误体是 JSON，带稳定的机器可读 `code`：

```json
{ "code": "missing-context", "error": "…", "partial": false, "retryable": false,
  "missing": ["chat.channel.name", "chat.user.nickname"] }
```

| 状态码 | `code` | 原因 |
| --- | --- | --- |
| 400 | `bad-ref` | `pack` 参数不是 `id@version`（此处不接受范围） |
| 400 | `not-content` | 要求渲染一个 `kind: library` pack |
| 400 | `app-not-registered` | 带 `X-Ph-App` 的应用从未调用 `/v1/initialize` |
| 401 | `unauthorized` | token 缺失或错误 |
| 403 | `origin-rejected` | 请求带 `Origin` 头（浏览器一律拒绝） |
| 403 | `egress-denied` | pack 声明的出网 host 未获本机授权 |
| 404 | `not-installed` | store 与内嵌层里都没有该 pack/版本 |
| 409 | `conflict` | 发布时版本冲突 |
| 422 | `missing-context` | 声明的 `requires` 字段没给（带 `missing[]`） |
| 424 | `dependency` | 依赖无法解析 |
| 500 | `integrity` | store 内容与 tree hash 不再一致 |
| 502 | `context-callback` | 应用的 context 端点失败 |

### `POST /v1/initialize`

把一个应用登记进 daemon 的会话表（上限 64），之后就可以为该应用按需拉取 context：

```jsonc
// 请求
{ "protocolVersion": "1", "app": "my-bot",
  "contextEndpoint": "http://127.0.0.1:9000/ph/context", "contextToken": "stub-secret",
  "capabilities": ["pull-context", "validate"] }

// 响应
{ "protocolVersion": "1", "daemon": "prompt-hub/0.1.0", "app": "my-bot",
  "agreed": ["pull-context", "validate"],
  "capabilities": ["pull-context", "validate", "describe", "dependencies", "embed-layer", "exact-version-pin"],
  "tiers": [ { "name": "template", "available": true }, … ],
  "contextConfigured": true }
```

随后 daemon 会向登记的回环端点 `POST {"fields": […]}`，并期望收到 `{"values": { … }}`。
约束是刻意收紧的：

- 回调必须是**回环** `http`/`https`；
- 登记按 `X-Ph-App` 区分，且**不回退到默认来源** —— 未登记应用的请求会以 `app-not-registered` 失败，
  而不是把另一个应用的 context 灌进这个 prompt；
- 查询串里内联给的值优先于回调取回的值；
- 回调响应上限 1 MiB、超时 2 秒。

---

## 注册表（`ph hub`）

注册表是文件式的：`packs/<id>/<version>.json` 元数据 + 去重的 `blobs/<treeHash>.pack` 归档。

| 方法与路径 | 说明 |
| --- | --- |
| `GET /v1/health` | 存活状态与已发布版本数 |
| `GET /v1/index` | 所有已发布版本及其 manifest 与实时 `missingDeps` |
| `GET /v1/packs/{id}/{version}` | 单个版本的元数据 |
| `GET /v1/blobs/{treeHash}` | 归档本体 |
| `POST /v1/packs` | 发布（配置了 `X-Ph-Publish-Token` 时必需；请求体上限 128 MiB） |

发布期策略：

- 带范围的依赖**必须**附带最新的 `pack.lock`，否则拒绝发布；
- **只有 library pack 能被依赖**，依赖一个 `content` pack 会被拒；
- 引用了尚未发布的 pack 会记为 `missingDeps`，由 `ph index` 给出告警，而不是阻断发布；
- 同版本、不同内容重发 = `409`，请改用新版本号；
- 客户端在下载后重算归档的 tree hash 并与元数据比对，不一致就拒绝安装。

`ph upgrade` 从不悄悄扩大能力。它对比 runtime、profile、`outputMax`、`requires` 与 `egress`，
在差异属于扩大时，没有 `--yes` 就拒绝继续：

```
nekochan@1.0.0 -> nekochan@1.1.0
  运行时     template -> php
  输出预算   4000 -> 8000
  新增 context 字段 +chat.user.affinity
  新增出网目标 +api.example.com
错误: 这次升级**扩大了能力**（见上），需要 --yes 明确同意
```

---

## profiles（字段元数据）

context 字段不是"有名字的字符串"，而是有类型、有定性的数据。profile 为每个字段声明
`stability` 与 `privacy`：

| `stability` | 含义 |
| --- | --- |
| `static` | 对所有用户、所有轮次都一样 —— 可以进全局冻结前缀 |
| `session` | 会话内固定，换会话才需要重算 |
| `per-request` | 每轮都变 —— 必须进 append-only 尾段 |

| `privacy` | 含义 |
| --- | --- |
| `public` | 不含个人数据 |
| `internal` | 内部标识 |
| `pii` | 含个人数据 —— 发给远端模型前应当让用户可见 |

`Classify` 会把 pack 的 `requires` 收敛成一个 **placement** 和一个 **最高隐私等级**：

| placement | 推导来源 | 后果 |
| --- | --- | --- |
| `frozen-global` | 只用 `static` 字段 | 可进全局冻结前缀 |
| `frozen-session` | 含 `session` 字段 | 按会话冻结；换会话需要重算 |
| `append-only` | 含 `per-request` 字段，或含未知字段 | 必须每轮追加 |

未知字段会被报告，而不是被忽略 —— 在 `ph check` 里、在 `ph serve` 日志里、在
`/v1/packs/{id}/{version}/describe` 的响应里都会出现。内置 `chat/1`（15 个字段）位于
[`internal/profile/chat@1.yaml`](internal/profile/chat@1.yaml)；额外的 profile 从
`<profiles>/<name>/<version>.yaml` 载入，同名会覆盖内置。

```console
$ ./bin/ph profile chat/1
chat/1  聊天类应用的标准上下文字段。

字段                          类型        稳定性       隐私       说明
chat.assistant.id           string     static       internal  助手自身的标识；对所有人、所有时刻都一样
chat.clock.now              timestamp  per-request  public    当前时间
…
```

---

## JavaScript SDK

[`sdk/js/packhub.mjs`](sdk/js/packhub.mjs) 是无依赖的 pack 格式实现，给跑在 Node.js（或边缘运行时，
算法相同）里的宿主使用：manifest 解析、tree hash、`.pack` 读取、资产闭包，以及 `template` 渲染。

```js
import { loadPack, renderTemplate } from './sdk/js/packhub.mjs';

const pack = loadPack('examples/nekochan');
const { body, warnings } = renderTemplate(pack, {
  'chat.user.nickname': '小兔',
  'chat.channel.name': '辩论队',
}, { turn: 'ev_1', now: new Date('2026-10-10T13:30:00Z') });

console.log(body);
```

导出面：`loadPack`、`loadSingleFile`、`loadArchive`、`isArchivePath`、`readTar`、`treeHash`、
`renderTemplate`、`parseManifestYaml`、`parseHeaderComment`、`normalizeManifest`、`canonicalManifestJson`、
`assetClosure`、`assetRefs`、`checkAssets`、`undeclaredAssetVars`、`parseAssetArg`、`isBuiltinVar`、
`truncateUtf8`、`formatNow`。

另有一个零依赖配套 CLI：

```console
$ node sdk/js/cli.mjs hash   examples/nekochan-asset
$ node sdk/js/cli.mjs check  examples/nekochan
$ node sdk/js/cli.mjs render examples/nekochan-asset --turn ev_1 \
    --ctx chat.user.nickname=小兔 --ctx chat.channel.name=辩论队 --dep cats-role=examples/cats-role
```

Go daemon 与这个 SDK 被要求**产出相同结果**：`internal/render/interop_test.go` 会用两套实现渲染仓库里的
fixture 并逐字节比对正文，其中包括跨依赖边界内联资产的 pack。

---

## 安全模型

`ph serve` 是**本机** daemon，整个设计都基于这个前提：

- **默认只绑回环，浏览器一律拒绝。** 绑定非 `127.0.0.1` 会给出警告；任何带 `Origin` 头的请求都以 `403`
  拒绝 —— 浏览器里的页面即使猜到端口也访问不到 daemon。
- **必须有 token。** 首次运行生成 32 字符 token，落在 `$PH_HOME/token`（`0600`），除 `/v1/health` 外所有路由都要它。
- **出网默认全禁，且需要两侧同意。** pack 必须声明 `egress`，宿主机还必须用 `--allow-egress` 授权；
  运行时另外会收到 `PH_EGRESS_ALLOW`，可以自行强制。
- **context 回调只走回环。** 登记的端点必须解析到回环地址，且各应用的登记彼此隔离。
- **渲染是有界的。** 超时、内存、进程数、输出大小、资产膨胀都有上限；沙箱没生效是错误而不是警告，
  除非本来就是显式关闭的。
- **每一跳都验内容。** 安装、首次渲染、依赖解析、注册表下载都会重算 tree hash，不一致即致命。
- **归档被当作敌意输入。** 越界路径、绝对路径、重复条目、畸形 tar 头、超大条目都在读取阶段被拒，
  在触碰文件系统之前。
- **store 按版本不可变。** `<packId>/<version>` 下只能有一个 tree hash；给已存在的版本装不同内容会被拒。

**不在范围内。** `prompt-hub` 不调用模型，不存对话数据（ledger 里只有 hash 和标识），
也不假装一个靠 token 保护的回环 daemon 是多租户服务。`php` 档会执行 pack 代码 ——
这些代码按定义就是不可信的，沙箱正是为此存在。

---

## 仓库结构

```
cmd/ph/                 CLI 与 daemon 入口（所有子命令）
internal/
  pack/                 manifest、版本范围、pack.lock、tree hash、归档
  store/                内容寻址 store、内嵌层、依赖闭包
  render/               运行时档位、资产展开、进程沙箱
    sdk/php/sdk.php     每次 PHP 渲染都注入的 PHP SDK
  httpapi/              daemon 路由、context 回调、应用会话、describe/validate
  hub/                  注册表服务端与客户端、能力差异对比
  profile/              profile schema、定性、内置 chat/1
  ledger/               追加式 JSONL 审计日志
  phpfetch/             经 sha256 校验的便携 PHP 获取
examples/               七个可跑的 pack + 一个宿主应用桩
sdk/js/                 零依赖 JavaScript SDK 及其测试
```

---

## 开发

```console
$ go build ./...                 # 构建
$ go vet ./...                   # 静态检查
$ go test ./...                  # 全量测试（含跨语言 fixture 对拍）
$ node --test sdk/js/test.mjs    # JavaScript SDK 测试
```

Go 测试覆盖 manifest 校验、版本范围语义、锁解析、归档加固、store 冲突与垃圾回收、
resolver 优先级、profile 定性、HTTP 守卫与错误码、注册表，以及沙箱（含真实 WASI 模块）。
需要可选工具链（跨语言对拍用的 `node`、PHP 档用的 `php`）的测试会自行跳过而不是失败，
因此干净的 checkout 在任何机器上都是绿的。

两点关于工作区整洁的说明：WASI 测试会就地重编译
[`internal/render/testdata/wasmstub/wasmstub.wasm`](internal/render/testdata/wasmstub/main.go)；
另有若干测试会在 `internal/render/` 下创建临时目录（`ph-wasm-*`、`ph-uni-*`、`ph-single-*`）——
这些都由 `.gitignore` 覆盖，并由测试自身清理。

目前没有接 CI：`go test ./...` 与 `node --test sdk/js/test.mjs` 就是全部测试，改动落地前在本地跑一遍即可。

改动的几条底线：

1. **两套实现必须一致。** 任何影响 hashing、manifest 或模板语义的改动，都要同步改 JS SDK 并更新对拍测试。
2. **错误信息也是 API。** 新的失败路径要有稳定 `code`、状态码，最好还有可执行的修复提示。
3. **不要新增静默回退。** 声明的能力兑现不了，就失败。
4. **fixture 即规范。** 行为变了，`examples/*/fixtures/*.json` 就跟着变。

---

## 示例

| pack | 档位 | 演示内容 |
| --- | --- | --- |
| [`nekochan`](examples/nekochan) | `template` | 最小 content pack + 黄金 fixture |
| [`cats-role`](examples/cats-role) | — | `kind: library`、可复用的人设资产 + 一个 PHP 导出 |
| [`nekochan-asset`](examples/nekochan-asset) | `template` | 跨依赖边界的 `{{asset}}`、递归展开、`pack.lock` |
| [`live-data`](examples/live-data) | `template` | 每轮变化的 context（`chat.clock.now`、`chat.turn.user_text`），以及它强制的 `append-only` 落位 |
| [`nekochan-php`](examples/nekochan-php) | `php` | 用 PHP 构造动态 prompt，两个 fixture 覆盖两个分支 |
| [`nekochan-lib`](examples/nekochan-lib) | `php` | 从 `vendor/` 里 `require` 被 include 的 library pack |
| [`nekochan-wasm`](examples/nekochan-wasm) | `wasm` | 用 Go/WASI 模块作为 prompt 入口（先编译：`GOOS=wasip1 GOARCH=wasm go build -o index.wasm .`） |
| [`host-stub.mjs`](examples/host-stub.mjs) | — | 宿主应用示范：握手、`validate`、context 回调、渲染 |

---

## 常见问题

**daemon 会调用模型吗？**
不会。它只产出文本。调用模型的部分留在你的应用里 —— 也正因如此，每次渲染都会返回它到底产出了什么的
tree hash 与 ETag。

**必须用 daemon 吗？**
不必。`ph check`、`ph test`、`ph hash`、`ph lock` 都是离线工具，JavaScript SDK 更是在完全没有 daemon 的
情况下渲染 `template` pack。

**为什么只有 `library` 能被 include？**
为了让组合关系始终有明确形状：library 自己不出 prompt，只提供你内联的片段或你调用的函数。
如果允许依赖一个 `content` pack，"到底哪个 prompt 跑了"就会变得含糊。

**为什么未知模板变量要被拒绝？**
因为 `{{chat.user.nickname}}` 里一个拼写错误，否则就会产出带洞的 prompt，而你是从用户那里知道的。

**为什么 `304 Not Modified` 也算一次渲染记录？**
因为使用缓存前缀仍然是一个"决定发送这份 prompt"的动作。ledger 里记 `notModified: true`，
这样审计流水能区分"刻意复用"与"根本没发生"。

**没装 `php` 档会怎样？**
任何声明 `runtime: php` 的 pack 都会失败，并在错误里给出确切的启用命令。`ph test` 对这类 pack 会跳过，
并给出同样的说明，而不是报错。

---

## 许可

以 [MIT 许可证](LICENSE) 发布 —— © 2026 YesWeAreBot。

每个示例 pack 另外在自己的 manifest 里声明了 `license: MIT`，那描述的是 pack 的内容，而不是这个仓库。
