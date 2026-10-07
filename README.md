# 链上数据治理与血缘工作台

## 用途

多源链上数据接入、规范化、质量规则、血缘谱系、结构演进、快照版本比较与可复验导出。

本仓库是可持续演进的自托管 Go 应用。领域核心位于 `chainledger/`，命令入口位于 `cmd/chainledger/`。

```bash
go run ./cmd/chainledger demo
go run ./cmd/chainledger version
go test ./...
```

命令行只提供 `demo`、`version`、`help` 三个固定入口：`demo` 运行一段内置的登记与血缘演示，`version` 打印版本号。命令行**不**接收数据集名称作为查询参数。要判断“某个数据集变化会影响哪些派生数据集”，请直接在 Go 代码中调用 `chainledger` 库的 `Impacts` 函数，方式见下文「查询下游影响（Go 库）」一节；完整可运行示例位于 [`examples/impacts`](examples/impacts/main.go)。分析变化时若想把某些已登记数据集设为传播截止点、查看停止传播后的影响范围，调用同一库的 `ImpactsWithCutoffs` 函数，见下文「带传播截止名单的下游影响（Go 库）」一节，示例位于 [`examples/impacts-cutoffs`](examples/impacts-cutoffs/main.go)。反过来，要追查“一份派生数据来自哪些上游”，调用同一库的 `Upstreams` 函数，见下文「查询上游来源（Go 库）」一节，示例位于 [`examples/upstreams`](examples/upstreams/main.go)。要比较两份已登记派生数据**最近共同追到哪些上游来源**（共同来源到两个目标各自的距离与说明路径），调用同一库的 `CommonUpstreams` 函数，见下文「查询两份派生数据的最近共同上游（Go 库）」一节，示例位于 [`examples/common-upstreams`](examples/common-upstreams/main.go)。要把某个数据集**实际依赖的完整上游血缘**（全部分支、全部现存直接依赖）导出为一份 JSON 文档供其他程序读取，调用同一库的 `ExportUpstreamLineage` 函数，见下文「导出完整上游血缘（Go 库）」一节，示例位于 [`examples/export-upstreams`](examples/export-upstreams/main.go)。只想导出**某一个已登记来源怎样参与某一个已登记目标的派生**（只含该来源到该目标现存路线上的节点和直接依赖），调用同一库的 `ExportSourceTargetLineage` 函数，见下文「限定来源的上游血缘导出（Go 库）」一节，示例位于 [`examples/export-source-target`](examples/export-source-target/main.go)。要把这样一份血缘 JSON 文本重新交回库、成功时得到一张**独立的新血缘图**并可直接继续登记与查询，调用同一库的 `ImportLineage` 函数，见下文「导入血缘 JSON（Go 库）」一节，示例位于 [`examples/import-lineage`](examples/import-lineage/main.go)；导入文档里夹带的统计、备注与大小写不同字段哪些会成为真实依赖、哪些会被忽略，见该节「端点键只认解码后精确等于 `from`、`to` 的键」起的两个小节，示例位于 [`examples/import-lineage-fields`](examples/import-lineage-fields/main.go)。数据集如何登记、同名登记会替换什么，则见下文「登记数据集与维护血缘（Go 库）」一节，示例位于 [`examples/register`](examples/register/main.go)。已登记数据集如何更名、更名怎样保留依赖位置，见下文「数据集更名（Go 库）」一节，示例位于 [`examples/rename`](examples/rename/main.go)。要从血缘图中移除一个数据集的登记，调用 `chainledger.Unregister`，见下文「移除数据集登记（Go 库）」一节，示例位于 [`examples/unregister`](examples/unregister/main.go)。

## 登记数据集与维护血缘（Go 库）

`chainledger.Register(graph, dataset, parents)` 把一个数据集登记进血缘图并维护双向边：`parents` 是该数据集的**直接上游**名称列表，图中每个上游的下游列表（反向边）由 `Register` 同步更新，`Impacts` 查询的就是这同一张内存 map。

参数作用需要说准确：

- 血缘只认 `Dataset.Name` 和单独传入的 `parents` 列表；`Dataset.Upstreams` 字段**不会**被读取，在结构体里填上游既不会登记，也不会追加或替代 `parents`。
- `SchemaVersion` 和 `Rows` 不会被保存为可查询元数据。当前库没有实现结构版本管理或行数管理：传入这两个字段不会报错，但之后查不回来，不要据此假设版本或行数已被记录。
- `parents` 切片会被拷贝，调用返回后修改原切片不影响图；列表中的重复名称只保留一次，以首次出现为准、相对顺序不变。

登记规则：

- **上游必须先登记。** 每个上游名称都必须已在图中；未登记的名称会被拒绝，错误信息直接指出该名称（如 `unknown parent ghost`），不会自动创建节点——新数据集本身和缺失的上游都不会留下。
- **同名再登记是替换整个直接上游列表，不是追加。** 对已存在的数据集再次调用 `Register`，其直接上游整体替换为本次的 `parents`；该数据集已有的下游一律保留，被移除上游的反向边同步删除，新上游的反向边追加到末尾，继续保留的上游位置不变。
- **成环会被拒绝，被拒绝后原来的血缘关系仍然有效。** 若某个上游已经能沿现有关系到达本数据集，这条新边就会闭合环，返回类似 `cycle through summary for dataset detail` 的错误并指出是哪个上游导致的。整个请求在改动图之前一次性校验，因此被拒绝时图与请求前**完全一致**：原有的上下游关系继续有效，请求中的新上游也不会凭空得到一个下游。
- **上游列表中有多个问题时，按 `parents` 的输入顺序报告最先遇到的问题**（每个候选名称依次检查“是不是自己 → 是否已登记 → 是否成环”），不会留下前几个名称“已部分生效”的关系。
- **多个上游共享祖先不等于循环。** 两条分支在更下游汇合（菱形血缘）是合法的；同名登记时把已有上游继续写在列表里也只是保留该边，不构成环。
- **空上游列表会解除全部上游。** `parents` 为空时数据集成为没有上游的根，数据集本身及其已有下游都保留；之后仍可再次登记新上游。
- 数据集名称不能为空（`dataset name is required`），上游也不能写成数据集自己（如 `dataset detail cannot be its own parent`）。

下面的示例与 [`examples/register`](examples/register/main.go) 中的可独立运行程序一致，可在仓库根目录执行 `go run ./examples/register` 复现。先建立 `raw -> detail -> summary` 血缘链和独立来源 `other`；第一次尝试把 `detail` 的直接上游替换为 `other` 与它自己的下游 `summary`，这会成环；修正为只依赖 `other` 后，分别从 `raw`、`other`、`detail` 查询下游：

```go
package main

import (
	"fmt"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

func main() {
	// The lineage graph is an ordinary in-memory map; Register fills it in.
	graph := map[string]*chainledger.Lineage{}

	register := func(name string, parents ...string) {
		err := chainledger.Register(graph, chainledger.Dataset{Name: name}, parents)
		if err != nil {
			fmt.Printf("Register(%s, %v) refused: %v\n", name, parents, err)
			return
		}
		fmt.Printf("Register(%s, %v) ok\n", name, parents)
	}

	// The graph is an ordinary map, so the stored result of each registration
	// is directly readable: Parents are the dataset's direct upstreams and
	// Children its direct downstreams (reverse edges maintained by Register).
	edges := func(names ...string) {
		for _, name := range names {
			e := graph[name]
			fmt.Printf("  %-7s parents=%v children=%v\n", name, e.Parents, e.Children)
		}
	}

	impacts := func(origin string) {
		found, err := chainledger.Impacts(graph, origin)
		if err != nil {
			fmt.Printf("Impacts(%q) error: %v\n", origin, err)
			return
		}
		fmt.Printf("Impacts(%q) -> %d downstream dataset(s)\n", origin, len(found))
		for _, im := range found {
			fmt.Printf("  %-8s distance=%d path=%v\n", im.Dataset, im.Distance, im.Path)
		}
	}

	// The starting lineage is raw -> detail -> summary; other is an
	// independent source unrelated to that chain.
	register("raw")
	register("detail", "raw")
	register("summary", "detail")
	register("other")

	// Try to replace detail's direct upstreams with other and summary. other is
	// already registered, but summary is detail's own downstream: the edge
	// detail -> summary would close the cycle detail -> summary -> detail.
	register("detail", "other", "summary")

	// The rejected request is atomic: detail still depends on raw, summary still
	// depends on detail, and other did not gain detail as a downstream.
	fmt.Println("after the rejected request:")
	edges("raw", "detail", "summary", "other")
	impacts("raw")
	impacts("other")

	// Fixed request: other becomes detail's only direct upstream. A same-name
	// Register REPLACES the whole direct-upstream list (it does not append raw),
	// while detail's existing downstream summary is retained.
	register("detail", "other")

	fmt.Println("after the fixed request:")
	edges("raw", "detail", "summary", "other")
	// raw no longer reaches detail or summary ...
	impacts("raw")
	// ... while other now affects both, and detail keeps its old downstream.
	impacts("other")
	impacts("detail")
}
```

实际输出（错误与血缘边都是程序真实打印，不是示意）：

```text
Register(raw, []) ok
Register(detail, [raw]) ok
Register(summary, [detail]) ok
Register(other, []) ok
Register(detail, [other summary]) refused: cycle through summary for dataset detail
after the rejected request:
  raw     parents=[] children=[detail]
  detail  parents=[raw] children=[summary]
  summary parents=[detail] children=[]
  other   parents=[] children=[]
Impacts("raw") -> 2 downstream dataset(s)
  detail   distance=1 path=[raw detail]
  summary  distance=2 path=[raw detail summary]
Impacts("other") -> 0 downstream dataset(s)
Register(detail, [other]) ok
after the fixed request:
  raw     parents=[] children=[]
  detail  parents=[other] children=[summary]
  summary parents=[detail] children=[]
  other   parents=[] children=[detail]
Impacts("raw") -> 0 downstream dataset(s)
Impacts("other") -> 2 downstream dataset(s)
  detail   distance=1 path=[other detail]
  summary  distance=2 path=[other detail summary]
Impacts("detail") -> 1 downstream dataset(s)
  summary  distance=1 path=[detail summary]
```

要点：

- 被拒绝的那次调用之后，`detail` 的直接上游仍是 `raw`、`summary` 仍依赖 `detail`（输出里的 `parents=`/`children=` 直接读自图这个 map），`Impacts("raw")` 照常到达二者；`other` 的下游列表为空，**没有**因为这次失败请求得到 `detail` 这个下游。
- 修正请求成功体现的是**整体替换**而不是追加：`raw` 不再影响 `detail` 和 `summary`（`Impacts("raw")` 查询成功但返回空列表），`other` 可以影响二者，距离分别为 1、2；`detail` 原有的下游 `summary` 仍然保留。
- 这里血缘查询的方向（只沿下游）、距离（最短依赖边数）和结果顺序（先距离后名称）与下一节文档化的 `Impacts` 公开行为完全一致。

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

## 带传播截止名单的下游影响（Go 库）

分析某个来源数据集的变化时，可以把一个或多个**已登记**数据集指定为"截止点"，查看变化在不越过这些数据集的前提下还能影响多大范围。调用：

```go
impacts, err := chainledger.ImpactsWithCutoffs(graph, origin, cutoffs)
```

`cutoffs` 是一个数据集名称切片（传 `nil` 或空切片时与 `Impacts` 完全等价）。返回值仍是 `[]chainledger.Impact`，每条记录含义与 `Impacts` 一致：`Dataset` 是受影响数据集、`Distance` 是从起点沿**允许传播的路线**走出的最短边数、`Path` 是该最短路线从起点写到该数据集的名称序列；每个数据集最多出现一次，记录仍按距离升序、同距离按名称排序。

传播规则：

- **截止点本身仍在结果里，但不再沿它的下游继续传播。** 只要截止点能从起点到达，它就按自己的最短距离出现（直接下游距离为 1），变化不会继续越过它。
- **位于截止点之后的数据集，只要还有一条不经过任何截止点的路线，就继续出现。** 只有从起点出发的**每条**路线都穿过某个截止点时，该数据集（以及只能经它到达的下游）才一起离开结果。
- **距离和说明路径只按允许传播的路线计算。** 完整查询中那条"更短但穿过截止点"的路线被截断后不会被沿用；若剩下的路线更长，距离就取更长的值，说明路径也换成绕行路线。选路仍是两层：先边数最少，同样短时从起点开始逐跳比较整条路径名称，取 Go 字符串顺序较小的一条。
- **截止名单为空时，结果与 `Impacts` 一致。** 重复名称只起一次作用；已登记但从起点不可达的截止点不改变结果。
- **起点自己被列为截止点时，查询成功并返回非 nil 的空列表**，起点仍不列为受影响数据集。名称按登记值精确匹配（区分大小写）。

沿用上面的双分支汇合血缘，考虑 `source` 分别派生 `a` 和 `b`，`a` 直接派生 `report`，`b` 经 `mid` 派生 `report`，`report` 再派生 `view`：

```
source ──> a ────────> report ──> view
  │                    ^
  └──> b ──> mid ──────┘
```

把 `a` 设为截止点后，`a` 仍是距离 1 的结果，但 `report` 改由 `b -> mid` 绕行到达，距离变为 3，`view` 距离为 4；若 `a`、`b` 都是截止点，则两者仍在结果中，`mid`、`report`、`view` 不再出现。完整可运行示例位于 [`examples/impacts-cutoffs`](examples/impacts-cutoffs/main.go)，可在仓库根目录执行 `go run ./examples/impacts-cutoffs` 复现下面的真实输出：

```text
ImpactsWithCutoffs("source", []) -> 5 dataset(s):
  a        distance=1 path=[source a]
  b        distance=1 path=[source b]
  mid      distance=2 path=[source b mid]
  report   distance=2 path=[source a report]
  view     distance=3 path=[source a report view]
ImpactsWithCutoffs("source", [a]) -> 5 dataset(s):
  a        distance=1 path=[source a]
  b        distance=1 path=[source b]
  mid      distance=2 path=[source b mid]
  report   distance=3 path=[source b mid report]
  view     distance=4 path=[source b mid report view]
ImpactsWithCutoffs("source", [a b]) -> 2 dataset(s):
  a        distance=1 path=[source a]
  b        distance=1 path=[source b]
```

失败与边界行为：

- **起点为空或不存在时，沿用 `Impacts` 的错误行为**：空名称返回 `dataset name is required`，未登记名称返回指出该名称的错误（如 `dataset not found: ghost`），结果均为 nil；起点的校验先于截止名单。
- **截止名单包含空名称**时整次查询失败：`cutoff dataset name is required`；**包含未登记名称**时整次查询失败并指出具体名称，如 `cutoff dataset not found: ghost`；名单中多个问题按输入顺序报告最先遇到的一个。两种情况都返回 nil 结果。
- 查询是只读的：不改变图中的任何节点、关系或列表顺序；返回的每条 `Path` 都是独立拷贝，修改返回结果不影响图或后续查询。

`Impacts(graph, origin)` 的原有签名、调用方式与完整查询行为保持不变；登记、更名、移除和上游查询也都照常使用。

## 查询上游来源（Go 库）

`chainledger.Upstreams(graph, target)` 是 `Impacts` 的反向查询：给定一个已登记的数据集名称，返回它的全部**直接和间接上游**，即沿“上游”方向从 `target` 出发沿 parent 边可以到达的全部来源数据集。查询只使用当前登记的血缘关系：`target` 自己、它的下游、以及没有依赖关系的数据集都不会出现；同一个来源经多条分支参与派生时，结果里只保留一条记录。

每个结果是一条 `chainledger.Upstream`：

- `Dataset`：上游数据集名称。
- `Distance`：从该上游到 `target` 的**最短依赖边数**。直接上游距离为 1。
- `Path`：这条最短路径上的名称序列，沿实际派生方向**从该上游写到 `target`**，首尾都包含在内。

路径选择与排序规则和 `Impacts` 完全对称：先选边数最少的路径；同样短时**从上游开始**逐跳比较整条路径上的名称，取 Go 字符串顺序较小的一条。结果按距离升序排列，同距离按上游名称排列，登记顺序和上游列表书写顺序都不影响结果。

沿用「查询下游影响」一节的血缘图（`source` 派生 `a`、`b`，`z` 依赖 `a`，`c` 依赖 `b`，`report` 依赖 `z`、`c`，另有 `report` 的下游 `view` 和无关的 `isolated`），查询 `report` 的来源：

```go
upstreams, err := chainledger.Upstreams(graph, "report")
if err != nil {
	fmt.Fprintf(os.Stderr, "Upstreams: %v\n", err)
	os.Exit(1)
}
for _, up := range upstreams {
	fmt.Printf("%-8s distance=%d path=%v\n", up.Dataset, up.Distance, up.Path)
}
```

预期输出：

```text
c        distance=1 path=[c report]
z        distance=1 path=[z report]
a        distance=2 path=[a z report]
b        distance=2 path=[b c report]
source   distance=3 path=[source a z report]
```

要点：

- `source` 同时经过 `a -> z` 和 `b -> c` 两条分支参与 `report` 的派生，但**只出现一次**，距离取最短边数 3。
- 两条长度都是 3 的路线中，选中的是 `[source, a, z, report]`：比较从上游 `source` 开始逐跳进行，第一个不同的名称是第 1 跳的 `a` 与 `b`，`a < b`，所以选 a 侧路线；**不能**只比较 `report` 的直接上游（那样会因 `c < z` 错选 b 侧路线）。
- `report` 的下游 `view` 和无关的 `isolated` 都不会出现在结果里。

若再给 `report` 增加对 `source` 的直接依赖（同名登记替换直接上游列表），`source` 的距离变为 1，路径为 `[source, report]`——边数最少优先于词典序：

```text
c        distance=1 path=[c report]
source   distance=1 path=[source report]
z        distance=1 path=[z report]
a        distance=2 path=[a z report]
b        distance=2 path=[b c report]
```

失败与边界行为也和 `Impacts` 一致：

- 名称为空：返回缺少名称的错误（`dataset name is required`），结果为 nil。
- 名称不存在（包括对空图或 nil 图查询任何非空名称）：错误信息指出该名称（如 `dataset not found: ghost`），结果为 nil。名称按登记值精确匹配，区分大小写。
- 已登记但没有上游的数据集：查询成功，返回**非 nil 的空列表**。

查询是只读的：不改变任何节点、关系或列表顺序；返回的每条 `Path` 都是独立拷贝，调用方修改它不会影响图、其他记录的路径或之后的查询结果。用同名 `Register` 替换直接上游后，再次查询反映新关系，而之前取得的结果仍保留原内容。完整可运行示例位于 [`examples/upstreams`](examples/upstreams/main.go)，可在仓库根目录执行 `go run ./examples/upstreams` 复现上面的输出。

## 查询两份派生数据的最近共同上游（Go 库）

`chainledger.CommonUpstreams(graph, first, second)` 在“分别列出上游”的基础上回答比较问题：两份已登记数据集**共同**能沿上游关系追到哪些数据集，并返回其中**最近的共同来源**。目标自身也算自己的来源；如果一个共同来源的直接或间接下游中还有其他共同来源，它就不出现在结果里——即结果是两份血缘交汇处之前的“共同来源前沿”，更早的共同祖先一律排除。

每个结果是一条 `chainledger.CommonUpstream`：

- `Dataset`：共同来源数据集名称。
- `DistanceToFirst` / `PathToFirst`：该来源到第一个目标的**最短依赖边数**，以及沿实际派生方向**从该来源写到第一个目标**的说明路径（首尾都包含）。
- `DistanceToSecond` / `PathToSecond`：该来源到第二个目标的对应距离与路径。

两侧的距离和路径各自独立计算，因此不必相同。路径选择与 `Upstreams` 完全一致：先选边数最少的路线；同样短时**从来源开始**逐跳比较整条路径上的名称，取 Go 字符串顺序较小的一条。每个来源只出现一次，结果**按来源名称的 Go 字符串顺序排列**（不按距离、不按登记顺序、不按直接上游列表顺序）。

典型情形：`raw` 派生 `a`、`b`，`a`、`b` 都参与 `left` 和 `right` 的派生，查询 `left`、`right` 时返回 `a`、`b`，排除 `raw`——因为 `a`、`b` 是位于 `raw` 之后的共同来源：

```
       ┌──> a ──┬──> left
  raw ─┤        └──> right
       └──> b ──┬──> left
                └──> right
```

即使 `raw` 同时直接参与两个目标的派生、到它们的距离更短（比如两个目标的直接上游都写成 `[raw a b]`），`raw` 也**仍然排除**：距离大小从不改变“前沿”判定，只要下游还存在另一个共同来源，更早的来源就被挡住。可以返回多个来源，既不按距离择优，也不限定为没有上游的根。

几个边界行为同样由这条规则推出：

- **两个目标相同**：唯一结果是该目标自身，两侧距离均为 0，两条路径都只包含自身名称。
- **一个目标是另一个目标的上游**：只返回这个上游目标，它到自身一侧的距离为 0、路径只含自身；它自己的上游祖先被它挡在结果之外。
- **两份血缘没有任何共同来源**：查询成功，返回**非 nil 的空列表**（例如两棵互不相连的血缘树，或其中一个是与对方无关的独立数据集）。

下面的程序与 [`examples/common-upstreams`](examples/common-upstreams/main.go) 一致，可在仓库根目录执行 `go run ./examples/common-upstreams` 复现。程序先建立上图血缘，再依次演示共同来源、目标相同、目标互为上游、无共同来源、交换查询顺序，以及给 `right` 增加直达 `raw` 的边后 `raw` 仍被排除：

```go
graph := map[string]*chainledger.Lineage{}
register("raw")
register("a", "raw")
register("b", "raw")
register("left", "a", "b")
register("right", "a", "b")

found, err := chainledger.CommonUpstreams(graph, "left", "right")
if err != nil { /* ... */ }
for _, c := range found {
    fmt.Printf("%s to left: distance=%d path=%v | to right: distance=%d path=%v\n",
        c.Dataset, c.DistanceToFirst, c.PathToFirst, c.DistanceToSecond, c.PathToSecond)
}
```

实际输出（错误与血缘边都是程序真实打印，不是示意；节选）：

```text
CommonUpstreams("left", "right") -> 2 shared source(s):
  a     to left: distance=1 path=[a left] | to right: distance=1 path=[a right]
  b     to left: distance=1 path=[b left] | to right: distance=1 path=[b right]
CommonUpstreams("left", "left") -> 1 shared source(s):
  left  to left: distance=0 path=[left] | to left: distance=0 path=[left]
CommonUpstreams("raw", "left") -> 1 shared source(s):
  raw   to raw: distance=0 path=[raw] | to left: distance=2 path=[raw a left]
CommonUpstreams("left", "otherchild") -> 0 shared source(s):
```

要点：

- `raw` 是两个目标更上游的共同祖先，但结果只返回最近的共同来源 `a`、`b`；给 `right` 增加直达 `raw` 的直接依赖后，虽然 `raw` 到 `right` 的最短距离缩到 1，它仍被 `a`、`b` 挡住而不出现。
- 交换两个目标的输入顺序只会交换“到第一目标 / 到第二目标”两侧字段，来源集合不变。
- 两侧路径不对称时（同一来源到两个目标距离不同、路线不同），两侧各自给出自己的最短路径与词典序选择。

失败与边界行为与 `Upstreams`、`Impacts` 一致：

- **目标按输入顺序校验。** 第一个目标为空时报缺少名称（`dataset name is required`），第一个目标未登记时错误指出该名称（如 `dataset not found: ghost`）；第一个目标合法后才校验第二个目标。对空图或 nil 图查询任何非空名称都按未登记处理。失败一律返回 nil 结果，不返回部分来源。
- 名称按登记值精确匹配，区分大小写。
- 查询是只读的：不改变任何节点、关系或列表顺序；返回的两条 `Path` 彼此独立、也独立于图，修改返回记录不会影响图、其他记录或之后的查询。

## 导出完整上游血缘（Go 库）

`chainledger.ExportUpstreamLineage(graph, target)` 把一个已登记数据集**实际依赖的完整上游血缘**导出为一份 JSON 文本，供其他程序读取其中的关系。`Upstreams` 查询为每个来源只给出一条最短说明路径，而导出保留目标实际依赖的**全部分支**：即使某个来源已经能直接到达目标，经过中间数据集的较长路线也完整留在文档里。

JSON 顶层包含两个数组：

- `nodes`：名称数组，包含目标自身及它的全部直接、间接上游，每个名称恰好出现一次。
- `edges`：对象数组，每项用 `from`、`to` 表示一条**现存的直接依赖**，方向从上游指向派生数据集（`from` 是上游，`to` 是由它派生的数据集）。所选节点之间的全部直接依赖都出现，每条边恰好出现一次。

目标的下游和没有参与其派生的独立数据集不进入输出。例如 `raw` 派生 `a`、`b`，`b` 派生 `mid`，`report` 同时直接依赖 `raw`、`a`、`mid`，`report` 还派生 `view`，另有独立的 `isolated`：

```
raw ──┬──> a ────────┐
      │              ├──> report ──> view
      └──> b ──> mid ┘
      └────────────────^ (report 也直接依赖 raw)
```

导出 `report` 时包含 `raw`、`a`、`b`、`mid`、`report` 五个节点及它们之间原有的六条直接依赖；`view` 和 `isolated` 不出现。虽然 `raw` 到 `report` 已有直达关系，经 `a` 和经 `b`、`mid` 的较长分支仍完整保留。下面的程序与 [`examples/export-upstreams`](examples/export-upstreams/main.go) 一致，可在仓库根目录执行 `go run ./examples/export-upstreams` 复现：

```go
graph := map[string]*chainledger.Lineage{}
register("raw")
register("b", "raw")
register("a", "raw")
register("mid", "b")
register("report", "raw", "a", "mid")
register("view", "report")
register("isolated")

text, err := chainledger.ExportUpstreamLineage(graph, "report")
if err != nil { /* ... */ }
fmt.Println(text)
```

实际输出（程序真实打印，不是示意）：

```text
{"nodes":["a","b","mid","raw","report"],"edges":[{"from":"a","to":"report"},{"from":"b","to":"mid"},{"from":"mid","to":"report"},{"from":"raw","to":"a"},{"from":"raw","to":"b"},{"from":"raw","to":"report"}]}
```

规则要点：

- **输出顺序稳定。** 节点按名称的 Go 字符串顺序排列，边先按 `from`、再按 `to` 的同一顺序排列；登记顺序和登记时给出的上游列表顺序都不影响输出，同一张图重复导出得到字节一致的文本。
- **名称按原值精确匹配，区分大小写**，不裁剪、不改写；名称中的引号、反斜线及换行按 JSON 正确转义，其他程序读取后仍得到原名称。
- **所选名称必须是合法 UTF-8。** 只有合法 UTF-8 的名称才能无损交给 JSON 读取方：名称中的非法字节会被 JSON 编码静默替换成“�”，两个字节不同的名称还可能在读取后变成同一个，导致节点和依赖失去原有身份。因此只要目标自身及其直接、间接上游闭包中有任一名称不是合法 UTF-8，整次导出都失败并返回空字符串，错误形如 `dataset name is not valid UTF-8 and cannot be exported losslessly: "a\xffb"`；名称以 Go 引号形式给出，不可显示的字节呈现为各自的 `\xNN` 转义，不会用同一个替代字符描述不同问题。多个名称同时无效时，按原始名称的 Go 字符串顺序报告最靠前的一个，因此同一血缘在不同登记顺序、上游列表顺序下得到相同错误。检查只针对本次导出的闭包：与目标无关的数据集、目标的下游即使名称编码无效也不影响这次导出。名称中真实存在的“�”字符（U+FFFD）本身是合法 UTF-8，仍正常导出且读取后与登记值相同，不会被误判为坏字节。
- **目标没有上游时也能成功**：`nodes` 只包含目标名称，`edges` 是 JSON 空数组（`[]`，不是 `null`）。
- **失败不返回部分导出内容。** 目标为空时返回缺少名称的错误（`dataset name is required`）；目标未登记时返回指出该名称的错误（如 `dataset not found: ghost`），对空图或 nil 图中的非空目标同样按未登记处理；这两项检查优先于名称编码检查；失败一律返回空字符串。
- **导出是只读的**：不改变图中的任何名称、双向关系或列表顺序；现有登记、更名、移除及各类血缘查询仍按当前名称精确匹配，不增加全图清洗或新的名称限制，调用方仍能用原始名称处理已有登记。

## 限定来源的上游血缘导出（Go 库）

`chainledger.ExportSourceTargetLineage(graph, source, target)` 在完整上游导出的基础上回答一个更窄的问题：给定**两个都已登记**的数据集名称 `source`（来源）和 `target`（目标），只导出**这个来源怎样参与这个目标的派生**。`ExportUpstreamLineage(graph, target)` 会把目标依赖的所有来源连同它们的分支一起带出，而限定来源的导出只保留从 `source` 到 `target` 的现存派生路线实际经过的部分。

文档的 JSON 形状与完整上游导出完全一致，顶层仍是两个数组：

- `nodes`：位于至少一条 `source -> ... -> target` 现存派生路线上的全部数据集名称，`source` 和 `target` 两个端点也包含在内，每个名称恰好出现一次，按 Go 字符串顺序排列。
- `edges`：这些节点之间的全部现存直接依赖，每条边恰好出现一次，写成 `{"from": 上游, "to": 派生数据集}`，先按 `from`、再按 `to` 的 Go 字符串顺序排列。

节点的入选条件可以精确表述为：**既是 `source` 的直接或间接下游，又是 `target` 的直接或间接上游**（沿 child 边能从 `source` 到达它、沿 parent 边能从它到达 `target`）；一条边入选当且仅当它的两个端点都入选。保留的是整个节点集合而不是挑出的一条路径，因此**所有路线同时保留**：直达关系和更短路线都不会让较长路线被省略。例如 `source` 直接派生 `report`，同时又经 `a` 到达 `report`，还经 `b`、`mid` 到达 `report`，限定 `source` 与 `report` 后三条路线都完整留在文档里。

哪些内容被排除也由同一个交集规则推出：

- **汇合点的其他独立来源不进入。** 如果 `a` 还依赖另一个独立来源 `extra`（`extra` 本身不是 `source` 的下游），`extra` 节点和 `extra -> a` 这条边都不进入文档。
- **来源的祖先不进入。** 例如 `source` 还依赖 `pre`，`pre` 及 `pre -> source` 不属于这次导出。
- **来源到不了目标的其他下游不进入。** `source` 的某个下游若无法到达 `target`，它及其分支都不出现。
- **目标的下游不进入。** `report` 之后派生的数据集与“`source` 怎样派生 `report`”无关。

输出顺序完全由名称决定，与登记顺序、登记时的上游列表顺序无关；同一张图重复导出得到字节一致的文本。名称按原值精确匹配、区分大小写，按原值逐字写出，JSON 转义规则与完整导出相同。

下面的程序与 [`examples/export-source-target`](examples/export-source-target/main.go) 一致，可在仓库根目录执行 `go run ./examples/export-source-target` 复现。程序先建立：`pre -> source`；`source` 派生 `a`、`b`、`sidetrack`，并直接派生 `report`；`a` 还依赖独立来源 `extra`；`b -> mid`；`report` 依赖 `source`、`a`、`mid` 和另一个独立来源 `lone`；`report` 再派生 `view`；另有完全独立的 `isolated`：

```
pre ──> source ──┬──> a（还依赖 extra）──┐
                ├──> b ──> mid ─────────┤
                ├──> sidetrack          ├──> report ──> view
                └──> report（直达）      │
                              extra ────┘
                              lone ─────┘
```

```go
text, err := chainledger.ExportSourceTargetLineage(graph, "source", "report")
if err != nil { /* ... */ }
fmt.Println(text)
```

实际输出（程序真实打印，不是示意）：

```text
{"nodes":["a","b","mid","report","source"],"edges":[{"from":"a","to":"report"},{"from":"b","to":"mid"},{"from":"mid","to":"report"},{"from":"source","to":"a"},{"from":"source","to":"b"},{"from":"source","to":"report"}]}
```

三条路线（直达、经 `a`、经 `b -> mid`）全部完整保留；`pre`、`extra`、`lone`、`sidetrack`、`view`、`isolated` 以及与它们相连的边都不出现。同一程序还演示了其余边界行为，真实输出如下：

```text
ExportSourceTargetLineage("source", "mid") ->
{"nodes":["b","mid","source"],"edges":[{"from":"b","to":"mid"},{"from":"source","to":"b"}]}
ExportSourceTargetLineage("source", "source") ->
{"nodes":["source"],"edges":[]}
ExportSourceTargetLineage("source", "extra") ->
{"nodes":[],"edges":[]}
ExportSourceTargetLineage("source", "isolated") ->
{"nodes":[],"edges":[]}
ExportSourceTargetLineage("source", "sidetrack") ->
{"nodes":["sidetrack","source"],"edges":[{"from":"source","to":"sidetrack"}]}
ExportSourceTargetLineage("", "report") error: dataset name is required
ExportSourceTargetLineage("ghost", "report") error: dataset not found: ghost
ExportSourceTargetLineage("source", "") error: dataset name is required
ExportSourceTargetLineage("source", "ghost") error: dataset not found: ghost
```

边界与失败规则：

- **来源与目标相同且已登记时**，只输出该节点和空的 `edges` 数组（`[]`，不是 `null`），不会把它的祖先或下游带进来。
- **两者都已登记、但不存在从来源到目标的派生路线时，查询成功并返回两个空数组**（`{"nodes":[],"edges":[]}`，两个数组都是 `[]` 而非 `null`），不保留任何孤立端点。互不相连的两个数据集如此；**反方向的依赖不算可达**——`a` 依赖 `extra` 并不意味着从 `source` 能到 `extra`，限定 `source`、`extra` 仍是两个空数组。
- **先校验来源，再校验目标。** 名称为空返回缺少名称的错误（`dataset name is required`）；名称未登记时错误指出对应名称（如来源未登记报 `dataset not found: ghost`），对空图或 nil 图同样按未登记处理。来源的空值或未登记错误优先于目标的任何问题。失败一律返回空字符串，绝不返回部分文档。
- **入选名称必须是合法 UTF-8。** 编码失败规则与完整导出一致：只要实际入选的节点（含两个端点）中有名称不是合法 UTF-8，整次导出失败、返回空字符串，错误以 Go 引号形式（`%q`）给出该名称的原始字节；多个名称同时无效时报告按 Go 字符串顺序最靠前的一个，因此错误也不受登记顺序和列表顺序影响。**未入选节点的非法名称不能阻止导出**：来源的祖先、目标的其他独立上游、到不了目标的来源下游、目标的下游，即使名称含非法字节也不影响本次限定导出（同一张图的完整上游导出仍可能因这些名称失败，两者各按各自的入选集合判断）。名称中真实存在的“�”字符（U+FFFD）是合法 UTF-8，正常导出。
- **导出是只读的**：成功或失败都不改动图中的任何节点、双向关系或列表顺序，也不新增全图清洗或名称限制。

`ExportUpstreamLineage(graph, target)` 的签名、调用方式与“导出目标全部上游”的行为保持不变；登记、更名、移除及现有血缘查询功能全部兼容。

## 导入血缘 JSON（Go 库）

`chainledger.ImportLineage(text)` 把一份血缘 JSON 文本重新交回库，成功时返回一张**全新的、独立的血缘图**（普通的 `map[string]*Lineage`），可以直接用于现有的全部功能：`Register`、`Rename`、`Unregister`、`Impacts`、`ImpactsWithCutoffs`、`Upstreams`、`CommonUpstreams`、`Roots` 以及两种导出。导入不接收、不读取、也不修改调用方原有的图。

文档沿用两种导出现有的 JSON 形状，顶层是一个对象，含两个数组：

- `nodes`：数据集名称数组。文档里列出的每个节点都会存在于新图中，**包括没有任何依赖的独立节点**；重复列出的名称只保留一次。
- `edges`：直接依赖对象数组，每项写成 `{"from": 上游, "to": 派生数据集}`，方向与导出一致（`from` 是上游，`to` 是由它派生的数据集）。每条直接依赖同时体现为派生数据集的直接上游（`Parents`）和来源的直接下游（`Children`，反向边）；同一条依赖重复出现只保留一次。

导入范围**恰好是文档列出的节点与关系**，不做任何推断或补全：

- 用限定来源导出（`ExportSourceTargetLineage`）得到的文档若只保留了一部分血缘，导入后也只得到这一部分——被排除的其他来源、来源的祖先、到不了目标的分支以及目标的下游都不会被补回；在新图里查询这些名称会按未登记处理（`dataset not found: <名称>`）。
- **直达关系与经过中间节点的较长分支一起保留**，互不排挤。说明路径仍由现有查询功能按“先最短边数、再按整条路径逐跳比较名称（Go 字符串顺序）”的规则选择，导入不改变这一规则。

节点与边在文档中的排列顺序不影响导入结果；导入后各节点的直接上游、直接下游列表都按 Go 字符串顺序排列。因此，把现有导出文档（包括带查询端点的限定来源导出）导入后，用**相同目标**或**相同来源与目标**重新导出，得到的是**字节一致**的文本。

名称按 JSON 解码后的**原值精确识别**：区分大小写，空格、中文、引号、反斜线和换行都原样保留，不裁剪、不改写。文本必须是合法 UTF-8，且包含**一个完整的 JSON 对象**；`nodes` 和 `edges` 都必须存在且是类型正确的数组（`null` 不是数组，元素类型也必须正确）。**两个空数组表示成功**：得到一张没有节点的空图，可以马上继续登记，不算是导入失败。

失败规则——整次导入先完整校验再返回图，任何失败都返回 `nil` 图和说明文档问题的错误，**绝不返回部分图**：

- 空文本或只有空白、非法 UTF-8、不是合法 JSON、顶层不是对象、存在两个 JSON 值、对象键重复、缺少 `nodes` 或 `edges`、字段为 `null` 或类型不对、数组元素类型不对，都会被拒绝。
- 节点名称不能为空（`dataset name is required`）。
- **名称中不能出现未配对的 Unicode 代理项转义。即使 JSON 文本本身是合法 UTF-8，`\uD800`–`\uDFFF` 中孤立的代理项转义仍会被标准 JSON 解码悄悄改写成替代字符“�”：两个写法不同的坏名称（如 `\ud800` 与 `\ud801`）会合成同一个名称，边端点用这类转义还可能错误地连到文档中真实名为“�”的数据集。因此节点名称以及每条边 `from`、`to` 的端点名称都先按原始文本校验：高代理项转义只有紧接低代理项转义、二者共同表示一个字符时才有效（如 `\ud83d\ude00` 与直接写入的 😀 识别为同一个名称，节点和边采用不同合法写法时也能连接成功）；孤立的高代理项、孤立的低代理项、次序颠倒或中间隔着其他字符/其他转义的组合都判为无效。名称任何位置出现这个问题，整次导入都返回 nil 图和错误，即使文档还包含合法节点和关系也不返回部分图。错误会明确说明名称存在未配对的 Unicode 代理项转义、保留出错的原始转义写法（`\uXXXX`），并指出来自节点名称还是某条边的 `from` 或 `to`，不同坏名称不会都只显示成同一个“�”。真实的“�”字符（U+FFFD，直接写入或写作 `\ufffd`）是合法名称；用转义反斜线表示的普通文字（如 JSON 字符串 `"\\ud800"`）实际名称只是反斜线加后面的字母数字，完整保留、可查询可导出，不会被当作代理项转义拒绝。
- 每条边的两个端点都必须在 `nodes` 中列出；边缺少 `from`/`to`，或端点名称未在节点中列出时，整次导入失败，错误指出该名称（如 `edge endpoint not listed in nodes: ghost`）。
- **自依赖或任意长度的依赖环**都整次失败，错误说明成环并沿实际派生方向列出环上的数据集，环从名称最小的数据集开始首尾相接，例如 `lineage contains a cycle: a -> b -> c -> a`；自依赖报 `lineage contains a cycle: a -> a`。多个分支汇合（菱形）不是环，正常导入。

#### 端点键只认解码后精确等于 `from`、`to` 的键

实际文档常常夹带统计、备注或大小写不同的字段；哪些会成为真实依赖、哪些会被忽略，由下面的规则确定：

- **端点按 JSON 解码后的键名精确识别。** 只有解码后恰好是 `from`、`to` 的键设置端点；`From`、`FROM`、`To`、`TO` 等大小写变体都是与 `note`、`stats` 一样的**未知字段**，其值既不校验也不使用。因此变体既不能覆盖正确的小写端点（`{"from":"raw","From":"other","to":"report"}` 导入后 `report` 只依赖 `raw`），也不能代替缺失的端点；字段排列顺序不改变结果，变体写在正确字段之前、之后或中间都一样。
- **合法 Unicode 转义写出的同一个键仍会被识别。** 识别基于解码后的键名而非原始拼写：`"\u0066rom"`（即 `f` 的转义）与 `"f\u0072om"` 解码后都是 `from`，照常连接端点；反过来 `"\\u0066rom"`（转义反斜线加字母）解码后只是普通文字，是未知字段。同一对象里同时用明文和转义两种形式写同一个键（如 `"from"` 与 `"\u0066rom"`）属于**重复键**，按重复键拒绝。
- **仅写 `From` 而缺少 `from` 的边整次导入失败**：返回 nil 图，错误与该字段真的不存在完全相同——`lineage document edge is missing its upstream endpoint ("from")`（只写 `To` 缺少 `to` 时报对应的 `("to")`）。同一份文档里排在它前面的合法边不会产生部分图。

#### 扩展字段被忽略，但忽略有明确边界

- **未知字段里的名称、对象或数组都不是节点与依赖。** 备注、统计对象中出现的数据集名称不会登记为节点，嵌套对象里的 `from`/`to` 形成员也不会连成边；这些值连“名称检查”都不参与（例如未知字段值里出现孤立代理项转义也不会按坏名称拒绝）。导入图恰好包含 `nodes` 声明的节点和小写端点声明的边。
- **扩展值只按 JSON 语法接受，不按浮点范围接受。** 合法 JSON 数字（如 `1e400`、`-1e400`、`1e-400` 或上百位的整数）作为附带值可以出现在顶层、边对象上或嵌套备注的任意深度，即使超出 float64 范围也不会失败——它们从不被解码成数值或保存为统计元数据。但语法非法的数字（`01`、`1e` 等）无论藏在哪一层都会使整次导入失败。
- **文档整体仍须是合法 JSON、且任何对象都不能有重复键。** “忽略未知字段”不放宽文档级校验：未知字段的嵌套对象里出现重复键（如 `"note":{"batch":"a","batch":"b"}`），整次导入仍返回 nil 图和指出重复键的错误（`lineage document contains a duplicated object key: batch`）；边对象上重复的大小写变体（两个 `"From"`）同理。
- **扩展字段不保存也不输出。** 重新导出（`ExportUpstreamLineage` 或 `ExportSourceTargetLineage`）只得到声明的节点与直接依赖，备注、统计和变体字段没有任何痕迹；同一份无扩展字段的导出文档往返字节一致的规则不受影响。

下面的程序与 [`examples/import-lineage-fields`](examples/import-lineage-fields/main.go) 一致，可在仓库根目录执行 `go run ./examples/import-lineage-fields` 复现。程序固定一份正确文档：`nodes` 只声明 `raw` 与 `report`，边对象用小写 `from`/`to` 声明 `raw -> report`，同时用大写 `From` 指向未登记的 `ghost`，边对象和顶层都带嵌套备注（含看似节点名与 `from`/`to` 形对象的数组）与 `1e400` 统计；随后打印来源查询与重新导出结果，再通过改动同一份文档展示“缺少端点”和“嵌套重复键”两种拒绝：

```go
graph, err := chainledger.ImportLineage(doc) // doc 见下
// ...
upstreams, _ := chainledger.Upstreams(graph, "report")
out, _ := chainledger.ExportUpstreamLineage(graph, "report")

// 改动一：删去小写 "from"，只留大写 "From"（指向 ghost）。
// 改动二：在顶层嵌套 note 对象里重复一个 "batch" 键。
```

程序导入的那份文档是：

```json
{
  "nodes": ["raw", "report"],
  "edges": [
    {
      "from": "raw",
      "to": "report",
      "From": "ghost",
      "note": {
        "author": "ingest-job",
        "trace": ["ghost", {"from": "ghost", "to": "phantom"}]
      },
      "stats": {"rows": 1e400, "ratio": 1e-400}
    }
  ],
  "note": {"batch": "2026-10-07", "extra": [1, 2, {"from": "ghost", "to": "phantom"}]},
  "stats": {"totalRows": 1e400}
}
```

实际输出（程序真实打印，不是示意）：

```text
import ok; datasets in graph: 2
  report parents=[raw]
  raw    children=[report]

Upstreams(report):
  raw   distance=1 path=[raw report]

Impacts(ghost) error: dataset not found: ghost

ExportUpstreamLineage(report):
{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}]}

variant 1: lowercase "from" removed, capitalized "From" stays
  graph is nil: true
  refused: lineage document edge is missing its upstream endpoint ("from")

variant 2: a key repeated inside the nested note object
  graph is nil: true
  refused: lineage document contains a duplicated object key: batch
```

要点：

- **进入血缘图的只有声明内容。** 图中只有 `raw`、`report` 两个节点；`report` 的唯一来源是 `raw`，距离 1，说明路径 `[raw report]`；大写 `From` 指向的 `ghost` 与备注里的 `phantom` 都不是节点，查询 `ghost` 明确按未登记报错。
- **重新导出没有附带信息。** 输出只剩 `{"nodes":["raw","report"],"edges":[{"from":"raw","to":"report"}]}`，备注、统计和 `ghost` 不保存也不输出——用户可以直接用这一步核对附带信息有没有进入血缘图。
- **两种改动都整次拒绝且返回 nil 图**：缺少小写端点时报与字段真的缺失相同的上游端点错误；嵌套备注里的重复键仍按重复键拒绝。

下面的程序与 [`examples/import-lineage`](examples/import-lineage/main.go) 一致，可在仓库根目录执行 `go run ./examples/import-lineage` 复现。程序先建立含祖先 `pre`、独立来源 `extra`/`lone` 和目标下游 `view` 的血缘，再限定 `source -> report` 导出；导入这份文档后，新图只含五个节点：

```go
text, err := chainledger.ExportSourceTargetLineage(graph, "source", "report")
// ...
imported, err := chainledger.ImportLineage(text)
impacts, err := chainledger.Impacts(imported, "source")
again, err := chainledger.ExportSourceTargetLineage(imported, "source", "report")
// again == text：重新导出与导入的文档字节一致
```

实际输出（错误与查询结果都是程序真实打印，不是示意）：

```text
exported document:
{"nodes":["a","b","mid","report","source"],"edges":[{"from":"a","to":"report"},{"from":"b","to":"mid"},{"from":"mid","to":"report"},{"from":"source","to":"a"},{"from":"source","to":"b"},{"from":"source","to":"report"}]}

datasets after import: 5 (the export omitted pre, extra, lone and view)

Impacts(source) on the imported graph:
  a       distance=1 path=[source a]
  b       distance=1 path=[source b]
  report  distance=1 path=[source report]
  mid     distance=2 path=[source b mid]

re-export is byte-identical to the imported document

registered newly into the imported graph; querying omitted datasets:
  Upstreams(view) error: dataset not found: view
  original graph still holds 9 datasets, imported graph holds 6

empty document imported as a ready-to-register graph holding "fresh"

empty text refused: lineage document is empty: expected one JSON object with nodes and edges arrays
missing endpoint refused: edge endpoint not listed in nodes: ghost
self dependency refused: lineage contains a cycle: a -> a
three-node cycle refused: lineage contains a cycle: a -> b -> c -> a
```

要点：

- **导入只创建新图。** 输出里调用方原图仍有 9 个数据集，导入图只有文档中的 5 个（再登记后 6 个）；对被排除的 `view` 查询明确按未登记报错。
- **直达边决定最短距离，长分支仍然完整。** `report` 经直达边距离为 1，但经 `a` 和经 `b -> mid` 的分支仍在新图中（`mid` 距离 2，继续作为 `report` 的直接上游）。
- **往返字节一致。** 同一份限定来源导出导入后再次导出，文本逐字节相同。
- **两个空数组是可用空图**，登记 `fresh` 成功；空文本、缺失端点、自依赖和三环都整次失败且不返回部分图。
- 现有导出格式、名称限制与查询行为保持不变；导入不引入新的名称清洗，也不经过命令行——命令行继续只有 `demo`、`version`、`help`。

## 数据集更名（Go 库）

`chainledger.Rename(graph, oldName, newName)` 把已登记的数据集 `oldName` 改名为 `newName`，同时保留它在血缘图中的依赖位置：节点自身的直接上游、直接下游列表原样保留，每个邻接节点里对旧名称的引用在原位置替换为新名称。不新增、不删除、不合并任何依赖边，各列表顺序不变，图中数据集总数不变。没有上游的根、没有下游的叶子和中间数据集都能更名。

更名规则：

- **更名作用于传入的当前图。** 成功后旧名称从图中消失，之后用旧名称查询按未登记处理（`dataset not found: <旧名>`）；新名称继承该节点原来的可达范围，`Impacts`、`Upstreams`、`Roots` 都按新名称重新计算——最短距离不变，但名称变化可能改变同距离结果的排序和等长说明路径的选择。之前已经返回的查询结果仍保留当时内容。
- **失败是原子的。** 原名称或新名称为空（`dataset name is required` / `new dataset name is required`）、原名称未登记（错误中指出该名称，如 `dataset not found: ghost`）、或新名称已被另一个数据集占用（错误中指出该名称，如 `dataset name already in use: leaf`）时返回错误，图中所有节点、关系和列表顺序完整保留。
- **名称按登记值精确匹配，区分大小写。** 与现有名称仅大小写不同的新名称是可用的新名，不冲突。
- **原名称与新名称相同且确实存在时，成功返回且图不变**；不存在的同名请求仍报告未登记。
- 更名后旧名称可以当作全新数据集重新 `Register`，更名后的节点也可以用新名称继续参与登记。

下面的示例与 [`examples/rename`](examples/rename/main.go) 中的可独立运行程序一致，可在仓库根目录执行 `go run ./examples/rename` 复现。程序从一张**空的内存血缘图**开始，用 `Register` 自己登记全部数据，不依赖任何外部服务。登记的是两条等长分支汇合到同一派生数据集 `report`、`report` 再派生 `view` 的血缘，另有一个与起点无关的 `isolated`；随后对其中一条分支里的中间数据集 `a` 做一次更名：

```
source ──> a ──> z ──┐
  │                  ├──> report ──> view
  └──> b ──> c ──────┘
```

特意先登记 `b` 侧分支，并在 `report` 的上游列表里把 `c` 写在 `z` 前面——这两点都不决定等长路径选哪条。新名称选 `m`：它在 Go 字符串顺序中排在 `b` 之后，能让更名前后等长说明路径从一条分支切到另一条分支。

```go
package main

import (
	"fmt"
	"os"

	"github.com/asdhoaiqqq/chainledger-governance/chainledger"
)

func main() {
	// 血缘图从一张空的内存 map 开始，由 Register 填充。
	graph := map[string]*chainledger.Lineage{}

	register := func(name string, parents ...string) {
		if err := chainledger.Register(graph, chainledger.Dataset{Name: name}, parents); err != nil {
			fmt.Fprintf(os.Stderr, "register %s: %v\n", name, err)
			os.Exit(1)
		}
	}

	register("source")
	register("b", "source")
	register("a", "source")
	register("c", "b")
	register("z", "a")
	register("report", "c", "z")
	register("view", "report")
	register("isolated")

	// edges 直接读图这个 map：parents 是直接上游，children 是直接下游；
	// 当前未登记的名称打印 <unregistered>，旧名换新名就在原位置可见。
	edges := func(names ...string) {
		for _, name := range names {
			if e, ok := graph[name]; ok {
				fmt.Printf("  %-8s parents=%v children=%v\n", name, e.Parents, e.Children)
			} else {
				fmt.Printf("  %-8s <unregistered>\n", name)
			}
		}
	}

	printImpacts := func(found []chainledger.Impact) {
		for _, im := range found {
			fmt.Printf("  %-8s distance=%d path=%v\n", im.Dataset, im.Distance, im.Path)
		}
	}
	printUpstreams := func(found []chainledger.Upstream) {
		for _, up := range found {
			fmt.Printf("  %-8s distance=%d path=%v\n", up.Dataset, up.Distance, up.Path)
		}
	}

	impacts := func(origin string) []chainledger.Impact {
		found, err := chainledger.Impacts(graph, origin)
		if err != nil {
			fmt.Printf("Impacts(%q) error: %v\n", origin, err)
			return nil
		}
		fmt.Printf("Impacts(%q) -> %d downstream dataset(s):\n", origin, len(found))
		printImpacts(found)
		return found
	}
	upstreams := func(target string) []chainledger.Upstream {
		found, err := chainledger.Upstreams(graph, target)
		if err != nil {
			fmt.Printf("Upstreams(%q) error: %v\n", target, err)
			return nil
		}
		fmt.Printf("Upstreams(%q) -> %d upstream dataset(s):\n", target, len(found))
		printUpstreams(found)
		return found
	}
	rename := func(oldName, newName string) {
		if err := chainledger.Rename(graph, oldName, newName); err != nil {
			fmt.Printf("Rename(%s, %s) refused: %v\n", oldName, newName, err)
			return
		}
		fmt.Printf("Rename(%s, %s) ok\n", oldName, newName)
	}

	// 旧名和将来的新名都列进邻接输出，每个阶段都有一个显示 <unregistered>。
	allNames := []string{"source", "a", "m", "b", "c", "z", "report", "view", "isolated"}

	// 更名前先在两个方向上各查一次并保存返回切片；每条 Path 都是独立拷贝，
	// 之后的更名和新查询不会改写这些结果。
	fmt.Println("== before the rename ==")
	edges(allNames...)
	fmt.Printf("datasets in graph: %d\n", len(graph))
	impactsBefore := impacts("source")
	upstreamsBefore := upstreams("report")

	// c 已被另一条分支上的数据集占用：新名称冲突，更名被拒绝，错误指出冲突名。
	rename("a", "c")
	fmt.Println("== after the refused rename ==")
	edges(allNames...)
	fmt.Printf("datasets in graph: %d\n", len(graph))
	impacts("source")
	upstreams("report")

	// 合法更名：m 在字符串顺序上排在 b 之后，等长路径的整路比较因此改选另一分支。
	rename("a", "m")
	fmt.Println("== after the rename ==")
	edges(allNames...)
	fmt.Printf("datasets in graph: %d\n", len(graph))

	// 更名前取得的切片仍是原名称、原路径；只有针对当前图重新查询才反映更名。
	fmt.Println("== results obtained before the rename, printed again now ==")
	fmt.Println(`Impacts("source") snapshot from before the rename:`)
	printImpacts(impactsBefore)
	fmt.Println(`Upstreams("report") snapshot from before the rename:`)
	printUpstreams(upstreamsBefore)

	fmt.Println("== results re-queried after the rename ==")
	impacts("source")
	upstreams("report")

	// 旧名称不是别名：更名成功后它就是未登记名称，两个查询方向都如此报告。
	impacts("a")
	upstreams("a")
}
```

实际输出（错误、邻接列表和查询结果都是程序真实打印，不是示意）：

```text
== before the rename ==
  source   parents=[] children=[b a]
  a        parents=[source] children=[z]
  m        <unregistered>
  b        parents=[source] children=[c]
  c        parents=[b] children=[report]
  z        parents=[a] children=[report]
  report   parents=[c z] children=[view]
  view     parents=[report] children=[]
  isolated parents=[] children=[]
datasets in graph: 8
Impacts("source") -> 6 downstream dataset(s):
  a        distance=1 path=[source a]
  b        distance=1 path=[source b]
  c        distance=2 path=[source b c]
  z        distance=2 path=[source a z]
  report   distance=3 path=[source a z report]
  view     distance=4 path=[source a z report view]
Upstreams("report") -> 5 upstream dataset(s):
  c        distance=1 path=[c report]
  z        distance=1 path=[z report]
  a        distance=2 path=[a z report]
  b        distance=2 path=[b c report]
  source   distance=3 path=[source a z report]
Rename(a, c) refused: dataset name already in use: c
== after the refused rename ==
  source   parents=[] children=[b a]
  a        parents=[source] children=[z]
  m        <unregistered>
  b        parents=[source] children=[c]
  c        parents=[b] children=[report]
  z        parents=[a] children=[report]
  report   parents=[c z] children=[view]
  view     parents=[report] children=[]
  isolated parents=[] children=[]
datasets in graph: 8
Impacts("source") -> 6 downstream dataset(s):
  a        distance=1 path=[source a]
  b        distance=1 path=[source b]
  c        distance=2 path=[source b c]
  z        distance=2 path=[source a z]
  report   distance=3 path=[source a z report]
  view     distance=4 path=[source a z report view]
Upstreams("report") -> 5 upstream dataset(s):
  c        distance=1 path=[c report]
  z        distance=1 path=[z report]
  a        distance=2 path=[a z report]
  b        distance=2 path=[b c report]
  source   distance=3 path=[source a z report]
Rename(a, m) ok
== after the rename ==
  source   parents=[] children=[b m]
  a        <unregistered>
  m        parents=[source] children=[z]
  b        parents=[source] children=[c]
  c        parents=[b] children=[report]
  z        parents=[m] children=[report]
  report   parents=[c z] children=[view]
  view     parents=[report] children=[]
  isolated parents=[] children=[]
datasets in graph: 8
== results obtained before the rename, printed again now ==
Impacts("source") snapshot from before the rename:
  a        distance=1 path=[source a]
  b        distance=1 path=[source b]
  c        distance=2 path=[source b c]
  z        distance=2 path=[source a z]
  report   distance=3 path=[source a z report]
  view     distance=4 path=[source a z report view]
Upstreams("report") snapshot from before the rename:
  c        distance=1 path=[c report]
  z        distance=1 path=[z report]
  a        distance=2 path=[a z report]
  b        distance=2 path=[b c report]
  source   distance=3 path=[source a z report]
== results re-queried after the rename ==
Impacts("source") -> 6 downstream dataset(s):
  b        distance=1 path=[source b]
  m        distance=1 path=[source m]
  c        distance=2 path=[source b c]
  z        distance=2 path=[source m z]
  report   distance=3 path=[source b c report]
  view     distance=4 path=[source b c report view]
Upstreams("report") -> 5 upstream dataset(s):
  c        distance=1 path=[c report]
  z        distance=1 path=[z report]
  b        distance=2 path=[b c report]
  m        distance=2 path=[m z report]
  source   distance=3 path=[source b c report]
Impacts("a") error: dataset not found: a
Upstreams("a") error: dataset not found: a
```

要点：

- **更名保留了原来的依赖位置。** 邻接列表直接读自图这个 map：更名后 `source` 的下游仍是两项，只是原位置的 `a` 换成 `m`（`children=[b m]`），`z` 的上游由 `[a]` 变为 `[m]`；`m` 自身仍是 `parents=[source] children=[z]`，汇合点 `report` 的 `parents=[c z]`、`view` 的关系都没有动。旧名 `a` 显示 `<unregistered>`，而图中数据集数量更名前后都是 8。
- **最短距离一个都没变。** 更名后 `Impacts("source")` 仍是 6 个下游，`m` 接替 `a` 在距离 1，`z` 仍在距离 2，`report` 距离 3、`view` 距离 4；`Upstreams("report")` 仍是 5 个上游，各距离也保持不变。变化的只有名称：同距离结果按当前名称排序（距离 1 由 `a, b` 变为 `b, m`）。
- **等长说明路径切到了另一条分支，而这来自整条路径逐跳比较。** 更名前汇合点 `report` 的两条等长最短路径是 `[source a z report]` 与 `[source b c report]`，从起点开始逐跳比较，第 1 跳 `a < b`，所以选 a 侧路线，`view` 在这条路线上再延伸一跳。更名后两条路线变成 `[source m z report]` 与 `[source b c report]`，仍是第 1 跳分出胜负，这次 `b < m`，于是改选 b 侧路线；`Upstreams("report")` 里共享来源 `source` 的路径同样切成 `[source b c report]`。如果只比较汇合点的直接上游，会因为 `c < z` 永远选 b 侧路线，根本观察不到更名带来的这次翻转——所以比较的是从路径起点开始的整条路径，不能给人“只比较汇合点直接上游”的印象。
- **两个查询方向的路径都沿实际派生方向书写。** `Impacts` 的路径从起点 `source` 写到受影响数据集；`Upstreams` 的路径同样从来源写到被查询的 `report`（如距离 1 的 `[c report]`、距离 3 的 `[source b c report]`），不会因为是“向上查”就倒着写。
- **旧结果保留原内容，之后的查询才反映新名称。** 更名前取得的 `impactsBefore`、`upstreamsBefore` 在更名后再次打印，仍是 `a` 和 `[source a z report]` 等原内容；只有更名后重新调用 `Impacts`、`Upstreams` 得到的结果才出现 `m` 和 b 侧路径。更名成功后再用旧名查询，两个方向都明确返回 `dataset not found: a`——旧名称不是仍可查询的别名。
- **新名称被占用的更名尝试被原子拒绝。** `Rename(a, c)` 因为 `c` 已被另一分支上的数据集登记而失败，错误直接指出冲突名称（`dataset name already in use: c`）；拒绝后邻接列表、数据集数量和两个方向的查询结果与更名前**完全一致**（仍是 `children=[b a]`、路径仍走 a 侧），随后合法的 `Rename(a, m)` 才生效。
- 更名只通过 Go 库调用；命令行继续只有 `demo`、`version`、`help` 三个固定入口，用途和行为不变。

## 移除数据集登记（Go 库）

`chainledger.Unregister(graph, name)` 从当前内存血缘图中移除一个数据集的登记，成功返回 `nil`，失败返回说明原因的错误。移除作用于调用方传入的这张图，与 `Register`、`Rename`、`Impacts`、`Upstreams` 操作的是同一个普通内存 map。

移除规则：

- **只允许移除没有任何直接下游的数据集。** 只要还有别的数据集直接依赖它（无论它自己有没有上游），就返回错误并保留该登记，错误信息指出待移除名称并说明它仍有下游，如 `dataset detail cannot be unregistered while it still has direct downstreams`。要移除中间数据集，必须先移除它下游链路上的数据集，使它成为叶子。
- **清理覆盖被移除数据集的全部直接上游。** 每个直接上游的下游列表都会去掉该名称，剩余名称的相对顺序不变；上游本身以及它的其他下游仍然存在。即使被移除数据集同时依赖多个来源，每个来源的反向引用都会被解除。没有直接连接该数据集的节点，其上下游列表保持原样。
- **既无上游也无下游的独立数据集也能直接移除。**
- **成功后名称彻底从图中消失。** 用已移除名称调用 `Impacts` 或 `Upstreams` 都按未登记处理（`dataset not found: <名称>`），图中不会留下仍可查询的空节点；再次 `Unregister` 同一名称同样按未登记报错。
- **失败是原子的。** 被拒绝后图中所有节点、关系和列表顺序与调用前完全一致，不会出现上游反向边已断开而节点仍在的部分修改。
- **名称为空**返回缺少名称的错误（`dataset name is required`）；**非空名称未登记**时错误中指出该名称（`dataset not found: ghost`），对空图或 nil 图调用也按未登记处理。名称按登记值精确匹配，区分大小写。
- 移除不影响其余数据集的可达性：从剩余节点查询影响范围和来源时，最短距离和说明路径按移除后的现存关系重新计算，含义与原来一致。

例如 `raw` 派生 `detail`，`detail` 再派生 `report` 和 `view`，且 `report` 还依赖独立来源 `extra`。移除叶子 `report` 后：`report` 的登记从图中消失，`detail` 的下游列表从 `[report view]` 变为 `[view]`（`view` 的位置和依赖关系保留），`extra` 也解除对 `report` 的反向引用但保留自己的其他下游。从 `raw` 查询影响范围仍能得到 `detail` 和 `view`，但不再包含 `report`；从 `view` 追查来源仍能到达 `detail` 和 `raw`，最短距离和说明路径保持原有含义。

下面的程序与 [`examples/unregister`](examples/unregister/main.go) 一致，可在仓库根目录执行 `go run ./examples/unregister` 复现：

```go
graph := map[string]*chainledger.Lineage{}
// raw -> detail -> report/view；report 还依赖 extra，extra 另有下游 otherchild。
register("raw")
register("detail", "raw")
register("report", "detail")
register("view", "detail")
register("extra")
register("otherchild", "extra")
register("report", "detail", "extra")

// detail 仍有直接下游 report 和 view：拒绝，图保持原样。
if err := chainledger.Unregister(graph, "detail"); err != nil {
	fmt.Println(err) // dataset detail cannot be unregistered while it still has direct downstreams
}

// report 是叶子，即使有两个上游也可以移除。
if err := chainledger.Unregister(graph, "report"); err != nil {
	fmt.Println(err)
	return
}
// detail 的下游只剩 view；extra 不再有 report 这个下游，但 otherchild 仍在。
```

实际输出（错误与血缘边都是程序真实打印，不是示意）：

```text
Unregister(detail) refused: dataset detail cannot be unregistered while it still has direct downstreams
after the refused request:
  raw     parents=[] children=[detail]
  detail  parents=[raw] children=[report view]
  report  parents=[detail extra] children=[]
  view    parents=[detail] children=[]
  extra   parents=[] children=[otherchild report]
  otherchild parents=[extra] children=[]
Unregister(report) ok
after removing report:
  raw     parents=[] children=[detail]
  detail  parents=[raw] children=[view]
  report  <unregistered>
  view    parents=[detail] children=[]
  extra   parents=[] children=[otherchild]
  otherchild parents=[extra] children=[]
Impacts("raw") -> [detail view]
Upstreams("view") -> [detail raw]
Unregister(report) refused: dataset not found: report
Impacts("report") error: dataset not found: report
Unregister(otherchild) ok
Unregister() refused: dataset name is required
Unregister(ghost) refused: dataset not found: ghost
Unregister(view) ok
Unregister(detail) ok
```

要点：

- 被拒绝的那次调用之后，`detail`、`report`、`view` 的关系原封不动，`extra` 仍有 `[otherchild report]` 两个下游——失败不会断开任何反向边。
- 成功移除 `report` 后，`detail` 的下游列表保留剩余名称的相对顺序（`[view]`），两个直接上游 `detail` 和 `extra` 都解除了对 `report` 的引用；`view` 的位置、对 `detail` 的依赖，以及 `raw -> detail -> view` 的最短距离与说明路径都不受影响。
- 已移除的名称在所有查询和再次移除中一律按未登记处理；空名称报缺少名称，空图或 nil 图上的任何非空名称都报未登记。
- 移除只提供 Go 库入口；命令行继续只有 `demo`、`version`、`help`。

## 技术方向

blockchain-indexer, data-lineage, onchain-data, data-pipeline, data-indexer, analytics

## 运行要求

Go 1.26，仅使用标准库，全部行为可在本机 CPU 上离线复现。
