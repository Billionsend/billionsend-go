// Command generate writes the typed surface of the SDK from the OpenAPI
// snapshot (openapi/api.openapi.json):
//
//   - types_gen.go: a Go type for every component schema, and for the inline
//     objects inside them;
//   - services_gen.go: one method per operation, grouped into services by the
//     operationId's dotted path (emails.receiving.attachments.list is
//     client.Emails.Receiving.Attachments.List), with a ListAll-style
//     iterator for every cursor-paginated list;
//   - coverage_gen_test.go: a call of every method, which the coverage test
//     runs against a server that knows only the snapshot's operations.
//
// Usage, from the repository root:
//
//	go run ./internal/cmd/generate          # rewrite the generated files
//	go run ./internal/cmd/generate -check   # fail when they differ from the snapshot
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type schema map[string]any

type document struct {
	Paths      map[string]map[string]json.RawMessage `json:"paths"`
	Components struct {
		Schemas map[string]schema `json:"schemas"`
	} `json:"components"`
}

type parameter struct {
	Name        string `json:"name"`
	In          string `json:"in"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
	Schema      schema `json:"schema"`
}

type operation struct {
	OperationID string      `json:"operationId"`
	Summary     string      `json:"summary"`
	Description string      `json:"description"`
	Parameters  []parameter `json:"parameters"`
	RequestBody *struct {
		Content map[string]struct {
			Schema schema `json:"schema"`
		} `json:"content"`
	} `json:"requestBody"`
	Responses map[string]struct {
		Content map[string]struct {
			Schema schema `json:"schema"`
		} `json:"content"`
	} `json:"responses"`
	Security *[]any `json:"security"`

	method, path string
}

// initialisms are written in capitals in Go names.
var initialisms = map[string]string{
	"id": "ID", "ids": "IDs", "api": "API", "url": "URL", "urls": "URLs", "html": "HTML",
	"http": "HTTP", "https": "HTTPS", "json": "JSON", "dns": "DNS", "dkim": "DKIM", "spf": "SPF",
	"smtp": "SMTP", "mx": "MX", "ip": "IP", "mcp": "MCP", "ses": "SES", "uri": "URI", "uuid": "UUID",
	"cc": "CC", "bcc": "BCC", "oauth": "OAuth", "ai": "AI", "csv": "CSV", "pdf": "PDF", "ttl": "TTL", "utc": "UTC", "dmarc": "DMARC",
}

var wordRE = regexp.MustCompile(`[A-Z]+[a-z0-9]*|[a-z0-9]+`)

// goName turns snake_case, camelCase or kebab-case into an exported Go name.
func goName(s string) string {
	var b strings.Builder
	for _, w := range wordRE.FindAllString(s, -1) {
		lw := strings.ToLower(w)
		if up, ok := initialisms[lw]; ok {
			b.WriteString(up)
			continue
		}
		b.WriteString(strings.ToUpper(lw[:1]) + lw[1:])
	}
	name := b.String()
	if name == "" {
		return "Value"
	}
	if name[0] >= '0' && name[0] <= '9' {
		name = "N" + name
	}
	return name
}

// paramName is an unexported Go name for a path parameter.
func paramName(s string) string {
	n := goName(s)
	for up := range map[string]bool{"ID": true, "IDs": true, "API": true, "URL": true} {
		if strings.HasPrefix(n, up) && (len(n) == len(up) || n[len(up)] >= 'A' && n[len(up)] <= 'Z') {
			return strings.ToLower(up) + n[len(up):]
		}
	}
	return strings.ToLower(n[:1]) + n[1:]
}

func refName(ref string) string { return ref[strings.LastIndex(ref, "/")+1:] }

type generator struct {
	doc     document
	types   bytes.Buffer
	emitted map[string]bool
	pending []func()
	arrays  map[string]bool // component schemas that are arrays (passed by value)
	// inPatch marks the types being generated as the body of an update (PATCH),
	// never received: there, leaving a field out keeps it and null clears it, so
	// their optional fields that accept null become *Nullable[T]. In a create,
	// null says no more than leaving the field out.
	inPatch bool
}

func str(s schema, k string) string {
	v, _ := s[k].(string)
	return v
}

// typeOf is the Go type of a schema; inline objects become named types `ctx`.
func (g *generator) typeOf(s schema, ctx string) (goType string, nullable bool) {
	if s == nil {
		return "any", false
	}
	if ref := str(s, "$ref"); ref != "" {
		return goName(refName(ref)), false
	}
	if all, ok := s["allOf"].([]any); ok {
		// The one item that gives a type; the others only describe it.
		var typed []schema
		for _, item := range all {
			if x := toSchema(item); x["$ref"] != nil || x["type"] != nil || x["properties"] != nil {
				typed = append(typed, x)
			}
		}
		if len(typed) == 1 {
			return g.typeOf(typed[0], ctx)
		}
	}
	if _, ok := s["anyOf"]; ok {
		return "any", true
	}
	if _, ok := s["oneOf"]; ok {
		return "any", true
	}
	kind := ""
	switch t := s["type"].(type) {
	case string:
		kind = t
	case []any:
		for _, x := range t {
			if x == "null" {
				nullable = true
			} else if xs, ok := x.(string); ok {
				kind = xs
			}
		}
	}
	switch kind {
	case "string":
		return "string", nullable
	case "integer":
		return "int64", nullable
	case "number":
		return "float64", nullable
	case "boolean":
		return "bool", nullable
	case "array":
		inner, _ := g.typeOf(toSchema(s["items"]), ctx+"Item")
		return "[]" + inner, nullable
	case "object", "":
		props, _ := s["properties"].(map[string]any)
		if len(props) > 0 {
			g.structType(ctx, s)
			return ctx, nullable
		}
		if ap, ok := s["additionalProperties"].(map[string]any); ok {
			inner, _ := g.typeOf(schema(ap), ctx+"Value")
			return "map[string]" + inner, nullable
		}
		if kind == "object" {
			return "map[string]any", nullable
		}
		return "any", nullable
	}
	return "any", nullable
}

func toSchema(v any) schema {
	if m, ok := v.(map[string]any); ok {
		return schema(m)
	}
	return nil
}

func comment(b *bytes.Buffer, indent, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	for _, line := range wrap(text, 76) {
		fmt.Fprintf(b, "%s// %s\n", indent, line)
	}
}

func wrap(text string, width int) []string {
	var lines []string
	for _, para := range strings.Split(text, "\n") {
		words := strings.Fields(para)
		line := ""
		for _, w := range words {
			if line != "" && len(line)+1+len(w) > width {
				lines = append(lines, line)
				line = w
				continue
			}
			if line == "" {
				line = w
			} else {
				line += " " + w
			}
		}
		lines = append(lines, line)
	}
	return lines
}

func enumNote(s schema) string {
	values, ok := s["enum"].([]any)
	if !ok {
		if items := toSchema(s["items"]); items != nil {
			values, ok = items["enum"].([]any)
		}
	}
	if !ok || len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, v := range values {
		if v == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%q", fmt.Sprint(v)))
	}
	return " One of: " + strings.Join(parts, ", ") + "."
}

// structType emits a struct for an object schema, once.
func (g *generator) structType(name string, s schema) {
	if g.emitted[name] {
		return
	}
	g.emitted[name] = true
	inPatch := g.inPatch
	g.pending = append(g.pending, func() {
		g.inPatch = inPatch
		props, _ := s["properties"].(map[string]any)
		required := map[string]bool{}
		if req, ok := s["required"].([]any); ok {
			for _, r := range req {
				required[fmt.Sprint(r)] = true
			}
		}
		keys := make([]string, 0, len(props))
		for k := range props {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b bytes.Buffer
		comment(&b, "", str(s, "description"))
		fmt.Fprintf(&b, "type %s struct {\n", name)
		used := map[string]bool{}
		for _, k := range keys {
			ps := toSchema(props[k])
			field := goName(k)
			for used[field] {
				field += "_"
			}
			used[field] = true
			t, nullable := g.typeOf(ps, name+field)
			comment(&b, "\t", str(ps, "description")+enumNote(ps))
			nilable := t == "any" || strings.HasPrefix(t, "[]") || strings.HasPrefix(t, "map[")
			tag := k
			switch {
			case inPatch && !required[k] && nullable && t != "any":
				// Leaving the field out and sending null mean different things.
				t = "*Nullable[" + t + "]"
				tag += ",omitempty"
			case required[k] && !nullable:
			case required[k] && nullable:
				if !nilable {
					t = "*" + t
				}
			default:
				if !nilable {
					t = "*" + t
				}
				tag += ",omitempty"
			}
			fmt.Fprintf(&b, "\t%s %s `json:%q`\n", field, t, tag)
		}
		b.WriteString("}\n\n")
		g.types.Write(b.Bytes())
	})
}

// component emits a named component schema.
func (g *generator) component(name string, s schema) {
	goN := goName(name)
	props, _ := s["properties"].(map[string]any)
	if len(props) > 0 {
		g.structType(goN, s)
		return
	}
	if g.emitted[goN] {
		return
	}
	g.emitted[goN] = true
	t, _ := g.typeOf(s, goN+"Value")
	if strings.HasPrefix(t, "[]") {
		g.arrays[goN] = true
	}
	var b bytes.Buffer
	comment(&b, "", str(s, "description")+enumNote(s))
	fmt.Fprintf(&b, "type %s = %s\n\n", goN, t)
	g.types.Write(b.Bytes())
}

func (g *generator) flush() {
	for len(g.pending) > 0 {
		next := g.pending[0]
		g.pending = g.pending[1:]
		next()
	}
}

type op struct {
	operation
	service    []string // Go names of the service chain
	method     string
	pathParams []parameter
	queryHdr   []parameter
	bodyType   string
	bodyArray  bool
	multipart  bool
	respType   string
	stream     bool
	idempotent bool
	noAuth     bool
	pageItem   string // the item type of a cursor-paginated list
	pageParams string
}

func (g *generator) operations() []*op {
	var ops []*op
	paths := make([]string, 0, len(g.doc.Paths))
	for p := range g.doc.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	ids := map[string]bool{}
	for _, p := range paths {
		for m, raw := range g.doc.Paths[p] {
			if !isMethod(m) {
				continue
			}
			var o operation
			if err := json.Unmarshal(raw, &o); err != nil {
				panic(err)
			}
			ids[o.OperationID] = true
		}
	}
	for _, p := range paths {
		methods := make([]string, 0)
		for m := range g.doc.Paths[p] {
			if isMethod(m) {
				methods = append(methods, m)
			}
		}
		sort.Strings(methods)
		for _, m := range methods {
			var o operation
			if err := json.Unmarshal(g.doc.Paths[p][m], &o); err != nil {
				panic(err)
			}
			o.method, o.path = strings.ToUpper(m), p
			ops = append(ops, g.operation(o, ids))
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].OperationID < ops[j].OperationID })
	return ops
}

const multipartType = "multipart/form-data"

// bodies walks every operation's bodies: the component schemas the updates
// (PATCH) send and no response answers, and the multipart forms, whose Go types are written by hand
// (upload.go) so a file is bytes and a JSON-encoded field is a typed value.
func (g *generator) bodies() (patchOnly, forms map[string]bool) {
	patched, received := map[string]bool{}, map[string]bool{}
	forms = map[string]bool{}
	var reach func(v any, into map[string]bool)
	reach = func(v any, into map[string]bool) {
		switch x := v.(type) {
		case map[string]any:
			if ref, ok := x["$ref"].(string); ok {
				if name := refName(ref); !into[name] {
					into[name] = true
					reach(map[string]any(g.doc.Components.Schemas[name]), into)
				}
			}
			for _, child := range x {
				reach(child, into)
			}
		case []any:
			for _, child := range x {
				reach(child, into)
			}
		}
	}
	for _, methods := range g.doc.Paths {
		for m, raw := range methods {
			if !isMethod(m) {
				continue
			}
			var o operation
			if err := json.Unmarshal(raw, &o); err != nil {
				panic(err)
			}
			if o.RequestBody != nil {
				for ct, c := range o.RequestBody.Content {
					if ct == multipartType {
						forms[refName(str(c.Schema, "$ref"))] = true
						continue
					}
					if m == "patch" {
						reach(map[string]any(c.Schema), patched)
					}
				}
			}
			for _, r := range o.Responses {
				for _, c := range r.Content {
					reach(map[string]any(c.Schema), received)
				}
			}
		}
	}
	patchOnly = map[string]bool{}
	for name := range patched {
		patchOnly[name] = !received[name]
	}
	return patchOnly, forms
}

func isMethod(m string) bool {
	switch m {
	case "get", "post", "put", "patch", "delete":
		return true
	}
	return false
}

func (g *generator) operation(o operation, ids map[string]bool) *op {
	segs := strings.Split(o.OperationID, ".")
	x := &op{operation: o}
	// An operation that is also the prefix of others (emails.metrics, with
	// emails.metrics.links) becomes the service's Get.
	hasChildren := false
	for id := range ids {
		if strings.HasPrefix(id, o.OperationID+".") {
			hasChildren = true
		}
	}
	if hasChildren {
		segs = append(segs, "get")
	}
	for _, s := range segs[:len(segs)-1] {
		x.service = append(x.service, goName(s))
	}
	x.method = goName(segs[len(segs)-1])
	byName := map[string]parameter{}
	for _, prm := range o.Parameters {
		byName[prm.In+":"+prm.Name] = prm
	}
	for _, m := range regexp.MustCompile(`\{([^}]+)\}`).FindAllStringSubmatch(o.path, -1) {
		x.pathParams = append(x.pathParams, byName["path:"+m[1]])
	}
	for _, prm := range o.Parameters {
		switch {
		case prm.In == "header" && strings.EqualFold(prm.Name, "Idempotency-Key"):
			x.idempotent = true
		case prm.In == "query" || prm.In == "header":
			x.queryHdr = append(x.queryHdr, prm)
		}
	}
	if o.RequestBody != nil {
		for ct, c := range o.RequestBody.Content {
			g.inPatch = o.method == "PATCH"
			x.bodyType, _ = g.typeOf(c.Schema, x.typePrefix()+"Body")
			g.inPatch = false
			x.bodyArray = strings.HasPrefix(x.bodyType, "[]") || g.arrays[x.bodyType]
			x.multipart = ct == multipartType
		}
	}
	codes := make([]string, 0)
	for code := range o.Responses {
		if strings.HasPrefix(code, "2") {
			codes = append(codes, code)
		}
	}
	sort.Strings(codes)
	if len(codes) > 0 {
		for ct, c := range o.Responses[codes[0]].Content {
			if ct == "text/event-stream" {
				x.stream = true
				continue
			}
			x.respType, _ = g.typeOf(c.Schema, x.typePrefix()+"Response")
		}
	}
	x.noAuth = o.Security != nil && len(*o.Security) == 0
	g.pagination(x)
	return x
}

func (x *op) typePrefix() string { return strings.Join(x.service, "") + x.method }

// pagination marks a cursor list: `after` / `before` query parameters and a
// response with data (items with an id) and has_more.
func (g *generator) pagination(x *op) {
	hasAfter := false
	for _, prm := range x.queryHdr {
		if prm.In == "query" && prm.Name == "after" {
			hasAfter = true
		}
	}
	if !hasAfter || x.respType == "" {
		return
	}
	var resp schema
	for name, s := range g.doc.Components.Schemas {
		if goName(name) == x.respType {
			resp = s
		}
	}
	props, _ := resp["properties"].(map[string]any)
	data := toSchema(props["data"])
	if data == nil || props["has_more"] == nil {
		return
	}
	item := toSchema(data["items"])
	t, _ := g.typeOf(item, x.respType+"DataItem")
	x.pageItem = t
}

func (g *generator) params(b *bytes.Buffer, x *op) string {
	if len(x.queryHdr) == 0 {
		return ""
	}
	name := x.typePrefix() + "Params"
	fmt.Fprintf(b, "// %s are the query and header parameters of %s.\n", name, x.typePrefix())
	fmt.Fprintf(b, "type %s struct {\n", name)
	for _, prm := range x.queryHdr {
		t, _ := g.typeOf(prm.Schema, name+goName(prm.Name))
		comment(b, "\t", prm.Description+enumNote(prm.Schema))
		if !strings.HasPrefix(t, "[]") && t != "any" {
			t = "*" + t
		}
		fmt.Fprintf(b, "\t%s %s `%s:%q`\n", goName(prm.Name), t, prm.In, prm.Name)
	}
	b.WriteString("}\n\n")
	return name
}

func main() {
	check := flag.Bool("check", false, "fail when the generated files differ")
	flag.Parse()
	raw, err := os.ReadFile("openapi/api.openapi.json")
	if err != nil {
		fail(err)
	}
	g := &generator{emitted: map[string]bool{}, arrays: map[string]bool{}}
	if err := json.Unmarshal(raw, &g.doc); err != nil {
		fail(err)
	}
	names := make([]string, 0, len(g.doc.Components.Schemas))
	for n := range g.doc.Components.Schemas {
		names = append(names, n)
	}
	sort.Strings(names)
	patchOnly, forms := g.bodies()
	for _, n := range names {
		if forms[n] {
			continue
		}
		g.inPatch = patchOnly[n]
		g.component(n, g.doc.Components.Schemas[n])
	}
	g.inPatch = false
	g.flush()
	ops := g.operations()
	g.flush()

	var svc bytes.Buffer
	services := map[string][]string{} // service key -> child service keys
	methods := map[string][]*op{}
	for _, x := range ops {
		for i := 1; i <= len(x.service); i++ {
			parent := strings.Join(x.service[:i-1], ".")
			child := strings.Join(x.service[:i], ".")
			if !contains(services[parent], child) {
				services[parent] = append(services[parent], child)
			}
			if _, ok := services[child]; !ok {
				services[child] = nil
			}
		}
		key := strings.Join(x.service, ".")
		methods[key] = append(methods[key], x)
	}
	keys := make([]string, 0, len(services))
	for k := range services {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sort.Strings(services[k])
		g.service(&svc, k, services[k], methods[k])
	}

	files := map[string][]byte{
		"types_gen.go":         render("types", g.types.Bytes()),
		"services_gen.go":      render("services", svc.Bytes()),
		"coverage_gen_test.go": render("coverage", coverage(ops)),
	}
	stale := false
	for name, content := range files {
		formatted, err := format.Source(content)
		if err != nil {
			_ = os.WriteFile(name+".broken", content, 0o644)
			fail(fmt.Errorf("%s: %w", name, err))
		}
		if *check {
			current, _ := os.ReadFile(name)
			if !bytes.Equal(current, formatted) {
				fmt.Fprintf(os.Stderr, "%s is out of date: run go run ./internal/cmd/generate\n", name)
				stale = true
			}
			continue
		}
		if err := os.WriteFile(filepath.Clean(name), formatted, 0o644); err != nil {
			fail(err)
		}
	}
	if stale {
		os.Exit(1)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func render(kind string, body []byte) []byte {
	var b bytes.Buffer
	b.WriteString("// Code generated by go run ./internal/cmd/generate from openapi/api.openapi.json. DO NOT EDIT.\n\n")
	b.WriteString("package billionsend\n\n")
	switch kind {
	case "services":
		b.WriteString("import (\n\t\"context\"\n\t\"iter\"\n)\n\n")
	case "coverage":
		b.WriteString("import \"context\"\n\n")
	}
	b.Write(body)
	return b.Bytes()
}

func serviceType(key string) string {
	if key == "" {
		return "Client"
	}
	return strings.ReplaceAll(key, ".", "") + "Service"
}

func (g *generator) service(b *bytes.Buffer, key string, children []string, ms []*op) {
	t := serviceType(key)
	if key != "" {
		fmt.Fprintf(b, "// %s groups the %s operations.\n", t, strings.ReplaceAll(key, ".", " "))
		fmt.Fprintf(b, "type %s struct {\n\tclient *Client\n", t)
		for _, c := range children {
			fmt.Fprintf(b, "\t%s *%s\n", c[strings.LastIndex(c, ".")+1:], serviceType(c))
		}
		b.WriteString("}\n\n")
		fmt.Fprintf(b, "func new%s(c *Client) *%s {\n\treturn &%s{client: c", t, t, t)
		for _, c := range children {
			fmt.Fprintf(b, ", %s: new%s(c)", c[strings.LastIndex(c, ".")+1:], serviceType(c))
		}
		b.WriteString("}\n}\n\n")
	} else {
		b.WriteString("// services is what a Client hands out: one field per top-level group.\ntype services struct {\n")
		for _, c := range children {
			fmt.Fprintf(b, "\t%s *%s\n", c, serviceType(c))
		}
		b.WriteString("}\n\nfunc (c *Client) initServices() {\n")
		for _, c := range children {
			fmt.Fprintf(b, "\tc.%s = new%s(c)\n", c, serviceType(c))
		}
		b.WriteString("}\n\n")
	}
	for _, x := range ms {
		g.method(b, t, x)
	}
}

func (g *generator) method(b *bytes.Buffer, recv string, x *op) {
	paramsType := g.params(b, x)
	var args, pathArgs []string
	for _, p := range x.pathParams {
		args = append(args, paramName(p.Name)+" string")
		pathArgs = append(pathArgs, fmt.Sprintf("%q, %s", p.Name, paramName(p.Name)))
	}
	if x.bodyType != "" {
		if x.bodyArray {
			args = append(args, "body "+x.bodyType)
		} else {
			args = append(args, "body *"+x.bodyType)
		}
	}
	if paramsType != "" {
		args = append(args, "params *"+paramsType)
	}
	args = append(args, "opts ...RequestOption")
	doc := x.Summary
	if x.Description != "" && x.Description != x.Summary {
		doc += "\n\n" + x.Description
	}
	fmt.Fprintf(b, "// %s calls %s %s (%s).\n", x.method, x.operation.method, x.operation.path, x.OperationID)
	if doc != "" {
		b.WriteString("//\n")
		comment(b, "", doc)
	}
	if x.idempotent {
		b.WriteString("//\n// It always sends an Idempotency-Key: WithIdempotencyKey's, or a new UUID for this\n// call that its retries reuse.\n")
	}
	if x.multipart {
		b.WriteString("//\n// It uploads multipart/form-data and is not repeated after a 5xx, a timeout or\n// a dropped connection; a 429 is still retried.\n")
	}
	spec := fmt.Sprintf("&call{operation: %q, method: %q, path: %q, pathParams: []string{%s}, idempotent: %t, noAuth: %t",
		x.OperationID, x.operation.method, x.operation.path, strings.Join(pathArgs, ", "), x.idempotent, x.noAuth)
	if x.bodyType != "" {
		spec += ", body: body"
	}
	if paramsType != "" {
		spec += ", params: params"
	}
	if x.multipart {
		spec += ", noRepeat: true"
	}
	spec += "}"
	ctxArgs := "ctx context.Context, " + strings.Join(args, ", ")
	switch {
	case x.stream:
		fmt.Fprintf(b, "func (s *%s) %s(%s) (*EventStream, error) {\n", recv, x.method, ctxArgs)
		ends := "nil"
		if len(x.pathParams) > 0 {
			ends = `[]string{"inbox.deleted", "inbox.expired"}`
		}
		fmt.Fprintf(b, "\treturn s.client.stream(ctx, %s, %s, opts)\n}\n\n", spec, ends)
	case x.respType != "":
		fmt.Fprintf(b, "func (s *%s) %s(%s) (*%s, error) {\n", recv, x.method, ctxArgs, x.respType)
		fmt.Fprintf(b, "\tout := new(%s)\n\tif err := s.client.do(ctx, %s, out, opts); err != nil {\n\t\treturn nil, err\n\t}\n\treturn out, nil\n}\n\n", x.respType, spec)
	default:
		fmt.Fprintf(b, "func (s *%s) %s(%s) error {\n", recv, x.method, ctxArgs)
		fmt.Fprintf(b, "\treturn s.client.do(ctx, %s, nil, opts)\n}\n\n", spec)
	}
	if x.pageItem != "" && paramsType != "" {
		all := x.method + "All"
		var callArgs []string
		var sig []string
		for _, p := range x.pathParams {
			sig = append(sig, paramName(p.Name)+" string")
			callArgs = append(callArgs, paramName(p.Name))
		}
		fmt.Fprintf(b, "// %s walks every item of %s, fetching pages as it goes: forward from\n// params.After, or backward when params.Before is set. A failed page ends the\n// walk with its error.\n", all, x.method)
		sig = append(sig, "params *"+paramsType, "opts ...RequestOption")
		fmt.Fprintf(b, "func (s *%s) %s(ctx context.Context, %s) iter.Seq2[%s, error] {\n", recv, all, strings.Join(sig, ", "), x.pageItem)
		fmt.Fprintf(b, "\treturn paginate(func(after, before *string) ([]%s, bool, error) {\n", x.pageItem)
		fmt.Fprintf(b, "\t\tp := %s{}\n\t\tif params != nil {\n\t\t\tp = *params\n\t\t}\n\t\tp.After, p.Before = after, before\n", paramsType)
		callArgs = append(callArgs, "&p", "opts...")
		fmt.Fprintf(b, "\t\tpage, err := s.%s(ctx, %s)\n\t\tif err != nil {\n\t\t\treturn nil, false, err\n\t\t}\n\t\treturn page.Data, page.HasMore, nil\n", x.method, strings.Join(callArgs, ", "))
		b.WriteString("\t}, startCursor(params), func(item " + x.pageItem + ") string { return itemID(item) })\n}\n\n")
		_ = callArgs
	}
}

// coverage writes one call of every operation for the coverage test.
func coverage(ops []*op) []byte {
	var b bytes.Buffer
	b.WriteString("// coverageCalls holds one call of every operation, keyed by operationId.\n")
	b.WriteString("var coverageCalls = map[string]func(ctx context.Context, c *Client) error{\n")
	for _, x := range ops {
		var args []string
		for i := range x.pathParams {
			args = append(args, fmt.Sprintf("%q", fmt.Sprintf("p%d", i+1)))
		}
		if x.bodyType != "" {
			if x.bodyArray {
				args = append(args, x.bodyType+"{}")
			} else {
				args = append(args, "&"+x.bodyType+"{}")
			}
		}
		if len(x.queryHdr) > 0 {
			args = append(args, "nil")
		}
		chain := "c." + strings.Join(append(append([]string{}, x.service...), x.method), ".")
		call := fmt.Sprintf("%s(ctx, %s)", chain, strings.Join(args, ", "))
		call = strings.Replace(call, "(ctx, )", "(ctx)", 1)
		switch {
		case x.stream:
			fmt.Fprintf(&b, "\t%q: func(ctx context.Context, c *Client) error {\n\t\ts, err := %s\n\t\tif err == nil {\n\t\t\ts.Close()\n\t\t}\n\t\treturn err\n\t},\n", x.OperationID, call)
		case x.respType != "":
			fmt.Fprintf(&b, "\t%q: func(ctx context.Context, c *Client) error {\n\t\t_, err := %s\n\t\treturn err\n\t},\n", x.OperationID, call)
		default:
			fmt.Fprintf(&b, "\t%q: func(ctx context.Context, c *Client) error {\n\t\treturn %s\n\t},\n", x.OperationID, call)
		}
	}
	b.WriteString("}\n")
	return b.Bytes()
}
