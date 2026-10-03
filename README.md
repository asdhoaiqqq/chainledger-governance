# 链上数据治理与血缘工作台

## 用途

多源链上数据接入、规范化、质量规则、血缘谱系、结构演进、快照版本比较与可复验导出。

本仓库是可持续演进的自托管 Go 应用。领域核心位于 `chainledger/`，命令入口位于 `cmd/chainledger/`。

```bash
go run ./cmd/chainledger demo
go run ./cmd/chainledger version
go test ./...
```

命令行目前只提供固定的演示入口：`demo` 运行一段内置的登记演示并打印根数据集，`version`
打印版本号，`help` 打印用法。命令行**不接收数据集名称作为参数**，不能用它查询任意数据集
的下游影响。下游影响查询是 `chainledger` 包提供的 Go 库功能，需要在你自己的 Go 程序中调用
`chainledger.Impacts`，用法见下一节。

## 技术方向

blockchain-indexer, data-lineage, onchain-data, data-pipeline, data-indexer, analytics

## 运行要求

Go 1.26，仅使用标准库，全部行为可在本机 CPU 上离线复现。

## 查询下游影响（Go 库）

`chainledger.Impacts(graph, origin)` 返回从 `origin` 出发、沿血缘下游边可以到达的全部
派生数据集，用来判断某个数据集一旦变化会波及哪些派生数据集。血缘图就是普通的内存 map：

```go
graph := map[string]*chainledger.Lineage{}
```

用 `chainledger.Register` 登记数据集并声明直接上游（上游必须已登记，且不允许成环）；
`Register` 与 `Impacts` 的错误都必须处理。

下面的例子登记两条分支：`source -> a -> z` 和 `source -> b -> c`，两条分支汇合到同一个
派生数据集 `report`，`report` 再下游还有 `view`；另外登记一个与它们无关的 `isolated`。
注意我们故意先登记 b 分支、并在 `report` 的上游列表里把 `c` 写在 `z` 前面——这些顺序都
不会影响查询结果。

```go
package main

import (
	"fmt"
	"log"
	"strings"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

func mustRegister(graph map[string]*chainledger.Lineage, name string, parents ...string) {
	if err := chainledger.Register(graph, chainledger.Dataset{Name: name}, parents); err != nil {
		log.Fatalf("register %s: %v", name, err)
	}
}

func printImpacts(graph map[string]*chainledger.Lineage, origin string) {
	impacts, err := chainledger.Impacts(graph, origin)
	if err != nil {
		fmt.Printf("query %q failed: %v\n", origin, err)
		return
	}
	fmt.Printf("impacts of %s (%d):\n", origin, len(impacts))
	for _, im := range impacts {
		fmt.Printf("  - %s distance=%d path=%s\n",
			im.Dataset, im.Distance, strings.Join(im.Path, " -> "))
	}
}

func main() {
	// 1. 创建内存血缘图并登记数据集。
	graph := map[string]*chainledger.Lineage{}
	mustRegister(graph, "source")
	mustRegister(graph, "a", "source")
	mustRegister(graph, "b", "source")
	mustRegister(graph, "z", "a")
	mustRegister(graph, "c", "b")
	mustRegister(graph, "report", "c", "z") // 两条分支汇合到 report
	mustRegister(graph, "view", "report")  // report 的下游
	mustRegister(graph, "isolated")        // 与上面的图无关

	// 2. 查询 source 变化会影响哪些派生数据集。
	printImpacts(graph, "source")

	// 3. 替换 report 的直接上游：由 [c, z] 改为 [c]，再查一次。
	if err := chainledger.Register(graph,
		chainledger.Dataset{Name: "report"}, []string{"c"}); err != nil {
		log.Fatalf("re-register report: %v", err)
	}
	fmt.Println("after report is re-registered with upstream [c]:")
	printImpacts(graph, "source")

	// 4. 区分三种名称情况：空名称、不存在的名称、已登记但没有下游。
	if _, err := chainledger.Impacts(graph, ""); err != nil {
		fmt.Println("empty name ->", err)
	}
	if _, err := chainledger.Impacts(graph, "ghost"); err != nil {
		fmt.Println("unknown name ->", err)
	}
	impacts, err := chainledger.Impacts(graph, "isolated")
	fmt.Printf("registered but no downstream -> err=%v len=%d\n", err, len(impacts))

	// 5. 返回的路径是副本，调用者可以随意修改，不会影响图中的记录。
	impacts, _ = chainledger.Impacts(graph, "source")
	impacts[0].Path[0] = "tampered"
	again, _ := chainledger.Impacts(graph, "source")
	fmt.Println("path recorded in graph after caller mutation:", again[0].Path)
}
```

预期输出：

```text
impacts of source (6):
  - a distance=1 path=source -> a
  - b distance=1 path=source -> b
  - c distance=2 path=source -> b -> c
  - z distance=2 path=source -> a -> z
  - report distance=3 path=source -> a -> z -> report
  - view distance=4 path=source -> a -> z -> report -> view
after report is re-registered with upstream [c]:
impacts of source (6):
  - a distance=1 path=source -> a
  - b distance=1 path=source -> b
  - c distance=2 path=source -> b -> c
  - z distance=2 path=source -> a -> z
  - report distance=3 path=source -> b -> c -> report
  - view distance=4 path=source -> b -> c -> report -> view
empty name -> dataset name is required
unknown name -> dataset not found: ghost
registered but no downstream -> err=<nil> len=0
path recorded in graph after caller mutation: [source a]
```

### 结果含义与排序

- 每个结果给出名称（`Dataset`）、距离（`Distance`）和说明路径（`Path`）。距离是从起点到
  该数据集经过的依赖边数：直接下游距离为 1，每多一跳加 1。说明路径从起点写到该数据集，
  解释“它为什么会被影响”。
- 结果先按距离从小到大排序；距离相同再按数据集名称排序（Go 字符串顺序）。登记顺序和
  登记时填写的上游列表顺序都不决定结果顺序。
- 同一个数据集即使能经多条路径到达，也只出现一次。查询起点本身不会出现在结果里，与起点
  之间没有上下游通路的数据集（如上例的 `isolated`）也不会出现。

### 说明路径的选择规则

当一个数据集可以经多条路径到达时：

1. 先选**边数最少**的路径，距离优先于名称顺序；
2. 若存在多条同样短的路径，从起点开始**逐个比较整条路径上的名称**，取 Go 字符串顺序较小
   的一条，而不是只比较汇合点的直接上游名称。

上例特意让两种比较规则给出不同答案：到达 `report` 的两条等长路径是
`source -> a -> z -> report` 和 `source -> b -> c -> report`。如果只比较汇合点 `report`
的直接上游，会因为 `c < z` 而错误选择经过 c 的路线；按整条路径从起点逐名比较时，两条
路径第一次出现差异是在第一跳：`a < b`，因此正确选择经过 a、z 的路线。`view` 的说明路径
在这条路线上再延长一跳。这也说明：先登记哪条分支、上游列表把谁写在前面，都不影响选择。

### 替换上游后再次查询

用同名数据集再次调用 `Register` 是**替换**它的直接上游列表，而不是在上游列表后追加；
该数据集已有的下游会保留。反向边会同步更新：被移除的上游不再把它列为下游，新加入的上游
会补上这条边。之后的查询完全依据当前关系重新计算影响范围和说明路径。

上例把 `report` 的直接上游从 `[c, z]` 替换为 `[c]` 后，原来选中的
`source -> a -> z -> report` 这条路线不再成立，但 `source` 仍能经
`source -> b -> c -> report` 到达汇合数据集，所以 `report` 和 `view` **仍然在影响范围内**
（并没有因为少了一条依赖路线就退出），只是距离与说明路径按现存路线重新给出，`view` 的
路径改为 `source -> b -> c -> report -> view`。失去一条依赖路线不等于数据集已经退出
影响范围；只有当从起点出发的**最后一条**路径也被替换掉时，该数据集及其独占下游才会从
结果中消失。

### 查询失败与“成功但没有下游”

这三种情况要区分开：

- 传入空名称：返回错误 `dataset name is required`（缺少名称），不返回结果；
- 传入图中不存在的名称：返回错误并指出该名称，如 `dataset not found: ghost`，不返回
  部分结果；
- 名称已登记、但当前没有任何下游：查询成功（错误为 nil），返回一个长度为 0 的空列表。

### 查询是只读的

`Impacts` 只读取血缘图：无论查询成功还是失败，都不会新增、删除或修改任何数据集、依赖边
或内部顺序。返回的每条说明路径都是独立拷贝，调用者修改返回的切片或路径不会影响图中记录
（见示例最后一段：改写返回路径后重新查询，图中路径仍是原样）。
