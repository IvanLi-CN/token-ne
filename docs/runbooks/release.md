# 产品版本发布 Runbook

本 Runbook 描述 target-only 手动发布。发布入口只接受一个 `target`，不接受版本文件、PR label 或兼容性分类作为版本来源。

## 前置条件

- `main` 上的 `ci` 检查全部成功，包括 `release-policy`。
- GitHub Actions 有 `contents: write` 和 `packages: write` 权限。
- 已确认当前没有其他 release workflow 在运行；workflow 使用 `concurrency: release` 串行化。
- GHCR 新 package 的默认可见性是 private；首次推送后必须在 GitHub package settings 中把 `token-ne-core` 与 `token-ne-api` 改为 public。控制面匿名拉取依赖 public，release workflow 会在创建 GitHub Release 前用匿名 `docker pull` 校验；未公开时发布会失败。
- 当前仓库没有 tag protection；如果以后添加 `v*` ruleset，必须允许 Actions 创建 tag。

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

## 首次发布

首次发布与其他发布多一个可见性步骤：

1. 正常 dispatch。镜像会先推送到 GHCR，此时两个 package 默认是 private。
2. `publish` job 的匿名 `docker pull` 会失败，workflow 在创建 GitHub Release 前停止。
3. 在 GitHub package settings 中分别把 `token-ne-core` 和 `token-ne-api` 的 visibility 改为 public。
4. 在原 workflow run 上选择 **Re-run all jobs**；同一 SHA/tag 会走 same-identity recovery，匿名拉取通过后才创建 GitHub Release。

## 失败恢复

- tag 已创建后失败：保留 tag，在原 workflow run 上选择 **Re-run all jobs**。不要在主分支前进后新建 dispatch；新的 dispatch 会解析新版本。
- 同一 SHA 已有唯一 `vX.Y.Z` tag：workflow 识别为 same-identity recovery，复用该版本；该 SHA 必须是当前 `origin/main` 的祖先。
- 恢复时已有镜像不会被重新构建或覆盖；workflow 校验 OCI `revision`/`version` 后复用原 digest。标签或 digest 与 source 不匹配时失败。
- 创建 GitHub Release 前会重新校验 tag 仍指向 source SHA，并校验匿名拉取到的 digest 与构建 digest 一致。
- GitHub Release 只在两个镜像都成功后创建；已存在时跳过重复创建。
- 不删除 tag、不移动 tag、不重新计算 successor、不生成下一个版本。

## 禁止事项

- 不使用 `latest`。
- 不使用 `<semver>-<sha>` 作为生产镜像标签。
- 不通过修改 `main` 或版本文件触发发布。
- 不把产品版本写入 `protocol_version`、`config_schema_version` 或数据库迁移版本。
- 发布失败后不要手工推送同名镜像覆盖 identity；先检查现有 tag、digest 和 OCI revision。
