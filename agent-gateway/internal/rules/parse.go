package rules

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// templateRE matches valid template expressions: ${secrets.<id>},
// ${agent.name}, ${agent.id}. Used at load time to validate syntax only.
var templateRE = regexp.MustCompile(`\$\{(secrets\.[A-Za-z_][A-Za-z0-9_]*|agent\.(?:name|id))\}`)

// invalidTemplateRE matches any ${...} expression.
var invalidTemplateRE = regexp.MustCompile(`\$\{[^}]*\}`)

// topLevelSchema describes the top-level structure of a rules file.
var topLevelSchema = &hcl.BodySchema{
	Blocks: []hcl.BlockHeaderSchema{
		{Type: "rule", LabelNames: []string{"name"}},
	},
}

// ruleBodySchema describes the attributes and blocks inside a `rule` block.
var ruleBodySchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{
		{Name: "agents", Required: false},
		{Name: "verdict", Required: true},
	},
	Blocks: []hcl.BlockHeaderSchema{
		{Type: "match"},
		{Type: "inject"},
	},
}

// matchBodySchema describes the attributes and blocks inside a `match` block.
var matchBodySchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{
		{Name: "host", Required: false},
		{Name: "method", Required: false},
		{Name: "path", Required: false},
		{Name: "headers", Required: false},
	},
	Blocks: []hcl.BlockHeaderSchema{
		{Type: "json_body"},
		{Type: "form_body"},
		{Type: "text_body"},
	},
}

// injectBodySchema describes the attributes inside an `inject` block.
var injectBodySchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{
		{Name: "replace_header", Required: false},
		{Name: "remove_header", Required: false},
	},
}

// jsonBodySchema describes the structure of a `json_body` block.
var jsonBodySchema = &hcl.BodySchema{
	Blocks: []hcl.BlockHeaderSchema{
		{Type: "jsonpath", LabelNames: []string{"path"}},
	},
}

// formBodySchema describes the structure of a `form_body` block.
var formBodySchema = &hcl.BodySchema{
	Blocks: []hcl.BlockHeaderSchema{
		{Type: "field", LabelNames: []string{"name"}},
	},
}

// textBodySchema describes the structure of a `text_body` block.
var textBodySchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{
		{Name: "matches", Required: true},
	},
}

// matcherBlockSchema describes a single `jsonpath` or `field` block body.
var matcherBlockSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{
		{Name: "matches", Required: true},
	},
}

// ParseDir reads all *.hcl files from dir in lexical filename order, parses
// each, and returns the concatenated slice of rules, any warnings, and any
// error. A non-nil error means the ruleset is unusable. Warnings are
// informational only.
func ParseDir(dir string) ([]Rule, []string, error) {
	pattern := filepath.Join(dir, "*.hcl")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, nil, fmt.Errorf("rules: glob %q: %w", pattern, err)
	}
	// filepath.Glob already returns results in lexical order per the docs,
	// but sort explicitly for clarity and safety.
	sort.Strings(files)

	var (
		allRules    []Rule
		allWarnings []string
		seen        = make(map[string]string) // name -> filename
	)
	for _, path := range files {
		rs, warns, err := parseFile(path)
		if err != nil {
			return nil, nil, err
		}
		allWarnings = append(allWarnings, warns...)
		for _, r := range rs {
			if prev, dup := seen[r.Name]; dup {
				return nil, nil, fmt.Errorf("rules: duplicate rule name %q in %q (already defined in %q)", r.Name, path, prev)
			}
			seen[r.Name] = path
			allRules = append(allRules, r)
		}
	}
	return allRules, allWarnings, nil
}

// parseFile parses a single HCL file and returns its rules.
func parseFile(path string) ([]Rule, []string, error) {
	parser := hclparse.NewParser()
	f, diags := parser.ParseHCLFile(path)
	if diags.HasErrors() {
		return nil, nil, fmt.Errorf("rules: parse %q: %s", path, diags.Error())
	}

	// Use Content (strict) so unknown top-level blocks/attributes produce errors.
	content, diags := f.Body.Content(topLevelSchema)
	if diags.HasErrors() {
		return nil, nil, fmt.Errorf("rules: decode %q: %s", path, diags.Error())
	}

	var (
		rs       []Rule
		warnings []string
	)
	for _, block := range content.Blocks {
		r, warns, err := decodeRuleBlock(block, path)
		if err != nil {
			return nil, nil, err
		}
		warnings = append(warnings, warns...)
		rs = append(rs, r)
	}
	return rs, warnings, nil
}

// decodeRuleBlock decodes a single `rule "name" { ... }` block.
func decodeRuleBlock(block *hcl.Block, path string) (Rule, []string, error) {
	name := block.Labels[0]

	content, diags := block.Body.Content(ruleBodySchema)
	if diags.HasErrors() {
		return Rule{}, nil, fmt.Errorf("rules: rule %q in %q: %s", name, path, diags.Error())
	}

	r := Rule{Name: name}
	var warnings []string

	// Decode agents attribute (optional).
	if attr, ok := content.Attributes["agents"]; ok {
		val, diags := attr.Expr.Value(nil)
		if diags.HasErrors() {
			return Rule{}, nil, fmt.Errorf("rules: rule %q agents: %s", name, diags.Error())
		}
		if !val.Type().IsTupleType() && !val.Type().IsListType() {
			return Rule{}, nil, fmt.Errorf("rules: rule %q agents must be a list of strings", name)
		}
		if val.LengthInt() == 0 {
			return Rule{}, nil, fmt.Errorf("rules: rule %q has agents = [] (empty); delete the rule to disable it", name)
		}
		agents := make([]string, 0, val.LengthInt())
		for it := val.ElementIterator(); it.Next(); {
			_, v := it.Element()
			if v.Type() != cty.String {
				return Rule{}, nil, fmt.Errorf("rules: rule %q agents must be a list of strings", name)
			}
			agents = append(agents, v.AsString())
		}
		r.Agents = agents
	}
	// Omitted agents → nil (all agents). Already the zero value.

	// Decode verdict (required).
	if attr, ok := content.Attributes["verdict"]; ok {
		val, diags := attr.Expr.Value(nil)
		if diags.HasErrors() {
			return Rule{}, nil, fmt.Errorf("rules: rule %q verdict: %s", name, diags.Error())
		}
		if val.Type() != cty.String {
			return Rule{}, nil, fmt.Errorf("rules: rule %q verdict must be a string", name)
		}
		v := val.AsString()
		switch v {
		case "allow", "deny", "require-approval":
		default:
			return Rule{}, nil, fmt.Errorf("rules: rule %q verdict %q is not one of allow|deny|require-approval", name, v)
		}
		r.Verdict = v
	}

	// Decode match block (required, exactly one).
	matchBlocks := blocksOfType(content.Blocks, "match")
	if len(matchBlocks) == 0 {
		return Rule{}, nil, fmt.Errorf("rules: rule %q in %q: missing match block", name, path)
	}
	if len(matchBlocks) > 1 {
		return Rule{}, nil, fmt.Errorf("rules: rule %q in %q: multiple match blocks", name, path)
	}
	m, warns, err := decodeMatchBlock(matchBlocks[0], name, path)
	if err != nil {
		return Rule{}, nil, err
	}
	warnings = append(warnings, warns...)
	r.Match = m

	// match.host is required so the CONNECT-time decision can target the rule
	// at a specific host (or set of hosts via glob). An empty host would make
	// the rule invisible to the host-scoped CONNECT filter and produce a
	// silent tunnel — a security trap. Operators wanting "all hosts" must
	// spell it explicitly.
	if r.Match.Host == "" {
		return Rule{}, nil, fmt.Errorf(
			"rules: rule %q in %q: match.host is required (use host = \"**\" to match all hosts)",
			name, path,
		)
	}

	// Decode inject block (optional, at most one).
	injectBlocks := blocksOfType(content.Blocks, "inject")
	if len(injectBlocks) > 1 {
		return Rule{}, nil, fmt.Errorf("rules: rule %q in %q: multiple inject blocks", name, path)
	}
	if len(injectBlocks) == 1 {
		inj, warns, err := decodeInjectBlock(injectBlocks[0], name, path)
		if err != nil {
			return Rule{}, nil, err
		}
		warnings = append(warnings, warns...)
		r.Inject = &inj
	}

	// Compile matchers.
	if err := compileRule(&r); err != nil {
		return Rule{}, nil, fmt.Errorf("rules: rule %q: %w", name, err)
	}

	return r, warnings, nil
}

// decodeMatchBlock decodes a `match { ... }` block.
func decodeMatchBlock(block *hcl.Block, ruleName, path string) (Match, []string, error) {
	content, diags := block.Body.Content(matchBodySchema)
	if diags.HasErrors() {
		return Match{}, nil, fmt.Errorf("rules: rule %q match: %s", ruleName, diags.Error())
	}

	var m Match
	var warnings []string

	if attr, ok := content.Attributes["host"]; ok {
		val, diags := attr.Expr.Value(nil)
		if diags.HasErrors() {
			return Match{}, nil, fmt.Errorf("rules: rule %q match.host: %s", ruleName, diags.Error())
		}
		m.Host = val.AsString()
	}
	if attr, ok := content.Attributes["method"]; ok {
		val, diags := attr.Expr.Value(nil)
		if diags.HasErrors() {
			return Match{}, nil, fmt.Errorf("rules: rule %q match.method: %s", ruleName, diags.Error())
		}
		m.Method = val.AsString()
	}
	if attr, ok := content.Attributes["path"]; ok {
		val, diags := attr.Expr.Value(nil)
		if diags.HasErrors() {
			return Match{}, nil, fmt.Errorf("rules: rule %q match.path: %s", ruleName, diags.Error())
		}
		m.Path = val.AsString()
	}
	if attr, ok := content.Attributes["headers"]; ok {
		val, diags := attr.Expr.Value(nil)
		if diags.HasErrors() {
			return Match{}, nil, fmt.Errorf("rules: rule %q match.headers: %s", ruleName, diags.Error())
		}
		if val.Type().IsObjectType() || val.Type().IsMapType() {
			m.Headers = make(map[string]string)
			for k, v := range val.AsValueMap() {
				if v.Type() != cty.String {
					return Match{}, nil, fmt.Errorf("rules: rule %q match.headers[%q] must be a string", ruleName, k)
				}
				m.Headers[k] = v.AsString()
			}
		}
	}

	// Body blocks: at most one of json_body, form_body, text_body.
	jsonBodyBlocks := blocksOfType(content.Blocks, "json_body")
	formBodyBlocks := blocksOfType(content.Blocks, "form_body")
	textBodyBlocks := blocksOfType(content.Blocks, "text_body")

	bodyCount := len(jsonBodyBlocks) + len(formBodyBlocks) + len(textBodyBlocks)
	if bodyCount > 1 {
		return Match{}, nil, fmt.Errorf("rules: rule %q match: only one body block allowed (json_body, form_body, or text_body)", ruleName)
	}

	if len(jsonBodyBlocks) == 1 {
		jb, err := decodeJSONBodyBlock(jsonBodyBlocks[0], ruleName)
		if err != nil {
			return Match{}, nil, err
		}
		m.JSONBody = &jb
	}
	if len(formBodyBlocks) == 1 {
		fb, err := decodeFormBodyBlock(formBodyBlocks[0], ruleName)
		if err != nil {
			return Match{}, nil, err
		}
		m.FormBody = &fb
	}
	if len(textBodyBlocks) == 1 {
		tb, err := decodeTextBodyBlock(textBodyBlocks[0], ruleName)
		if err != nil {
			return Match{}, nil, err
		}
		m.TextBody = &tb
	}

	return m, warnings, nil
}

// decodeJSONBodyBlock decodes a `json_body { jsonpath "..." { ... } }` block.
func decodeJSONBodyBlock(block *hcl.Block, ruleName string) (JSONBodyMatch, error) {
	content, diags := block.Body.Content(jsonBodySchema)
	if diags.HasErrors() {
		return JSONBodyMatch{}, fmt.Errorf("rules: rule %q json_body: %s", ruleName, diags.Error())
	}

	var jb JSONBodyMatch
	for _, b := range content.Blocks {
		jpath := b.Labels[0]
		mc, diags := b.Body.Content(matcherBlockSchema)
		if diags.HasErrors() {
			return JSONBodyMatch{}, fmt.Errorf("rules: rule %q jsonpath %q: %s", ruleName, jpath, diags.Error())
		}
		matcher, err := stringAttr(mc.Attributes["matches"])
		if err != nil {
			return JSONBodyMatch{}, fmt.Errorf("rules: rule %q jsonpath %q matches: %w", ruleName, jpath, err)
		}
		jb.Paths = append(jb.Paths, JSONPathMatcher{
			Path:    jpath,
			Matches: matcher,
		})
	}
	return jb, nil
}

// decodeFormBodyBlock decodes a `form_body { field "name" { matches = "..." } }` block.
func decodeFormBodyBlock(block *hcl.Block, ruleName string) (FormBodyMatch, error) {
	content, diags := block.Body.Content(formBodySchema)
	if diags.HasErrors() {
		return FormBodyMatch{}, fmt.Errorf("rules: rule %q form_body: %s", ruleName, diags.Error())
	}

	var fb FormBodyMatch
	for _, b := range content.Blocks {
		fieldName := b.Labels[0]
		mc, diags := b.Body.Content(matcherBlockSchema)
		if diags.HasErrors() {
			return FormBodyMatch{}, fmt.Errorf("rules: rule %q field %q: %s", ruleName, fieldName, diags.Error())
		}
		matcher, err := stringAttr(mc.Attributes["matches"])
		if err != nil {
			return FormBodyMatch{}, fmt.Errorf("rules: rule %q field %q matches: %w", ruleName, fieldName, err)
		}
		fb.Fields = append(fb.Fields, FormFieldMatcher{
			Field:   fieldName,
			Matches: matcher,
		})
	}
	return fb, nil
}

// decodeTextBodyBlock decodes a `text_body { matches = "..." }` block.
func decodeTextBodyBlock(block *hcl.Block, ruleName string) (TextBodyMatch, error) {
	content, diags := block.Body.Content(textBodySchema)
	if diags.HasErrors() {
		return TextBodyMatch{}, fmt.Errorf("rules: rule %q text_body: %s", ruleName, diags.Error())
	}
	matcher, err := stringAttr(content.Attributes["matches"])
	if err != nil {
		return TextBodyMatch{}, fmt.Errorf("rules: rule %q text_body matches: %w", ruleName, err)
	}
	return TextBodyMatch{Matches: matcher}, nil
}

// decodeInjectBlock decodes an `inject { ... }` block.
func decodeInjectBlock(block *hcl.Block, ruleName, path string) (Inject, []string, error) {
	content, diags := block.Body.Content(injectBodySchema)
	if diags.HasErrors() {
		return Inject{}, nil, fmt.Errorf("rules: rule %q inject: %s", ruleName, diags.Error())
	}

	var inj Inject
	var warnings []string

	if attr, ok := content.Attributes["replace_header"]; ok {
		kvMap, err := decodeStringTemplateMap(attr.Expr, fmt.Sprintf("rule %q inject.replace_header", ruleName))
		if err != nil {
			return Inject{}, nil, err
		}
		inj.ReplaceHeaders = kvMap
		// Validate template syntax for each value in deterministic order.
		keys := make([]string, 0, len(kvMap))
		for k := range kvMap {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			w := validateTemplates(kvMap[k], fmt.Sprintf("rule %q inject.replace_header[%q]", ruleName, k))
			warnings = append(warnings, w...)
		}
	}

	if attr, ok := content.Attributes["remove_header"]; ok {
		val, diags := attr.Expr.Value(nil)
		if diags.HasErrors() {
			return Inject{}, nil, fmt.Errorf("rules: rule %q inject.remove_header: %s", ruleName, diags.Error())
		}
		if !val.Type().IsTupleType() && !val.Type().IsListType() {
			return Inject{}, nil, fmt.Errorf("rules: rule %q inject.remove_header must be a list of strings", ruleName)
		}
		for it := val.ElementIterator(); it.Next(); {
			_, v := it.Element()
			if v.Type() != cty.String {
				return Inject{}, nil, fmt.Errorf("rules: rule %q inject.remove_header elements must be strings", ruleName)
			}
			inj.RemoveHeaders = append(inj.RemoveHeaders, v.AsString())
		}
	}

	return inj, warnings, nil
}

// validateTemplates checks that all ${...} expressions in s are syntactically
// valid template references. Returns a warning string for each invalid form.
func validateTemplates(s, location string) []string {
	var warnings []string
	all := invalidTemplateRE.FindAllString(s, -1)
	for _, expr := range all {
		if !templateRE.MatchString(expr) {
			warnings = append(warnings, fmt.Sprintf(
				"rules: %s: unrecognised template expression %q (valid forms: ${secrets.<name>}, ${agent.name}, ${agent.id})",
				location, expr,
			))
		}
	}
	return warnings
}

// compileRule compiles globs, regexps, and body matchers in-place.
func compileRule(r *Rule) error {
	if r.Match.Host != "" {
		r.hostGlob = compileGlob(r.Match.Host, ".")
	}
	if r.Match.Path != "" {
		r.pathGlob = compileGlob(r.Match.Path, "/")
	}
	if len(r.Match.Headers) > 0 {
		r.headerREs = make(map[string]*regexp.Regexp, len(r.Match.Headers))
		for k, pattern := range r.Match.Headers {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return fmt.Errorf("match.headers[%q]: invalid regexp %q: %w", k, pattern, err)
			}
			r.headerREs[k] = re
		}
	}
	switch {
	case r.Match.JSONBody != nil:
		for i := range r.Match.JSONBody.Paths {
			re, err := regexp.Compile(r.Match.JSONBody.Paths[i].Matches)
			if err != nil {
				return fmt.Errorf("json_body jsonpath %q matches: invalid regexp: %w", r.Match.JSONBody.Paths[i].Path, err)
			}
			r.Match.JSONBody.Paths[i].re = re
		}
		r.body = r.Match.JSONBody
	case r.Match.FormBody != nil:
		for i := range r.Match.FormBody.Fields {
			re, err := regexp.Compile(r.Match.FormBody.Fields[i].Matches)
			if err != nil {
				return fmt.Errorf("form_body field %q matches: invalid regexp: %w", r.Match.FormBody.Fields[i].Field, err)
			}
			r.Match.FormBody.Fields[i].re = re
		}
		r.body = r.Match.FormBody
	case r.Match.TextBody != nil:
		re, err := regexp.Compile(r.Match.TextBody.Matches)
		if err != nil {
			return fmt.Errorf("text_body matches: invalid regexp: %w", err)
		}
		r.Match.TextBody.re = re
		r.body = r.Match.TextBody
	}
	return nil
}

// compileGlob builds a globMatcher from a pattern string.
// The sep parameter is the segment separator: "." for host, "/" for path.
// Glob semantics:
//
//   - "**" matches any number of segments (including zero) across the separator.
//   - "*"  matches any sequence of characters within a single segment (no sep crossing).
//   - All other characters are treated as literals.
func compileGlob(pattern, sep string) globMatcher {
	re := globToRegexp(pattern, sep)
	return globMatcher{
		pattern: pattern,
		re:      regexp.MustCompile(re),
	}
}

// globToRegexp translates a glob pattern and separator into an anchored
// regular expression string.
func globToRegexp(pattern, sep string) string {
	// Escape the separator for use in the regex character class.
	escapedSep := regexp.QuoteMeta(sep)

	var sb strings.Builder
	sb.WriteString("^")
	i := 0
	for i < len(pattern) {
		// Check for ** (must come before * check).
		if i+1 < len(pattern) && pattern[i] == '*' && pattern[i+1] == '*' {
			i += 2
			// If "**" is immediately followed by the separator, the separator
			// must also be optional so that zero segments matches the suffix
			// without a leading separator (e.g. "**.enterprise.local" matches
			// "enterprise.local" as well as "sub.enterprise.local").
			if i < len(pattern) && string(pattern[i]) == sep {
				sb.WriteString("(?:.*" + escapedSep + ")?")
				i++ // consume the separator
			} else {
				// "**" matches zero or more characters across separators (any depth).
				sb.WriteString(".*")
			}
			continue
		}
		if pattern[i] == '*' {
			// "*" matches any sequence within a segment (no separator crossing).
			sb.WriteString("[^" + escapedSep + "]*")
			i++
			continue
		}
		// Literal character — escape it for use in a regex.
		sb.WriteString(regexp.QuoteMeta(string(pattern[i])))
		i++
	}
	sb.WriteString("$")
	return sb.String()
}

// blocksOfType filters an hcl.Blocks slice by block type.
func blocksOfType(blocks hcl.Blocks, typ string) hcl.Blocks {
	var out hcl.Blocks
	for _, b := range blocks {
		if b.Type == typ {
			out = append(out, b)
		}
	}
	return out
}

// decodeStringTemplateMap decodes an HCL object/map expression into a
// map[string]string, where values may contain ${...} template expressions
// that are preserved verbatim.
func decodeStringTemplateMap(expr hcl.Expression, location string) (map[string]string, error) {
	// Use ExprMap to get the key-value pairs at the expression level.
	kvPairs, diags := hcl.ExprMap(expr)
	if diags.HasErrors() {
		return nil, fmt.Errorf("rules: %s: %s", location, diags.Error())
	}
	result := make(map[string]string, len(kvPairs))
	for _, kv := range kvPairs {
		// Key must evaluate to a plain string (no templates in keys).
		keyVal, diags := kv.Key.Value(nil)
		if diags.HasErrors() {
			return nil, fmt.Errorf("rules: %s key: %s", location, diags.Error())
		}
		if keyVal.Type() != cty.String {
			return nil, fmt.Errorf("rules: %s: map key must be a string", location)
		}
		k := keyVal.AsString()
		// Value may contain template expressions.
		v, err := templateStringFromExpr(kv.Value)
		if err != nil {
			return nil, fmt.Errorf("rules: %s[%q]: %w", location, k, err)
		}
		result[k] = v
	}
	return result, nil
}

// stringAttr extracts a plain string value from an HCL attribute.
// Values containing ${...} template expressions are not supported here;
// use decodeStringTemplateMap for inject values that carry templates.
func stringAttr(attr *hcl.Attribute) (string, error) {
	if attr == nil {
		return "", fmt.Errorf("attribute is nil")
	}
	val, diags := attr.Expr.Value(nil)
	if diags.HasErrors() {
		return "", fmt.Errorf("%s", diags.Error())
	}
	if val.Type() != cty.String {
		return "", fmt.Errorf("expected string, got %s", val.Type().FriendlyName())
	}
	return val.AsString(), nil
}

// templateStringFromExpr reconstructs a template string from an HCL expression,
// preserving ${...} variable references as literal text.
func templateStringFromExpr(expr hcl.Expression) (string, error) {
	switch e := expr.(type) {
	case *hclsyntax.LiteralValueExpr:
		// Plain string literal with no template expressions.
		if e.Val.Type() != cty.String {
			return "", fmt.Errorf("expected string literal")
		}
		return e.Val.AsString(), nil
	case *hclsyntax.TemplateWrapExpr:
		// A wrapped expression like "${secrets.x}" that is the only part.
		return templateStringFromExpr(e.Wrapped)
	case *hclsyntax.TemplateExpr:
		// A template string with one or more parts.
		var sb strings.Builder
		for _, part := range e.Parts {
			s, err := templateStringFromExpr(part)
			if err != nil {
				return "", err
			}
			sb.WriteString(s)
		}
		return sb.String(), nil
	case *hclsyntax.ScopeTraversalExpr:
		// A bare variable reference used as a template part.
		return traversalToTemplate(e.Traversal)
	default:
		// Fall back to plain evaluation for other expression types.
		val, diags := expr.Value(nil)
		if diags.HasErrors() {
			return "", fmt.Errorf("%s", diags.Error())
		}
		if val.Type() != cty.String {
			return "", fmt.Errorf("expected string, got %s", val.Type().FriendlyName())
		}
		return val.AsString(), nil
	}
}

// traversalToTemplate converts an hcl.Traversal into a ${...} template string.
// e.g. [TraverseRoot{"secrets"}, TraverseAttr{"gh_bot"}] → "${secrets.gh_bot}"
func traversalToTemplate(traversal hcl.Traversal) (string, error) {
	var parts []string
	for _, step := range traversal {
		switch s := step.(type) {
		case hcl.TraverseRoot:
			parts = append(parts, s.Name)
		case hcl.TraverseAttr:
			parts = append(parts, s.Name)
		default:
			return "", fmt.Errorf("unsupported traversal step type %T in template", step)
		}
	}
	return "${" + strings.Join(parts, ".") + "}", nil
}
