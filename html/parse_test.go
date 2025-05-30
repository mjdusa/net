// Copyright 2010 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package html

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"golang.org/x/net/html/atom"
)

type testAttrs struct {
	text, want, context string
	scripting           bool
}

// readParseTest reads a single test case from r.
func readParseTest(r *bufio.Reader) (*testAttrs, error) {
	ta := &testAttrs{scripting: true}
	line, err := r.ReadSlice('\n')
	if err != nil {
		return nil, err
	}
	var b []byte

	// Read the HTML.
	if string(line) != "#data\n" {
		return nil, fmt.Errorf(`got %q want "#data\n"`, line)
	}
	for {
		line, err = r.ReadSlice('\n')
		if err != nil {
			return nil, err
		}
		if line[0] == '#' {
			break
		}
		b = append(b, line...)
	}
	ta.text = strings.TrimSuffix(string(b), "\n")
	b = b[:0]

	// Skip the error list.
	if string(line) != "#errors\n" {
		return nil, fmt.Errorf(`got %q want "#errors\n"`, line)
	}
	for {
		line, err = r.ReadSlice('\n')
		if err != nil {
			return nil, err
		}
		if line[0] == '#' {
			break
		}
	}

	// Skip the new-errors list.
	if string(line) == "#new-errors\n" {
		for {
			line, err = r.ReadSlice('\n')
			if err != nil {
				return nil, err
			}
			if line[0] == '#' {
				break
			}
		}
	}

	if ls := string(line); strings.HasPrefix(ls, "#script-") {
		switch {
		case strings.HasSuffix(ls, "-on\n"):
			ta.scripting = true
		case strings.HasSuffix(ls, "-off\n"):
			ta.scripting = false
		default:
			return nil, fmt.Errorf(`got %q, want "#script-on" or "#script-off"`, line)
		}
		for {
			line, err = r.ReadSlice('\n')
			if err != nil {
				return nil, err
			}
			if line[0] == '#' {
				break
			}
		}
	}

	if string(line) == "#document-fragment\n" {
		line, err = r.ReadSlice('\n')
		if err != nil {
			return nil, err
		}
		ta.context = strings.TrimSpace(string(line))
		line, err = r.ReadSlice('\n')
		if err != nil {
			return nil, err
		}
	}

	// Read the dump of what the parse tree should be.
	if string(line) != "#document\n" {
		return nil, fmt.Errorf(`got %q want "#document\n"`, line)
	}
	inQuote := false
	for {
		line, err = r.ReadSlice('\n')
		if err != nil && err != io.EOF {
			return nil, err
		}
		trimmed := bytes.Trim(line, "| \n")
		if len(trimmed) > 0 {
			if line[0] == '|' && trimmed[0] == '"' {
				inQuote = true
			}
			if trimmed[len(trimmed)-1] == '"' && !(line[0] == '|' && len(trimmed) == 1) {
				inQuote = false
			}
		}
		if len(line) == 0 || len(line) == 1 && line[0] == '\n' && !inQuote {
			break
		}
		b = append(b, line...)
	}
	ta.want = string(b)
	return ta, nil
}

func dumpIndent(w io.Writer, level int) {
	io.WriteString(w, "| ")
	for i := 0; i < level; i++ {
		io.WriteString(w, "  ")
	}
}

type sortedAttributes []Attribute

func (a sortedAttributes) Len() int {
	return len(a)
}

func (a sortedAttributes) Less(i, j int) bool {
	if a[i].Namespace != a[j].Namespace {
		return a[i].Namespace < a[j].Namespace
	}
	return a[i].Key < a[j].Key
}

func (a sortedAttributes) Swap(i, j int) {
	a[i], a[j] = a[j], a[i]
}

func dumpLevel(w io.Writer, n *Node, level int) error {
	dumpIndent(w, level)
	level++
	switch n.Type {
	case ErrorNode:
		return errors.New("unexpected ErrorNode")
	case DocumentNode:
		return errors.New("unexpected DocumentNode")
	case ElementNode:
		if n.Namespace != "" {
			fmt.Fprintf(w, "<%s %s>", n.Namespace, n.Data)
		} else {
			fmt.Fprintf(w, "<%s>", n.Data)
		}
		attr := sortedAttributes(n.Attr)
		sort.Sort(attr)
		for _, a := range attr {
			io.WriteString(w, "\n")
			dumpIndent(w, level)
			if a.Namespace != "" {
				fmt.Fprintf(w, `%s %s="%s"`, a.Namespace, a.Key, a.Val)
			} else {
				fmt.Fprintf(w, `%s="%s"`, a.Key, a.Val)
			}
		}
		if n.Namespace == "" && n.DataAtom == atom.Template {
			io.WriteString(w, "\n")
			dumpIndent(w, level)
			level++
			io.WriteString(w, "content")
		}
	case TextNode:
		fmt.Fprintf(w, `"%s"`, n.Data)
	case CommentNode:
		fmt.Fprintf(w, "<!-- %s -->", n.Data)
	case DoctypeNode:
		fmt.Fprintf(w, "<!DOCTYPE %s", n.Data)
		if n.Attr != nil {
			var p, s string
			for _, a := range n.Attr {
				switch a.Key {
				case "public":
					p = a.Val
				case "system":
					s = a.Val
				}
			}
			if p != "" || s != "" {
				fmt.Fprintf(w, ` "%s"`, p)
				fmt.Fprintf(w, ` "%s"`, s)
			}
		}
		io.WriteString(w, ">")
	case scopeMarkerNode:
		return errors.New("unexpected scopeMarkerNode")
	default:
		return errors.New("unknown node type")
	}
	io.WriteString(w, "\n")
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if err := dumpLevel(w, c, level); err != nil {
			return err
		}
	}
	return nil
}

func dump(n *Node) (string, error) {
	if n == nil || n.FirstChild == nil {
		return "", nil
	}
	var b bytes.Buffer
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if err := dumpLevel(&b, c, 0); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}

var testDataDirs = []string{"testdata/webkit/", "testdata/go/"}

func TestParser(t *testing.T) {
	for _, testDataDir := range testDataDirs {
		testFiles, err := filepath.Glob(testDataDir + "*.dat")
		if err != nil {
			t.Fatal(err)
		}
		for _, tf := range testFiles {
			f, err := os.Open(tf)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			r := bufio.NewReader(f)

			for i := 0; ; i++ {
				ta, err := readParseTest(r)
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if parseTestBlacklist[ta.text] {
					continue
				}

				err = testParseCase(ta.text, ta.want, ta.context, ParseOptionEnableScripting(ta.scripting))

				if err != nil {
					t.Errorf("%s test #%d %q, %s", tf, i, ta.text, err)
				}
			}
		}
	}
}

// Issue 16318
func TestParserWithoutScripting(t *testing.T) {
	text := `<noscript><img src='https://golang.org/doc/gopher/frontpage.png' /></noscript><p><img src='https://golang.org/doc/gopher/doc.png' /></p>`
	want := `| <html>
|   <head>
|     <noscript>
|   <body>
|     <img>
|       src="https://golang.org/doc/gopher/frontpage.png"
|     <p>
|       <img>
|         src="https://golang.org/doc/gopher/doc.png"
`

	if err := testParseCase(text, want, "", ParseOptionEnableScripting(false)); err != nil {
		t.Errorf("test with scripting is disabled, %q, %s", text, err)
	}
}

// testParseCase tests one test case from the test files. If the test does not
// pass, it returns an error that explains the failure.
// text is the HTML to be parsed, want is a dump of the correct parse tree,
// and context is the name of the context node, if any.
func testParseCase(text, want, context string, opts ...ParseOption) (err error) {
	defer func() {
		if x := recover(); x != nil {
			switch e := x.(type) {
			case error:
				err = e
			default:
				err = fmt.Errorf("%v", e)
			}
		}
	}()

	var doc *Node
	if context == "" {
		doc, err = ParseWithOptions(strings.NewReader(text), opts...)
		if err != nil {
			return err
		}
	} else {
		namespace := ""
		if i := strings.IndexByte(context, ' '); i >= 0 {
			namespace, context = context[:i], context[i+1:]
		}
		contextNode := &Node{
			Data:      context,
			DataAtom:  atom.Lookup([]byte(context)),
			Namespace: namespace,
			Type:      ElementNode,
		}
		nodes, err := ParseFragmentWithOptions(strings.NewReader(text), contextNode, opts...)
		if err != nil {
			return err
		}
		doc = &Node{
			Type: DocumentNode,
		}
		for _, n := range nodes {
			doc.AppendChild(n)
		}
	}

	if err := checkTreeConsistency(doc); err != nil {
		return err
	}

	got, err := dump(doc)
	if err != nil {
		return err
	}
	// Compare the parsed tree to the #document section.
	if got != want {
		return fmt.Errorf("got vs want:\n----\n%s----\n%s----", got, want)
	}

	if renderTestBlacklist[text] || context != "" {
		return nil
	}

	// Check that rendering and re-parsing results in an identical tree.
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(Render(pw, doc))
	}()
	doc1, err := ParseWithOptions(pr, opts...)
	if err != nil {
		return err
	}
	got1, err := dump(doc1)
	if err != nil {
		return err
	}
	if got != got1 {
		return fmt.Errorf("got vs got1:\n----\n%s----\n%s----", got, got1)
	}

	return nil
}

// Some test inputs are simply skipped - we would otherwise fail the test. We
// blacklist such inputs from the parse test.
var parseTestBlacklist = map[string]bool{
	// See the a.Template TODO in inHeadIM.
	`<math><template><mo><template>`:                                     true,
	`<template><svg><foo><template><foreignObject><div></template><div>`: true,
}

// Some test input result in parse trees are not 'well-formed' despite
// following the HTML5 recovery algorithms. Rendering and re-parsing such a
// tree will not result in an exact clone of that tree. We blacklist such
// inputs from the render test.
var renderTestBlacklist = map[string]bool{
	// The second <a> will be reparented to the first <table>'s parent. This
	// results in an <a> whose parent is an <a>, which is not 'well-formed'.
	`<a><table><td><a><table></table><a></tr><a></table><b>X</b>C<a>Y`: true,
	// The same thing with a <p>:
	`<p><table></p>`: true,
	// More cases of <a> being reparented:
	`<a href="blah">aba<table><a href="foo">br<tr><td></td></tr>x</table>aoe`: true,
	`<a><table><a></table><p><a><div><a>`:                                     true,
	`<a><table><td><a><table></table><a></tr><a></table><a>`:                  true,
	`<template><a><table><a>`:                                                 true,
	// A similar reparenting situation involving <nobr>:
	`<!DOCTYPE html><body><b><nobr>1<table><nobr></b><i><nobr>2<nobr></i>3`: true,
	// A <plaintext> element is reparented, putting it before a table.
	// A <plaintext> element can't have anything after it in HTML.
	`<table><plaintext><td>`:                                   true,
	`<!doctype html><table><plaintext></plaintext>`:            true,
	`<!doctype html><table><tbody><plaintext></plaintext>`:     true,
	`<!doctype html><table><tbody><tr><plaintext></plaintext>`: true,
	// A form inside a table inside a form doesn't work either.
	`<!doctype html><form><table></form><form></table></form>`: true,
	// A script that ends at EOF may escape its own closing tag when rendered.
	`<!doctype html><script><!--<script `:          true,
	`<!doctype html><script><!--<script <`:         true,
	`<!doctype html><script><!--<script <a`:        true,
	`<!doctype html><script><!--<script </`:        true,
	`<!doctype html><script><!--<script </s`:       true,
	`<!doctype html><script><!--<script </script`:  true,
	`<!doctype html><script><!--<script </scripta`: true,
	`<!doctype html><script><!--<script -`:         true,
	`<!doctype html><script><!--<script -a`:        true,
	`<!doctype html><script><!--<script -<`:        true,
	`<!doctype html><script><!--<script --`:        true,
	`<!doctype html><script><!--<script --a`:       true,
	`<!doctype html><script><!--<script --<`:       true,
	`<script><!--<script `:                         true,
	`<script><!--<script <a`:                       true,
	`<script><!--<script </script`:                 true,
	`<script><!--<script </scripta`:                true,
	`<script><!--<script -`:                        true,
	`<script><!--<script -a`:                       true,
	`<script><!--<script --`:                       true,
	`<script><!--<script --a`:                      true,
	`<script><!--<script <`:                        true,
	`<script><!--<script </`:                       true,
	`<script><!--<script </s`:                      true,
	// Reconstructing the active formatting elements results in a <plaintext>
	// element that contains an <a> element.
	`<!doctype html><p><a><plaintext>b`:                       true,
	`<table><math><select><mi><select></table>`:               true,
	`<!doctype html><table><colgroup><plaintext></plaintext>`: true,
	`<!doctype html><svg><plaintext>a</plaintext>b`:           true,
}

func TestNodeConsistency(t *testing.T) {
	// inconsistentNode is a Node whose DataAtom and Data do not agree.
	inconsistentNode := &Node{
		Type:     ElementNode,
		DataAtom: atom.Frameset,
		Data:     "table",
	}
	if _, err := ParseFragment(strings.NewReader("<p>hello</p>"), inconsistentNode); err == nil {
		t.Errorf("got nil error, want non-nil")
	}
}

func TestParseFragmentWithNilContext(t *testing.T) {
	// This shouldn't panic.
	ParseFragment(strings.NewReader("<p>hello</p>"), nil)
}

func TestParseFragmentForeignContentTemplates(t *testing.T) {
	srcs := []string{
		"<math><html><template><mn><template></template></template>",
		"<math><math><head><mi><template>",
		"<svg><head><title><select><input>",
	}
	for _, src := range srcs {
		// The next line shouldn't infinite-loop.
		ParseFragment(strings.NewReader(src), nil)
	}
}

func TestSearchTagClosesP(t *testing.T) {
	data := `<p>Unclosed paragraph<search>Search content</search>`
	node, err := Parse(strings.NewReader(data))
	if err != nil {
		t.Fatalf("Error parsing HTML: %v", err)
	}

	var builder strings.Builder
	Render(&builder, node)
	output := builder.String()

	expected := `<html><head></head><body><p>Unclosed paragraph</p><search>Search content</search></body></html>`
	if output != expected {
		t.Errorf("Parse(%q) = %q, want %q", data, output, expected)
	}
}

func BenchmarkParser(b *testing.B) {
	buf, err := os.ReadFile("testdata/go1.html")
	if err != nil {
		b.Fatalf("could not read testdata/go1.html: %v", err)
	}
	b.SetBytes(int64(len(buf)))
	runtime.GC()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Parse(bytes.NewBuffer(buf))
	}
}
