# 链上数据治理与血缘工作台

## 用途

多源链上数据接入、规范化、质量规则、血缘谱系、结构演进、快照版本比较与可复验导出。

本仓库是可持续演进的自托管 Go 应用。领域核心位于 `chainledger/`，命令入口位于 `cmd/chainledger/`。

```bash
go run ./cmd/chainledger demo
go run ./cmd/chainledger version
go test ./...
```

命令行只提供 `demo`、`version`、`help` 三个固定入口：`demo` 运行一段内置的登记与血缘演示，`version` 打印版本号。命令行**不**接收数据集名称作为查询参数。要判断“某个数据集变化会影响哪些派生数据集”，请直接在 Go 代码中调用 `chainledger` 库的 `Impacts` 函数，方式见下文「查询下游影响（Go 库）」一节；完整可运行示例位于 [`examples/impacts`](examples/impacts/main.go)。反过来，要追查“一份派生数据来自哪些上游”，调用同一库的 `Upstreams` 函数，见下文「查询上游来源（Go 库）」一节，示例位于 [`examples/upstreams`](examples/upstreams/main.go)。数据集如何登记、同名登记会替换什么，则见下文「登记数据集与维护血缘（Go 库）」一节，示例位于 [`examples/register`](examples/register/main.go)。

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

## 数据集更名（Go 库）

`chainledger.Rename(graph, oldName, newName)` 把已登记的数据集 `oldName` 改名为 `newName`，同时保留它在血缘图中的依赖位置：节点自身的直接上游、直接下游列表原样保留，每个邻接节点里对旧名称的引用在原位置替换为新名称。不新增、不删除、不合并任何依赖边，各列表顺序不变，图中数据集总数不变。没有上游的根、没有下游的叶子和中间数据集都能更名。

更名规则：

- **更名作用于传入的当前图。** 成功后旧名称从图中消失，之后用旧名称查询按未登记处理（`dataset not found: <旧名>`）；新名称继承该节点原来的可达范围，`Impacts`、`Upstreams`、`Roots` 都按新名称重新计算——最短距离不变，但名称变化可能改变同距离结果的排序和等长说明路径的选择。之前已经返回的查询结果仍保留当时内容。
- **失败是原子的。** 原名称或新名称为空（`dataset name is required` / `new dataset name is required`）、原名称未登记（错误中指出该名称，如 `dataset not found: ghost`）、或新名称已被另一个数据集占用（错误中指出该名称，如 `dataset name already in use: leaf`）时返回错误，图中所有节点、关系和列表顺序完整保留。
- **名称按登记值精确匹配，区分大小写。** 与现有名称仅大小写不同的新名称是可用的新名，不冲突。
- **原名称与新名称相同且确实存在时，成功返回且图不变**；不存在的同名请求仍报告未登记。
- 更名后旧名称可以当作全新数据集重新 `Register`，更名后的节点也可以用新名称继续参与登记。

```go
graph := map[string]*chainledger.Lineage{}
// ... 先登记 source -> a, source -> b, report 依赖 a 和 b ...
if err := chainledger.Rename(graph, "a", "z"); err != nil {
	fmt.Println(err) // 失败原因，如 dataset not found: a
	return
}
// report 的直接上游中原来 a 的位置变为 z；从 source 查询 report 的
// 最短说明路径按新名称重新计算为 [source b report]（b < z）。
```

## 技术方向

blockchain-indexer, data-lineage, onchain-data, data-pipeline, data-indexer, analytics

## 运行要求

Go 1.26，仅使用标准库，全部行为可在本机 CPU 上离线复现。
