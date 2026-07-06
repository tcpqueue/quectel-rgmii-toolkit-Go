# Windows 本地测试 SimpleAdmin Go 服务

这个目录用于在 Windows 上测试 Go 版 SimpleAdmin 的 Web/Vue 页面、登录认证、路由和 `/api/*` 接口。

## 一键运行

在项目根目录双击：

```bat
run_windows_test.bat
```

浏览器会打开：

```text
http://127.0.0.1:18080/
```

默认账号密码：

```text
admin / admin
```

## 运行模式

Windows 测试使用 `--mock` 模式：

- 不访问真实 `/dev/smd11`
- 不创建 Linux PTY 造口
- 不调用 `iptables/ip6tables`
- 不安装 systemd 服务
- AT、短信、TTL、基础状态返回本地模拟数据
- `/console` 只显示 Windows 测试说明，不启动 Linux 原生 PTY shell
- 启动窗口支持手动输入首页 AT 测试数据，不需要改 `index.js` 里的测试常量

这样可以在没有模块、没有 ADB、没有串口设备的 Windows 电脑上检查页面和接口是否正常。

## 手动输入首页 AT 测试数据

运行 `run_windows_test.bat` 后，在启动服务的命令行窗口输入：

```text
at
```

然后粘贴整段首页 AT 返回，最后单独输入一行：

```text
.end
```

页面下一次刷新会使用这段数据，后端会通过 `/api/dashboard_data` 返回解析后的结构化 JSON。

也可以只替换部分内容：

```text
qca     # 粘贴 QCAINFO 测试数据
qeng    # 粘贴 QENG 测试数据
parse   # 在控制台打印当前首页解析结果
show    # 查看当前是否启用手动输入
clear   # 清除全部手动输入，恢复默认 mock 数据
```

## 浏览器开发者工具 Console 输入

Windows mock 控制台输入会保留，同时也可以在网页开发者工具 Console 里直接输入：

```js
SimpleAdmin.MockAT.at(`粘贴整段首页 AT 返回`)
SimpleAdmin.MockAT.qca(`粘贴 QCAINFO 返回`)
SimpleAdmin.MockAT.qeng(`粘贴 QENG 返回`)
SimpleAdmin.MockAT.parse()
SimpleAdmin.MockAT.show()
SimpleAdmin.MockAT.clear()
```

也提供简写：

```js
saAt(`粘贴整段首页 AT 返回`)
saQca(`粘贴 QCAINFO 返回`)
saQeng(`粘贴 QENG 返回`)
saParseAT()
saShowAT()
saClearAT()
```

这些命令通过 `POST /api/mock_at` 写入 Windows mock 后端，浏览器首页下一次刷新会使用新的 AT 测试数据。真实模块运行时该接口会拒绝。

## 手动编译

```bat
windows-test\build_windows_test.bat
```

输出文件：

```text
windows-test\bin\simpleadmin-httpd-windows-amd64.exe
```

## 本地数据

运行时数据保存在：

```text
windows-test\data\
```

包括：

- `simpleadmin.auth`
- `ttlvalue`
- `server.crt`
- `server.key`

## 与模块版本的区别

Windows 测试版只用于本地调试 Web 和 API 行为。真正安装到模块时仍然使用：

```text
toolkit.bat
```

模块运行的是：

```text
development\simpleadmin\simpleadmin-httpd.armv7
```

## 404 处理

如果浏览器显示：

```text
Access Error: 404 -- Not Found
Cannot locate document: /
```

通常不是 SimpleAdmin 页面本身的问题，而是浏览器访问到了其它旧服务，或者测试服务没有使用正确的静态网页目录。新版 `run_windows_test.bat` 已做这些处理：

- 使用 `18080` 端口，避开常见的 `8080` 冲突。
- 启动前检查 `development\simpleadmin\www\index.html` 是否存在。
- 启动时在窗口里打印实际使用的 `Static` 路径。
- Go 服务启动时会校验 `--static` 目录，不正确会直接报错退出，不会启动一个空 Web 根目录。

请使用根目录的：

```bat
run_windows_test.bat
```

然后访问：

```text
http://127.0.0.1:18080/
```
