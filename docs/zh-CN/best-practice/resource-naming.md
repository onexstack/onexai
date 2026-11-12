# 资源命名决策指南

本文介绍在使用 `onexai` 创建一个新资源（如 CVM、Redis、MySQL、COS 对象存储、TKE 集群、PostgreSQL 等）时，如何决策资源的命名，并给出完整示例。

## 1. 什么是资源

在 `onexai` 中，「资源」（resource / kind）是 REST API 的核心数据实体，通过 `onexai create api` 命令创建。每创建一个资源，`onexai` 会为其生成六层代码：

```
proto（接口定义）→ handler（HTTP/RPC 处理）→ validation（校验）
→ store（数据访问）→ biz（业务逻辑）→ model（数据模型）
```

例如：

```bash
# 创建一个名为 post 的资源
onexai create api --kinds post --binary-name mb-apiserver

# 一次创建多个资源
onexai create api --kinds cvm,redis,mysql --binary-name mb-apiserver
```

资源是业务实体的抽象。你要为「CVM 云主机」「Redis 实例」「MySQL 数据库」这类对象建 API 时，首先要决策的就是它们的**资源类型名（kind）**，以及由此派生的整套命名。

## 2. 命名决策的核心：单一输入，自动派生

`onexai` 的命名决策遵循一条原则：

> **你只需要决策一个东西 —— 资源的 kind（蛇形命名），其余命名全部自动派生。**

你无需逐个操心 Go 类型名、复数形式、文件名、模型名、URL 路径等。`onexai` 根据 kind 自动推导出一套一致的命名，并贯穿六层代码。

因此，命名的关键决策点集中在「如何正确地写出 kind」，而不是「如何给每一处起名」。

## 3. 第一步：决策 kind（资源类型名）

kind 是资源的蛇形（snake_case）标识符，是命名的**唯一事实来源**。

### 3.1 规则

1. **一律小写**：`cvm`，不是 `CVM` 或 `Cvm`。
2. **多词用下划线分隔**：`tke_cluster`、`cron_job`，不是 `tkecluster` 或 `tkeCluster`。
3. **缩略词全小写**：CVM → `cvm`、COS → `cos`、TKE → `tke`、MySQL → `mysql`、PostgreSQL → `postgresql`。
4. **用名词单数**：kind 表示单个实体，用单数形式，复数由 `onexai` 自动派生。

### 3.2 反例与正例

| 想表达的资源 | ❌ 错误 kind | ✅ 正确 kind |
|---|---|---|
| CVM 云主机 | `CVM`、`cvmInstance`、`cvm-instance` | `cvm` |
| Redis 实例 | `Redis`、`redis_instance`、`redisInstance` | `redis` |
| MySQL 数据库 | `MySQL`、`mysqlDB`、`mysql_db` | `mysql` |
| COS 对象存储 | `COS`、`cosBucket`、`cos_bucket` | `cos` |
| TKE 集群 | `TKE`、`tkeCluster`、`TkeCluster` | `tke_cluster` |
| PostgreSQL 数据库 | `PostgreSQL`、`postgresDB`、`postgres_db` | `postgresql` |

## 4. 自动派生规则（kind → 全套命名）

给定一个 kind，`onexai` 会通过 `strutil.CamelCase` 与 `flect.Pluralize` 派生出以下命名变体（对应内部 `types.REST` 与 `types.RESTGen` 结构）。

### 4.1 REST 命名变体

以 `kind = cron_job` 为例：

| 派生字段 | 含义 | 示例（cron_job） | 用于 |
|---|---|---|---|
| `SingularName` | 大驼峰单数 | `CronJob` | Go 类型、方法名 |
| `PluralName` | 大驼峰复数 | `CronJobs` | 集合类型、URL 路径段 |
| `SingularLowerFirst` | 小驼峰单数 | `cronJob` | proto 字段名 |
| `PluralLowerFirst` | 小驼峰复数 | `cronJobs` | proto 集合字段名 |
| `SingularLower` | 全小写单数 | `cronjob` | 目录名、包名 |
| `PluralLower` | 全小写复数 | `cronjobs` | URL 路径段 |
| `GORMModel` | GORM 模型名 | `CronJobM` | model 层结构体 |
| `BusinessFactoryName` | biz 工厂名 | `CronJobv1` | biz 层工厂方法 |
| `FileName` | 生成文件名 | `cronjob.go` | 各层 Go 文件 |
| `ResourcePathPrefix` | 资源路径前缀 | （可选） | 嵌套资源分组 |

> **嵌套资源**：kind 支持路径形式，例如 `jobv1/cron_job`。此时 `cron_job` 为资源名，`jobv1` 成为 `ResourcePathPrefix`，用于目录嵌套与 URL 分组（如 `/api/v1/jobv1/cronjobs`）。

> **列表方法命名**：`List` 方法使用 `SingularName`，即 `ListCronJob`，不要写成 `ListCronJobs`；URL 路径段仍使用 `PluralLower`。

### 4.2 组件与模块命名

资源之外的命名由项目创建时决策，与 kind 解耦：

| 命名项 | 规则 | 示例 |
|---|---|---|
| `binary-name` | `前缀-组件名`，组件名从最后一个 `-` 后提取 | `mb-apiserver` → 组件名 `apiserver` |
| `module-path` | Go 模块导入路径 | `github.com/myorg/myproject` |
| `project-name` | 项目名，用于环境变量前缀 | `miniblog` |
| 环境变量前缀 | `PROJECT_COMPONENT` 大写 | `MINIBLOG_APISERVER` |

### 4.3 资源 ID（RID）

生成的项目中，`internal/pkg/rid/rid.go` 定义资源 ID 前缀，用于生成全局唯一的资源 ID（形如 `user-xxxxxx`）。RID 前缀通常取资源的 `SingularLower`：

```go
type ResourceID string

const (
    UserID ResourceID = "user" // 前缀为 "user"，ID 形如 user-a1b2c3
)
```

## 5. 两条命名路径：`--kinds` vs `--openapi-spec`

这是命名决策中最重要的一个分叉点。

### 5.1 路径 A：`--kinds`（自动派生）

```bash
onexai create api --kinds cvm,redis,mysql --binary-name mb-apiserver
```

`onexai` 用 `flect.Pluralize` 自动推导复数、`strutil.CamelCase` 自动推导大小写。

**适用场景**：常规英文单词资源（`post`、`user`、`order`），自动结果准确。

**局限**：对缩略词、品牌词、不可数名词，自动推导可能不符合预期（详见第 7 节）。

### 5.2 路径 B：`--openapi-spec`（显式命名）

```bash
onexai create api --openapi-spec ./openapi.yaml --binary-name mb-apiserver
```

从 OpenAPI 3.0/3.1 规范中读取资源，`name`（单数）、`pluralName`（复数）、`lower`（小驼峰）等命名由你在规范中**显式声明**，完全绕开 `flect` 的自动推导。

**适用场景**：需要精确控制命名（如 `CVM`、`MySQL`、`PostgreSQL`）的资源。

> 决策建议：普通资源用 `--kinds` 快速起步；命名敏感的资源（尤其是缩略词、品牌词）优先用 `--openapi-spec` 显式声明，确保 `CVM`/`MySQL`/`PostgreSQL` 这类大小写准确。

## 6. 完整示例

以下示例均为 `--kinds` 路径下 `onexai` 的实际派生结果（`APIVersion = v1`）。

### 示例 1：CVM 云主机

```bash
onexai create api --kinds cvm --binary-name mb-apiserver
```

| 派生字段 | 值 |
|---|---|
| SingularName | `Cvm` |
| PluralName | `Cvms` |
| SingularLower | `cvm` |
| GORMModel | `CvmM` |
| FileName | `cvm.go` |
| BusinessFactoryName | `Cvmv1` |

### 示例 2：Redis 实例

```bash
onexai create api --kinds redis --binary-name mb-apiserver
```

| 派生字段 | 值 |
|---|---|
| SingularName | `Redis` |
| PluralName | `Redis` |
| SingularLower | `redis` |
| GORMModel | `RedisM` |
| FileName | `redis.go` |
| BusinessFactoryName | `Redisv1` |

### 示例 3：MySQL 数据库

```bash
onexai create api --kinds mysql --binary-name mb-apiserver
```

| 派生字段 | 值 |
|---|---|
| SingularName | `Mysql` |
| PluralName | `Mysqls` |
| SingularLower | `mysql` |
| GORMModel | `MysqlM` |
| FileName | `mysql.go` |
| BusinessFactoryName | `Mysqlv1` |

### 示例 4：COS 对象存储

```bash
onexai create api --kinds cos --binary-name mb-apiserver
```

| 派生字段 | 值 |
|---|---|
| SingularName | `Cos` |
| PluralName | `Cos` |
| SingularLower | `cos` |
| GORMModel | `CosM` |
| FileName | `cos.go` |
| BusinessFactoryName | `Cosv1` |

### 示例 5：TKE 集群

```bash
onexai create api --kinds tke_cluster --binary-name mb-apiserver
```

| 派生字段 | 值 |
|---|---|
| SingularName | `TkeCluster` |
| PluralName | `TkeClusters` |
| SingularLower | `tkecluster` |
| GORMModel | `TkeClusterM` |
| FileName | `tkecluster.go` |
| BusinessFactoryName | `TkeClusterv1` |

### 示例 6：PostgreSQL 数据库

```bash
onexai create api --kinds postgresql --binary-name mb-apiserver
```

| 派生字段 | 值 |
|---|---|
| SingularName | `Postgresql` |
| PluralName | `Postgresqls` |
| SingularLower | `postgresql` |
| GORMModel | `PostgresqlM` |
| FileName | `postgresql.go` |
| BusinessFactoryName | `Postgresqlv1` |

## 7. flect 复数化的已知局限与应对

`onexai` 当前使用 `github.com/gobuffalo/flect` 做复数化。它按「普通英文单词」规则处理，对缩略词、品牌词、不可数名词存在以下偏差：

| kind | 自动结果 | 问题 |
|---|---|---|
| `cvm` | `Cvm` / `Cvms` | 期望 `CVM`（全大写缩写），复数 `Cvms` 不自然 |
| `mysql` | `Mysql` / `Mysqls` | 期望 `MySQL`（品牌词大小写），`Mysqls` 复数错误 |
| `postgresql` | `Postgresql` / `Postgresqls` | 期望 `PostgreSQL`，`Postgresqls` 复数错误 |
| `redis` | `Redis` / `Redis` | 不可数名词，恰好保持单数 ✅ |
| `cos` | `Cos` / `Cos` | 缩略词，恰好保持 ✅ |
| `tke_cluster` | `TkeCluster` / `TkeClusters` | 期望 `TKECluster`（缩写全大写） |

### 应对策略

1. **对大小写敏感的资源（CVM、MySQL、PostgreSQL）**：使用 `--openapi-spec` 显式声明 `name: CVM`、`name: MySQL`、`name: PostgreSQL`，绕开自动推导。
2. **对复数不自然的资源**：在 OpenAPI 规范中显式指定 `pluralName`。
3. **接受自动结果**：若团队约定不强求缩写全大写，可接受 `Cvm`、`Mysql` 等结果，保持全仓命名一致即可。

> 核心原则：**宁可选择一个不完美但一致的命名，也不要在一个仓库里混用 `Cvm` 与 `CVM`。**

## 8. 命名决策清单

创建新资源前，按以下顺序决策：

1. **确定资源的业务实体**：这个 API 管的是「单个什么」？（一台云主机、一个数据库实例、一个集群……）
2. **写出 kind**：全小写 snake_case 单数（`cvm`、`redis`、`mysql`、`cos`、`tke_cluster`、`postgresql`）。
3. **判断命名敏感度**：
   - 普通单词 → 用 `--kinds` 自动派生。
   - 缩略词/品牌词/不可数名词 → 用 `--openapi-spec` 显式声明 `name`/`pluralName`。
4. **确认派生结果**：核对 `SingularName`、`PluralName`、`GORMModel`、`FileName` 是否符合预期。
5. **保持全局一致**：同类资源（如所有云产品）采用同一套命名风格，不混用大小写。

---

相关命令参考：

- `onexai create api --kinds <kind> --binary-name <binary>` —— 用 kind 创建资源
- `onexai create api --openapi-spec <file> --binary-name <binary>` —— 从 OpenAPI 规范创建资源
- `onexai create api --help` —— 查看完整参数说明
