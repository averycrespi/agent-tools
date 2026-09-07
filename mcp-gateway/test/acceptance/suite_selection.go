package acceptance

import (
	"context"
	"fmt"
	"go/ast"
	"go/build"
	"go/build/constraint"
	"go/doc"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
)

type SuiteTest struct {
	Package  string   `json:"package"`
	File     string   `json:"file"`
	Name     string   `json:"name"`
	Owner    string   `json:"owner"`
	Tags     []string `json:"tags"`
	Selected bool     `json:"selected"`
}

type SuiteGoCommand struct {
	Argv  []string    `json:"argv"`
	Tests []SuiteTest `json:"tests"`
}

type SuiteInventory struct {
	GOOS   string      `json:"goos"`
	GOARCH string      `json:"goarch"`
	Tests  []SuiteTest `json:"tests"`
}

func suiteCommandArgv(id string) []string {
	return []string{"go", "run", "./test/acceptance/cmd", "run-suite", id}
}

func suiteTimeout(id string) time.Duration {
	switch id {
	case "test-security", "test-browser-privacy", "frontend-static-tests":
		return 30 * time.Second
	case "test-browser-visual":
		return time.Minute
	case "test-browser-accessibility", "test-browser-cross":
		return 45 * time.Second
	case "test-browser-workflows":
		return 3 * time.Minute
	case "test-stress", "test-frontend-development-browser":
		return 2 * time.Minute
	case "test-keyring-native":
		return 10 * time.Second
	default:
		return 5 * time.Minute
	}
}

func suiteOwner(path string, tags map[string]bool) (string, []string, error) {
	if len(tags) > 1 && (len(tags) != 2 || !tags["e2e"] || !tags["browser"]) {
		return "", nil, fmt.Errorf("conflicting suite build tags: %s", path)
	}
	if tags["browser"] {
		if !tags["e2e"] {
			return "", nil, fmt.Errorf("browser test lacks e2e context: %s", path)
		}
		owner := "test-browser-workflows"
		switch filepath.Base(path) {
		case "browser_secret_storage_privacy_test.go":
			owner = "test-browser-privacy"
		case "browser_visual_responsive_test.go":
			owner = "test-browser-visual"
		case "browser_accessibility_test.go":
			owner = "test-browser-accessibility"
		case "browser_cross_compatibility_test.go":
			owner = "test-browser-cross"
		case "frontend_development_test.go", "frontend_control_plane_test.go":
			owner = "test-frontend-development-browser"
		}
		return owner, []string{"e2e", "browser"}, nil
	}
	for _, tag := range []string{"e2e", "security", "stress", "frontend", "keyringnative", "integration"} {
		if tags[tag] {
			owner := "test-" + tag
			switch tag {
			case "frontend":
				owner = "frontend-static-tests"
			case "keyringnative":
				owner = "test-keyring-native"
			case "e2e":
				if strings.HasSuffix(path, "_harness_test.go") || strings.HasSuffix(path, "/harness_self_test.go") || strings.HasSuffix(path, "/process_supervisor_test.go") {
					owner = "test-harness"
				}
			}
			return owner, []string{tag}, nil
		}
	}
	pkg := filepath.ToSlash(filepath.Dir(path))
	switch pkg {
	case "internal/contract", "internal/strictjson", "internal/discovery", "internal/credentialauthority", "internal/events", "internal/lifecycle":
		return "test-unit", nil, nil
	case "test/material":
		return "test-material", nil, nil
	case "internal/testutil":
		return "test-harness", nil, nil
	}
	if strings.HasPrefix(pkg, "test/acceptance") || strings.HasPrefix(pkg, "test/keyringnative") {
		return "test-harness", nil, nil
	}
	if strings.HasPrefix(pkg, "internal/") || strings.HasPrefix(pkg, "cmd/") {
		return "test-integration", []string{"integration"}, nil
	}
	return "", nil, fmt.Errorf("test package has no suite owner: %s", pkg)
}

func suiteBuildTags(file *ast.File) (map[string]bool, error) {
	tags := make(map[string]bool)
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if !constraint.IsGoBuild(comment.Text) {
				continue
			}
			expr, err := constraint.Parse(comment.Text)
			if err != nil {
				return nil, err
			}
			var unknown string
			expr.Eval(func(tag string) bool {
				switch tag {
				case "e2e", "browser", "security", "stress", "frontend", "keyringnative", "integration":
					tags[tag] = true
				case "aix", "android", "darwin", "dragonfly", "freebsd", "hurd", "illumos", "ios", "js", "linux", "netbsd", "openbsd", "plan9", "solaris", "wasip1", "windows", "unix", "cgo", "race", "gc", "gccgo", "386", "amd64", "arm", "arm64", "loong64", "mips", "mipsle", "mips64", "mips64le", "ppc64", "ppc64le", "riscv64", "s390x", "wasm":
				default:
					if !strings.HasPrefix(tag, "go1.") {
						unknown = tag
					}
				}
				return false
			})
			if unknown != "" {
				return nil, fmt.Errorf("unowned build tag %s", unknown)
			}
		}
	}
	return tags, nil
}

func runnableSuiteNames(file *ast.File) []string {
	var names []string
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv != nil || function.Name.Name == "TestMain" {
			continue
		}
		for _, prefix := range []string{"Test", "Fuzz"} {
			name := function.Name.Name
			if strings.HasPrefix(name, prefix) && (len(name) == len(prefix) || !unicode.IsLower([]rune(name[len(prefix):])[0])) {
				names = append(names, name)
			}
		}
	}
	for _, example := range doc.Examples(file) {
		if example.Output != "" || example.EmptyOutput {
			names = append(names, "Example"+example.Name)
		}
	}
	return names
}

func suiteContext(goos, goarch string, tags []string) build.Context {
	ctx := build.Default
	ctx.GOOS, ctx.GOARCH = goos, goarch
	ctx.BuildTags = append(append([]string(nil), tags...), "race")
	return ctx
}

func DiscoverSuiteInventory(moduleRoot, goos, goarch string) (SuiteInventory, error) {
	inventory := SuiteInventory{GOOS: goos, GOARCH: goarch}
	err := filepath.WalkDir(moduleRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != moduleRoot && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "node_modules" || entry.Name() == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		tags, err := suiteBuildTags(file)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		names := runnableSuiteNames(file)
		if len(names) == 0 {
			return nil
		}
		relative, err := filepath.Rel(moduleRoot, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		owner, buildTags, err := suiteOwner(relative, tags)
		if err != nil {
			return err
		}
		ctx := suiteContext(goos, goarch, buildTags)
		selected, err := ctx.MatchFile(filepath.Dir(path), filepath.Base(path))
		if err != nil {
			return err
		}
		if !selected {
			platformMatch := false
			for _, platform := range []string{"linux", "darwin", "windows", "freebsd", "openbsd", "netbsd", "dragonfly", "solaris", "illumos", "aix", "plan9", "android", "ios", "js", "wasip1"} {
				for _, architecture := range []string{"amd64", "arm64", "386", "arm", "ppc64", "ppc64le", "mips", "mipsle", "mips64", "mips64le", "riscv64", "s390x", "loong64", "wasm"} {
					candidate := suiteContext(platform, architecture, buildTags)
					matches, err := candidate.MatchFile(filepath.Dir(path), filepath.Base(path))
					if err != nil {
						return err
					}
					platformMatch = platformMatch || matches
				}
			}
			if !platformMatch {
				return fmt.Errorf("unselected tagged tests: %s", relative)
			}
		}
		for _, name := range names {
			inventory.Tests = append(inventory.Tests, SuiteTest{Package: "./" + filepath.ToSlash(filepath.Dir(relative)), File: relative, Name: name, Owner: owner, Tags: buildTags, Selected: selected})
		}
		return nil
	})
	if err != nil {
		return inventory, err
	}
	return inventory, nil
}

func PlanSuite(moduleRoot, id string, inventory SuiteInventory, repeats int) ([]SuiteGoCommand, error) {
	if repeats < 1 || (id != "test-stress" && repeats != 1) {
		return nil, fmt.Errorf("only stress permits repeated execution")
	}
	groups := make(map[string][]SuiteTest)
	for _, test := range inventory.Tests {
		if test.Owner == id && test.Selected {
			groups[strings.Join(test.Tags, ",")] = append(groups[strings.Join(test.Tags, ",")], test)
		}
	}
	if len(groups) == 0 {
		return nil, fmt.Errorf("empty or unknown suite %s for %s/%s", id, inventory.GOOS, inventory.GOARCH)
	}
	var keys []string
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var commands []SuiteGoCommand
	for _, tags := range keys {
		tests := groups[tags]
		command := suiteGoCommand(id, tags, tests, repeats)
		if err := validateSuiteCommand(moduleRoot, inventory, command); err == nil && id != "test-stress" {
			commands = append(commands, command)
			continue
		}
		packages := make(map[string][]SuiteTest)
		for _, test := range tests {
			packages[test.Package] = append(packages[test.Package], test)
		}
		var paths []string
		for path := range packages {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			command := suiteGoCommand(id, tags, packages[path], repeats)
			if err := validateSuiteCommand(moduleRoot, inventory, command); err != nil {
				return nil, err
			}
			commands = append(commands, command)
		}
	}
	return commands, nil
}

func suiteGoCommand(id, tags string, tests []SuiteTest, repeats int) SuiteGoCommand {
	names, packages := make([]string, 0, len(tests)), make([]string, 0, len(tests))
	for _, test := range tests {
		names = append(names, regexp.QuoteMeta(test.Name))
		packages = append(packages, test.Package)
	}
	sort.Strings(names)
	sort.Strings(packages)
	timeout := suiteTimeout(id)
	if id == "test-stress" && tests[0].Package != "./internal/grantrequests" {
		timeout = 90 * time.Second
	}
	argv := []string{"go", "test", "-race", fmt.Sprintf("-count=%d", repeats), "-timeout=" + timeout.String()}
	if tags != "" {
		argv = append(argv, "-tags="+tags)
	}
	argv = append(argv, "-run", "^("+strings.Join(slices.Compact(names), "|")+")$")
	argv = append(argv, slices.Compact(packages)...)
	return SuiteGoCommand{Argv: argv, Tests: tests}
}

func validateSuiteCommand(moduleRoot string, inventory SuiteInventory, command SuiteGoCommand) error {
	wanted := make(map[string]bool)
	packages := make(map[string]bool)
	for _, test := range command.Tests {
		key := test.Package + "/" + test.Name
		if wanted[key] {
			return fmt.Errorf("duplicate executable identity %s", key)
		}
		wanted[key], packages[test.Package] = true, true
	}
	var selector string
	var tags []string
	for index, argument := range command.Argv {
		if argument == "-run" {
			selector = command.Argv[index+1]
		}
		if strings.HasPrefix(argument, "-tags=") {
			tags = strings.Split(strings.TrimPrefix(argument, "-tags="), ",")
		}
	}
	pattern, err := regexp.Compile(selector)
	if err != nil || selector == "" {
		return fmt.Errorf("invalid suite selector")
	}
	ctx := suiteContext(inventory.GOOS, inventory.GOARCH, tags)
	seen := make(map[string]bool)
	for _, test := range inventory.Tests {
		if !packages[test.Package] || !pattern.MatchString(test.Name) {
			continue
		}
		selected, err := ctx.MatchFile(filepath.Join(moduleRoot, filepath.Dir(test.File)), filepath.Base(test.File))
		if err != nil {
			return err
		}
		if !selected {
			continue
		}
		key := test.Package + "/" + test.Name
		if !wanted[key] || seen[key] {
			return fmt.Errorf("unintended duplicate or foreign selection %s", key)
		}
		seen[key] = true
	}
	if len(seen) != len(wanted) {
		return fmt.Errorf("suite selector omits executable tests")
	}
	return nil
}

type SuiteExecutor struct{}

func (SuiteExecutor) Run(ctx context.Context, root string, command Command) ([]byte, error) {
	// The outer acceptance executor owns the inherited ledger, including its live parent command.
	return runOSCommand(ctx, root, command, false)
}

func RunSuite(ctx context.Context, root, id string, repeats int, executor Executor) error {
	if id == "test-keyring-native" && os.Getenv("MCP_GATEWAY_KEYRING_NATIVE") != "1" {
		return fmt.Errorf("native execution requires the isolated native wrapper")
	}
	moduleRoot := filepath.Join(root, "mcp-gateway")
	inventory, err := DiscoverSuiteInventory(moduleRoot, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	commands, err := PlanSuite(moduleRoot, id, inventory, repeats)
	if err != nil {
		return err
	}
	for _, command := range commands {
		commandContext, cancel := context.WithTimeout(ctx, suiteTimeout(id)+time.Minute)
		_, err := executor.Run(commandContext, moduleRoot, Command{CheckName: id, Name: command.Argv[0], Arguments: command.Argv[1:], Timeout: suiteTimeout(id)})
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}
