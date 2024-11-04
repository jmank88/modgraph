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
	flag.Parse()
	if *prefix == "" {
		slog.Error("Must provide -prefix")
		os.Exit(1)
	}

	repos := make(map[string][]string)
	seen := make(map[string]struct{})
	deps := make(map[string][]string)
	sawMod := func(path string) {
		if _, ok := seen[path]; !ok {
			seen[path] = struct{}{}

			repo, _, _ := strings.Cut(path, "/")
			if _, ok = repos[repo]; !ok {
				repos[repo] = []string{path}
			} else {
				repos[repo] = append(repos[repo], path)
			}
		}
	}
	for mod, dep := range scanDeps(os.Stdin) {
		if !strings.HasPrefix(mod, *prefix) || !strings.HasPrefix(dep, *prefix) {
			if *verbose {
				slog.Info("Prefix mismatch", "mod", mod, "dep", dep, "prefix", *prefix)
			}
			continue
		}

		modPath, depPath := strings.TrimPrefix(mod, *prefix), strings.TrimPrefix(dep, *prefix)
		sawMod(modPath)
		sawMod(depPath)
		if d, ok := deps[modPath]; ok {
			if !slices.Contains(d, depPath) {
				deps[modPath] = append(d, depPath)
			}
		} else {
			deps[modPath] = []string{depPath}
		}
	}

	keys := slices.Collect(maps.Keys(deps))
	slices.Sort(keys)
	slices.Reverse(keys)
	for _, m := range keys {
		ds := deps[m]
		slices.Sort(ds)
		for _, d := range ds {
			fmt.Printf("\t%s --> %s\n", m, d)
		}
		repo, _, _ := strings.Cut(m, "/")
		fmt.Printf("\tclick %s href \"https://%s%s\"\n", m, *prefix, repo)
	}

	keys = slices.Collect(maps.Keys(repos))
	slices.Sort(keys)
	slices.Reverse(keys)
	subgraphs := []string{}
	for _, repo := range keys {
		mods := repos[repo]
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
