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

## 登记数据集与维护上游（Go 库）

`chainledger.Register(graph, dataset, parents)` 把数据集登记进血缘图；对**已登记**的同名数据集再次调用，会用 `parents` **整体替换**它的直接上游列表（不是追加），它已有的下游全部保留，反向边同步更新。整个请求先校验后落库：只要校验失败，图中任何节点、边和顺序都不会改变，原来的血缘关系继续有效。

参数的作用范围要分清：

- 血缘只由 `dataset.Name` 和单独传入的 `parents` 列表维护。`Dataset.Upstreams` 字段不会被 `Register` 读取，不能用它替代 `parents`。
- `Dataset.SchemaVersion` 和 `Dataset.Rows` 不会被保存为可查询的元数据；当前版本没有实现结构版本或行数管理，登记时填不填都不影响血缘行为。

登记规则：

- 上游必须**先登记**。`parents` 里出现未登记的名称会被拒绝，错误信息指出该名称（`unknown parent <名称>`），不会自动创建节点。
- 数据集不能作为自己的上游；新边与现有血缘形成循环也会被拒绝，错误信息指出经由哪个上游成环。
- `parents` 列表里有多个问题时，按输入顺序报告先遇到的那一个；被拒绝的请求不留下任何部分关系——列表中排在前面的、本身合法的上游也不会生效。
- 多个上游共享祖先**不**算循环（例如 `b`、`c` 都依赖 `source` 时，`report` 可以同时依赖 `b` 和 `c`）；已经登记过的上游也可以继续保留在列表里。
- 传入空 `parents`（`nil` 或空切片）会解除该数据集的全部上游，但数据集本身和它的下游都保留。

下面的完整示例（[`examples/register`](examples/register/main.go)，可用 `go run ./examples/register` 独立运行）建立 `raw -> detail -> summary` 的血缘链和独立来源 `other`，先故意发起一个会成环的替换请求，再修正为合法请求，并用 `Impacts` 查询验证每一步的实际血缘：

```go
package main

import (
	"fmt"
	"os"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

func main() {
	graph := map[string]*chainledger.Lineage{}

	mustRegister := func(name string, parents ...string) {
		if err := chainledger.Register(graph, chainledger.Dataset{Name: name}, parents); err != nil {
			fmt.Fprintf(os.Stderr, "register %s: %v\n", name, err)
			os.Exit(1)
		}
		fmt.Printf("registered %s parents=%v\n", name, parents)
	}
	mustRegister("raw")
	mustRegister("detail", "raw")
	mustRegister("summary", "detail")
	mustRegister("other")

	report := func(origin string) {
		impacts, err := chainledger.Impacts(graph, origin)
		if err != nil {
			fmt.Printf("Impacts(%q) error: %v\n", origin, err)
			return
		}
		fmt.Printf("Impacts(%q) -> %d downstream dataset(s):\n", origin, len(impacts))
		for _, im := range impacts {
			fmt.Printf("  %-8s distance=%d path=%v\n", im.Dataset, im.Distance, im.Path)
		}
	}

	// other 已登记且本身合法，但 summary 是 detail 自己的下游，
	// 新边 detail -> summary 会成环，整个请求被拒绝。
	fmt.Println("\n-- re-register detail with upstreams [other summary] --")
	if err := chainledger.Register(graph, chainledger.Dataset{Name: "detail"}, []string{"other", "summary"}); err != nil {
		fmt.Printf("register detail refused: %v\n", err)
	}
	fmt.Println("after the refusal, nothing changed:")
	report("raw")   // detail 和 summary 仍依赖 raw
	report("other") // other 没有因被拒绝的请求获得任何下游

	// 修正为只依赖 other：同名 Register 整体替换直接上游列表，
	// raw 被移除；detail 自己的下游 summary 保留。
	fmt.Println("\n-- re-register detail with upstreams [other] --")
	mustRegister("detail", "other")
	report("raw")   // raw 不再影响任何数据集
	report("other") // other 现在能影响 detail，并经由它影响 summary
}
```

预期输出：

```text
registered raw parents=[]
registered detail parents=[raw]
registered summary parents=[detail]
registered other parents=[]

-- re-register detail with upstreams [other summary] --
register detail refused: cycle through summary for dataset detail
after the refusal, nothing changed:
Impacts("raw") -> 2 downstream dataset(s):
  detail   distance=1 path=[raw detail]
  summary  distance=2 path=[raw detail summary]
Impacts("other") -> 0 downstream dataset(s):

-- re-register detail with upstreams [other] --
registered detail parents=[other]
Impacts("raw") -> 0 downstream dataset(s):
Impacts("other") -> 2 downstream dataset(s):
  detail   distance=1 path=[other detail]
  summary  distance=2 path=[other detail summary]
```

要点：

- 被拒绝的请求不会部分生效：`other` 虽然已登记且本身合法，但整个请求被拒绝后 `detail` 仍依赖 `raw`，`summary` 仍依赖 `detail`，`other` 也没有获得 `detail` 这个下游。
- 修正后的同名 `Register` 用 `[other]` 替换了 `detail` 的整个直接上游列表，而不是把 `other` 追加到 `raw` 后面：`raw` 不再影响 `detail` 和 `summary`，`other` 能影响二者；`detail` 原有的下游 `summary` 保留，所以 `other` 的影响沿 `other -> detail -> summary` 传递。查询方向、距离含义和结果顺序与下一节描述的公开行为一致。

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
