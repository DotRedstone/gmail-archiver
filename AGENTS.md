# gmail-archiver 维护约定

- 开始前阅读 README.md，运行 `git status --short`；保留无关改动。
- Go 服务只负责归档、花名册和受信客户端 API；QQ 事件、角色权限与 LLM 属于独立 AstrBot 插件。
- 运行 `go test ./...`；鉴权、路径和并发变更运行对应 `go test -race`。测试只使用合成数据和临时目录，不读取真实 `data/`、邮件或密钥。
- HTTP API key 是管理级凭据，不能发给学生、OneBot 或写入 URL。新增接口默认鉴权，身份绑定不能以姓名和学号作为身份凭据。
- 新样例使用 `example.invalid`、`990000000001`、`9000000001` 和“示例甲”，不添加个人部署值。
- 不更新无关锁文件，不进行在线部署、历史重写或公开发布。提交使用中文 Conventional Commits，行为、迁移和文档分别提交。
