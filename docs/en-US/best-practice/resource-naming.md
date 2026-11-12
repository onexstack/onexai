# Resource naming decision guide

This document explains how to decide resource naming when using `onexai` to create a new resource (such as CVM, Redis, MySQL, COS object storage, TKE cluster, PostgreSQL, etc.), with complete examples.

## 1. What is a resource

In `onexai`, a "resource" (resource / kind) is the core data entity of the REST API, created with the `onexai create api` command. For each resource created, `onexai` generates six layers of code:

```
proto（接口定义）→ handler（HTTP/RPC 处理）→ validation（校验）
→ store（数据访问）→ biz（业务逻辑）→ model（数据模型）
```

For example:

```bash
# 创建一个名为 post 的资源
onexai create api --kinds post --binary-name mb-apiserver

# 一次创建多个资源
onexai create api --kinds cvm,redis,mysql --binary-name mb-apiserver
```

A resource is an abstraction of a business entity. When you need to build an API for objects like "CVM cloud host", "Redis instance", or "MySQL database", the first decision is their **resource type name (kind)** and the entire set of names derived from it.

## 2. The core of naming decisions: a single input, automatic derivation

`onexai`'s naming decisions follow one principle:

> **You only need to decide one thing — the resource's kind (snake_case). All other names are derived automatically.**

You do not need to worry about Go type names, plurals, file names, model names, URL paths, etc., one by one. `onexai` derives a consistent set of names from the kind, spanning all six layers of code.

Therefore, the key decision point for naming is concentrated on "how to write the kind correctly", not "how to name every place".

## 3. Step one: decide the kind (resource type name)

The kind is the resource's snake_case identifier and the **single source of truth** for naming.

### 3.1 Rules

1. **Always lowercase**: `cvm`, not `CVM` or `Cvm`.
2. **Separate multiple words with underscores**: `tke_cluster`, `cron_job`, not `tkecluster` or `tkeCluster`.
3. **Acronyms all lowercase**: CVM → `cvm`, COS → `cos`, TKE → `tke`, MySQL → `mysql`, PostgreSQL → `postgresql`.
4. **Use singular nouns**: a kind represents a single entity, so use the singular form; the plural is derived automatically by `onexai`.

### 3.2 Counterexamples and correct examples

| Resource you want to express | ❌ Wrong kind | ✅ Correct kind |
|---|---|---|
| CVM cloud host | `CVM`, `cvmInstance`, `cvm-instance` | `cvm` |
| Redis instance | `Redis`, `redis_instance`, `redisInstance` | `redis` |
| MySQL database | `MySQL`, `mysqlDB`, `mysql_db` | `mysql` |
| COS object storage | `COS`, `cosBucket`, `cos_bucket` | `cos` |
| TKE cluster | `TKE`, `tkeCluster`, `TkeCluster` | `tke_cluster` |
| PostgreSQL database | `PostgreSQL`, `postgresDB`, `postgres_db` | `postgresql` |

## 4. Automatic derivation rules (kind → the full set of names)

Given a kind, `onexai` derives the following naming variants through `strutil.CamelCase` and `flect.Pluralize` (corresponding to the internal `types.REST` and `types.RESTGen` structures).

### 4.1 REST naming variants

Taking `kind = cron_job` as an example:

| Derived field | Meaning | Example (cron_job) | Used for |
|---|---|---|---|
| `SingularName` | UpperCamel singular | `CronJob` | Go types, method names |
| `PluralName` | UpperCamel plural | `CronJobs` | Collection types, URL path segments |
| `SingularLowerFirst` | lowerCamel singular | `cronJob` | proto field names |
| `PluralLowerFirst` | lowerCamel plural | `cronJobs` | proto collection field names |
| `SingularLower` | all-lowercase singular | `cronjob` | Directory names, package names |
| `PluralLower` | all-lowercase plural | `cronjobs` | URL path segments |
| `GORMModel` | GORM model name | `CronJobM` | model-layer struct |
| `BusinessFactoryName` | biz factory name | `CronJobv1` | biz-layer factory method |
| `FileName` | Generated file name | `cronjob.go` | Go files in each layer |
| `ResourcePathPrefix` | Resource path prefix | (optional) | Nested resource grouping |

> **Nested resources**: a kind supports a path form, e.g. `jobv1/cron_job`. Then `cron_job` is the resource name, and `jobv1` becomes the `ResourcePathPrefix`, used for directory nesting and URL grouping (e.g. `/api/v1/jobv1/cronjobs`).

> **List method naming**: `List` methods use `SingularName`, e.g. `ListCronJob`, never `ListCronJobs`; URL path segments keep using `PluralLower`.

### 4.2 Component and module naming

Names outside the resource are decided at project creation and decoupled from the kind:

| Naming item | Rule | Example |
|---|---|---|
| `binary-name` | `prefix-componentname`, component name extracted after the last `-` | `mb-apiserver` → component name `apiserver` |
| `module-path` | Go module import path | `github.com/myorg/myproject` |
| `project-name` | Project name, used for the environment variable prefix | `miniblog` |
| Environment variable prefix | `PROJECT_COMPONENT` uppercase | `MINIBLOG_APISERVER` |

### 4.3 Resource ID (RID)

In a generated project, `internal/pkg/rid/rid.go` defines resource ID prefixes, used to generate globally unique resource IDs (of the form `user-xxxxxx`). The RID prefix is usually the resource's `SingularLower`:

```go
type ResourceID string

const (
    UserID ResourceID = "user" // 前缀为 "user"，ID 形如 user-a1b2c3
)
```

## 5. Two naming paths: `--kinds` vs `--openapi-spec`

This is the most important fork in naming decisions.

### 5.1 Path A: `--kinds` (automatic derivation)

```bash
onexai create api --kinds cvm,redis,mysql --binary-name mb-apiserver
```

`onexai` uses `flect.Pluralize` to derive plurals automatically and `strutil.CamelCase` to derive casing automatically.

**Applicable scenarios**: resources with ordinary English words (`post`, `user`, `order`), where automatic results are accurate.

**Limitations**: for acronyms, brand words, and uncountable nouns, automatic derivation may not match expectations (see §7 for details).

### 5.2 Path B: `--openapi-spec` (explicit naming)

```bash
onexai create api --openapi-spec ./openapi.yaml --binary-name mb-apiserver
```

Resources are read from an OpenAPI 3.0/3.1 specification; names such as `name` (singular), `pluralName` (plural), and `lower` (lowerCamel) are **explicitly declared** by you in the spec, completely bypassing `flect`'s automatic derivation.

**Applicable scenarios**: resources requiring precise naming control (such as `CVM`, `MySQL`, `PostgreSQL`).

> Decision advice: use `--kinds` to get started quickly for ordinary resources; for naming-sensitive resources (especially acronyms and brand words), prefer `--openapi-spec` with explicit declarations to ensure accurate casing for `CVM`/`MySQL`/`PostgreSQL`.

## 6. Complete examples

The following examples are the actual derivation results of `onexai` under the `--kinds` path (`APIVersion = v1`).

### Example 1: CVM cloud host

```bash
onexai create api --kinds cvm --binary-name mb-apiserver
```

| Derived field | Value |
|---|---|
| SingularName | `Cvm` |
| PluralName | `Cvms` |
| SingularLower | `cvm` |
| GORMModel | `CvmM` |
| FileName | `cvm.go` |
| BusinessFactoryName | `Cvmv1` |

### Example 2: Redis instance

```bash
onexai create api --kinds redis --binary-name mb-apiserver
```

| Derived field | Value |
|---|---|
| SingularName | `Redis` |
| PluralName | `Redis` |
| SingularLower | `redis` |
| GORMModel | `RedisM` |
| FileName | `redis.go` |
| BusinessFactoryName | `Redisv1` |

### Example 3: MySQL database

```bash
onexai create api --kinds mysql --binary-name mb-apiserver
```

| Derived field | Value |
|---|---|
| SingularName | `Mysql` |
| PluralName | `Mysqls` |
| SingularLower | `mysql` |
| GORMModel | `MysqlM` |
| FileName | `mysql.go` |
| BusinessFactoryName | `Mysqlv1` |

### Example 4: COS object storage

```bash
onexai create api --kinds cos --binary-name mb-apiserver
```

| Derived field | Value |
|---|---|
| SingularName | `Cos` |
| PluralName | `Cos` |
| SingularLower | `cos` |
| GORMModel | `CosM` |
| FileName | `cos.go` |
| BusinessFactoryName | `Cosv1` |

### Example 5: TKE cluster

```bash
onexai create api --kinds tke_cluster --binary-name mb-apiserver
```

| Derived field | Value |
|---|---|
| SingularName | `TkeCluster` |
| PluralName | `TkeClusters` |
| SingularLower | `tkecluster` |
| GORMModel | `TkeClusterM` |
| FileName | `tkecluster.go` |
| BusinessFactoryName | `TkeClusterv1` |

### Example 6: PostgreSQL database

```bash
onexai create api --kinds postgresql --binary-name mb-apiserver
```

| Derived field | Value |
|---|---|
| SingularName | `Postgresql` |
| PluralName | `Postgresqls` |
| SingularLower | `postgresql` |
| GORMModel | `PostgresqlM` |
| FileName | `postgresql.go` |
| BusinessFactoryName | `Postgresqlv1` |

## 7. Known limitations of flect pluralization and how to handle them

`onexai` currently uses `github.com/gobuffalo/flect` for pluralization. It applies "ordinary English word" rules, with the following deviations for acronyms, brand words, and uncountable nouns:

| kind | Automatic result | Problem |
|---|---|---|
| `cvm` | `Cvm` / `Cvms` | Expects `CVM` (all-caps acronym); the plural `Cvms` is unnatural |
| `mysql` | `Mysql` / `Mysqls` | Expects `MySQL` (brand word casing); the plural `Mysqls` is wrong |
| `postgresql` | `Postgresql` / `Postgresqls` | Expects `PostgreSQL`; the plural `Postgresqls` is wrong |
| `redis` | `Redis` / `Redis` | Uncountable noun, happens to stay singular ✅ |
| `cos` | `Cos` / `Cos` | Acronym, happens to stay ✅ |
| `tke_cluster` | `TkeCluster` / `TkeClusters` | Expects `TKECluster` (acronym all-caps) |

### Handling strategies

1. **For case-sensitive resources (CVM, MySQL, PostgreSQL)**: use `--openapi-spec` to explicitly declare `name: CVM`, `name: MySQL`, `name: PostgreSQL`, bypassing automatic derivation.
2. **For resources with unnatural plurals**: specify `pluralName` explicitly in the OpenAPI spec.
3. **Accept the automatic result**: if the team convention does not require acronyms to be all-caps, accept results like `Cvm` and `Mysql`, as long as naming is consistent across the repo.

> Core principle: **Better to choose an imperfect but consistent naming than to mix `Cvm` and `CVM` in one repository.**

## 8. Naming decision checklist

Before creating a new resource, decide in the following order:

1. **Determine the resource's business entity**: what single thing does this API manage? (a cloud host, a database instance, a cluster…)
2. **Write the kind**: all-lowercase snake_case singular (`cvm`, `redis`, `mysql`, `cos`, `tke_cluster`, `postgresql`).
3. **Judge naming sensitivity**:
   - Ordinary words → use `--kinds` for automatic derivation.
   - Acronyms/brand words/uncountable nouns → use `--openapi-spec` to explicitly declare `name`/`pluralName`.
4. **Confirm the derived results**: check whether `SingularName`, `PluralName`, `GORMModel`, `FileName` match expectations.
5. **Keep global consistency**: resources of the same class (e.g. all cloud products) use the same naming style, without mixing casing.

---

Related command reference:

- `onexai create api --kinds <kind> --binary-name <binary>` —— create resources by kind
- `onexai create api --openapi-spec <file> --binary-name <binary>` —— create resources from an OpenAPI spec
- `onexai create api --help` —— view the complete parameter documentation
