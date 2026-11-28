package runner

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"cyberstrike-ai/internal/pluginhub/manifest"

	"gopkg.in/yaml.v3"
)

type ModelConfig struct {
	Env               map[string]string
	Templates         []ModelTemplate
	GatewayRoot       string
	DockerHostMapping string
}

type ModelTemplate struct {
	Manifest manifest.ConfigTemplate
	Values   map[string]string
}

const modelSubdir = ".csai-model"

func renderModelConfig(spec Spec) (map[string]string, []string, error) {
	base := filepath.Join(spec.VersionDir(), modelSubdir)
	// This workspace contains only generated configuration for this generation.
	if err := os.RemoveAll(base); err != nil {
		return nil, nil, err
	}
	if spec.Model == nil {
		return nil, nil, nil
	}
	env := map[string]string{}
	var sensitive []string
	for k, v := range spec.Model.Env {
		env[k] = v
		sensitive = append(sensitive, v)
	}
	if len(spec.Model.Templates) > 0 {
		if err := os.MkdirAll(base, 0700); err != nil {
			return nil, nil, err
		}
		root, err := os.OpenRoot(base)
		if err != nil {
			return nil, nil, err
		}
		defer root.Close()
		for _, template := range spec.Model.Templates {
			data, err := renderTemplate(spec.VersionDir(), template)
			if err != nil {
				return nil, nil, err
			}
			target := filepath.FromSlash(template.Manifest.Path)
			if !filepath.IsLocal(target) {
				return nil, nil, fmt.Errorf("invalid plugin model template output path")
			}
			if dir := filepath.Dir(target); dir != "." {
				if err := root.MkdirAll(dir, 0700); err != nil {
					return nil, nil, err
				}
			}
			f, err := root.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return nil, nil, err
			}
			_, writeErr := f.Write(data)
			closeErr := f.Close()
			if writeErr != nil {
				return nil, nil, writeErr
			}
			if closeErr != nil {
				return nil, nil, closeErr
			}
			for _, value := range template.Values {
				sensitive = append(sensitive, value)
			}
		}
		env["CSAI_PLUGIN_MODEL_CONFIG_DIR"] = base
	}
	return env, sensitive, nil
}

func renderTemplate(version string, t ModelTemplate) ([]byte, error) {
	var original []byte
	if t.Manifest.SourcePath != "" {
		filename, err := existingPath(version, filepath.FromSlash(t.Manifest.SourcePath))
		if err != nil {
			return nil, err
		}
		file, err := os.Open(filename)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		original, err = io.ReadAll(io.LimitReader(file, manifest.MaxDocumentBytes+1))
		if err != nil || len(original) > manifest.MaxDocumentBytes {
			return nil, fmt.Errorf("plugin model template too large")
		}
	}
	if t.Manifest.Format == "env" {
		lines := strings.Split(strings.TrimSuffix(string(original), "\n"), "\n")
		var out strings.Builder
		for _, line := range lines {
			if strings.TrimSpace(line) == "" {
				continue
			}
			if key, _, ok := strings.Cut(line, "="); ok {
				if _, replaced := t.Values[strings.TrimSpace(key)]; replaced {
					continue
				}
			}
			out.WriteString(line + "\n")
		}
		keys := make([]string, 0, len(t.Values))
		for k := range t.Values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if strings.ContainsAny(key, "=\r\n") || strings.ContainsAny(t.Values[key], "\r\n\x00") {
				return nil, fmt.Errorf("invalid model template environment value")
			}
			out.WriteString(key + "=" + t.Values[key] + "\n")
		}
		return []byte(out.String()), nil
	}
	var document any = map[string]any{}
	if len(original) != 0 {
		var err error
		switch t.Manifest.Format {
		case "json":
			err = json.Unmarshal(original, &document)
		case "yaml":
			err = yaml.Unmarshal(original, &document)
		default:
			return nil, fmt.Errorf("unrecognized plugin model template format")
		}
		if err != nil {
			return nil, fmt.Errorf("invalid plugin model template")
		}
	}
	keys := make([]string, 0, len(t.Values))
	for k := range t.Values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		parts := strings.Split(strings.TrimPrefix(key, "/"), "/")
		for i, v := range parts {
			parts[i] = strings.ReplaceAll(strings.ReplaceAll(v, "~1", "/"), "~0", "~")
		}
		if err := replaceTemplateValue(document, parts, t.Values[key]); err != nil {
			return nil, err
		}
	}
	if t.Manifest.Format == "yaml" {
		return yaml.Marshal(document)
	}
	return json.MarshalIndent(document, "", "  ")
}

func replaceTemplateValue(node any, parts []string, value string) error {
	switch current := node.(type) {
	case map[string]any:
		if len(parts) == 1 {
			current[parts[0]] = value
			return nil
		}
		child := current[parts[0]]
		if child == nil {
			child = map[string]any{}
			current[parts[0]] = child
		}
		return replaceTemplateValue(child, parts[1:], value)
	case []any:
		i, err := strconv.Atoi(parts[0])
		if err != nil || i < 0 || i >= len(current) {
			return fmt.Errorf("invalid plugin model template array index")
		}
		if len(parts) == 1 {
			current[i] = value
			return nil
		}
		return replaceTemplateValue(current[i], parts[1:], value)
	default:
		return fmt.Errorf("invalid plugin model template pointer")
	}
}
