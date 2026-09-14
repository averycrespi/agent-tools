package service

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

type wordSpan struct{ start, end int }
type argumentState struct {
	index int
	serve bool
	root  string
}

// ps loses argv boundaries. Consider all bounded closed-grammar interpretations
// instead of pretending whitespace or a root substring is an identity. A possible
// selected owner wins; ambiguous implicit/relative ownership never means absence.
func relevantCommand(command string, d definition) (bool, error) {
	argv := d.argv
	if argv == nil {
		argv = d.arguments()
	}
	if command == strings.Join(argv, " ") {
		return true, nil
	}
	arguments := ""
	found := false
	// comm was already matched to the exact executable; argv[0] may be a PATH name.
	for _, name := range []string{d.Binary, filepath.Base(d.Binary)} {
		if tail, ok := strings.CutPrefix(command, name+" "); ok {
			arguments = tail
			found = true
			break
		}
		if command == name {
			return false, nil
		}
	}
	if !found {
		return false, errors.New("candidate executable argv is unknown")
	}
	if len(arguments) > 64*1024 {
		return false, errors.New("candidate argument observation bound exceeded")
	}
	if strings.ContainsAny(arguments, "\t\r\n") {
		return false, errors.New("candidate argv contains ambiguous controls")
	}
	var words []wordSpan
	for i := 0; i < len(arguments); {
		if arguments[i] == ' ' {
			i++
			continue
		}
		start := i
		for i < len(arguments) && arguments[i] != ' ' {
			i++
		}
		words = append(words, wordSpan{start, i})
		if len(words) > 512 {
			return false, errors.New("candidate argument observation bound exceeded")
		}
	}
	if len(words) == 0 {
		return false, nil
	}
	queue := []argumentState{{}}
	visited := map[argumentState]bool{}
	unknown, valid, exceeded := false, false, false
	enqueue := func(state argumentState) {
		if len(queue) >= 4096 {
			exceeded = true
			return
		}
		queue = append(queue, state)
	}
	for cursor := 0; cursor < len(queue); cursor++ {
		state := queue[cursor]
		if visited[state] {
			continue
		}
		visited[state] = true
		if state.index == len(words) {
			if !state.serve {
				valid = true
				continue
			}
			if len(state.root) > 4096 {
				continue
			}
			if !filepath.IsAbs(state.root) {
				unknown = true
				continue
			}
			if sameRoot(state.root, d.DataDir) {
				return true, nil
			}
			valid = true
			continue
		}
		span := words[state.index]
		word := arguments[span.start:span.end]
		if !state.serve && !strings.HasPrefix(word, "-") {
			if word != "serve" {
				valid = true
				continue
			}
			state.serve = true
			state.index++
			enqueue(state)
			continue
		}
		flag, value, equals := strings.Cut(word, "=")
		if flag == "--help" || flag == "-h" {
			switch {
			case !equals || value == "true":
				valid = true
			case value == "false":
				state.index++
				enqueue(state)
			}
			continue
		}
		if flag == "--json" && state.serve {
			if equals && value != "true" && value != "false" {
				continue
			}
			state.index++
			enqueue(state)
			continue
		}
		if flag == "--data-dir" {
			first := state.index + 1
			start := 0
			switch {
			case equals:
				first = state.index
				start = span.start + len(flag) + 1
			case first < len(words):
				start = words[first].start
			default:
				continue
			}
			// Preserve internal/trailing spaces: only the one argv-rendering separator
			// before the next word is excluded from a candidate literal value.
			for next := first + 1; next <= len(words); next++ {
				end := len(arguments)
				if next < len(words) {
					nextWord := arguments[words[next].start:words[next].end]
					nextFlag, _, _ := strings.Cut(nextWord, "=")
					if state.serve && !strings.HasPrefix(nextWord, "--") && nextFlag != "-h" {
						continue
					}
					end = words[next].start - 1
				}
				if end <= start {
					continue
				}
				enqueue(argumentState{index: next, serve: state.serve, root: arguments[start:end]})
			}
			continue
		}
		if !state.serve {
			continue
		}
		next := state.index + 1
		if !equals {
			if next >= len(words) {
				continue
			}
			value = arguments[words[next].start:words[next].end]
			next++
		}
		if !observationFlag(flag, value) {
			continue
		}
		state.index = next
		enqueue(state)
	}
	if exceeded || unknown || !valid {
		return false, errors.New("candidate installation selection is ambiguous or exceeds the observation bound")
	}
	return false, nil
}
func sameRoot(candidate, selected string) bool {
	if filepath.Clean(candidate) == filepath.Clean(selected) {
		return true
	}
	// Detect alternate filesystem spellings (including case-insensitive aliases)
	// using metadata only, never reading private installation contents.
	left, e := os.Stat(candidate)
	if e != nil {
		return false
	}
	right, e := os.Stat(selected)
	return e == nil && os.SameFile(left, right)
}
func observationFlag(flag, value string) bool {
	switch flag {
	case "--listen":
		addr, e := netip.ParseAddrPort(value)
		return e == nil && addr.Addr().Is4() && addr.Addr().IsLoopback() && addr.Port() != 0
	case "--allowed-host":
		_, ok := contract.NormalizeHostname(value)
		return ok
	case "--log-level":
		return value == "warn" || value == "info" || value == "debug"
	case "--output":
		return value == "human" || value == "json"
	default:
		return false
	}
}
