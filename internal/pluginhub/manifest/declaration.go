package manifest

import (
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

const (
	OpenAI    = "openai_chat"
	Anthropic = "anthropic_messages"
)

type Declaration struct {
	Canonical    []byte            `json:"-"`
	Metadata     Metadata          `json:"metadata"`
	Distribution []Distribution    `json:"distribution"`
	Launch       *Launch           `json:"launch"`
	Web          Web               `json:"web"`
	Access       Access            `json:"access"`
	LLM          *LLM              `json:"llm"`
	Secrets      map[string]Secret `json:"secrets"`
	Permissions  Permissions       `json:"permissions"`
	Extensions   Extensions        `json:"extensions"`
}

type Metadata struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Summary    string `json:"summary"`
	Version    string `json:"version"`
	Repository string `json:"repository"`
	License    string `json:"license"`
}

type Distribution struct {
	Type       string      `json:"type"`
	Assets     []Asset     `json:"assets"`
	URL        string      `json:"url"`
	Revision   string      `json:"revision"`
	OutputRoot string      `json:"outputRoot"`
	Entrypoint string      `json:"entrypoint"`
	Build      []BuildStep `json:"build"`
	Mode       string      `json:"mode"`
	Services   []Service   `json:"services"`
}

type Asset struct {
	OS              string `json:"os"`
	Arch            string `json:"arch"`
	Name            string `json:"name"`
	Format          string `json:"format"`
	SHA256          string `json:"sha256"`
	StripComponents int    `json:"stripComponents"`
	URL             string `json:"url"`
	Entrypoint      string `json:"entrypoint"`
	Attestation     string `json:"attestationURL"`
}

type BuildStep struct {
	Program string   `json:"program"`
	WorkDir string   `json:"workDir"`
	Args    []string `json:"args"`
}

type Environment struct {
	Env       map[string]string    `json:"env"`
	StaticEnv map[string]string    `json:"staticEnv"`
	SecretEnv map[string]SecretUse `json:"secretEnv"`
}

type Launch struct {
	Environment
	Entrypoint string   `json:"entrypoint"`
	WorkDir    string   `json:"workDir"`
	Args       []string `json:"args"`
}

type Secret struct {
	Generator string `json:"generator"`
	Bytes     int    `json:"bytes"`
}

type SecretUse struct {
	Kind      string            `json:"kind"`
	SecretRef string            `json:"secretRef"`
	Service   string            `json:"service"`
	Scheme    string            `json:"scheme"`
	Username  string            `json:"username"`
	Database  string            `json:"database"`
	Options   map[string]string `json:"options"`
}

type Service struct {
	Environment
	Name          string            `json:"name"`
	Role          string            `json:"role"`
	Image         string            `json:"image"`
	ContainerPort int               `json:"containerPort"`
	DependsOn     map[string]string `json:"depends_on"`
	Healthcheck   *Healthcheck      `json:"healthcheck"`
	DataVolumes   []Mount           `json:"dataVolumes"`
	ConfigMounts  []Mount           `json:"configMounts"`
	Resources     Resources         `json:"resources"`
}

type Resources struct {
	MemoryMiB int `json:"memoryMiB"`
	PIDs      int `json:"pids"`
}

type Mount struct {
	Name      string `json:"name"`
	Source    string `json:"source"`
	MountPath string `json:"mountPath"`
}

type Healthcheck struct {
	Kind               string   `json:"kind"`
	Path               string   `json:"path"`
	Argv               []string `json:"argv"`
	IntervalSeconds    int      `json:"intervalSeconds"`
	TimeoutSeconds     int      `json:"timeoutSeconds"`
	Retries            int      `json:"retries"`
	StartPeriodSeconds int      `json:"startPeriodSeconds"`
}

type Web struct {
	Route               string `json:"route"`
	EntryPath           string `json:"entryPath"`
	HealthPath          string `json:"healthPath"`
	ReadyHeaderCheck    bool   `json:"readyHeaderCheck"`
	AllowHealthRedirect bool   `json:"allowHealthRedirect"`
}

type Access struct {
	ApplicationBearer []string   `json:"applicationBearer"`
	Bootstrap         *Bootstrap `json:"bootstrap"`
}

type Bootstrap struct {
	UsernameEnv     string `json:"usernameEnv"`
	PasswordFileEnv string `json:"passwordFileEnv"`
}

type Permissions struct {
	PersistentData bool     `json:"persistentData"`
	Egress         []string `json:"egress"`
}

type Extensions struct {
	Capabilities []Capability `json:"capabilities"`
}

type Capability struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

type LLM struct {
	Selection string    `json:"selection"`
	Bindings  []Binding `json:"bindings"`
}

type Binding struct {
	Protocol       string            `json:"protocol"`
	ProviderValue  string            `json:"providerValue"`
	Env            map[string]string `json:"env"`
	ConfigTemplate *ConfigTemplate   `json:"configTemplate"`
}

type ConfigTemplate struct {
	Format     string            `json:"format"`
	Path       string            `json:"path"`
	SourcePath string            `json:"sourcePath"`
	Values     map[string]string `json:"values"`
}

// DecodeDeclaration parses declarative metadata. Successful parsing never grants execution rights.
func DecodeDeclaration(data []byte) (*Declaration, error) {
	var declaration Declaration
	canonical, err := decode(data, declarationSchema, &declaration)
	if err != nil {
		return nil, err
	}
	declaration.Canonical = canonical
	if err := declaration.validate(); err != nil {
		return nil, err
	}
	return &declaration, nil
}

func (m *Declaration) validate() error {
	if err := ValidateHTTPS(m.Metadata.Repository); err != nil {
		return fmt.Errorf("metadata.repository: %w", err)
	}
	if err := validateAppPaths(append([]string{m.Web.EntryPath, m.Web.HealthPath}, m.Access.ApplicationBearer...)...); err != nil {
		return err
	}
	if m.Launch != nil {
		if err := validateFiles(m.Launch.Entrypoint, m.Launch.WorkDir); err != nil {
			return err
		}
		if err := m.validateEnvironment(m.Launch.Environment, nil, true); err != nil {
			return fmt.Errorf("launch: %w", err)
		}
		if err := validateArgs(m.Launch.Args); err != nil {
			return err
		}
	}
	drivers := map[string]bool{}
	for _, distribution := range m.Distribution {
		if drivers[distribution.Type] {
			return fmt.Errorf("duplicate %s distribution", distribution.Type)
		}
		drivers[distribution.Type] = true
		if err := m.validateDistribution(distribution); err != nil {
			return fmt.Errorf("%s distribution: %w", distribution.Type, err)
		}
	}
	capabilities := map[string]bool{}
	for _, capability := range m.Extensions.Capabilities {
		if capabilities[capability.ID] {
			return fmt.Errorf("duplicate capability ID")
		}
		capabilities[capability.ID] = true
		if err := validateAppPaths(capability.Path); err != nil {
			return err
		}
	}
	return m.validateBindings()
}

func (m *Declaration) validateDistribution(d Distribution) error {
	switch d.Type {
	case "release":
		platforms := map[string]bool{}
		for _, asset := range d.Assets {
			platform := asset.OS + "/" + asset.Arch
			if platforms[platform] {
				return fmt.Errorf("duplicate asset platform %s", platform)
			}
			platforms[platform] = true
			if err := validateFiles(asset.Name, asset.Entrypoint); err != nil || strings.Contains(asset.Name, "/") {
				return fmt.Errorf("invalid release asset filename or entrypoint")
			}
			if err := ValidateHTTPS(asset.URL); err != nil {
				return err
			}
			if asset.Attestation != "" {
				if err := ValidateHTTPS(asset.Attestation); err != nil {
					return err
				}
			}
		}
	case "git":
		if err := ValidateHTTPS(d.URL); err != nil {
			return err
		}
		if err := ValidateGitRef(d.Revision); err != nil {
			return err
		}
		if err := validateFiles(d.OutputRoot, d.Entrypoint); err != nil {
			return err
		}
		for _, step := range d.Build {
			if err := validateFiles(step.WorkDir); err != nil {
				return err
			}
			if err := validateArgs(step.Args); err != nil {
				return err
			}
		}
	case "container":
		return m.validateServices(d.Services)
	}
	return nil
}

func (m *Declaration) validateServices(services []Service) error {
	byName := make(map[string]Service, len(services))
	webCount := 0
	for _, service := range services {
		if _, exists := byName[service.Name]; exists {
			return fmt.Errorf("duplicate container service name")
		}
		if err := ValidateImageReference(service.Image); err != nil {
			return err
		}
		byName[service.Name] = service
		if service.Role == "web" {
			webCount++
		}
	}
	if webCount != 1 {
		return fmt.Errorf("expected exactly one web container service")
	}
	for _, service := range services {
		if err := m.validateEnvironment(service.Environment, byName, service.Role == "web"); err != nil {
			return fmt.Errorf("service %s: %w", service.Name, err)
		}
		if err := m.validateService(service, byName); err != nil {
			return fmt.Errorf("service %s: %w", service.Name, err)
		}
	}
	return validateDependencyGraph(byName)
}

func (m *Declaration) validateService(service Service, services map[string]Service) error {
	if service.Healthcheck != nil {
		check := service.Healthcheck
		if check.TimeoutSeconds > check.IntervalSeconds || (check.Kind != "exec" && service.ContainerPort == 0) {
			return fmt.Errorf("invalid healthcheck timing or missing private port")
		}
		if err := validateArgs(check.Argv); err != nil {
			return err
		}
		if check.Path != "" {
			if err := validateAppPaths(check.Path); err != nil {
				return err
			}
		}
	}
	mountPaths := map[string]bool{}
	mountNames := map[string]bool{}
	for _, mount := range append(slices.Clone(service.DataVolumes), service.ConfigMounts...) {
		if mount.MountPath == "/" || mountPaths[mount.MountPath] || (mount.Name != "" && mountNames[mount.Name]) {
			return fmt.Errorf("duplicate or invalid mount")
		}
		mountPaths[mount.MountPath] = true
		mountNames[mount.Name] = mount.Name != ""
		if err := validateAppPaths(mount.MountPath); err != nil {
			return err
		}
		if err := validateFiles(mount.Source); err != nil {
			return err
		}
	}
	if len(service.DataVolumes) > 0 && !m.Permissions.PersistentData {
		return fmt.Errorf("data volumes require persistentData")
	}
	for name, condition := range service.DependsOn {
		dependency, exists := services[name]
		if !exists || (condition == "service_healthy" && dependency.Healthcheck == nil) {
			return fmt.Errorf("dependency missing or lacks a healthcheck")
		}
	}
	if len(service.DependsOn) > 0 && !slices.Contains(m.Permissions.Egress, "managed_dependency") {
		return fmt.Errorf("service dependency requires managed_dependency egress")
	}
	return nil
}

func validateDependencyGraph(services map[string]Service) error {
	visiting := map[string]bool{}
	done := map[string]bool{}
	var visit func(string) bool
	visit = func(name string) bool {
		if visiting[name] {
			return false
		}
		if done[name] {
			return true
		}
		visiting[name] = true
		for dependency := range services[name].DependsOn {
			if !visit(dependency) {
				return false
			}
		}
		delete(visiting, name)
		done[name] = true
		return true
	}
	for name := range services {
		if !visit(name) {
			return fmt.Errorf("container services have a dependency cycle")
		}
	}
	return nil
}

func (m *Declaration) validateEnvironment(env Environment, services map[string]Service, web bool) error {
	names := map[string]bool{}
	if web && m.Access.Bootstrap != nil {
		if err := addEnv(names, m.Access.Bootstrap.UsernameEnv); err != nil {
			return err
		}
		if err := addEnv(names, m.Access.Bootstrap.PasswordFileEnv); err != nil {
			return err
		}
	}
	if web && m.LLM != nil {
		for _, binding := range m.LLM.Bindings {
			for name := range binding.Env {
				names[name] = true
			}
		}
	}
	for name := range env.Env {
		if err := addEnv(names, name); err != nil {
			return err
		}
	}
	for name, value := range env.StaticEnv {
		if err := addEnv(names, name); err != nil {
			return err
		}
		if strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("invalid control character in a static environment value")
		}
	}
	for name, use := range env.SecretEnv {
		if err := addEnv(names, name); err != nil {
			return err
		}
		if _, exists := m.Secrets[use.SecretRef]; !exists {
			return fmt.Errorf("unknown Secret reference")
		}
		if use.Kind == "connection_uri" {
			destination, exists := services[use.Service]
			if !exists || destination.ContainerPort == 0 || destination.Role != "dependency" ||
				!slices.Contains(m.Permissions.Egress, "managed_dependency") {
				return fmt.Errorf("connection URI requires a private dependency service with a port")
			}
		}
	}
	return nil
}

func (m *Declaration) validateBindings() error {
	if m.LLM == nil && slices.Contains(m.Permissions.Egress, "managed_model_gateway") {
		return fmt.Errorf("gateway egress requires an LLM declaration")
	}
	names := map[string]bool{}
	if m.Access.Bootstrap != nil {
		if err := addEnv(names, m.Access.Bootstrap.UsernameEnv); err != nil {
			return err
		}
		if err := addEnv(names, m.Access.Bootstrap.PasswordFileEnv); err != nil {
			return err
		}
	}
	if m.LLM == nil {
		return nil
	}
	if m.LLM.Selection == "" {
		m.LLM.Selection = "multi"
	}
	protocols := map[string]bool{}
	templatePaths := map[string]bool{}
	for _, binding := range m.LLM.Bindings {
		if protocols[binding.Protocol] {
			return fmt.Errorf("duplicate LLM protocol binding")
		}
		protocols[binding.Protocol] = true
		used := make(map[string]bool, len(names))
		for name := range names {
			used[name] = true
		}
		values := map[string]bool{}
		for name, value := range binding.Env {
			if err := addEnv(used, name); err != nil {
				return err
			}
			values[value] = true
		}
		if m.LLM.Selection == "multi" {
			names = used
		}
		if template := binding.ConfigTemplate; template != nil {
			if err := validateFiles(template.Path, template.SourcePath); err != nil {
				return err
			}
			key := strings.ToLower(template.Path)
			if templatePaths[key] && m.LLM.Selection == "multi" {
				return fmt.Errorf("concurrent LLM bindings share a template path")
			}
			templatePaths[key] = true
			for _, value := range template.Values {
				values[value] = true
			}
		}
		urlValue, otherURL := "gateway_v1_url", "gateway_root_url"
		if binding.Protocol == Anthropic {
			urlValue, otherURL = otherURL, urlValue
		}
		if !values["gateway_token"] || !values[urlValue] || values[otherURL] {
			return fmt.Errorf("LLM binding requires a gateway token and the protocol-specific URL")
		}
	}
	return nil
}

// ActiveBindings selects the bindings to consider when rendering a future runtime
// environment. A single-active declaration must have an explicitly selected protocol.
func (l LLM) ActiveBindings(protocol string) ([]Binding, error) {
	if l.Selection == "multi" || l.Selection == "" {
		return slices.Clone(l.Bindings), nil
	}
	for _, binding := range l.Bindings {
		if binding.Protocol == protocol {
			return []Binding{binding}, nil
		}
	}
	return nil, fmt.Errorf("active LLM protocol is missing or undeclared")
}

func addEnv(names map[string]bool, name string) error {
	if names[name] || strings.HasPrefix(name, "CSAI_PLUGIN_") || strings.HasPrefix(name, "LD_") || strings.HasPrefix(name, "DYLD_") || strings.HasPrefix(name, "GIT_") ||
		strings.HasPrefix(name, "BASH_") || slices.Contains([]string{
		"PATH", "PATHEXT", "HOME", "USERPROFILE", "COMSPEC", "SYSTEMROOT", "SSL_CERT_FILE", "SSL_CERT_DIR",
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "NODE_OPTIONS", "PYTHONPATH", "PYTHONHOME", "ENV",
	}, name) {
		return fmt.Errorf("duplicate or reserved environment variable %s", name)
	}
	names[name] = true
	return nil
}

var refPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._/-]{0,179}$`)
var imagePart = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)
var imageTag = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
var imageRegistry = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*(?::[0-9]{1,5})?$`)

func ValidateGitRef(ref string) error {
	if ref == "" {
		return nil
	}
	if !refPattern.MatchString(ref) || strings.Contains(ref, "..") || strings.HasSuffix(ref, ".") ||
		(strings.HasPrefix(ref, "refs/") && !strings.HasPrefix(ref, "refs/heads/") && !strings.HasPrefix(ref, "refs/tags/")) {
		return fmt.Errorf("invalid Git reference")
	}
	for part := range strings.SplitSeq(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return fmt.Errorf("invalid Git reference component")
		}
	}
	return nil
}

func ValidateHTTPS(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" || u.Port() != "" ||
		u.RawQuery != "" || u.ForceQuery || strings.ContainsAny(raw, "#\\") {
		return fmt.Errorf("expected an HTTPS URL without credentials, explicit port, query or fragment")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if addr, err := netip.ParseAddr(host); err == nil {
		addr = addr.Unmap()
		if !addr.IsGlobalUnicast() || addr.IsPrivate() {
			return fmt.Errorf("non-public URL host")
		}
	} else if !strings.Contains(host, ".") || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return fmt.Errorf("non-public URL host")
	}
	for part := range strings.SplitSeq(u.Path, "/") {
		if part == "." || part == ".." || strings.ContainsAny(part, "\x00\r\n") {
			return fmt.Errorf("invalid URL path")
		}
	}
	if strings.Contains(strings.ToLower(u.EscapedPath()), "%2f") || strings.Contains(strings.ToLower(u.EscapedPath()), "%5c") {
		return fmt.Errorf("encoded URL separator")
	}
	return nil
}

func ValidateImageReference(ref string) error {
	_, _, _, err := ParseImageReference(ref)
	return err
}

// ParseImageReference splits an explicit OCI reference. The tag is descriptive
// when a digest is present, including for repository:tag@sha256 references.
func ParseImageReference(ref string) (repository, tag, digest string, err error) {
	repository, digest, pinned := strings.Cut(ref, "@")
	if pinned && (!strings.HasPrefix(digest, "sha256:") || !hex256.MatchString(strings.TrimPrefix(digest, "sha256:"))) {
		return "", "", "", fmt.Errorf("invalid image digest")
	}
	if sep := strings.LastIndex(repository, ":"); sep > strings.LastIndex(repository, "/") {
		tag, repository = repository[sep+1:], repository[:sep]
		if !imageTag.MatchString(tag) {
			return "", "", "", fmt.Errorf("invalid image tag")
		}
	}
	if !pinned && tag == "" {
		return "", "", "", fmt.Errorf("expected explicit OCI tag or digest")
	}
	return repository, tag, digest, validateImageRepository(repository)
}

func validateImageRepository(repository string) error {
	parts := strings.Split(repository, "/")
	if len(parts) > 1 && strings.ContainsAny(parts[0], ".:") {
		if !imageRegistry.MatchString(parts[0]) || strings.Contains(parts[0], "..") {
			return fmt.Errorf("invalid OCI registry")
		}
		parts = parts[1:]
	}
	for _, part := range parts {
		if !imagePart.MatchString(part) {
			return fmt.Errorf("invalid OCI repository")
		}
	}
	return nil
}

func validateArgs(args []string) error {
	for _, arg := range args {
		if strings.ContainsAny(arg, "\x00\r\n") {
			return fmt.Errorf("invalid command argument")
		}
	}
	return nil
}

func validateAppPaths(paths ...string) error {
	for _, path := range paths {
		if strings.HasPrefix(path, "/_csai_plugin/") || path == "/_csai_plugin" {
			return fmt.Errorf("reserved plugin proxy path")
		}
		for part := range strings.SplitSeq(path, "/") {
			if part == "." || part == ".." {
				return fmt.Errorf("invalid application path")
			}
		}
	}
	return nil
}

func validateFiles(paths ...string) error {
	for _, path := range paths {
		if path == "" {
			continue
		}
		for part := range strings.SplitSeq(path, "/") {
			base, _, _ := strings.Cut(strings.ToUpper(part), ".")
			if part == "" || strings.HasSuffix(part, ".") ||
				slices.Contains([]string{"CON", "NUL", "PRN", "AUX", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9"}, base) {
				return fmt.Errorf("invalid portable file path")
			}
		}
	}
	return nil
}
