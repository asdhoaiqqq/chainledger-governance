# 链上数据治理与血缘工作台

## 用途

多源链上数据接入、规范化、质量规则、血缘谱系、结构演进、快照版本比较与可复验导出。

本仓库是可持续演进的自托管 Go 应用。领域核心位于 `chainledger/`，命令入口位于 `cmd/chainledger/`。

```bash
go run ./cmd/chainledger demo
go run ./cmd/chainledger version
go test ./...
```

命令行只提供 `demo`、`version`、`help` 三个固定入口：`demo` 运行一段内置的登记与血缘演示，`version` 打印版本号。命令行**不**接收数据集名称作为查询参数。要判断“某个数据集变化会影响哪些派生数据集”，请直接在 Go 代码中调用 `chainledger` 库的 `Impacts` 函数，方式见下一节；完整可运行示例位于 [`examples/impacts`](examples/impacts/main.go)。

## 查询下游影响（Go 库）

`chainledger.Impacts(graph, origin)` 返回所有直接或间接依赖 `origin` 的数据集，即沿“下游”方向从 `origin` 出发可以到达的全部派生数据集。血缘图本身就是一个普通的内存 map，由 `chainledger.Register` 登记填充。

每个结果是一条 `chainledger.Impact`：

- `Dataset`：受影响的下游数据集名称。
- `Distance`：从起点到该数据集的**最短依赖边数**。直接下游距离为 1。
- `Path`：这条最短路径上的名称序列，从起点写到该数据集，逐跳说明影响是如何传过去的。

结果先按距离升序排列，距离相同再按数据集名称（Go 字符串顺序）排列；登记顺序和登记时给出的上游列表顺序都不影响结果。

下面的示例建立两条汇合到同一派生数据集 `report` 的分支，以及 `report` 的下游 `view`，另外登记一个与起点无关的 `isolated`：

```
source ──> a ──> z ──┐
  │                  ├──> report ──> view
  └──> b ──> c ──────┘
```

```go
package main

import (
	"fmt"
	"os"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

func main() {
	// 血缘图就是一个普通的内存 map，由 Register 填充。
	graph := map[string]*chainledger.Lineage{}

	register := func(name string, parents ...string) {
		if err := chainledger.Register(graph, chainledger.Dataset{Name: name}, parents); err != nil {
			fmt.Fprintf(os.Stderr, "register %s: %v\n", name, err)
			os.Exit(1)
		}
	}

	// 故意先登记 b 侧分支，并在 report 的上游列表中把 c 写在 z 前面。
	register("source")
	register("b", "source")
	register("a", "source")
	register("c", "b")
	register("z", "a")
	register("report", "c", "z")
	register("view", "report")
	register("isolated")

	impacts, err := chainledger.Impacts(graph, "source")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Impacts: %v\n", err)
		os.Exit(1)
	}
	for _, im := range impacts {
		fmt.Printf("%-8s distance=%d path=%v\n", im.Dataset, im.Distance, im.Path)
	}
}
```

预期输出：

```text
a        distance=1 path=[source a]
b        distance=1 path=[source b]
c        distance=2 path=[source b c]
z        distance=2 path=[source a z]
report   distance=3 path=[source a z report]
view     distance=4 path=[source a z report view]
```

要点：

- `report` 同时经过 `a -> z` 和 `b -> c` 两条路径到达，但**只出现一次**，距离取最短边数，路径取其中一条最短路径。
- 查询起点 `source` 自己不会出现在结果里；与起点没有血缘关系的 `isolated` 也不会出现。

### 说明路径如何选择

选择规则分两层：

1. **先选边数最少的路径**。只要存在更短路径，词典序再小的长路径也不会被选中。
2. **若有多条同样短的路径，从起点开始逐跳比较整条路径上的名称**，取 Go 字符串顺序较小的一条；前缀完全相同时，较短的路径更小。

注意比较的是**整条路径**，不能只比较汇合点的直接上游。上例中 `report` 的两条最短路径长度相同：

- `[source, a, z, report]`
- `[source, b, c, report]`

如果只看汇合点的直接上游，会因为 `c < z` 而错误地选择 `b -> c` 路线。实际规则从起点开始逐跳比较：两条路径第一个不同的名称是第 1 跳的 `a` 与 `b`，因为 `a < b`，选中的是 `[source, a, z, report]`。`view` 的说明路径在这条路线上再延伸一跳。

正因为比较基于名称而不是登记历史，示例中“先登记 b 侧分支”和“`report` 的上游列表把 `c` 写在 `z` 前面”都不会改变结果；无论按什么顺序登记、上游列表按什么顺序书写，只要最终关系相同，查询结果就相同。

### 替换上游后再次查询

用同名再次调用 `Register` 是**替换**该数据集的直接上游列表，而不是追加；它已有的下游会保留，反向边同步更新。再次查询时，影响范围与说明路径都按当前关系重新计算。

接上例，把 `report` 的直接上游替换为只剩 `c`：

```go
if err := chainledger.Register(graph, chainledger.Dataset{Name: "report"}, []string{"c"}); err != nil {
	fmt.Fprintf(os.Stderr, "register report: %v\n", err)
	os.Exit(1)
}
impacts, err = chainledger.Impacts(graph, "source")
// 错误处理同上，略
```

原先选中的 `source -> a -> z -> report` 这条路线不再成立（`z` 不再是 `report` 的直接上游），但 `source -> b -> c -> report` 这条路线仍然能到达汇合点，所以 `report` 并没有退出影响范围，`view` 也仍然在其中；只是说明路径换成现存的路线：

```text
a        distance=1 path=[source a]
b        distance=1 path=[source b]
c        distance=2 path=[source b c]
z        distance=2 path=[source a z]
report   distance=3 path=[source b c report]
view     distance=4 path=[source b c report view]
```

失去一条依赖路线不等于数据集退出影响范围；只有当从起点出发的**所有**路线都被移除时，该数据集及其仅经此可达的下游才会从结果中一起消失。

### 查询失败与“成功但没有下游”

这三种情况要区分开：

```go
// 空名称：报错，提示缺少名称，返回 nil 结果。
if _, err := chainledger.Impacts(graph, ""); err != nil {
	fmt.Println(err) // dataset name is required
}

// 名称不存在：报错，错误信息中指出该名称，返回 nil 结果。
if _, err := chainledger.Impacts(graph, "ghost"); err != nil {
	fmt.Println(err) // dataset not found: ghost
}

// 已登记但没有下游的数据集：查询成功，返回空列表（非 nil）。
impacts, err := chainledger.Impacts(graph, "isolated")
// err == nil，len(impacts) == 0
fmt.Printf("downstream=%d err=%v\n", len(impacts), err) // downstream=0 err=<nil>
```

名称按登记值精确匹配（区分大小写）；对空图或 nil 图查询任何名称都按“不存在”处理。

### 查询是只读的

`Impacts` 不会修改血缘关系：成功或失败的查询都不改变任何节点、边或内部列表顺序。返回的每个 `Path` 都是独立拷贝，调用方可以随意修改返回的切片，不会影响图中记录，也不会影响之后的查询。反过来，图被新的 `Register` 调用改写后，需要再次调用 `Impacts` 才能得到与当前关系一致的结果。

## 技术方向

blockchain-indexer, data-lineage, onchain-data, data-pipeline, data-indexer, analytics

## 运行要求

Go 1.26，仅使用标准库，全部行为可在本机 CPU 上离线复现。
