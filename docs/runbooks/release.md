# 产品版本发布 Runbook

本 Runbook 描述 target-only 手动发布。发布入口只接受一个 `target`，不接受版本文件、PR label 或兼容性分类作为版本来源。

## 前置条件

- `main` 上的 `ci` 检查全部成功，包括 `release-policy`。
- GitHub Actions 有 `contents: write` 和 `packages: write` 权限。
- `v*` tag protection 允许 release workflow 创建 tag。
- GHCR package 允许本仓库发布 `token-ne-core` 与 `token-ne-api`。
- 已确认当前没有其他 release workflow 在运行；workflow 使用 `concurrency: release` 串行化。

## 正常发布

1. 选择 target：
   - `patch`：修复版本
   - `minor`：向后兼容能力
   - `major`：破坏性变更
   - `X.Y.Z`：bootstrap 或明确版本
2. 从仓库根目录触发：

```bash
gh workflow run release.yml --ref main -f target=minor
```

3. 观察运行：

```bash
gh run list --workflow release.yml --limit 1
gh run watch --workflow release.yml
```

4. 验证结果：

```bash
version=0.1.0
gh release view "v${version}"
docker buildx imagetools inspect "ghcr.io/ivanli-cn/token-ne-core:${version}"
docker buildx imagetools inspect "ghcr.io/ivanli-cn/token-ne-api:${version}"
```

5. 部署后验证运行时版本：

```bash
curl -fsS https://$PUBLIC_DOMAIN/api/version
```

返回值中的 `version` 必须等于 release version，`commit` 必须是本次 merge/source SHA 的前缀。

## 版本解析

- 正常发布从当前 `main` 可达的最高 final `vX.Y.Z` tag 计算下一版本。
- 没有 final tag 时 baseline 为 `0.0.0`；首个 `minor` 得到 `0.1.0`。
- 精确 `X.Y.Z` 只能大于 baseline，或在当前 source 已有同 tag 时用于恢复。
- 已存在 tag 指向其他 SHA 时 fail closed，不移动 tag。
- workflow 会检查当前 SHA 的 `ci` 结果；未完成或失败的 SHA 不允许发布。

## 失败恢复

- tag 已创建后失败：保留 tag，重新运行 release workflow。
- 同一 SHA 已有唯一 `vX.Y.Z` tag：workflow 识别为 same-identity recovery，复用该版本。
- 镜像已存在：
  - `recovery=true` 时允许重试同一 identity。
  - 非恢复发布遇到已存在镜像时 fail closed，不覆盖。
- GitHub Release 只在两个镜像都成功后创建；已存在时跳过重复创建。
- 不删除 tag、不移动 tag、不重新计算 successor、不生成下一个版本。

## 禁止事项

- 不使用 `latest`。
- 不使用 `<semver>-<sha>` 作为生产镜像标签。
- 不通过修改 `main` 或版本文件触发发布。
- 不把产品版本写入 `protocol_version`、`config_schema_version` 或数据库迁移版本。
- 发布失败后不要手工推送同名镜像覆盖 identity；先检查现有 tag、digest 和 OCI revision。
