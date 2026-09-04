package exedev

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type runnerFunc func(context.Context, string, ...string) ([]byte, []byte, error)

func (f runnerFunc) Run(ctx context.Context, program string, args ...string) ([]byte, []byte, error) {
	return f(ctx, program, args...)
}

type recordedCall struct {
	program string
	args    []string
}

type fakeRunner struct {
	calls  []recordedCall
	stdout []byte
	stderr []byte
	err    error
}

func (r *fakeRunner) Run(_ context.Context, program string, args ...string) ([]byte, []byte, error) {
	r.calls = append(r.calls, recordedCall{program: program, args: append([]string(nil), args...)})
	return append([]byte(nil), r.stdout...), append([]byte(nil), r.stderr...), r.err
}

type codedError struct {
	code int
}

func (e codedError) Error() string { return "command failed" }
func (e codedError) ExitCode() int { return e.code }

func newTestClient(t *testing.T, runner CommandRunner) *CLIClient {
	t.Helper()
	client, err := NewCLIClient("control.exe.dev", runner)
	if err != nil {
		t.Fatalf("NewCLIClient() error = %v", err)
	}
	return client
}

func TestListUsesStdoutOnlyAndParsesUnknownFields(t *testing.T) {
	runner := &fakeRunner{
		stdout: []byte(`{
			"request_id":"future-field",
			"vms":[{
				"vm_name":"runner-a",
				"status":"running",
				"ssh_dest":"vm+runner-a@vm.exe.xyz",
				"ssh_host":"vm.exe.xyz",
				"ssh_user":"vm+runner-a",
				"https_url":"https://runner-a.exe.xyz",
				"region":"lon",
				"region_display":"London, UK",
				"tags":["fleet:queen-a","owned"],
				"future_item_field":{"nested":true}
			}]
		}`),
		stderr: []byte("Welcome to exe.dev\nmaintenance banner\n"),
	}
	client := newTestClient(t, runner)

	vms, err := client.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	wantVMs := []VM{{
		VMName:        "runner-a",
		Status:        "running",
		SSHDest:       "vm+runner-a@vm.exe.xyz",
		SSHHost:       "vm.exe.xyz",
		SSHUser:       "vm+runner-a",
		HTTPSURL:      "https://runner-a.exe.xyz",
		Region:        "lon",
		RegionDisplay: "London, UK",
		Tags:          []string{"fleet:queen-a", "owned"},
		TagsPresent:   true,
	}}
	if !reflect.DeepEqual(vms, wantVMs) {
		t.Fatalf("List() = %#v, want %#v", vms, wantVMs)
	}

	assertCalls(t, runner.calls, []recordedCall{{
		program: "ssh",
		args: append(defaultSSHArgs(),
			"control.exe.dev", "ls", "-l", "--json"),
	}})
}

func TestListRequiresNonNullVMsArray(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
	}{
		{name: "empty object", stdout: `{}`},
		{name: "top-level null", stdout: `null`},
		{name: "null vms", stdout: `{"vms":null}`},
		{name: "error envelope", stdout: `{"error":"not authorized"}`},
		{name: "object vms", stdout: `{"vms":{}}`},
		{name: "malformed JSON", stdout: `{"vms":[`},
		{name: "wrong top level", stdout: `[]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newTestClient(t, &fakeRunner{stdout: []byte(tt.stdout)})
			vms, err := client.List(context.Background())
			if err == nil {
				t.Fatalf("List() = %#v, nil; want envelope error", vms)
			}
			if vms != nil {
				t.Fatalf("List() result = %#v, want nil", vms)
			}
			if !strings.Contains(err.Error(), "decode exe.dev list response") {
				t.Fatalf("List() error = %q, want decode context", err)
			}
		})
	}
}

func TestListAcceptsEmptyVMsArray(t *testing.T) {
	client := newTestClient(t, &fakeRunner{stdout: []byte(`{"vms":[]}`)})
	vms, err := client.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if vms == nil || len(vms) != 0 {
		t.Fatalf("List() = %#v, want non-nil empty slice", vms)
	}
}

func TestListDecodeErrorReturnsNoPartialResult(t *testing.T) {
	client := newTestClient(t, &fakeRunner{stdout: []byte(`{"vms":[{"vm_name":"valid"},{"vm_name":42}]}`)})
	vms, err := client.List(context.Background())
	if err == nil {
		t.Fatal("List() error = nil, want decode error")
	}
	if vms != nil {
		t.Fatalf("List() result = %#v, want nil", vms)
	}
}

func TestListRejectsMalformedVMEntries(t *testing.T) {
	for _, body := range []string{
		`{"vms":[null]}`,
		`{"vms":[{}]}`,
		`{"vms":[{"vm_name":"bad name"}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			runner := &fakeRunner{stdout: []byte(body)}
			client := newTestClient(t, runner)
			if _, err := client.List(context.Background()); err == nil {
				t.Fatal("List() error = nil, want malformed VM entry error")
			}
		})
	}
}

func TestVMTagsPresence(t *testing.T) {
	client := newTestClient(t, &fakeRunner{stdout: []byte(`{"vms":[
		{"vm_name":"absent"},
		{"vm_name":"empty","tags":[]},
		{"vm_name":"null","tags":null}
	]}`)})
	vms, err := client.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if vms[0].TagsPresent || vms[0].Tags != nil {
		t.Fatalf("absent tags decoded as Tags=%#v TagsPresent=%v", vms[0].Tags, vms[0].TagsPresent)
	}
	if !vms[1].TagsPresent || vms[1].Tags == nil || len(vms[1].Tags) != 0 {
		t.Fatalf("empty tags decoded as Tags=%#v TagsPresent=%v", vms[1].Tags, vms[1].TagsPresent)
	}
	if !vms[2].TagsPresent || vms[2].Tags != nil {
		t.Fatalf("null tags decoded as Tags=%#v TagsPresent=%v", vms[2].Tags, vms[2].TagsPresent)
	}
}

func TestCreateExactCommandAndOptionalPool(t *testing.T) {
	t.Run("pool supplied", func(t *testing.T) {
		runner := &fakeRunner{stdout: []byte("response schema is intentionally ignored")}
		client := newTestClient(t, runner)
		request := validCreateRequest()
		request.Tags = []string{"owned", "fleet-queen-a"}
		originalTags := append([]string(nil), request.Tags...)

		if err := client.Create(context.Background(), request); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if !reflect.DeepEqual(request.Tags, originalTags) {
			t.Fatalf("Create() mutated tags to %v, want %v", request.Tags, originalTags)
		}
		wantArgs := append(defaultSSHArgs(),
			"control.exe.dev", "new", "--json", "--no-email",
			"--name=runner-a", "--cpu=2", "--memory=4GB", "--disk=20GB",
			"--image=ubuntu:22.04", "--pool=ci", "--tag=fleet-queen-a", "--tag=owned")
		assertCalls(t, runner.calls, []recordedCall{{program: "ssh", args: wantArgs}})
	})

	t.Run("pool and image omitted", func(t *testing.T) {
		runner := &fakeRunner{}
		client := newTestClient(t, runner)
		request := validCreateRequest()
		request.Pool = ""
		request.Image = ""
		if err := client.Create(context.Background(), request); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		for _, arg := range runner.calls[0].args {
			if strings.HasPrefix(arg, "--pool=") || strings.HasPrefix(arg, "--image=") {
				t.Fatalf("Create() args contain optional field: %v", runner.calls[0].args)
			}
		}
	})
}

func TestDeleteExactCommand(t *testing.T) {
	runner := &fakeRunner{stdout: []byte("not JSON")}
	client := newTestClient(t, runner)
	if err := client.Delete(context.Background(), []string{"runner-b", "runner-a"}); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	wantArgs := append(defaultSSHArgs(), "control.exe.dev", "rm", "--json", "runner-b", "runner-a")
	assertCalls(t, runner.calls, []recordedCall{{program: "ssh", args: wantArgs}})
}

func TestCLIClientOptionsArgvAndDefaults(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		runner := &fakeRunner{stdout: []byte(`{"vms":[]}`)}
		client, err := NewCLIClient("control.exe.dev", runner)
		if err != nil {
			t.Fatalf("NewCLIClient() error = %v", err)
		}
		if client.sshBinary != "ssh" || client.commandTimeout != 45*time.Second {
			t.Fatalf("defaults = binary %q timeout %v", client.sshBinary, client.commandTimeout)
		}
		if _, err := client.List(context.Background()); err != nil {
			t.Fatalf("List() error = %v", err)
		}
		wantArgs := append(defaultSSHArgs(), "control.exe.dev", "ls", "-l", "--json")
		assertCalls(t, runner.calls, []recordedCall{{program: "ssh", args: wantArgs}})
	})

	t.Run("all options", func(t *testing.T) {
		runner := &fakeRunner{stdout: []byte(`{"vms":[]}`)}
		client, err := NewCLIClientWithOptions(CLIClientOptions{
			ControlHost:    "api@control.exe.dev",
			SSHBinary:      "/usr/local/bin/ssh",
			IdentityFile:   "/home/runner/.ssh/id_ed25519",
			KnownHostsFile: "/home/runner/.ssh/known_hosts",
			CommandTimeout: 12 * time.Second,
			Runner:         runner,
		})
		if err != nil {
			t.Fatalf("NewCLIClientWithOptions() error = %v", err)
		}
		if _, err := client.List(context.Background()); err != nil {
			t.Fatalf("List() error = %v", err)
		}
		wantArgs := []string{
			"-F", "/dev/null",
			"-oBatchMode=yes",
			"-oNumberOfPasswordPrompts=0",
			"-oIdentitiesOnly=yes", "-i", "/home/runner/.ssh/id_ed25519",
			"-oStrictHostKeyChecking=yes",
			"-oLogLevel=ERROR",
			"-oConnectTimeout=10",
			"-oServerAliveInterval=10",
			"-oServerAliveCountMax=3",
			"-oUserKnownHostsFile=/home/runner/.ssh/known_hosts",
			"api@control.exe.dev", "ls", "-l", "--json",
		}
		assertCalls(t, runner.calls, []recordedCall{{program: "/usr/local/bin/ssh", args: wantArgs}})
	})
}

func TestOptionsValidateDynamicValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CLIClientOptions)
	}{
		{name: "bad control host", mutate: func(o *CLIClientOptions) { o.ControlHost = "-oProxyCommand=bad" }},
		{name: "binary option", mutate: func(o *CLIClientOptions) { o.SSHBinary = "-V" }},
		{name: "binary relative path", mutate: func(o *CLIClientOptions) { o.SSHBinary = "bin/ssh" }},
		{name: "identity relative", mutate: func(o *CLIClientOptions) { o.IdentityFile = ".ssh/id" }},
		{name: "identity traversal", mutate: func(o *CLIClientOptions) { o.IdentityFile = "/tmp/../secret" }},
		{name: "identity whitespace", mutate: func(o *CLIClientOptions) { o.IdentityFile = "/tmp/id bad" }},
		{name: "identity metacharacter", mutate: func(o *CLIClientOptions) { o.IdentityFile = "/tmp/id;bad" }},
		{name: "known hosts relative", mutate: func(o *CLIClientOptions) { o.KnownHostsFile = "known_hosts" }},
		{name: "known hosts newline", mutate: func(o *CLIClientOptions) { o.KnownHostsFile = "/tmp/known\nhosts" }},
		{name: "negative timeout", mutate: func(o *CLIClientOptions) { o.CommandTimeout = -time.Second }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := CLIClientOptions{ControlHost: "control.exe.dev", Runner: &fakeRunner{}}
			tt.mutate(&options)
			if _, err := NewCLIClientWithOptions(options); err == nil {
				t.Fatal("NewCLIClientWithOptions() error = nil, want validation error")
			}
		})
	}

	if _, err := NewCLIClientWithOptions(CLIClientOptions{
		ControlHost:    "control.exe.dev",
		SSHBinary:      "/opt/openssh/bin/ssh",
		IdentityFile:   "/home/runner/.ssh/id_ed25519",
		KnownHostsFile: "/home/runner/.ssh/known_hosts",
	}); err != nil {
		t.Fatalf("absolute Unix paths rejected: %v", err)
	}
}

func TestCommandErrorPrefersStderrAndFallsBackToStdout(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		stderr string
		want   string
	}{
		{name: "stderr preferred", stdout: "JSON partial response", stderr: "remote diagnostic", want: "remote diagnostic"},
		{name: "stdout fallback", stdout: "stdout diagnostic", want: "stdout diagnostic"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{stdout: []byte(tt.stdout), stderr: []byte(tt.stderr), err: codedError{code: 17}}
			_, err := newTestClient(t, runner).List(context.Background())
			var commandErr *CommandError
			if !errors.As(err, &commandErr) {
				t.Fatalf("error = %v (%T), want *CommandError", err, err)
			}
			if commandErr.Operation != "list" || commandErr.ExitCode != 17 {
				t.Fatalf("CommandError = %#v", commandErr)
			}
			if commandErr.Output != tt.want || commandErr.Truncated {
				t.Fatalf("Output = %q, Truncated = %v; want %q, false", commandErr.Output, commandErr.Truncated, tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Error() = %q, want diagnostic", err)
			}
			if !errors.Is(err, runner.err) {
				t.Fatal("CommandError does not unwrap to runner error")
			}
		})
	}
}

func TestCommandErrorBoundsAndSanitizesDiagnostic(t *testing.T) {
	stderr := "  first\nline\x00\t" + strings.Repeat("界", maxCommandErrorOutput)
	runnerErr := errors.New("could not\nstart\tssh")
	client := newTestClient(t, &fakeRunner{stderr: []byte(stderr), err: runnerErr})
	_, err := client.List(context.Background())
	var commandErr *CommandError
	if !errors.As(err, &commandErr) {
		t.Fatalf("List() error = %v (%T), want *CommandError", err, err)
	}
	if commandErr.ExitCode != -1 || len(commandErr.Output) != maxCommandErrorOutput || !commandErr.Truncated {
		t.Fatalf("CommandError = %#v", commandErr)
	}
	message := err.Error()
	if strings.ContainsAny(message, "\n\r\t\x00") {
		t.Fatalf("Error() contains unsanitized controls: %q", message)
	}
	if !strings.Contains(message, "could not start ssh") || !strings.Contains(message, "first line") {
		t.Fatalf("Error() = %q, want sanitized cause and diagnostic excerpt", message)
	}
	if utf8.RuneCountInString(message) > maxDiagnosticRunes+100 {
		t.Fatalf("Error() diagnostic was not kept short: %d runes", utf8.RuneCountInString(message))
	}
}

func TestCommandTimeoutAndCancellation(t *testing.T) {
	t.Run("per-operation timeout", func(t *testing.T) {
		started := make(chan time.Time, 1)
		runner := runnerFunc(func(ctx context.Context, _ string, _ ...string) ([]byte, []byte, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				return nil, nil, errors.New("runner context has no deadline")
			}
			started <- deadline
			<-ctx.Done()
			return nil, []byte("timed out remotely"), errors.New("ssh stopped")
		})
		client, err := NewCLIClientWithOptions(CLIClientOptions{
			ControlHost:    "control.exe.dev",
			CommandTimeout: 30 * time.Millisecond,
			Runner:         runner,
		})
		if err != nil {
			t.Fatalf("NewCLIClientWithOptions() error = %v", err)
		}
		before := time.Now()
		_, err = client.List(context.Background())
		deadline := <-started
		if delta := deadline.Sub(before); delta < 10*time.Millisecond || delta > 200*time.Millisecond {
			t.Fatalf("runner deadline delta = %v, want configured timeout", delta)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("List() error = %v, want context.DeadlineExceeded", err)
		}
		var commandErr *CommandError
		if !errors.As(err, &commandErr) {
			t.Fatalf("List() error type = %T, want *CommandError", err)
		}
	})

	t.Run("caller cancellation preserved", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		started := make(chan struct{})
		runner := runnerFunc(func(ctx context.Context, _ string, _ ...string) ([]byte, []byte, error) {
			close(started)
			<-ctx.Done()
			return nil, []byte("canceled"), errors.New("ssh killed")
		})
		client := newTestClient(t, runner)
		result := make(chan error, 1)
		go func() {
			_, err := client.List(ctx)
			result <- err
		}()
		<-started
		cancel()
		err := <-result
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("List() error = %v, want context.Canceled", err)
		}
	})

	t.Run("already canceled does not run", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		runner := &fakeRunner{}
		_, err := newTestClient(t, runner).List(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("List() error = %v, want context.Canceled", err)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("runner calls = %#v, want none", runner.calls)
		}
	})
}

func TestExecCommandRunnerSeparatesStreamsAndSetsWaitDelay(t *testing.T) {
	runner := ExecCommandRunner{}
	stdout, stderr, err := runner.Run(context.Background(), os.Args[0], "-test.run=^TestExecHelperProcess$", "--", "streams")
	if err != nil {
		t.Fatalf("Run() error = %v, stderr = %q", err, stderr)
	}
	if string(stdout) != "stdout" || string(stderr) != "stderr" {
		t.Fatalf("Run() stdout=%q stderr=%q", stdout, stderr)
	}
	if execWaitDelay != 5*time.Second {
		t.Fatalf("exec wait delay = %v, want 5s", execWaitDelay)
	}
}

func TestExecCommandRunnerWaitDelayBehavior(t *testing.T) {
	if testing.Short() {
		t.Skip("WaitDelay behavior takes five seconds")
	}
	runner := ExecCommandRunner{}
	start := time.Now()
	_, _, err := runner.Run(context.Background(), os.Args[0], "-test.run=^TestExecHelperProcess$", "--", "hold-pipes")
	elapsed := time.Since(start)
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("Run() error = %v, want exec.ErrWaitDelay", err)
	}
	if elapsed < 4*time.Second || elapsed > 8*time.Second {
		t.Fatalf("Run() elapsed = %v, want approximately 5s", elapsed)
	}
}

func TestExecHelperProcess(t *testing.T) {
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	switch os.Args[separator+1] {
	case "streams":
		_, _ = os.Stdout.WriteString("stdout")
		_, _ = os.Stderr.WriteString("stderr")
		os.Exit(0)
	case "hold-pipes":
		child := exec.Command(os.Args[0], "-test.run=^TestExecHelperProcess$", "--", "sleep")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			_, _ = fmt.Fprint(os.Stderr, err)
			os.Exit(2)
		}
		_ = child.Process.Release()
		os.Exit(0)
	case "sleep":
		time.Sleep(7 * time.Second)
		os.Exit(0)
	}
}

func TestVMNameAndRequestInjectionValidation(t *testing.T) {
	validNames := []string{"a", "runner-a", strings.Repeat("a", 63), "0-runner-9"}
	for _, name := range validNames {
		if err := ValidateVMName(name); err != nil {
			t.Errorf("ValidateVMName(%q) = %v", name, err)
		}
	}
	invalidNames := []string{
		"", "Runner", "runner_a", "runner.a", "-runner", "runner-",
		strings.Repeat("a", 64), "runner bad", "runner\nbad", "runner;id", "$(id)", "--help",
	}
	for _, name := range invalidNames {
		if err := ValidateVMName(name); err == nil {
			t.Errorf("ValidateVMName(%q) = nil, want error", name)
		}
	}

	createTests := []struct {
		name   string
		mutate func(*CreateRequest)
	}{
		{name: "unsafe name", mutate: func(r *CreateRequest) { r.Name = "runner;id" }},
		{name: "cpu option", mutate: func(r *CreateRequest) { r.CPU = "2 --image=bad" }},
		{name: "memory punctuation", mutate: func(r *CreateRequest) { r.Memory = "4GB;id" }},
		{name: "disk whitespace", mutate: func(r *CreateRequest) { r.Disk = "50 GB" }},
		{name: "image expansion", mutate: func(r *CreateRequest) { r.Image = "$(id)" }},
		{name: "pool option", mutate: func(r *CreateRequest) { r.Pool = "--bad" }},
		{name: "pool slash", mutate: func(r *CreateRequest) { r.Pool = "team/pool" }},
		{name: "no tags", mutate: func(r *CreateRequest) { r.Tags = nil }},
		{name: "empty tag", mutate: func(r *CreateRequest) { r.Tags = []string{""} }},
		{name: "tag comma", mutate: func(r *CreateRequest) { r.Tags = []string{"a,b"} }},
		{name: "tag backtick", mutate: func(r *CreateRequest) { r.Tags = []string{"`id`"} }},
	}
	for _, tt := range createTests {
		t.Run("create "+tt.name, func(t *testing.T) {
			runner := &fakeRunner{}
			request := validCreateRequest()
			tt.mutate(&request)
			if err := newTestClient(t, runner).Create(context.Background(), request); err == nil {
				t.Fatal("Create() error = nil, want validation error")
			}
			if len(runner.calls) != 0 {
				t.Fatalf("runner calls = %#v, want none", runner.calls)
			}
		})
	}

	deleteTests := [][]string{nil, {""}, {"--help"}, {"runner a"}, {"runner;id"}, {"runner-a", "$(id)"}}
	for _, names := range deleteTests {
		runner := &fakeRunner{}
		if err := newTestClient(t, runner).Delete(context.Background(), names); err == nil {
			t.Errorf("Delete(%q) error = nil, want validation error", names)
		}
		if len(runner.calls) != 0 {
			t.Errorf("Delete(%q) ran command: %#v", names, runner.calls)
		}
	}
}

func validCreateRequest() CreateRequest {
	return CreateRequest{
		Name:   "runner-a",
		CPU:    "2",
		Memory: "4GB",
		Disk:   "20GB",
		Image:  "ubuntu:22.04",
		Pool:   "ci",
		Tags:   []string{"owned"},
	}
}

func defaultSSHArgs() []string {
	return []string{
		"-oBatchMode=yes",
		"-oNumberOfPasswordPrompts=0",
		"-oStrictHostKeyChecking=yes",
		"-oLogLevel=ERROR",
		"-oConnectTimeout=10",
		"-oServerAliveInterval=10",
		"-oServerAliveCountMax=3",
	}
}

func assertCalls(t *testing.T, got, want []recordedCall) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("runner calls = %#v, want %#v", got, want)
	}
}
