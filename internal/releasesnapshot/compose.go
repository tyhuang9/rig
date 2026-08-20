package releasesnapshot

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func validateComposeWorkspace(workspace, composePath string) error {
	if composePath == "" || strings.ContainsAny(composePath, "$\\:") || filepath.IsAbs(composePath) || filepath.ToSlash(filepath.Clean(composePath)) != composePath {
		return errors.New("unsafe compose path")
	}
	compose := filepath.Join(workspace, filepath.FromSlash(composePath))
	if !within(workspace, compose) {
		return errors.New("workspace escape")
	}
	contents, err := os.ReadFile(compose)
	if err != nil || len(contents) > 1<<20 {
		return errors.New("compose not found")
	}
	var document yaml.Node
	if yaml.Unmarshal(contents, &document) != nil {
		return errors.New("invalid compose")
	}
	if !safeYAML(&document) {
		return errors.New("invalid compose")
	}
	root := mappingRoot(&document)
	if root == nil {
		return errors.New("invalid compose")
	}
	if mapValue(root, "include") != nil {
		return errors.New("unsupported compose include")
	}
	base := filepath.Dir(compose)
	if services := mapValue(root, "services"); services != nil && services.Kind == yaml.MappingNode {
		for i := 1; i < len(services.Content); i += 2 {
			service := services.Content[i]
			if err := validateServicePaths(workspace, base, service); err != nil {
				return err
			}
		}
	}
	for _, section := range []string{"configs", "secrets"} {
		if values := mapValue(root, section); values != nil && values.Kind == yaml.MappingNode {
			for i := 1; i < len(values.Content); i += 2 {
				if file := mapValue(values.Content[i], "file"); file != nil {
					if err := validatePathNode(workspace, base, file, false); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}
func safeYAML(node *yaml.Node) bool {
	if node == nil {
		return false
	}
	if node.Anchor != "" || node.Alias != nil {
		return false
	}
	if node.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Value == "<<" || seen[key.Value] {
				return false
			}
			seen[key.Value] = true
		}
	}
	for _, child := range node.Content {
		if !safeYAML(child) {
			return false
		}
	}
	return true
}
func validateServicePaths(workspace, base string, service *yaml.Node) error {
	if service == nil || service.Kind != yaml.MappingNode {
		return errors.New("invalid compose service")
	}
	if build := mapValue(service, "build"); build != nil {
		if build.Kind == yaml.ScalarNode {
			if err := validatePathNode(workspace, base, build, true); err != nil {
				return err
			}
		} else if build.Kind == yaml.MappingNode {
			contextPath := base
			if v := mapValue(build, "context"); v != nil {
				var err error
				contextPath, err = resolvePathNode(workspace, base, v, true)
				if err != nil {
					return err
				}
			}
			if v := mapValue(build, "dockerfile"); v != nil {
				if _, err := resolvePathNode(workspace, contextPath, v, false); err != nil {
					return err
				}
			}
			if v := mapValue(build, "additional_contexts"); v != nil {
				if err := validateAdditionalContexts(workspace, base, v); err != nil {
					return err
				}
			}
		} else {
			return errors.New("invalid build")
		}
	}
	if env := mapValue(service, "env_file"); env != nil {
		if env.Kind == yaml.ScalarNode {
			if err := validatePathNode(workspace, base, env, false); err != nil {
				return err
			}
		} else if env.Kind == yaml.SequenceNode {
			for _, v := range env.Content {
				if err := validatePathNode(workspace, base, v, false); err != nil {
					return err
				}
			}
		} else {
			return errors.New("invalid env_file")
		}
	}
	if ext := mapValue(service, "extends"); ext != nil && ext.Kind == yaml.MappingNode {
		if f := mapValue(ext, "file"); f != nil {
			if err := validatePathNode(workspace, base, f, false); err != nil {
				return err
			}
		}
	}
	return nil
}
func validateAdditionalContexts(workspace, base string, node *yaml.Node) error {
	switch node.Kind {
	case yaml.MappingNode:
		for i := 1; i < len(node.Content); i += 2 {
			if _, err := resolveAdditionalContext(workspace, base, node.Content[i]); err != nil {
				return err
			}
		}
		return nil
	case yaml.SequenceNode:
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode || !strings.Contains(item.Value, "=") {
				return errors.New("invalid additional context")
			}
			value := strings.SplitN(item.Value, "=", 2)[1]
			copy := *item
			copy.Value = value
			if _, err := resolveAdditionalContext(workspace, base, &copy); err != nil {
				return err
			}
		}
		return nil
	default:
		return errors.New("invalid additional contexts")
	}
}
func resolveAdditionalContext(workspace, base string, node *yaml.Node) (string, error) {
	value := strings.TrimSpace(node.Value)
	if strings.HasPrefix(value, "service:") || strings.HasPrefix(value, "docker-image:") {
		return "", errors.New("unsupported additional context")
	}
	return resolvePathNode(workspace, base, node, true)
}
func validatePathNode(workspace, base string, node *yaml.Node, wantDir bool) error {
	_, err := resolvePathNode(workspace, base, node, wantDir)
	return err
}
func resolvePathNode(workspace, base string, node *yaml.Node, wantDir bool) (string, error) {
	if node == nil || node.Kind != yaml.ScalarNode {
		return "", errors.New("invalid compose path")
	}
	value := strings.TrimSpace(node.Value)
	if value == "" || strings.ContainsAny(value, "$\\:") || strings.HasPrefix(value, "git@") || filepath.IsAbs(value) || strings.HasPrefix(value, "/") {
		return "", errors.New("unsafe compose path")
	}
	candidate := filepath.Join(base, filepath.FromSlash(strings.ReplaceAll(value, "\\", "/")))
	if !within(workspace, candidate) {
		return "", errors.New("workspace escape")
	}
	info, err := os.Stat(candidate)
	if err != nil {
		return "", errors.New("compose path not found")
	}
	if wantDir && !info.IsDir() {
		return "", errors.New("compose build context is not directory")
	}
	if !wantDir && info.IsDir() {
		return "", errors.New("compose resource is not file")
	}
	return candidate, nil
}
func mappingRoot(document *yaml.Node) *yaml.Node {
	if document.Kind == yaml.DocumentNode && len(document.Content) == 1 && document.Content[0].Kind == yaml.MappingNode {
		return document.Content[0]
	}
	return nil
}
func mapValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}
