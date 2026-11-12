// Package yamlutil preserves safe decoder locations without echoing submitted values.
package yamlutil

import (
	"fmt"
	"regexp"
	"strconv"

	"go.yaml.in/yaml/v3"
)

var yamlLine = regexp.MustCompile(`line ([0-9]+):`)
var fieldName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,79}$`)

// decodeError extracts location metadata, never the decoder's value-bearing text.
func DecodeError(scope string, data []byte, cause error) error {
	match := yamlLine.FindStringSubmatch(cause.Error())
	if len(match) != 2 {
		return fmt.Errorf("%s: invalid YAML or field type", scope)
	}
	line, _ := strconv.Atoi(match[1])
	field := "document"
	var document yaml.Node
	if yaml.Unmarshal(data, &document) == nil {
		bestLine := 0
		var visit func(*yaml.Node, string)
		visit = func(node *yaml.Node, path string) {
			if node.Line <= line && node.Line >= bestLine && path != "" {
				bestLine = node.Line
				field = path
			}
			switch node.Kind {
			case yaml.DocumentNode:
				for _, child := range node.Content {
					visit(child, path)
				}
			case yaml.MappingNode:
				for i := 0; i+1 < len(node.Content); i += 2 {
					key, value := node.Content[i], node.Content[i+1]
					name := key.Value
					if !fieldName.MatchString(name) {
						name = "[unknown field]"
					}
					childPath := name
					if path != "" {
						childPath = path + "." + name
					}
					visit(key, childPath)
					visit(value, childPath)
				}
			case yaml.SequenceNode:
				for i, child := range node.Content {
					visit(child, fmt.Sprintf("%s[%d]", path, i))
				}
			}
		}
		visit(&document, "")
	}
	return fmt.Errorf("%s: invalid field %s at line %d; check schema, field type and duplicate keys", scope, field, line)
}
