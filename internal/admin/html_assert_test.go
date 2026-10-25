package admin

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// requireElement checks browser-relevant attributes independently of HTML layout.
func requireElement(t *testing.T, body []byte, tag string, attributes map[string]string, text string) {
	t.Helper()

	document, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}

	var matches func(*html.Node) bool

	matches = func(node *html.Node) bool {
		if node.Type == html.ElementNode && node.Data == tag && hasAttributes(node, attributes) && (text == "" || strings.Join(strings.Fields(elementText(node)), " ") == text) {
			return true
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if matches(child) {
				return true
			}
		}

		return false
	}
	if !matches(document) {
		t.Fatalf("missing %s with attributes %v and text %q in %s", tag, attributes, text, body)
	}
}

func hasAttributes(node *html.Node, wanted map[string]string) bool {
	for name, value := range wanted {
		found := false

		for _, attribute := range node.Attr {
			if attribute.Key == name && attribute.Val == value {
				found = true
				break
			}
		}

		if !found {
			return false
		}
	}

	return true
}

func elementText(node *html.Node) string {
	if node.Type == html.TextNode {
		return node.Data
	}

	var result strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		result.WriteString(elementText(child))
	}

	return result.String()
}
