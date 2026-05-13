package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"
)

var (
	prefix       = flag.String("prefix", "", "prefix to filter")
	verbose      = flag.Bool("v", false, "verbose mode")
	detectCycles = flag.Bool("detect-cycles", false,
		"fail if the module-name graph (versions collapsed) contains cycles")
)

func main() {
	os.Exit(Main())
}

func Main() int {
	flag.Parse()
	if *prefix == "" {
		slog.Error("Must provide -prefix")
		return 1
	}

	deps := newState()
	for mod, dep := range scanDeps(os.Stdin) {
		if !strings.HasPrefix(mod, *prefix) || !strings.HasPrefix(dep, *prefix) {
			if *verbose {
				slog.Info("Prefix mismatch", "mod", mod, "dep", dep, "prefix", *prefix)
			}
			continue
		}

		modPath, depPath := strings.TrimPrefix(mod, *prefix), strings.TrimPrefix(dep, *prefix)
		deps.add(modPath, depPath)
	}

	if *detectCycles {
		cycles := deps.findCycles()
		for _, c := range cycles {
			slog.Error("Cycle detected", "path", strings.Join(c, " -> "))
		}
		if len(cycles) > 0 {
			return 2
		}
	}

	deps.transitiveReduction()

	for m, ds := range deps.depsSorted() {
		if len(ds) == 0 {
			fmt.Printf("\t%s\n", m)
		} else {
			slices.Sort(ds)
			for _, d := range ds {
				fmt.Printf("\t%s --> %s\n", m, d)
			}
		}
		repo, _, _ := strings.Cut(m, "/")
		fmt.Printf("\tclick %s href \"https://%s%s\"\n", m, *prefix, repo)
	}

	var subgraphs []string
	for repo, mods := range deps.reposSorted() {
		if len(mods) <= 1 {
			if *verbose {
				slog.Info("Skipping repo with single module", "repo", repo, "mods", mods)
			}
			continue
		}
		subgraphs = append(subgraphs, repo+"-repo")

		slices.Sort(mods)
		fmt.Printf("\n\tsubgraph %[1]s-repo[%[1]s]\n", repo)
		for _, mod := range mods {
			fmt.Printf("\t\t %s\n", mod)
		}
		fmt.Println("\tend")
		fmt.Printf("\tclick %[1]s-repo href \"https://%s%[1]s\"\n", repo, *prefix)
	}

	if len(subgraphs) > 0 {
		fmt.Println("\n\tclassDef outline stroke-dasharray:6,fill:none;")
		fmt.Printf("\tclass %s outline\n", strings.Join(subgraphs, ","))
	}
	return 0
}

// scanDeps returns parsed modules from go mod graph output
// Example: module@version dep-module@version
func scanDeps(r io.Reader) iter.Seq2[string, string] {
	return func(yield func(string, string) bool) {
		scanner := bufio.NewScanner(r)
		var total, invalid int
		for scanner.Scan() {
			total++
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			parts := strings.Split(line, " ")
			if len(parts) != 2 {
				invalid++
				slog.Warn("Invalid line: expected one space", "spaces", len(parts)-1, "line", line)
				continue
			}

			mod, _, _ := strings.Cut(parts[0], "@")
			dep, _, _ := strings.Cut(parts[1], "@")
			if !yield(mod, dep) {
				return
			}
		}
		if *verbose {
			slog.Info("Scanned lines", "total", total, "invalid", invalid)
		}
		if err := scanner.Err(); err != nil {
			slog.Error("Failed to read input", "err", err)
			os.Exit(1)
		}
	}
}

type state struct {
	deps  map[string][]string // [path][]path
	seen  map[string]struct{} // [path]
	repos map[string][]string // [repo][]path
}

func newState() *state {
	return &state{
		deps:  make(map[string][]string),
		seen:  make(map[string]struct{}),
		repos: make(map[string][]string),
	}
}

func (s *state) add(mod, dep string) {
	s.sawMod(mod)
	s.sawMod(dep)
	if d, ok := s.deps[mod]; ok {
		if !slices.Contains(d, dep) {
			s.deps[mod] = append(d, dep)
		}
	} else {
		s.deps[mod] = []string{dep}
	}
}

func (s *state) sawMod(path string) {
	if _, ok := s.seen[path]; !ok {
		s.seen[path] = struct{}{}

		repo, _, _ := strings.Cut(path, "/")
		if _, ok = s.repos[repo]; !ok {
			s.repos[repo] = []string{path}
		} else {
			s.repos[repo] = append(s.repos[repo], path)
		}
	}
}

// findCycles returns cycles between deps
// deploy@v2.0.0 -> common@v2.0.0
// common@v2.0.0 -> deploy@v0.5.0
// deploy@v0.5.0 -> common@v1.0.0
//
// back-edge DFS algorithm
// edge (u, v), v is visited and an ancestor of u = we have a cycle
//
// Caveat: reports one cycle per back-edge, not every simple cycle. A cycle is
// hidden when its target has already been visited and popped via another path.
// Example: edges {a->b, a->c, b->c, c->a} — DFS at a descends a->b->c, sees
// c->a as a back-edge and reports [a,b,c]. The direct a->c->a cycle is missed
// because c is no longer on the stack when the loop at a reaches it. Fixing
// the reported cycle and re-running surfaces the hidden one, eventually, we'll find
// all the cycles.
// // Alternatively, Tarjan algorithm can be used to detect all the cycles in one run
// but it's slightly more complex to understand.
func (s *state) findCycles() [][]string {
	var cycles [][]string
	visited := make(map[string]struct{})
	// current stack of paths, ex.: a@v2.0 -> b@v1.0 -> c@v0.5
	stack := make([]string, 0)
	// positions on stack of paths, ex: {a:0, b:1, c:2}
	onStack := make(map[string]int)

	var dfs func(string)
	dfs = func(n string) {
		visited[n] = struct{}{}
		stack = append(stack, n)
		onStack[n] = len(stack) - 1

		children := slices.Clone(s.deps[n])
		slices.Sort(children)
		for _, d := range children {
			if _, ok := visited[d]; !ok {
				dfs(d)
			} else if idx, ok := onStack[d]; ok {
				// cycle detected, clone [first_ancenstor:current] which has back-edge
				cycles = append(cycles, slices.Clone(stack[idx:]))
			}
		}

		delete(onStack, n)
		stack = stack[:len(stack)-1]
	}

	deps := slices.Sorted(maps.Keys(s.deps))
	for _, dep := range deps {
		if _, ok := visited[dep]; !ok {
			dfs(dep)
		}
	}
	// append the ancestor for printing, ex.: a->b->c->a
	for ci := range cycles {
		cycles[ci] = append(cycles[ci], cycles[ci][0])
	}
	return cycles
}

func (s *state) transitiveReduction() {
	noPath := make(map[string]map[string]struct{}) // [path][path]

	// Iterate modules in a stable order
	mods := slices.Sorted(maps.Keys(s.deps))
	for _, m := range mods {
		deps := s.deps[m]
		s.deps[m] = slices.DeleteFunc(deps, func(d string) bool {
			// BFS for indirect paths to d, tracking nodes we touch along the way
			var touched []string
			// visited guards against cycles in the graph
			visited := make(map[string]struct{})
			children := slices.DeleteFunc(slices.Clone(deps), func(s string) bool { return s == d }) // exclude direct
			for len(children) > 0 {
				var next []string
				for _, child := range children {
					if _, ok := visited[child]; ok {
						continue
					}
					visited[child] = struct{}{}
					touched = append(touched, child)

					if child == d {
						if *verbose {
							slog.Info("Excluding transitive edge", "mod", m, "dep", d)
						}
						return true // found an indirect path, so remove this edge
					}
					if none, ok := noPath[child]; ok {
						if _, ok2 := none[d]; ok2 {
							if *verbose {
								slog.Info("Skipping known disconnected nodes", "mod", child, "dep", d)
							}
							continue // known not to lead to d
						}
					}
					next = append(next, s.deps[child]...)
				}
				children = next
			}
			// none of the touched nodes have direct paths
			for _, t := range touched {
				none, ok := noPath[t]
				if !ok {
					none = make(map[string]struct{})
					noPath[t] = none
				}
				none[d] = struct{}{}
			}
			return false
		})
	}
}

func (s *state) depsSorted() iter.Seq2[string, []string] {
	return func(yield func(string, []string) bool) {
		keys := slices.Collect(maps.Keys(s.seen))
		slices.Sort(keys)
		for _, m := range keys {
			if !yield(m, s.deps[m]) {
				return
			}
		}
	}
}

func (s *state) reposSorted() iter.Seq2[string, []string] {
	return func(yield func(string, []string) bool) {
		keys := slices.Collect(maps.Keys(s.repos))
		slices.Sort(keys)
		for _, repo := range keys {
			if !yield(repo, s.repos[repo]) {
				return
			}
		}
	}
}
