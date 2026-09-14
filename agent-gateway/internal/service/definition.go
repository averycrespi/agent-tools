// Package service owns only the canonical per-user LaunchAgent, not Gateway storage.
package service

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

const Label = "dev.agent-tools.agent-gateway"
const utilityPath = "/usr/bin:/bin:/usr/sbin:/sbin"
const definitionLimit = 1 << 20

// Settings are literal installed selections; Output and JSON preserve old installer argv.
type Settings struct {
	Binary       string   `json:"binary"`
	DataDir      string   `json:"data_dir"`
	Listen       string   `json:"listen"`
	AllowedHosts []string `json:"allowed_hosts"`
	LogLevel     string   `json:"log_level,omitempty"`
	Output       string   `json:"output,omitempty"`
	JSON         bool     `json:"json,omitempty"`
}

type definition struct {
	Settings
	Stdout string
	Stderr string
	argv   []string
}

func validLiteral(s string) bool {
	return s != "" && len(s) <= 4096 && !strings.ContainsFunc(s, unicode.IsControl)
}

func absolute(s string) bool { return validLiteral(s) && filepath.IsAbs(s) && filepath.Clean(s) == s }

func (s Settings) validate() error {
	if !absolute(s.Binary) || !absolute(s.DataDir) {
		return errors.New("binary and data-dir must be clean absolute paths without control characters")
	}
	addr, err := netip.ParseAddrPort(s.Listen)
	if err != nil || !addr.Addr().Is4() || !addr.Addr().IsLoopback() || addr.Port() == 0 || addr.String() != s.Listen {
		return errors.New("listen must be an exact numeric IPv4 loopback authority, for example 127.0.0.1:8210")
	}
	if len(s.AllowedHosts) > 128 {
		return errors.New("too many allowed hosts")
	}
	for _, host := range s.AllowedHosts {
		if _, ok := contract.NormalizeHostname(host); !ok {
			return errors.New("allowed-host must be an ASCII DNS hostname without a port")
		}
	}
	if s.LogLevel != "" && !slices.Contains([]string{"warn", "info", "debug"}, s.LogLevel) {
		return errors.New("log-level must be warn, info, or debug")
	}
	if s.Output != "" && !slices.Contains([]string{"human", "json"}, s.Output) || s.JSON && s.Output != "" {
		return errors.New("unsupported or conflicting output selection")
	}
	return nil
}

func (s Settings) arguments() []string {
	args := []string{s.Binary, "serve", "--data-dir", s.DataDir, "--listen", s.Listen}
	for _, host := range s.AllowedHosts {
		args = append(args, "--allowed-host", host)
	}
	if s.LogLevel != "" {
		args = append(args, "--log-level", s.LogLevel)
	}
	if s.Output != "" {
		args = append(args, "--output", s.Output)
	}
	if s.JSON {
		args = append(args, "--json")
	}
	return args
}

func parseArguments(args []string) (Settings, error) {
	var s Settings
	if len(args) < 6 || args[1] != "serve" || args[2] != "--data-dir" || args[4] != "--listen" {
		return s, errors.New("unsupported argument prefix")
	}
	s.Binary, s.DataDir, s.Listen = args[0], args[3], args[5]
	seen := map[string]bool{}
	for i := 6; i < len(args); i++ {
		flag := args[i]
		if seen[flag] && flag != "--allowed-host" {
			return s, errors.New("duplicate service selection")
		}
		seen[flag] = true
		if flag == "--json" {
			s.JSON = true
			continue
		}
		i++
		if i >= len(args) {
			return s, errors.New("incomplete service selection")
		}
		switch flag {
		case "--allowed-host":
			s.AllowedHosts = append(s.AllowedHosts, args[i])
		case "--log-level":
			s.LogLevel = args[i]
		case "--output":
			s.Output = args[i]
		default:
			return s, errors.New("unknown service argument; reconcile the installed plist explicitly")
		}
	}
	return s, s.validate()
}

// The closed plist tree rejects unknown keys, duplicate keys and unsupported types.
// encoding/xml performs literal escaping and never resolves external entities.
type plistNode struct {
	XMLName  xml.Name
	Attrs    []xml.Attr  `xml:",any,attr"`
	Text     string      `xml:",chardata"`
	Children []plistNode `xml:",any"`
}

func node(kind, text string, children ...plistNode) plistNode {
	return plistNode{XMLName: xml.Name{Local: kind}, Text: text, Children: children}
}
func pair(key string, value plistNode) []plistNode { return []plistNode{node("key", key), value} }
func (d definition) encode() ([]byte, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	args := d.argv
	if args == nil {
		args = d.arguments()
	}
	var values []plistNode
	for _, arg := range args {
		values = append(values, node("string", arg))
	}
	var children []plistNode
	for _, entry := range []struct {
		key   string
		value plistNode
	}{
		{"Label", node("string", Label)}, {"ProgramArguments", node("array", "", values...)},
		{"RunAtLoad", node("true", "")}, {"KeepAlive", node("true", "")}, {"ExitTimeOut", node("integer", "30")},
		{"EnvironmentVariables", node("dict", "", pair("PATH", node("string", utilityPath))...)},
		{"StandardOutPath", node("string", d.Stdout)}, {"StandardErrorPath", node("string", d.Stderr)},
	} {
		children = append(children, pair(entry.key, entry.value)...)
	}
	root := node("plist", "", node("dict", "", children...))
	root.Attrs = []xml.Attr{{Name: xml.Name{Local: "version"}, Value: "1.0"}}
	data, err := xml.MarshalIndent(root, "", "  ")
	return append([]byte(xml.Header), append(data, '\n')...), err
}

func dictionary(n plistNode) (map[string]plistNode, error) {
	if n.XMLName.Local != "dict" || strings.TrimSpace(n.Text) != "" || len(n.Children)%2 != 0 {
		return nil, errors.New("invalid plist dictionary")
	}
	result := map[string]plistNode{}
	for i := 0; i < len(n.Children); i += 2 {
		key := n.Children[i]
		if key.XMLName.Local != "key" || len(key.Children) != 0 || key.Text == "" {
			return nil, errors.New("invalid plist key")
		}
		if _, ok := result[key.Text]; ok {
			return nil, errors.New("duplicate plist key")
		}
		result[key.Text] = n.Children[i+1]
	}
	return result, nil
}

func decode(data []byte) (d definition, err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("unsupported installed definition: %w; reconcile the private canonical plist explicitly before management", err)
		}
	}()
	if len(data) > definitionLimit {
		return d, errors.New("plist exceeds bound")
	}
	// Bound depth and tokens before unmarshalling the recursive tree.
	decoder := xml.NewDecoder(bytes.NewReader(data))
	depth, tokens, roots := 0, 0, 0
	for {
		token, e := decoder.Token()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return d, e
		}
		tokens++
		if tokens > 4096 {
			return d, errors.New("plist token bound exceeded")
		}
		switch value := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
				if roots != 1 {
					return d, errors.New("trailing plist document")
				}
			}
			if value.Name.Space != "" || (depth > 0 && len(value.Attr) != 0) {
				return d, errors.New("unsupported plist namespace or attributes")
			}
			if depth == 0 && (len(value.Attr) != 1 || value.Attr[0].Name.Local != "version" || value.Attr[0].Value != "1.0") {
				return d, errors.New("unsupported plist version")
			}
			depth++
			if depth > 5 {
				return d, errors.New("plist depth exceeded")
			}
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(value)) != "" {
				return d, errors.New("text outside plist")
			}
		}
	}
	var root plistNode
	if err = xml.Unmarshal(data, &root); err != nil {
		return d, err
	}
	if root.XMLName.Local != "plist" || len(root.Children) != 1 || strings.TrimSpace(root.Text) != "" {
		return d, errors.New("invalid plist root")
	}
	dict, err := dictionary(root.Children[0])
	if err != nil {
		return d, err
	}
	if len(dict) != 8 {
		return d, errors.New("unknown or missing plist fields")
	}
	text := func(key string) (string, error) {
		n, ok := dict[key]
		if !ok || n.XMLName.Local != "string" || len(n.Children) != 0 || !validLiteral(n.Text) {
			return "", errors.New("invalid plist string")
		}
		return n.Text, nil
	}
	label, err := text("Label")
	if err != nil || label != Label {
		return d, errors.New("noncanonical label")
	}
	for _, key := range []string{"RunAtLoad", "KeepAlive"} {
		n := dict[key]
		if n.XMLName.Local != "true" || len(n.Children) != 0 || strings.TrimSpace(n.Text) != "" {
			return d, errors.New("unsupported launch policy")
		}
	}
	exit := dict["ExitTimeOut"]
	allowance, e := strconv.Atoi(exit.Text)
	if e != nil || allowance != 30 || exit.XMLName.Local != "integer" || len(exit.Children) != 0 {
		return d, errors.New("unsupported shutdown allowance")
	}
	env, e := dictionary(dict["EnvironmentVariables"])
	if e != nil || len(env) != 1 || env["PATH"].XMLName.Local != "string" || env["PATH"].Text != utilityPath || len(env["PATH"].Children) != 0 {
		return d, errors.New("unsupported environment")
	}
	if d.Stdout, err = text("StandardOutPath"); err != nil {
		return d, err
	}
	if d.Stderr, err = text("StandardErrorPath"); err != nil {
		return d, err
	}
	args := dict["ProgramArguments"]
	if args.XMLName.Local != "array" || strings.TrimSpace(args.Text) != "" {
		return d, errors.New("invalid argument array")
	}
	for _, arg := range args.Children {
		if arg.XMLName.Local != "string" || len(arg.Children) != 0 || !validLiteral(arg.Text) {
			return d, errors.New("invalid literal argument")
		}
		d.argv = append(d.argv, arg.Text)
	}
	d.Settings, err = parseArguments(d.argv)
	return d, err
}
