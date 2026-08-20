package releasesnapshot

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func validateComposeWorkspace(workspace, composePath string) error {
	if composePath == "" || strings.Contains(composePath, "${") {
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
			for _, key := range []string{"context", "dockerfile"} {
				if v := mapValue(build, key); v != nil {
					if err := validatePathNode(workspace, base, v, key == "context"); err != nil {
						return err
					}
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
func validatePathNode(workspace, base string, node *yaml.Node, wantDir bool) error {
	if node == nil || node.Kind != yaml.ScalarNode {
		return errors.New("invalid compose path")
	}
	value := strings.TrimSpace(node.Value)
	if value == "" || strings.Contains(value, "${") || strings.Contains(value, "}") || strings.Contains(value, "://") || strings.HasPrefix(value, "git@") || filepath.IsAbs(value) || strings.HasPrefix(value, "\\") || (len(value) > 1 && value[1] == ':') {
		return errors.New("unsafe compose path")
	}
	candidate := filepath.Join(base, filepath.FromSlash(strings.ReplaceAll(value, "\\", "/")))
	if !within(workspace, candidate) {
		return errors.New("workspace escape")
	}
	info, err := os.Stat(candidate)
	if err != nil {
		return errors.New("compose path not found")
	}
	if wantDir && !info.IsDir() {
		return errors.New("compose build context is not directory")
	}
	if !wantDir && info.IsDir() {
		return errors.New("compose resource is not file")
	}
	return nil
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
