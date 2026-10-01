# 链上数据治理与血缘工作台

## 用途

多源链上数据接入、规范化、质量规则、血缘谱系、结构演进、快照版本比较与可复验导出。

本仓库是可持续演进的自托管 Go 应用。领域核心位于 `chainledger/`，命令入口位于 `cmd/chainledger/`。

```bash
go run ./cmd/chainledger demo
go run ./cmd/chainledger version

# 批量血缘调整：先预览影响，再决定是否应用
go run ./cmd/chainledger lineage preview graph.json plan.json
go run ./cmd/chainledger lineage apply   graph.json plan.json

go test ./...
```

血缘文件均为 JSON，每条记录包含数据集名称及直接上游名称列表：

```json
{ "nodes": [ { "name": "B", "upstreams": ["A"] } ] }
```

```json
{ "adjustments": [ { "name": "A", "upstreams": ["B"] } ] }
```

`lineage preview` 输出影响报告且不改动图文件；`lineage apply` 使用相同的
判断与报告，成功后把最终图原子写回原图文件。整批调整可登记新数据集（同批
新数据集可相互引用），也可整体替换已有数据集的直接上游，合法性以全部替换
完成后的图为准；非法批次被整体拒绝并返回非零退出码，原图保持完整。

## 技术方向

blockchain-indexer, data-lineage, onchain-data, data-pipeline, data-indexer, analytics

## 运行要求

Go 1.26，仅使用标准库，全部行为可在本机 CPU 上离线复现。
