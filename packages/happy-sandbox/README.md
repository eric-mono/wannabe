# happy-sandbox

一个 rootless Podman 容器，用起来像一台小 Linux 服务器（systemd、cron、Node、Claude Code、happy 都在里面）。

默认镜像不带 Codex：宿主镜像模式下用宿主自己装的 `codex`；隔离模式下要用的话，在沙箱里自己装，或者构建镜像时加 `WITH_CODEX=1`（`WITH_CODEX=1 tools/build-image.sh …`）。
**它对外只有一条路：一个 VLESS 出口，并且锁定在这个出口的 IP 上。** 出口不对或连不上，所有对外连接立即断开，不会走宿主的网络。

它和 happy 是两套东西：`happy update` 和 happy 的安装脚本都不会装它，也不会下载它的镜像。

## 需要什么

- Linux x86_64（arm64 暂不支持）
- rootless Podman 3.4+（Ubuntu 22.04 起自带：`sudo apt install podman uidmap`）、cgroup v2
- 你的用户有 subuid/subgid（`/etc/subuid`、`/etc/subgid` 里有你的用户名）
- 约 10 GB 磁盘（镜像约 2 GB，加上每个沙箱的 home）
- 一个 mihomo 格式的出口节点文件（一个节点），以及这个节点的出口公网 IP

安装时会先检查这些；缺什么会说明为什么需要、怎么解决，然后停下，不会装一半。随时可以用 `happy-sandbox check` 再查。

## 安装、更新和卸载

和 happy 的安装一样：

```bash
bash ~/wannabe-dist/sandbox/install.sh   # 你构建的发布目录（见仓库首页 README）
```

- 工具装在 `~/.happy-sandbox/bin`，`~/.local/bin/happy-sandbox` 是指向它的链接。
- 装了 happy 的机器上也可以统一用 `happy cage …`（和 `happy-sandbox …` 完全一样）：`happy cage install`、`happy cage update`、`happy cage uninstall`。
- 镜像约 700 MB 下载（解开约 2 GB），装进你自己的 Podman。
- 更新：`~/.happy-sandbox/bin/install.sh`（镜像没变就不重新下载）
- 卸载：先 `happy-sandbox destroy N --yes` 删掉每个沙箱，再 `~/.happy-sandbox/bin/install.sh --uninstall`

全程不需要 root，也不会调用 sudo。

## 开始用

```bash
chmod 600 node.yaml
happy-sandbox init --node node.yaml --exit-ip 203.0.113.7 \
    --timezone America/Los_Angeles --locale en_US.UTF-8 \
    --share ~/proj:/workspace/proj --share ~/data:/reference/data:ro
happy-sandbox up          # 起容器和网络；确认出口 IP 正确后才放行
shell1                    # 以 dev 用户进去（= happy-sandbox shell）
```

沙箱按顺序编号：`init` 总是建成最小的空闲序号（1、2、3…），不能起名字。其他命令在子命令后面
跟序号选沙箱（`happy-sandbox up 2`），不写就是 1 号。

第一次进去后：

```bash
happy auth login          # 扫码；App 里会多出一台机器，叫「宿主名-序号」（如 ls5-1）
claude                    # 登录你自己的账号
codex login
```

登录信息都在沙箱的 home 里，重启、升级镜像以后都还在。happy daemon 由沙箱里的 cron 保持运行，happy 每天夜里自动更新。

时区和 locale 请选得和出口所在地区一致。不写的话默认是美国西部、en_US.UTF-8。

想让沙箱和宿主几乎一样（同一个用户、home、conda、/mnt、内存和 GPU），见下面「宿主镜像模式」。

## 命令

```
happy-sandbox init --node FILE --exit-ip IP [--profile isolated|host] [--manual] [--timezone TZ] [--locale L]
                   [--share SRC[:TARGET[:ro]]]...
                                    建下一个沙箱（最小的空闲序号），默认跟宿主一起启动；
                                    --manual：只手动启动
happy-sandbox up | down | restart | status | shell [N]
happy-sandbox exec [N] -- CMD...
happy-sandbox here [N] [CMD...]     在宿主当前目录对应的沙箱路径里运行 CMD（默认一个 shell）
happy-sandbox config [N] show | set KEY VALUE | unset KEY | edit | preset host|isolated
                                    沙箱能用宿主的哪些东西（见「宿主镜像模式」）
happy-sandbox packages [N] list | add PKG... | rm PKG...
                                    沙箱要一直保留的 apt 包（容器重建后自动重装）
happy-sandbox identity [N]          这台沙箱从里面看是什么样的机器
happy-sandbox share [N] list | add SRC [TARGET] [--ro] [--submounts] | rm TARGET
happy-sandbox enable | disable [N]  跟宿主一起启动；网络进程出错时自动重启
happy-sandbox list
happy-sandbox links [N] [--remove]  重新生成（或删除）沙箱的命令 claudeN 等（不写 N：全部）
happy-sandbox check                 宿主能不能跑沙箱（不能的话说明原因；也报告 GPU）
happy-sandbox selinux-policy [--install]   CentOS/RHEL/Fedora：TAP 设备的那条 SELinux 规则
happy-sandbox image list | load FILE
happy-sandbox destroy [N] --yes     删掉这个沙箱和它的整个目录；下次 init 会补上这个序号
happy-sandbox version
```

`N` 是沙箱序号，只能紧跟在子命令后面；不写就是 1 号。每个沙箱可以用不同的出口。

## 编号沙箱和命令

沙箱只用序号命名：`~/.happy-sandbox/1`、`~/.happy-sandbox/2`……每个沙箱在 `~/.local/bin`
里有四个命令，都在宿主的当前目录（对应的沙箱路径）里运行：

| 命令 | 运行 |
| --- | --- |
| `claudeN` | `claude`，带 `access.tool_args`（full access） |
| `codexN` | `codex`，同上 |
| `happyN` | `happy --yolo`：一个 Happy 会话，手机 App 上也能看到、接管；`happyN codex` 是 Codex 会话 |
| `shellN` | 一个 shell；`shellN CMD...` 运行 CMD |

`HSB_SAFE=1` 时都不加 full access 参数。`init` 会写这些命令，`destroy` 会删掉。
在沙箱里面直接敲 `happy` / `claude` 不经过这些命令：full access 的沙箱（`tool_args` 给 claude
`--dangerously-skip-permissions`，新建的沙箱默认都是）在 `up` 时给沙箱自己的 Claude 配置写上
`permissions.defaultMode: bypassPermissions`，所以也默认 bypass；你自己设过的 defaultMode 不会被改。
同名的文件如果不是 happy-sandbox 写的（比如你自己的 `codex2`），不会覆盖，会提示你。

每个沙箱的 Claude、Codex、happy 登录各自独立（宿主镜像模式下在 `state/` 里），可以登录
不同的账号：`shell2` 进去后 `happy auth login`、`claude`、`codex login` 各一次。Happy App 里
沙箱 N 显示为「宿主名-N」：daemon 在 App 里还没有这台机器的名字时写一次，你在 App 里改过的名字
不会被改回。

以前建的非数字沙箱（如 `default`）会在第一次运行任何 happy-sandbox 命令时自动改成下一个空闲序号：
先停下，改目录名、开机启动和命令，原来在运行的再启动起来；home、登录、身份都保留。

改配置（`config`、`share`）后执行 `happy-sandbox restart` 生效：容器按新配置重建，
home、登录信息和数据都保留。

## 文件在哪

每个沙箱的所有东西都在 `~/.happy-sandbox/<名字>/`：

| 文件 | 内容 |
| --- | --- |
| `config.json` | 出口 IP、镜像、共享目录、`access`（能用宿主的哪些东西）；可选的宿主端口 |
| `identity.json` | 机器身份（见下） |
| `machine-id` | 由 identity.json 生成，只读挂载为 `/etc/machine-id` |
| `node.yaml` | 出口节点（权限 600） |
| `home/` | 沙箱自己的 home（隔离模式）：登录信息、代码、在里面生成的 ssh/git 密钥 |
| `state/` | `access.home.private` 里工具的私有状态（宿主镜像模式下 Claude / Codex / happy 的登录），以及宿主镜像模式下 apt 的自动记录（`state/apt`） |
| `apt-lists/` | apt 的软件包索引（配置了 `packages` 时），容器重建后继续用 |
| `guest/` | 由配置生成、只读挂载到沙箱 `/etc/happy-sandbox` 的启动脚本和环境 |
| `run/` | 网络状态和日志：`gate.log`（出口检查）、`net.log`、`mihomo.log` |

容器本身随时可以从这些文件重建。改配置、改共享目录、改身份、换新镜像、宿主升级显卡驱动时都会自动重建，
home 保留。只有 home 以外的系统改动会丢掉；apt 装的包会自动补回（见下）。

隔离模式下，宿主机上的东西只有共享目录会进到沙箱：不带入宿主 home，也不带入 ssh/git 凭据。
需要的话在沙箱里自己配，配好后会跟着 home 一起保留。

共享目录的目标路径默认和宿主相同；可以放在除系统目录（`/usr`、`/etc`、`/var`、`/tmp`、
`/proc` 等）以外的任何地方。读写还是只读由 `:ro` / `--ro` 决定。`--submounts` 会把源目录
下面挂载的每块盘（包括 autofs / NFS）也一起挂进去，例如 `share add /mnt --submounts`。

## 宿主镜像模式

```bash
happy-sandbox init --profile host --node node.yaml --exit-ip 203.0.113.7 --timezone America/New_York
# 或者把已有的沙箱改过来（出口、身份、数据不变）：
happy-sandbox config preset host && happy-sandbox restart
```

沙箱里运行的程序和在宿主上运行几乎没有区别，只有这几样是它自己的：

1. **网络**：仍然只有门禁这一条路，出口 IP 不对就断网。
2. **机器身份**：machine-id、MAC、主机名、时区。
3. **Claude / Codex / happy 的登录**：存在 `~/.happy-sandbox/<名字>/state/`，和宿主的互不影响。
   happy 的账号和 daemon 是沙箱自己的，App 里它是一台单独的机器（沙箱的 hostname），
   宿主上另外装的 happy 不受影响。

`happy-sandbox config show` 里的 `access` 段决定沙箱能用宿主的什么，每一项都能单独改：

| 配置 | host 预设 | 含义 |
| --- | --- | --- |
| `user` | `host` | 沙箱里的用户名、uid、gid、home 路径、shell 和宿主一样（隔离模式是 `dev`） |
| `home.mode` | `host` | 宿主的 home 以原路径读写挂载：同一个 `.bashrc`、conda、git 配置、工具 |
| `home.private` | `claude`, `codex`, `happy` | 这些工具的登录和设置不用宿主的，用沙箱自己的一份 |
| `home.protect` | 门禁相关的几个目录 | 在读写的 home 上再挂一层只读，沙箱改不了自己的出口、节点和门禁程序 |
| `env.unset` | 各种 `*_proxy` | 进入沙箱时清掉的环境变量（宿主 `.bashrc` 里指向局域网代理的变量在沙箱里会断网） |
| `env.set` | — | 进入沙箱时额外设置的变量 |
| `resources.memory` | 默认 | 默认宿主内存的 90%（至少给宿主留 4G）；也可写 `64G`、`max` |
| `resources.pids` | 默认 | 进程数上限，默认不限；也可写 `4096` |
| `resources.cpus` | 默认 | 默认全部 CPU；或者一个数，例如 `8` |
| `resources.shm` | 默认 | `/dev/shm` 大小，默认和宿主一样；PyTorch 多进程加载数据、把缓存放在 `/dev/shm` 的程序需要大一些 |
| `resources.ulimits` | 默认 | 文件句柄等上限，默认跟随宿主；`podman` 用 Podman 自己的 |
| `devices.gpu` | `auto` | `auto`、`none`、`nvidia`、`amd`，或者 NVIDIA 的某一块卡（`0`、UUID），见下 |
| `devices.extra` | — | 其他设备，例如 `/dev/ttyUSB0` |
| `tool_args` | Claude / Codex 的 full access 参数 | `happy-sandbox here claude` 时自动加上；`HSB_SAFE=1` 时不加。隔离预设也默认带，新建的沙箱都是 full access；要权限确认就 `config unset access.tool_args` |
| `packages` | `openssh-client` | 要一直保留的 apt 包，见下 |
| `shares` | `/mnt`、`/media`（`submounts`） | 宿主的其他目录，原路径读写 |

例：`happy-sandbox config set access.resources.memory 64G`，然后 `happy-sandbox restart`。
隔离模式下这些项也都能用（资源、GPU、packages、env）。

**这个模式放开了什么**：沙箱可以读写宿主 home 和 /mnt 下的一切（`protect` 的除外）。
沙箱写进宿主的东西（conda 里的包、`.bashrc`、脚本），之后**由宿主运行时走的是宿主的网络**，
门禁管不到。门禁保证的是：沙箱里运行的程序（Claude Code 等）发出的流量永远只走那个出口。

系统目录（`/usr`、`/etc`，apt 装的软件）仍然是镜像里的：容器要运行自己的 systemd。
镜像里的 Node、happy 装在 `/home/dev`（容器层）：宿主的工具优先，宿主没有的才用镜像的。
容器重建后它们回到镜像里的版本，下一次夜间更新再跟上。

### 在宿主当前目录里用

```bash
cd /mnt/某个项目
claude1                        # = happy-sandbox here claude：full access（tool_args），路径和宿主一样
shell1                         # 一个 shell
shell1 python train.py
```

## apt 包（重建后自动补回）

`apt install` 装的东西在容器层里，容器重建时本来会丢。有两种办法让它们回来，可以同时用：

**自动记录**：每次 `apt install`、`apt remove` 之后，沙箱都会把「你在镜像之外加装的包」
记到 `apt-packages.auto`；容器重建后开机，等网络通了，在里面自动装回来（日志：
`journalctl -u hsb-packages`）。平时照常 `sudo apt install` 就行。记录在：

- 隔离模式：home 里的 `~/.config/happy-sandbox/`
- 宿主镜像模式：`~/.happy-sandbox/<名字>/state/apt/`（不写进宿主的 home）

同目录的 `apt-packages` 可以手写，一行一个，`#` 后面是注释，和自动记录的合在一起装。

**`packages` 列表**（在 `config.json` 里，从宿主管理）：每次 `up` 时检查，缺了就通过门禁装上：

```bash
happy-sandbox packages add openssh-client htop    # 沙箱开着的话立刻安装
happy-sandbox packages list
happy-sandbox packages rm htop
```

用了 `packages` 时，apt 的软件包索引保存在 `~/.happy-sandbox/<名字>/apt-lists/`，只在第一次
下载（几十 MB，走门禁要几分钟）；之后重建容器只需要重新下载包本身。

镜像里已经自带常用工具：vim、jq、rsync、ripgrep、tree、htop、file、zip、lsof、strace、ssh 客户端，还有 Chromium 需要的系统库和中日韩字体。

## 资源限制

`access.resources`（见上表）：默认内存是宿主的 90%（至少给宿主留 4G）、不限进程数、全部 CPU、
`/dev/shm` 和 ulimits 跟随宿主。限制是整个沙箱共用的，里面所有会话加在一起算。

- 进程数设得太小，编译、测试这类并行任务会出现 `fork: resource temporarily unavailable`。
- 每一项都要宿主把对应的 cgroup 控制器交给普通用户（`happy-sandbox check` 会列出有哪些）。Ubuntu 默认只给 memory 和 pids，要用 `cpus` 得先加一个 systemd 配置：`/etc/systemd/system/user@.service.d/delegate.conf` 写 `[Service]` 和 `Delegate=cpu cpuset io memory pids`，然后 `sudo systemctl daemon-reload`，重新登录。
- 旧写法 `config.json` 顶层的 `memory_bytes`（字节）、`cpus`、`pids_limit` 仍然有效，`access.resources` 里写了的项优先。

## GPU

`devices.gpu` 为 `auto`（默认）时：宿主装好了 NVIDIA 的 CDI 描述就给所有 NVIDIA GPU，否则装了
ROCm 就用 AMD，都没有就不给。`none` 不给；`0` 或某块卡的 UUID 只给那一块。
（旧写法：`config.json` 顶层的 `"gpu"`，`access.devices.gpu` 没写时才看它。）

**NVIDIA**：

- 沙箱里能直接用 `nvidia-smi`。驱动用的是宿主的，宿主升级驱动后，下次 `up` 会自动重建容器跟上，home 保留。
- PyTorch 这类框架自带 CUDA 运行库，只需要宿主的驱动：隔离模式装在 home 里，宿主镜像模式下宿主 conda 里的 CUDA 版 PyTorch 直接能用。
- 每台机器做一次：

```bash
nvidia-smi                                   # 先确认宿主的驱动正常
curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey \
  | sudo gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg
curl -fsSL https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list \
  | sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' \
  | sudo tee /etc/apt/sources.list.d/nvidia-container-toolkit.list
sudo apt update && sudo apt install -y nvidia-container-toolkit
sudo nvidia-ctk cdi generate --output=/etc/cdi/nvidia.yaml   # 每次升级驱动后重新执行
happy-sandbox check                          # 应显示 NVIDIA GPU (optional) ok
happy-sandbox restart
```

**AMD（实验性）**：需要宿主装好 ROCm，用户在 `render`、`video` 组里。`check` 会报告。

## 开发用：从沙箱连宿主端口（默认关）

`config.json` 里写 `"host_ports": [22]`：沙箱里访问 `192.168.1.254:22`，就连到宿主本机的 `127.0.0.1:22`（比如从沙箱 ssh 到宿主）。只开列出的 TCP 端口、只到宿主本机，其他流量照旧只走出口。改完 `down` 再 `up` 生效，不重建容器。

## 网络是怎么锁住的

- 容器没有自己的网络（`--network none`）。它唯一的网卡由宿主上的一个用户态网络栈提供。这个网络栈只把 TCP 交给 mihomo（只配置了这一个节点）；DNS 也经同一条路走 DoH。
- UDP、IPv6、局域网和宿主本机都不通，也不存在任何直连路由（`host_ports` 明确打开的宿主端口除外）。
- 出口在开始时是关着的。宿主每 15 秒通过这条路访问 AI 服务商（Anthropic、OpenAI）的 Cloudflare trace，看对方看到的 IP 是不是你配置的出口 IP，是才放行。
- 出口 IP 一旦不对，立即关闭，并断开所有已有连接。连续连不上约 15–25 秒后关闭。超过 90 秒没有确认，也会自动关闭。
- 关闭和恢复时，宿主会推送到沙箱里登录的那个 happy 账号的设备上：「⚠️ 沙箱网络已断开」「✅ 沙箱网络已恢复」。
- 沙箱里 `ping` 任何地址都会"通"：这是网络栈自己回的，没有任何数据出去。

## 机器身份

从沙箱里面看到的"这台机器"，分三层：

1. **identity.json（我们控制）**：machine-id、网卡 MAC、hostname、时区、locale。`init` 时随机生成，不取宿主的任何信息，镜像里也不带任何身份，所以同一个镜像的不同用户、不同沙箱都各不相同。之后每次 `up` 都按这个文件来，重启宿主、换新镜像、重建容器，看到的都是同一台机器。改这个文件（或从备份恢复）后，下次 `up` 就按新的来。
   - **hostname**：默认是一个普通工作站的名字（如 `ws-7k3q2m`、`dev-k9f2xa`），不带宿主的名字，也不带 sandbox 字样。**同一台宿主上每个沙箱的 hostname 都不同**，也不会和宿主自己的一样：`init`、`identity set`、`up` 都会检查，重名就拒绝。hostname 只能随机生成，不能指定。手机 App 里看到的机器名是「宿主名-序号」，不是它。
   - **MAC**：真实网卡厂商（Intel、Realtek）的前缀加随机的后三字节，不是一看就是虚拟网卡的「本地管理」地址。网卡名和宿主的板载网卡一样（如 `enp6s0`），存在 identity.json 的 `interface`；沙箱的网络看起来是一个普通家用局域网（本机 `192.168.1.105`，路由器 `192.168.1.1`）。容器本身仍然查得出来：TAP 网卡、没有 PCI 设备、`/run/.containerenv`（Podman 绑定挂载进来的，沙箱没有 CAP_SYS_ADMIN，删不掉也卸不掉）、`systemd-detect-virt` 显示 podman。
   - **`/etc/hosts`** 由沙箱自己写，只有 `localhost` 和自己的 hostname，不会出现宿主的主机名、局域网 IP 或容器名。
   - 改：`happy-sandbox identity N set hostname random`（新随机一个）、`set mac random`、`set timezone TZ`、`set locale L`，然后 `happy-sandbox restart N`。
2. **宿主硬件（如实暴露）**：CPU 型号和核数、内存、内核版本。容器和宿主共用内核，改不了，同一台宿主上的所有沙箱都一样。我们不去伪造它们。硬件序列号这类只有宿主 root 能读的信息，沙箱里读不到。
3. **工具自己的标识（在 home 里）**：Claude Code、Codex、happy 第一次运行时各自生成的安装 ID 和登录信息，都存在 home 里，一直保留。

对外的公网 IP 就是出口 IP。

## 跟宿主一起启动

`init` 建的沙箱默认就跟宿主一起启动（`init --manual` 则不会）。之前建的、或 `disable` 过的沙箱：

```bash
happy-sandbox enable
sudo loginctl enable-linger $USER     # 只需一次：不登录也能在开机时启动（宿主级设置，需要管理员）
```

宿主上没有 systemd 用户服务时，`init` 照样建好沙箱，只是提示没能启用，之后只能手动 `up`。

`enable` 会为这个沙箱装一个 systemd 用户服务（`happy-sandbox@<名字>.service`）：开机启动；网络进程出错时，15 秒后自动重启，容器不受影响。启用以后 `up` / `down` 会通过 systemd 来执行。`disable` 取消开机启动，并停下沙箱。

## SELinux（CentOS / RHEL / Fedora）

这些系统默认不让容器打开 TAP 设备，而沙箱唯一的网卡就是一个 TAP。不装规则的话，
`up` 之后网络一直是 down，沙箱里连 DNS 都用不了，表现为 `EAI_AGAIN`。

装一次即可（需要 root）：

```bash
happy-sandbox selinux-policy --install    # 让它执行（会要 sudo 密码）
happy-sandbox selinux-policy              # 或者只打印命令，自己执行
```

装完 `happy-sandbox up` 就正常。撤销：`sudo semodule -r happy-sandbox`。
`happy-sandbox check` 会告诉你这条规则装了没有。

**从 0.3.0 起要重装一次规则**（模块版本 1.1）：0.3.0 的网络改成容器里的服务通过 socket 连宿主上的网络进程，
SELinux 默认也不允许这一步。旧规则（1.0）只放行了 TAP 设备。升级到 0.3.0 以后，先执行一次
`happy-sandbox selinux-policy --install`，再 `happy-sandbox down && happy-sandbox up`。
没装新规则时，`up` 会停在「等待沙箱连接」，并提示这条命令；被拒绝的记录可以用
`sudo ausearch -m avc -ts recent | grep happy` 查看。

共享目录如果在 SMB / NFS 上，还有另一个限制：网络文件系统没法打 SELinux 标签。
在挂载那个网络盘时加容器上下文（SMB 的 fstab 加
`context=system_u:object_r:container_file_t:s0`），或者把要共享的内容放本地磁盘。

## 出问题时

- `happy-sandbox status`：容器、网络（出口、上次检查时间、关闭原因）、开机启动、happy 登录状态
- `~/.happy-sandbox/<名字>/run/gate.log`：每次出口检查的结果
- 启用了开机启动的：`journalctl --user -u happy-sandbox@<名字>`
- 网络一直关着：先看节点能不能用，以及 `--exit-ip` 是不是这个节点真实的出口 IP（`gate.log` 里会写看到的是哪个 IP）
- CentOS / RHEL / Fedora（SELinux 开着）：沙箱自己的目录会自动打上容器标签，不用管。
  TAP 设备需要一条 SELinux 规则：见上面「SELinux」一节。
  共享目录如果在网络盘上（SMB、NFS），SELinux 没法给它打标签，沙箱里会读不了：
  挂载那个网络盘时加容器上下文，例如 SMB 的 fstab 里加
  `context=system_u:object_r:container_file_t:s0`；或者把要共享的内容放到本地磁盘。

## 许可

网络核心来自 Claude Cage（MIT，见 `LICENSE.cage`、`NOTICE.cage`），用户态网络栈来自 gvisor-tap-vsock（Apache-2.0）。
