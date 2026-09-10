# [实施计划] 角色权限控制 (RBAC) 与敏感界面访问限制方案

为 OpenWiki Portal（Unit 2）与 Orchestrator（Unit 1）引入基于角色的权限控制（Role-Based Access Control, RBAC），划分**系统管理员（`admin`）**与**普通用户（`user`）**两级权限。严格限制普通用户对仓库新增/修改/删除配置、手动触发构建以及审计日志界面的访问，仅保留 Wiki 内容查看与 AI 问答对话权限。

## 用户审核事项

> [!IMPORTANT]
> 1. **权限划分判定原则**：
>    - **管理员（`admin`）**：支持在 `config.yaml` 的 `auth.admin_users` 列表中声明用户名（例如 `["admin", "bigc"]`），或通过 LDAP 返回的 Admin 组（如 `admin_group`）自动识别。拥有全部权限（仓库 CRUD、触发构建、审计日志）。
>    - **普通用户（`user`）**：仅允许执行只读操作（查看仓库列表 `GET /api/repos`、读取 Wiki 页面 `/wiki/*`、图拓扑 `/api/graph`）及发起 AI 问答会话（`POST /api/chat`）。
> 2. **后端鉴权机制**：
>    - 在 Portal 的 HTTP Router 中增加 `RequireAdmin` 鉴权中间件。
>    - 对敏感接口（`POST/DELETE /api/repos`、`POST /api/build/trigger`、`GET /api/audit`）实施强制身份验证与角色校验，非法访问返回 `HTTP 403 Forbidden`。
> 3. **前端界面自适应（Portal Web UI）**：
>    - Portal 网页端根据登录用户的 `role` 动态隐藏/禁用“添加仓库”、“编辑仓库”、“手动构建”与“审计中心”入口。

## 拟变更文件

### 配置规范更新

#### [修改] [config.go](file:///Users/bigc/openwiki/deploy/unit2-portal/internal/config/config.go)
- 在 `Config` 结构体中增加 `Auth` 模块配置：
  ```yaml
  auth:
    admin_users: ["admin", "bigc"]       # 静态管理员白名单列表
    admin_group: "CN=WikiAdmins,..."     # (可选) LDAP 管理员组名
  ```

#### [修改] [config.yaml (模版与构建脚本)](file:///Users/bigc/openwiki/deploy/scripts/build-arm.sh)
- 在生成的默认配置文件中内置 `auth.admin_users` 节点示例。

---

### 后端权限鉴权中间件与逻辑

#### [修改] [session.go](file:///Users/bigc/openwiki/deploy/unit2-portal/internal/auth/session.go)
- 在 Session 结构体中增加 `Role string` 字段 (`"admin"` / `"user"`)。
- 在用户登录/LDAP 验证成功后，匹配 `admin_users` 列表或 LDAP Group 设置用户角色。

#### [修改] [router.go](file:///Users/bigc/openwiki/deploy/unit2-portal/internal/api/router.go)
- 实现 `RequireAdmin` 中间件，拦截无管理员权限的请求。
- 路由权限细化映射：
  - `GET /api/repos` -> **公开/普通用户**
  - `POST /api/repos` -> **仅管理员 (`RequireAdmin`)**
  - `DELETE /api/repos/{id}` -> **仅管理员 (`RequireAdmin`)**
  - `POST /api/build/trigger` -> **仅管理员 (`RequireAdmin`)**
  - `GET /api/audit` -> **仅管理员 (`RequireAdmin`)**
  - `POST /api/chat` -> **普通用户可访问**

---

### 前端 UI 自适应调整

#### [修改] [public/app.js (或登录控制页)](file:///Users/bigc/openwiki/deploy/unit2-portal/public/)
- 在登录响应中返回用户角色 `role: "admin" | "user"`。
- 根据 `role` 控制控制台导航与按钮显示：
  - 普通用户隐去“新增仓库”、“修改配置”、“手动触发更新”及“审计日志”菜单。

## 验证计划

### 自动化验证
- 编写 Go 单元/集成测试：
  - 使用普通用户 Session 请求 `POST /api/repos`，断言返回 `403 Forbidden`。
  - 使用管理员 Session 请求 `POST /api/repos`，断言正常成功。
  - 使用普通用户 Session 请求 `POST /api/chat` 与 `GET /api/repos`，断言返回 `200 OK`。

### 手动验证
- 在 `config.yaml` 中配置 `admin_users: ["admin"]`。
- 以 `alice`（普通用户）身份登录，验证界面无管理员功能按钮，API 拦截生效。
- 以 `admin`（管理员）身份登录，验证可成功配置仓库与触发构建。
