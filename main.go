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
	prefix  = flag.String("prefix", "", "prefix to filter")
	verbose = flag.Bool("v", false, "verbose mode")
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

	fmt.Println("\n\tclassDef outline stroke-dasharray:6,fill:none;")
	fmt.Printf("\tclass %s outline\n", strings.Join(subgraphs, ","))
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

func (s *state) transitiveReduction() {
	noPath := make(map[string]map[string]struct{}) // [path][path]
	for m, deps := range s.deps {
		s.deps[m] = slices.DeleteFunc(deps, func(d string) bool {
			// BFS for indirect paths to d, tracking nodes we touch along the way
			var touched []string
			children := slices.DeleteFunc(slices.Clone(deps), func(s string) bool { return s == d }) // exclude direct
			for len(children) > 0 {
				touched = append(touched, children...)
				var next []string
				for _, child := range children {
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
