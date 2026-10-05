# 链上数据治理与血缘工作台

## 用途

多源链上数据接入、规范化、质量规则、血缘谱系、结构演进、快照版本比较与可复验导出。

本仓库是可持续演进的自托管 Go 应用。领域核心位于 `chainledger/`，命令入口位于 `cmd/chainledger/`。

```bash
go run ./cmd/chainledger demo
go run ./cmd/chainledger version
go test ./...
```

命令行只提供 `demo`、`version`、`help` 三个固定入口：`demo` 运行一段内置的登记与血缘演示，`version` 打印版本号。命令行**不**接收数据集名称作为查询参数。要判断“某个数据集变化会影响哪些派生数据集”，请直接在 Go 代码中调用 `chainledger` 库的 `Impacts` 函数，方式见下文「查询下游影响（Go 库）」一节；完整可运行示例位于 [`examples/impacts`](examples/impacts/main.go)。分析变化时若想把某些已登记数据集设为传播截止点、查看停止传播后的影响范围，调用同一库的 `ImpactsWithCutoffs` 函数，见下文「带传播截止名单的下游影响（Go 库）」一节，示例位于 [`examples/impacts-cutoffs`](examples/impacts-cutoffs/main.go)。反过来，要追查“一份派生数据来自哪些上游”，调用同一库的 `Upstreams` 函数，见下文「查询上游来源（Go 库）」一节，示例位于 [`examples/upstreams`](examples/upstreams/main.go)。要比较两份派生数据**最近**共同来自哪些数据集，调用同一库的 `CommonUpstreams` 函数，见下文「查询两份数据集的最近共同上游（Go 库）」一节，示例位于 [`examples/common-upstreams`](examples/common-upstreams/main.go)。数据集如何登记、同名登记会替换什么，则见下文「登记数据集与维护血缘（Go 库）」一节，示例位于 [`examples/register`](examples/register/main.go)。已登记数据集如何更名、更名怎样保留依赖位置，见下文「数据集更名（Go 库）」一节，示例位于 [`examples/rename`](examples/rename/main.go)。要从血缘图中移除一个数据集的登记，调用 `chainledger.Unregister`，见下文「移除数据集登记（Go 库）」一节，示例位于 [`examples/unregister`](examples/unregister/main.go)。

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

## 查询两份数据集的最近共同上游（Go 库）

`Upstreams` 只能分别列出每份派生数据的来源；要比较两份派生数据“最近共同来自哪些数据集”，调用：

```go
common, err := chainledger.CommonUpstreams(graph, first, second)
```

`first`、`second` 是两个已登记数据集名称，按当前血缘图分别沿 parent 边向上追溯。每个结果是一条 `chainledger.CommonUpstream`：

- `Dataset`：两个目标都能沿上游关系追到的共同来源名称。**目标自身也算共同来源。**
- `DistanceToFirst` / `DistanceToSecond`：该来源到第一个、第二个目标的**最短依赖边数**（直接上游距离 1，目标命名自身时距离 0）。两侧距离各自独立计算，可以不同。
- `PathToFirst` / `PathToSecond`：对应的最短路径，沿实际派生方向**从该来源写到对应目标**，首尾都包含在内。

### “最近”的判定：后面还有共同来源的，排除

共同来源之间可能本身也有上下游关系。规则是：**如果一个共同来源的直接或间接下游中还存在另一个共同来源，它就不出现在结果里。**判定只看“下游能不能到达另一个共同来源”，不看那条路线是否同时通向两个目标，也不看距离长短。

例如 `raw` 派生 `a`、`b`，`a`、`b` 都参与 `left` 和 `right` 的派生：

```
raw ──> a ──┬──> left
  │         │
  └──> b ──┴──> right
```

查询 `CommonUpstreams(graph, "left", "right")` 返回 `a`、`b`，**排除 `raw`**：`a`、`b` 都是 `raw` 之后的共同来源。即使之后让 `raw` 同时**直接**参与两个目标的派生、到它们的边数更短，`raw` 仍然被排除——“最近”规则先于距离，不因短路边而放宽。

要点：

- 结果可以有多个，也可能只有一个；**不按距离大小挑选**，也不限定为没有上游的根。
- 每个来源只出现一次；结果按**来源名称的 Go 字符串顺序**排列（不是按距离、登记顺序或直接上游列表顺序）。
- 两个目标相同（如 `CommonUpstreams(graph, "left", "left")`）时，唯一结果是 `left` 自身，两侧距离均为 0，两条路径都只包含 `left`。
- 一个目标本身就是另一个目标的上游时（如 `("a", "left")`），唯一结果是这个上游目标，它到自身一侧的距离为 0、路径只含自身；更老的共同来源仍被排除。
- 两个已登记目标没有任何共同来源时，查询成功并返回**非 nil 的空列表**。

### 两侧距离与说明路径

两侧距离分别取该来源到每个目标的最短边数，因此同一来源可能出现 `(2, 1)` 这样不对称的距离。路径沿实际派生方向从来源写到对应目标；当从某来源到某个目标存在多条同样短的路线时，选路规则与 `Upstreams` 完全一致：**从来源开始逐跳比较整条路径名称**，取 Go 字符串顺序较小的一条。登记顺序和直接上游列表顺序都不影响结果。

下例中 `r2` 经 `y1`、`y2` 两条等长路线派生 `mixL`（登记时故意把 `y2` 写在前面），同时直接派生 `mixR`：查询 `("mixL", "mixR")` 时 `r2` 是唯一共同来源，到 `mixL` 的路径选词典序更小的 `[r2 y1 mixL]`，到 `mixR` 为 `[r2 mixR]`。

### 示例与真实输出

完整可运行示例位于 [`examples/common-upstreams`](examples/common-upstreams/main.go)，可在仓库根目录执行 `go run ./examples/common-upstreams` 复现：

```text
CommonUpstreams("left", "right") -> 2 common source(s):
  a    to-first  distance=1 path=[a left]
       to-second distance=1 path=[a right]
  b    to-first  distance=1 path=[b left]
       to-second distance=1 path=[b right]
CommonUpstreams("left", "right") -> 2 common source(s):
  a    to-first  distance=1 path=[a left]
       to-second distance=1 path=[a right]
  b    to-first  distance=1 path=[b left]
       to-second distance=1 path=[b right]
CommonUpstreams("left", "left") -> 1 common source(s):
  left to-first  distance=0 path=[left]
       to-second distance=0 path=[left]
CommonUpstreams("a", "left") -> 1 common source(s):
  a    to-first  distance=0 path=[a]
       to-second distance=1 path=[a left]
CommonUpstreams("left", "oright") -> 0 common source(s)
CommonUpstreams("mixL", "mixR") -> 1 common source(s):
  r2   to-first  distance=2 path=[r2 y1 mixL]
       to-second distance=1 path=[r2 mixR]
CommonUpstreams("", "right") error: dataset name is required
CommonUpstreams("left", "ghost") error: dataset not found: ghost
```

第二段 `left/right` 结果是在给两个目标都增加对 `raw` 的直接依赖之后再次查询的：`raw` 到两个目标的边数变为 1，但结果仍是 `a`、`b`——更短的直接边不能让被排除的老来源回来。交换两个目标参数会对调每条记录的两侧字段（距离与路径），来源集合与排序不变。

### 失败与边界行为

- 两个目标**按输入顺序校验**：第一个目标名称为空报 `dataset name is required`；第一个合法而第二个为空时同样报缺少名称；名称非空但未登记时，错误指出该名称（如 `dataset not found: ghost`）。对空图或 nil 图查询任何非空名称都按未登记处理。
- 任何校验失败都返回 nil 结果，不会返回部分来源。
- 名称按登记值精确匹配，区分大小写。
- 查询是只读的：不改变节点、关系或列表顺序；每条记录的两侧路径都是彼此独立的拷贝（同一记录的两条路径也不共享底层数组），修改它们不会影响图、其他记录或之后的查询。
- 现有登记、更名、移除与 `Impacts`、`Upstreams` 查询入口的签名和行为保持不变；命令行继续只有 `demo`、`version`、`help`。

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
