//go:build e2e

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type options struct {
	module          string
	start           func(string, []string, []string) (*child, error)
	remove          func(string) error
	configureClient func(*client)
	fixturePrefix   []string
	childEnv        []string
	startupTimeout  time.Duration
}

func defaultOptions() options {
	_, source, _, _ := runtime.Caller(0)
	return options{module: filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..")), start: startChild, remove: os.RemoveAll, startupTimeout: 60 * time.Second}
}
func validAuthority(address string) bool {
	addr, err := netip.ParseAddrPort(address)
	return err == nil && addr.Addr().Is4() && addr.Addr().IsLoopback() && addr.Port() != 0 && addr.String() == address
}
func readBearer(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("protected credential read failed")
	}
	return strings.TrimSpace(string(data)), nil
}
func waitUntil(ctx context.Context, children []*child, label string, check func() (bool, error)) error {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if ctx.Err() != nil {
			return errors.New(label + " deadline exceeded or interrupted")
		}
		for _, c := range children {
			if err := c.check(ctx); err != nil {
				return err
			}
		}
		if ok, err := check(); err != nil {
			return err
		} else if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New(label + " deadline exceeded or interrupted")
		case <-tick.C:
		}
	}
}
func run(ctx context.Context, listen, dataset string, out io.Writer, opts options) (result error) {
	probe, err := net.Listen("tcp4", listen)
	if err != nil {
		return errors.New("demo listener unavailable; choose another --listen authority")
	}
	if err = probe.Close(); err != nil {
		return errors.New("listener probe close failed")
	}
	root, err := os.MkdirTemp("", "mcp-gateway-demo-")
	if err != nil {
		return errors.New("demo root creation failed")
	}
	children := []*child{}
	defer func() {
		failed := false
		for i := len(children) - 1; i >= 0; i-- {
			if err := children[i].finish(context.Background(), 0, false); err != nil {
				failed = true
			}
		}
		if failed {
			result = errors.New("cleanup unconfirmed; retained " + root)
			return
		}
		if err := opts.remove(root); err != nil {
			result = errors.New("cleanup unconfirmed; retained " + root)
		}
	}()
	home := filepath.Join(root, "home")
	if err = os.Mkdir(home, 0700); err != nil {
		return errors.New("demo home creation failed")
	}
	binary := filepath.Join(root, "mcp-gateway")
	if _, err = fmt.Fprintln(out, "Building demo Gateway from "+opts.module); err != nil {
		return errors.New("demo output failed")
	}
	build, err := opts.start("Gateway build", []string{"go", "-C", opts.module, "build", "-mod=readonly", "-tags=e2e", "-o", binary, "./cmd/mcp-gateway"}, os.Environ())
	if err != nil {
		return err
	}
	children = append(children, build)
	if err = build.finish(ctx, 300*time.Second, true); err != nil {
		return err
	}
	children = children[:0]
	env := []string{"PATH=/usr/bin:/bin", "HOME=" + home, "TMPDIR=" + root, "XDG_CONFIG_HOME=" + filepath.Join(home, "config"), "XDG_DATA_HOME=" + filepath.Join(home, "data"), "MCP_GATEWAY_E2E_ACCOUNT_HOME=" + home}
	env = append(env, opts.childEnv...)
	command := func(label string, args []string) error {
		if ctx.Err() != nil {
			return errors.New("interrupted before " + label)
		}
		argv := append([]string{binary}, args...)
		argv = append(argv, "--data-dir", filepath.Join(root, "data"))
		c, startErr := opts.start(label, argv, env)
		if startErr != nil {
			return startErr
		}
		children = append(children, c)
		if finishErr := c.finish(ctx, 15*time.Second, true); finishErr != nil {
			return finishErr
		}
		children = children[:len(children)-1]
		return nil
	}
	if err = command("initialize", []string{"initialize", "--secret-output", filepath.Join(root, "admin-bearer")}); err != nil {
		return err
	}
	gateway, err := opts.start("Gateway", []string{binary, "serve", "--data-dir", filepath.Join(root, "data"), "--listen", listen}, env)
	if err != nil {
		return err
	}
	children = append(children, gateway)
	seedCtx, cancel := context.WithTimeout(ctx, opts.startupTimeout)
	defer cancel()
	bearer, err := readBearer(filepath.Join(root, "admin-bearer"))
	if err != nil {
		return err
	}
	c := newClient(seedCtx, listen, bearer)
	if opts.configureClient != nil {
		opts.configureClient(c)
	}
	err = waitUntil(seedCtx, children, "Gateway readiness", func() (bool, error) {
		response, _ := c.request("GET", "/readyz", nil, nil, 200, "")
		ready := c.err == nil && response != nil
		c.err = nil
		return ready, nil
	})
	if err != nil {
		return err
	}
	endpoints := map[string]string{}
	if dataset == "curated" {
		executable, exeErr := os.Executable()
		if exeErr != nil {
			return errors.New("demo executable lookup failed")
		}
		for _, kind := range []string{"workshop", "library"} {
			endpoint := filepath.Join(root, kind+"-port")
			argv := append([]string{executable}, opts.fixturePrefix...)
			argv = append(argv, "--fixture", kind, endpoint)
			fixture, startErr := opts.start(kind+" fixture", argv, env)
			if startErr != nil {
				return startErr
			}
			children = append(children, fixture)
			err = waitUntil(seedCtx, children, "fixture startup", func() (bool, error) {
				data, readErr := os.ReadFile(endpoint)
				if errors.Is(readErr, os.ErrNotExist) {
					return false, nil
				}
				if readErr != nil {
					return false, errors.New("fixture endpoint read failed")
				}
				port := string(data)
				n, parseErr := strconv.Atoi(port)
				if parseErr != nil || n < 1 || n > 65535 {
					return false, nil
				}
				endpoints[kind] = port
				return true, nil
			})
			if err != nil {
				return err
			}
		}
		if err = seed(seedCtx, c, root, endpoints, children, command); err != nil {
			return err
		}
	} else {
		for _, collection := range []string{"servers", "principals", "grants", "grant-requests", "invocations"} {
			response := c.get(collection)
			c.require(value(response, "items") != nil && len(rows(response, "items")) == 0, "empty dataset contains records")
		}
		if c.err != nil {
			return c.err
		}
	}
	processes := map[string]int{}
	for _, child := range children {
		if err = child.check(ctx); err != nil {
			return err
		}
		processes[child.label] = child.cmd.Process.Pid
	}
	manifest, _ := json.Marshal(object{"dataset": dataset, "listen": listen, "processes": processes, "fixtures": endpoints})
	if err = writePrivate(filepath.Join(root, "ready.json"), manifest); err != nil {
		return errors.New("ready manifest publication failed")
	}
	var output strings.Builder
	fmt.Fprintf(&output, "\nDemo Gateway ready (%s)\n  URL:          http://%s/\n  Run root:     %s\n  Data:         %s\n  Admin bearer: %s\n", dataset, listen, root, filepath.Join(root, "data"), filepath.Join(root, "admin-bearer"))
	if dataset == "curated" {
		fmt.Fprintf(&output, "  Explorer:     %s (all demo tools)\n  Reader:       %s (echo/lookup; pending arithmetic request)\n  Disabled:     %s (authentication denied)\nUse an MCP client with an Authorization bearer read from its protected file at /mcp.\nTools: demo_workshop.echo/add/controlled_error and demo_library.lookup.\n", filepath.Join(root, "explorer-bearer"), filepath.Join(root, "reader-bearer"), filepath.Join(root, "disabled-bearer"))
	}
	fmt.Fprintf(&output, "Separate Vite: MCP_GATEWAY_UI_GATEWAY=http://%s npm run ui:dev\nStop with Ctrl-C; all temporary state will be removed. Relaunch for fresh data; DEMO_DATASET=empty for empty state.\n", listen)
	if _, err = io.WriteString(out, output.String()); err != nil {
		return errors.New("demo output failed")
	}
	return waitUntil(ctx, children, "demo lifetime", func() (bool, error) { return false, nil })
}
func entry(args []string, out, stderr io.Writer, opts options) int {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	if len(args) == 3 && args[0] == "--fixture" {
		if err := serveFixture(ctx, args[1], args[2]); err != nil {
			_, _ = fmt.Fprintln(stderr, "serve-demo: fixture failed")
			return 1
		}
		return 0
	}
	flags := flag.NewFlagSet("serve-demo", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	listen := flags.String("listen", "127.0.0.1:8211", "canonical numeric IPv4 loopback listener")
	dataset := flags.String("dataset", "curated", "curated or empty")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		if _, err := fmt.Fprintln(out, "serve-demo [--dataset curated|empty] [--listen 127.0.0.1:8211]"); err != nil {
			return 1
		}
		return 0
	} else if err != nil || flags.NArg() != 0 || !validAuthority(*listen) || (*dataset != "curated" && *dataset != "empty") {
		_, _ = fmt.Fprintln(stderr, "serve-demo: use --dataset curated|empty and --listen canonical numeric IPv4 loopback:port")
		return 2
	}
	if err := run(ctx, *listen, *dataset, out, opts); err != nil {
		_, _ = fmt.Fprintln(stderr, "serve-demo: "+err.Error())
		return 1
	}
	return 0
}
func main() { os.Exit(entry(os.Args[1:], os.Stdout, os.Stderr, defaultOptions())) }
