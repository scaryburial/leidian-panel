# 贡献指南

本项目是 **雷电面板（ui3344）**，基于 [3x-ui](https://github.com/MHSanaei/3x-ui)（GPL-3.0）的定制分支。
欢迎通过 Issue / Pull Request 反馈问题或提交改进。

- **面向本分支**：请针对本仓库（`scaryburial/leidian-panel`）提 Issue/PR。
- **上游功能/协议相关**：请优先到上游 [MHSanaei/3x-ui](https://github.com/MHSanaei/3x-ui) 反馈。
- 提交 PR 时请说明：改动目的、涉及文件、验证方式。
- 前端改动需通过 `cd frontend && npm run typecheck && npm run build`。
- 后端改动需通过 `go build ./...`。

## 开发环境
- Go 1.27+、Node 24+、Python 3（安装脚本用）。
- 构建打包：`bash build-release.sh`（前端 → 后端 → 组装 → 打 tar 包）。
