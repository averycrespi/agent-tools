package main

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

type examplePlistNode struct {
	XMLName xml.Name
	Text    string             `xml:",chardata"`
	Nodes   []examplePlistNode `xml:",any"`
}

func TestOperatorSupervisorExamplesUseSupportedForegroundArguments(t *testing.T) {
	assertArgs := func(args []string) {
		t.Helper()
		require.True(t, filepath.IsAbs(args[0]))
		require.Equal(t, "agent-gateway", filepath.Base(args[0]))
		require.Equal(t, "serve", args[1])
		command, _, err := newRootCmd().Find(args[1:])
		require.NoError(t, err)
		require.Equal(t, "serve", command.Name())
		require.NoError(t, command.ParseFlags(args[2:]))
		root, err := command.Flags().GetString("data-dir")
		require.NoError(t, err)
		require.True(t, filepath.IsAbs(root))
		require.Equal(t, filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(args[0]))), ".local", "share", "agent-gateway"), root)
		proxy, err := selectedHTTPProxy(command, false)
		require.NoError(t, err)
		require.Equal(t, "127.0.0.1:8212", proxy)
		require.NotContains(t, strings.Join(args, " "), "$")
	}
	t.Run("launchd", func(t *testing.T) {
		data, err := os.ReadFile("../../examples/launchd/agent-gateway.plist")
		require.NoError(t, err)
		var plist examplePlistNode
		require.NoError(t, xml.Unmarshal(data, &plist))
		require.Equal(t, "plist", plist.XMLName.Local)
		require.Len(t, plist.Nodes, 1)
		dict := plist.Nodes[0]
		require.Equal(t, "dict", dict.XMLName.Local)
		require.Zero(t, len(dict.Nodes)%2)
		values := map[string]examplePlistNode{}
		for i := 0; i < len(dict.Nodes); i += 2 {
			require.Equal(t, "key", dict.Nodes[i].XMLName.Local)
			key := dict.Nodes[i].Text
			require.NotContains(t, values, key)
			values[key] = dict.Nodes[i+1]
		}
		require.Equal(t, "dev.agent-tools.agent-gateway", values["Label"].Text)
		require.Equal(t, "true", values["RunAtLoad"].XMLName.Local)
		require.Equal(t, "true", values["KeepAlive"].XMLName.Local)
		require.Equal(t, "array", values["ProgramArguments"].XMLName.Local)
		var args []string
		for _, value := range values["ProgramArguments"].Nodes {
			require.Equal(t, "string", value.XMLName.Local)
			args = append(args, value.Text)
		}
		assertArgs(args)
		for _, name := range []string{"StandardOutPath", "StandardErrorPath"} {
			require.True(t, filepath.IsAbs(values[name].Text))
			require.Contains(t, values[name].Text, "/Users/alice/Library/Logs/agent-gateway/")
		}
		seconds, err := strconv.Atoi(values["ExitTimeOut"].Text)
		require.NoError(t, err)
		require.Equal(t, 30, seconds)
		require.Greater(t, time.Duration(seconds)*time.Second, contract.GracefulShutdownDeadline)
	})
	t.Run("systemd", func(t *testing.T) {
		data, err := os.ReadFile("../../examples/systemd/agent-gateway.service")
		require.NoError(t, err)
		values := map[string]string{}
		section := ""
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if strings.HasPrefix(line, "[") {
				section = line
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			require.True(t, ok)
			key = section + key
			require.NotContains(t, values, key)
			values[key] = value
		}
		assertArgs(strings.Fields(values["[Service]ExecStart"]))
		for key, value := range map[string]string{"Type": "exec", "UMask": "0077", "Restart": "on-failure", "RestartSec": "5s", "KillSignal": "SIGTERM", "KillMode": "mixed", "TimeoutStopSec": "30s", "SendSIGKILL": "yes", "StandardOutput": "journal", "StandardError": "journal"} {
			require.Equal(t, value, values["[Service]"+key], key)
		}
		require.NotContains(t, values, "[Service]User")
		require.NotContains(t, values, "[Service]ExecStop")
		require.Equal(t, "default.target", values["[Install]WantedBy"])
	})
}
