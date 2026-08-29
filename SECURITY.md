# Security policy

## Supported versions

项目在发布首个稳定版本前，仅维护最新版本。

## Reporting a vulnerability

请通过 GitHub 的私有漏洞报告功能提交安全问题：

<https://github.com/ShawnKung/limitping/security/advisories/new>

如果该入口不可用，请创建一个不包含利用细节和敏感信息的 issue，请求维护者提供私下联系
方式。不要公开提交 token、`auth.json`、account ID、内网地址或完整个人日志。

报告中可以包含：

- 受影响版本与平台；
- 最小复现步骤；
- 影响范围；
- 已脱敏的日志或测试用例；
- 建议修复（如有）。

## Security boundaries

`limitping` 会读取 Codex CLI 的 OAuth 凭据，并在 token 失效时更新该文件；也可以把用量
元数据发送到用户指定的 Pushgateway。对这两部分的变更会按安全敏感变更处理。
