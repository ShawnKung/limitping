# Contributing

感谢你为 `limitping` 做贡献。

## 开始之前

- 不要提交 `~/.codex/auth.json`、token、account ID、真实 Pushgateway 地址、主机名或日志。
- 行为变更应同时更新测试与 README。
- 保持 CLI flag 精简；新增配置前请先说明无法复用现有行为的原因。
- 修改 Codex 后端解析时，请使用脱敏 fixture，不要提交真实响应。

## 本地开发

需要 Go 1.22 或更高版本：

```sh
git clone https://github.com/ShawnKung/limitping.git
cd limitping
make check
make build
```

提交 pull request 前请确保：

```sh
make check
git diff --check
```

## Pull request

- 一个 PR 聚焦一个问题。
- 描述用户可见的变化、验证方式和兼容性影响。
- 涉及 OAuth、文件权限、外部命令或网络请求的变更，应说明安全边界。
- 不依赖真实 Codex 账号的逻辑应有单元测试。

## 报告问题

请提供操作系统、`limitping version`、Codex CLI 版本、执行命令和脱敏后的错误信息。
不要粘贴认证文件、HTTP Authorization header 或包含 token 的 URL。
