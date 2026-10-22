# QQ 课程插件 v3 后端契约

状态：本地源码已加入外部身份绑定、自述确认后上传、查重缓存和实验卡草稿，尚未部署或迁移现网。机器人指令、角色、QQ 事件和回复文案由独立的 `astrbot-plugin-gmail-homework` 项目维护；本服务只保存花名册、提交与身份映射等权威数据。

## 外部身份

旧 `student_bindings.qq_id` 和 `/api/bindings/{qq_id}` 保留用于旧客户端，不会自动解释为 QQ 官方 OpenID。新表 `external_bindings` 以 `(platform_id, bot_id, external_user_id)` 定位一个平台账号，`student_id` 在新表内唯一。若该学号仍在旧绑定表占用，新自述绑定请求返回冲突，须由管理员逐条复核；不得批量把旧 QQ 号复制到新键。

新接口均在 `/api/v3` 下，并受现有 `X-API-Key` 或 Bearer 管理凭据保护：

| 接口 | 行为 |
| --- | --- |
| `POST /binding-intents` | 依据课程花名册核对学号和姓名，创建五分钟待确认状态；只返回必要的确认信息。 |
| `GET /binding-intents` | 受信插件查询某平台账号的待确认状态；过期返回 410；并发创建新意向不会被旧意向的过期清理误删。 |
| `POST /binding-intents/confirm` | 再核对花名册，在事务中消费意向并建立唯一绑定。来源固定为 `self_asserted`。 |
| `POST /binding-intents/cancel` | 取消该平台账号的待确认状态。 |
| `GET /bindings` | 按完整平台身份键或学号查询；无参数仅供受信管理客户端列出绑定。 |
| `GET /bindings?platform_id=...&bot_id=...` | 仅列出当前平台实例与机器人下的绑定，供受信插件处理提醒与纠错。 |
| `POST /my-assignments/query` | JSON 请求体传完整平台身份键；从绑定键推导本人学号，返回本人应交作业和当前是否已交。 |
| `POST /assignments/{id}/mine/query` | 同样传 JSON 身份键，只返回本人该次作业的版本、文件名与提交时间，不返回存储路径或其他学生记录。 |
| `POST /bindings/{student_id}/verify` | 管理员在独立核验后将准确匹配的绑定标记为 `staff_verified`，记录核验者与时间。 |
| `DELETE /bindings/{student_id}` | 管理员解除新绑定；旧表记录不因此改变。 |
| `POST /assignments/{id}/upload` | 接受 `self_asserted` 或 `staff_verified` 平台身份；后端从绑定记录填充学生字段，并再次检查名单、文件名和 ZIP 容器。 |

姓名与学号不是 QQ 账号归属的证明。`self_asserted` 表示五分钟内完成本人声明确认，允许该平台身份提交并查询自身作业；这接受冒领风险，身份争议由课程管理员核对后解绑处理。插件只能用完整平台身份键解析对应学号，不得把任意学号作为学生自助查询参数，也不得据此开放其他学生资料。管理凭据只交给受信插件及运维，不得传给 QQ 消息、学生或 URL。后端不能从 HTTP 参数自行证明 QQ 事件真实，插件必须核验平台实例、机器人和私聊来源，执行角色权限检查。

已有作业规则时，课程花名册取已加载规则的名单；首份作业创建前，可先在 `data/rosters/` 放置 CSV 供绑定和 `/api/roster` 使用。独立名单缺失、损坏或同一学号跨文件对应不同姓名/班级时返回 503，不把“名单不可用”误报成学生填错。旧上传接口仍是管理级接口；插件必须使用 v3 上传接口，不能把旧接口交给自述账号。

## 查重与提交

内容相同的合法提交照常归档，疑似重复只供管理员人工核查。每次直接上传和邮箱导入都会给新提交版本建立 `plagiarism_index_progress`，保存处理中、完成或失败状态；失败不撤销已接收的提交。`GET /api/assignments/{id}/plagiarism` 只比较每名学生当前有效版本：全部索引完成时返回 200 与 `index_status: complete`，仍在处理时返回 202，存在失败或迁移前未建状态的记录时返回 503 与安全的失败类别计数。202/503 不携带可能过时的相似结果。管理客户端可调用 `POST /api/assignments/{id}/plagiarism/reindex` 分批重建至多 10 条当前记录；处理中超过 10 分钟的记录才会被回收，明确失败与旧记录可立即重建。该接口需管理级 API key，返回本批修复数和剩余状态；旧版只读取 200 响应的插件不会误把未完成当作无重复。缓存只在全部当前版本索引完成后供查询复用；上传响应的 `plagiarism_indexed` 表示本次索引及缓存预热是否均成功。该数据保存于 SQLite，不暴露给普通学生。

## 实验卡草稿

`POST /api/v3/lab-cards` 只收 `实验卡N.pdf/.docx/.doc`，限制 25 MiB 并检查容器签名；原文件保存在受限数据目录。multipart 字段 `file` 必填，`deadline` 可填 RFC3339 时间，`targets` 可填 JSON 数组。目标项为 `platform_id`、`bot_id`、`group_openid`、`class_names`；后端要求它们属于同一平台实例与机器人，班级存在于花名册，再为每群计算 `expected_count`，保存学生名单快照。响应包含独立随机 `draft_id`、建议截止、实际 `deadline`、目标群快照和总 `expected_count`。上传只保存草稿，不创建作业；同一实验号已有草稿或作业时返回 409，须先取消旧草稿，不能覆盖。

`GET /api/v3/publication-drafts/{draft_id}` 返回 `card`、`assignment_id`、`publication_status`（`draft`、`preparing`、`ready`）和 `jobs`。未确认时可用 `PATCH` 提交 `deadline` 和/或 `targets` 修改截止与范围，或用 `DELETE` 取消；确认后两者返回 409。`GET /api/v3/lab-cards/{number}` 和 `GET .../download` 保留按实验号查草稿与下载实验卡的接口。班群登记、停用及群 OpenID 的权威来源是插件受限账本；后端只接受插件显式提交的目标快照，不把“所有活动群”推断为发布范围。插件发送前还须复核目标群仍启用；停用群写回 `group_disabled`，不得转发到其他群。

`POST /api/v3/publication-drafts/{draft_id}/confirm` 要求目标群与班级范围非空、预期学生数大于零、提醒时刻尚未过去。数据库在同一事务内固定草稿、`experiment_N` 发布记录和每群 `file`、`notice`、`reminder` 三项投递任务；不同草稿占用同一实验号会冲突。作业规则与快照 CSV 是数据库提交后的可重建投影；只有投影就绪并标记 `ready` 后，任务才能被领取。若投影失败，状态保留 `preparing`，重试同一确认可恢复，不能直接发群。重复确认只返回同一作业、目标快照和稳定 `job_id`，不创建新任务。如果已标记 ready 的规则文件或名单投影丢失，任务列表和领取入口会拒绝将任务交给插件；再次确认会从数据库快照修复投影，期间不会生成第二批任务。返回 200 表示作业规则已准备好，**不表示 QQ 已收到消息或文件**。

`GET /api/v3/publication-jobs?platform_id=<...>&bot_id=<...>` 只列当前机器人已到期且仍为 `pending` 的任务。插件外发前用 `POST /api/v3/publication-jobs/{job_id}/claim` 传 `platform_id`、`bot_id`；后端原子记为 `unknown/interrupted`，避免进程崩溃后盲目重发。随后用 `POST .../{job_id}/result` 传相同身份键、`status`，`sent` 须带 `message_id`；其他结果须带白名单 `failure_category`。状态可为 `sent`、`failed`、`unknown`、`skipped`、`expired`、`manual_pending`；类别包括 `platform_rejected`、`quota`、`timeout`、`interrupted`、`missing_receipt`、`group_disabled`、`already_complete`、`admins_only_missing`、`roster_unavailable`、`dependency_unknown`、`deadline_passed`、`other`。重复提交同一结果幂等；`unknown` 不自动返回待发队列。截止前提醒只针对快照群；若规则准备太迟，已过提醒时刻的任务记为 `expired/deadline_passed`。平台消息 ID 仅是接口回执，不证明群成员已读。

旧客户端的 `POST /api/v3/lab-cards/{number}/confirm` 仍只走人工发布路径，持久状态为 `manual_pending`；按草稿 ID 查询这类记录时 `publication_status` 为 `manual_pending`，没有可领取任务。已有 `confirmed/manual_pending` 记录迁移后没有新任务，不会被自动补发。插件须在切换到新任务接口后才启用自动投递；未知结果先人工核对。

## 发布边界

本地提交和单元测试不改变 Hopper 上的服务。上线需先确认现网二进制、数据与旧绑定表，受限备份 SQLite 的 WAL 一致快照，再在副本上验证迁移和新旧客户端兼容。插件与后端的实际协议需在同一维护窗口验收；失败时按备份和固定版本成对回退，不把旧数字 QQ 号转换为 OpenID。
