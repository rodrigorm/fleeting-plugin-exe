// Package exedev provides a small command client for the documented exe.dev SSH API.
package exedev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxCommandErrorOutput = 64 * 1024
	maxDiagnosticRunes    = 240
	defaultCommandTimeout = 45 * time.Second
	execWaitDelay         = 5 * time.Second
)

var (
	vmNamePattern       = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	controlHostPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@:-]*$`)
	binaryNamePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
	absolutePathPattern = regexp.MustCompile(`^/[A-Za-z0-9._/+@-]+$`)
	resourcePattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	tokenPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@+=-]*$`)
	tagPattern          = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	cpuPattern          = regexp.MustCompile(`^[0-9]+$`)
	sizePattern         = regexp.MustCompile(`^[0-9]+[A-Za-z]{0,4}$`)
)

// Client implements the VM lifecycle operations whose command and response
// formats are documented by exe.dev.
type Client interface {
	List(context.Context) ([]VM, error)
	Create(context.Context, CreateRequest) error
	Delete(context.Context, []string) error
}

// VM is the documented portion of an item returned by ls -l --json.
// Additional fields returned by exe.dev are deliberately ignored. TagsPresent
// distinguishes an omitted tags field from an explicitly empty tags array.
type VM struct {
	VMName        string   `json:"vm_name"`
	Status        string   `json:"status"`
	SSHDest       string   `json:"ssh_dest"`
	SSHHost       string   `json:"ssh_host"`
	SSHUser       string   `json:"ssh_user"`
	HTTPSURL      string   `json:"https_url"`
	Region        string   `json:"region"`
	RegionDisplay string   `json:"region_display"`
	Tags          []string `json:"tags"`
	TagsPresent   bool     `json:"-"`
}

// UnmarshalJSON records whether the tags member was present while retaining
// encoding/json's normal behavior of ignoring unknown VM fields.
func (v *VM) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return errors.New("VM entry must be a non-null JSON object")
	}

	type plainVM VM
	var decoded plainVM
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	_, decoded.TagsPresent = fields["tags"]
	if err := validateListedVMName(decoded.VMName); err != nil {
		return err
	}
	*v = VM(decoded)
	return nil
}

// CreateRequest contains the explicit inputs used to create a VM. Pool and
// Image are optional; CPU, Memory, Disk, Name, and at least one tag are required.
type CreateRequest struct {
	Name   string
	CPU    string
	Memory string
	Disk   string
	Image  string
	Pool   string
	Tags   []string
}

// CommandRunner runs a program directly, without a shell, and returns stdout
// and stderr separately.
type CommandRunner interface {
	Run(ctx context.Context, program string, args ...string) (stdout, stderr []byte, err error)
}

// ExecCommandRunner executes commands directly with os/exec.
type ExecCommandRunner struct{}

// Run executes program directly, captures each output stream independently,
// and bounds pipe-drain waiting after the process exits.
func (ExecCommandRunner) Run(ctx context.Context, program string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, program, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = execWaitDelay
	err := cmd.Run()
	return append([]byte(nil), stdout.Bytes()...), append([]byte(nil), stderr.Bytes()...), err
}

// CommandError reports a failed ssh process. ExitCode is -1 when the runner
// cannot provide one. Output contains at most 64 KiB of stderr, or stdout only
// when stderr was empty.
type CommandError struct {
	Operation string
	ExitCode  int
	Output    string
	Truncated bool
	Err       error
}

func (e *CommandError) Error() string {
	reason := "unknown error"
	if e.Err != nil {
		if excerpt := diagnosticExcerpt(e.Err.Error()); excerpt != "" {
			reason = excerpt
		}
	}

	var message string
	if e.ExitCode >= 0 {
		message = fmt.Sprintf("exe.dev %s command failed with exit code %d: %s", e.Operation, e.ExitCode, reason)
	} else {
		message = fmt.Sprintf("exe.dev %s command failed: %s", e.Operation, reason)
	}
	if excerpt := diagnosticExcerpt(e.Output); excerpt != "" {
		message += ": " + excerpt
	}
	return message
}

// Unwrap exposes the command runner error, including context cancellation.
func (e *CommandError) Unwrap() error {
	return e.Err
}

// CLIClientOptions configures an OpenSSH-backed client.
type CLIClientOptions struct {
	ControlHost    string
	SSHBinary      string
	IdentityFile   string
	KnownHostsFile string
	CommandTimeout time.Duration
	Runner         CommandRunner
}

// CLIOptions is a concise alias for CLIClientOptions.
type CLIOptions = CLIClientOptions

// CLIClient invokes the exe.dev API through OpenSSH.
type CLIClient struct {
	controlHost    string
	sshBinary      string
	identityFile   string
	knownHostsFile string
	commandTimeout time.Duration
	runner         CommandRunner
}

var _ Client = (*CLIClient)(nil)

// NewCLIClient is the convenient constructor using the default ssh binary and
// command timeout. A nil runner selects ExecCommandRunner.
func NewCLIClient(controlHost string, runner CommandRunner) (*CLIClient, error) {
	return NewCLIClientWithOptions(CLIClientOptions{
		ControlHost: controlHost,
		Runner:      runner,
	})
}

// NewCLIClientWithOptions constructs a client with explicit SSH options.
func NewCLIClientWithOptions(options CLIClientOptions) (*CLIClient, error) {
	if err := validateValue("control host", options.ControlHost, controlHostPattern); err != nil {
		return nil, err
	}

	sshBinary := options.SSHBinary
	if sshBinary == "" {
		sshBinary = "ssh"
	}
	if err := validateExecutable("ssh binary", sshBinary); err != nil {
		return nil, err
	}
	if options.IdentityFile != "" {
		if err := validateAbsolutePath("identity file", options.IdentityFile); err != nil {
			return nil, err
		}
	}
	if options.KnownHostsFile != "" {
		if err := validateAbsolutePath("known_hosts file", options.KnownHostsFile); err != nil {
			return nil, err
		}
	}

	commandTimeout := options.CommandTimeout
	if commandTimeout == 0 {
		commandTimeout = defaultCommandTimeout
	}
	if commandTimeout < 0 {
		return nil, errors.New("command timeout must not be negative")
	}

	runner := options.Runner
	if runner == nil {
		runner = ExecCommandRunner{}
	}
	return &CLIClient{
		controlHost:    options.ControlHost,
		sshBinary:      sshBinary,
		identityFile:   options.IdentityFile,
		knownHostsFile: options.KnownHostsFile,
		commandTimeout: commandTimeout,
		runner:         runner,
	}, nil
}

// List returns the documented fields from ssh <control-host> ls -l --json.
func (c *CLIClient) List(ctx context.Context) ([]VM, error) {
	stdout, err := c.run(ctx, "list", "ls", "-l", "--json")
	if err != nil {
		return nil, err
	}

	var response struct {
		VMs json.RawMessage `json:"vms"`
	}
	if err := json.Unmarshal(stdout, &response); err != nil {
		return nil, fmt.Errorf("decode exe.dev list response: %w", err)
	}
	if len(response.VMs) == 0 || bytes.Equal(bytes.TrimSpace(response.VMs), []byte("null")) {
		return nil, errors.New("decode exe.dev list response: required non-null vms array is missing")
	}

	var vms []VM
	if err := json.Unmarshal(response.VMs, &vms); err != nil {
		return nil, fmt.Errorf("decode exe.dev list response vms array: %w", err)
	}
	if vms == nil {
		return nil, errors.New("decode exe.dev list response: required vms array is null")
	}
	return vms, nil
}

// Create creates one VM. The response body is intentionally not parsed because
// exe.dev does not document the JSON response schema for new.
func (c *CLIClient) Create(ctx context.Context, request CreateRequest) error {
	if err := ValidateCreateRequest(request); err != nil {
		return err
	}

	tags := append([]string(nil), request.Tags...)
	sort.Strings(tags)

	args := []string{
		"new",
		"--json",
		"--no-email",
		"--name=" + request.Name,
		"--cpu=" + request.CPU,
		"--memory=" + request.Memory,
		"--disk=" + request.Disk,
	}
	if request.Image != "" {
		args = append(args, "--image="+request.Image)
	}
	if request.Pool != "" {
		args = append(args, "--pool="+request.Pool)
	}
	for _, tag := range tags {
		args = append(args, "--tag="+tag)
	}

	_, err := c.run(ctx, "create", args...)
	return err
}

// Delete removes the named VMs. The response body is intentionally not parsed
// because exe.dev does not document the JSON response schema for rm.
func (c *CLIClient) Delete(ctx context.Context, names []string) error {
	if len(names) == 0 {
		return errors.New("delete requires at least one VM name")
	}
	for i, name := range names {
		if err := ValidateVMName(name); err != nil {
			return fmt.Errorf("name[%d]: %w", i, err)
		}
	}

	args := make([]string, 0, len(names)+2)
	args = append(args, "rm", "--json")
	args = append(args, names...)
	_, err := c.run(ctx, "delete", args...)
	return err
}

func (c *CLIClient) run(ctx context.Context, operation string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	commandCtx, cancel := context.WithTimeout(ctx, c.commandTimeout)
	defer cancel()

	sshArgs := c.sshArgs(args...)
	stdout, stderr, err := c.runner.Run(commandCtx, c.sshBinary, sshArgs...)
	if err == nil {
		if contextErr := commandCtx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return stdout, nil
	}

	cause := err
	if contextErr := commandCtx.Err(); contextErr != nil && !errors.Is(err, contextErr) {
		cause = errors.Join(err, contextErr)
	}

	exitCode := -1
	var exitErr interface{ ExitCode() int }
	if errors.As(err, &exitErr) {
		if code := exitErr.ExitCode(); code >= 0 {
			exitCode = code
		}
	}

	diagnostic := stderr
	if len(diagnostic) == 0 {
		diagnostic = stdout
	}
	boundedOutput, truncated := boundOutput(diagnostic)
	return nil, &CommandError{
		Operation: operation,
		ExitCode:  exitCode,
		Output:    boundedOutput,
		Truncated: truncated,
		Err:       cause,
	}
}

func (c *CLIClient) sshArgs(commandArgs ...string) []string {
	sshArgs := make([]string, 0, len(commandArgs)+18)
	if c.identityFile != "" && c.knownHostsFile != "" {
		sshArgs = append(sshArgs, "-F", "/dev/null")
	}
	sshArgs = append(sshArgs,
		"-oBatchMode=yes",
		"-oNumberOfPasswordPrompts=0",
	)
	if c.identityFile != "" {
		sshArgs = append(sshArgs, "-oIdentitiesOnly=yes", "-i", c.identityFile)
	}
	sshArgs = append(sshArgs,
		"-oStrictHostKeyChecking=yes",
		"-oLogLevel=ERROR",
		"-oConnectTimeout=10",
		"-oServerAliveInterval=10",
		"-oServerAliveCountMax=3",
	)
	if c.knownHostsFile != "" {
		sshArgs = append(sshArgs, "-oUserKnownHostsFile="+c.knownHostsFile)
	}
	sshArgs = append(sshArgs, c.controlHost)
	return append(sshArgs, commandArgs...)
}

func validateListedVMName(name string) error {
	if name == "" {
		return errors.New("VM entry has an empty vm_name")
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return errors.New("VM entry vm_name contains whitespace or control characters")
		}
	}
	return nil
}

// ValidateVMName checks the conservative exe.dev VM-name subset used by this
// client: a lowercase DNS label of at most 63 characters.
func ValidateVMName(name string) error {
	if name == "" {
		return errors.New("VM name must not be empty")
	}
	if len(name) > 63 || !vmNamePattern.MatchString(name) {
		return errors.New("VM name must be a lowercase DNS label of at most 63 characters")
	}
	return nil
}

// ValidateCreateRequest checks that a request can be safely sent through the
// SSH command transport without shell interpretation.
func ValidateCreateRequest(request CreateRequest) error {
	if err := ValidateVMName(request.Name); err != nil {
		return fmt.Errorf("name: %w", err)
	}

	validations := []struct {
		field   string
		value   string
		pattern *regexp.Regexp
	}{
		{field: "cpu", value: request.CPU, pattern: cpuPattern},
		{field: "memory", value: request.Memory, pattern: sizePattern},
		{field: "disk", value: request.Disk, pattern: sizePattern},
	}
	if request.Image != "" {
		validations = append(validations, struct {
			field   string
			value   string
			pattern *regexp.Regexp
		}{field: "image", value: request.Image, pattern: tokenPattern})
	}
	if request.Pool != "" {
		validations = append(validations, struct {
			field   string
			value   string
			pattern *regexp.Regexp
		}{field: "pool", value: request.Pool, pattern: resourcePattern})
	}
	for _, validation := range validations {
		if err := validateValue(validation.field, validation.value, validation.pattern); err != nil {
			return err
		}
	}
	if len(request.Tags) == 0 {
		return errors.New("tags must contain at least one tag")
	}
	for i, tag := range request.Tags {
		if err := validateValue(fmt.Sprintf("tags[%d]", i), tag, tagPattern); err != nil {
			return err
		}
	}
	return nil
}

func validateValue(field, value string, pattern *regexp.Regexp) error {
	if value == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	if strings.HasPrefix(value, "-") {
		return fmt.Errorf("%s must not start with '-'", field)
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return fmt.Errorf("%s must not contain whitespace or control characters", field)
		}
	}
	if !pattern.MatchString(value) {
		return fmt.Errorf("%s contains unsafe characters", field)
	}
	return nil
}

func validateExecutable(field, value string) error {
	if filepath.IsAbs(value) {
		return validateAbsolutePath(field, value)
	}
	return validateValue(field, value, binaryNamePattern)
}

func validateAbsolutePath(field, value string) error {
	if !filepath.IsAbs(value) {
		return fmt.Errorf("%s must be an absolute Unix path", field)
	}
	if value != filepath.Clean(value) {
		return fmt.Errorf("%s must be a clean absolute Unix path", field)
	}
	if !absolutePathPattern.MatchString(value) {
		return fmt.Errorf("%s contains unsafe path characters", field)
	}
	for _, r := range value {
		if r == 0 || unicode.IsControl(r) || unicode.IsSpace(r) {
			return fmt.Errorf("%s must not contain whitespace or control characters", field)
		}
	}
	return nil
}

func boundOutput(output []byte) (string, bool) {
	if len(output) <= maxCommandErrorOutput {
		return string(append([]byte(nil), output...)), false
	}
	return string(append([]byte(nil), output[:maxCommandErrorOutput]...)), true
}

func diagnosticExcerpt(output string) string {
	output = strings.ToValidUTF8(output, "�")
	fields := strings.FieldsFunc(output, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	})
	if len(fields) == 0 {
		return ""
	}
	clean := strings.Join(fields, " ")
	if utf8.RuneCountInString(clean) <= maxDiagnosticRunes {
		return clean
	}
	runes := []rune(clean)
	return string(runes[:maxDiagnosticRunes]) + "…"
}
