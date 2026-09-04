package exe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/rodrigorm/fleeting-plugin-exe/internal/exedev"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"
)

const managedTag = "managed-by-gitlab-exe-runner"

var _ provider.InstanceGroup = (*InstanceGroup)(nil)

// InstanceGroup is configured from runners.autoscaler.plugin_config.
type InstanceGroup struct {
	FleetID               string   `json:"fleet_id"`
	NamePrefix            string   `json:"name_prefix"`
	MaxSize               int      `json:"max_size"`
	ControlHost           string   `json:"control_host"`
	ControlIdentityFile   string   `json:"control_identity_file"`
	ControlKnownHostsFile string   `json:"control_known_hosts_file"`
	Image                 string   `json:"image"`
	CPU                   int      `json:"cpu"`
	Memory                string   `json:"memory"`
	Disk                  string   `json:"disk"`
	Pool                  string   `json:"pool"`
	ExtraTags             []string `json:"extra_tags"`

	log      hclog.Logger
	settings provider.Settings
	client   exedev.Client

	lifecycleMu sync.Mutex
	stateMu     sync.RWMutex
	instances   map[string]exedev.VM
	inflightMu  sync.Mutex
	inflight    map[string]time.Time
	reclaimMu   sync.Mutex
	reclaiming  map[string]struct{}
}

func (g *InstanceGroup) Init(_ context.Context, log hclog.Logger, settings provider.Settings) (provider.ProviderInfo, error) {
	g.applyDefaults()
	if err := g.validate(); err != nil {
		return provider.ProviderInfo{}, err
	}
	if settings.Protocol != "" && settings.Protocol != provider.ProtocolSSH {
		return provider.ProviderInfo{}, fmt.Errorf("connector protocol %q is unsupported: exe.dev workers require ssh", settings.Protocol)
	}

	client, err := exedev.NewCLIClientWithOptions(exedev.CLIClientOptions{
		ControlHost:    g.ControlHost,
		IdentityFile:   g.ControlIdentityFile,
		KnownHostsFile: g.ControlKnownHostsFile,
	})
	if err != nil {
		return provider.ProviderInfo{}, fmt.Errorf("initialize exe.dev client: %w", err)
	}
	if err := exedev.ValidateCreateRequest(exedev.CreateRequest{
		Name:   g.NamePrefix + "validation",
		CPU:    strconv.Itoa(g.CPU),
		Memory: g.Memory,
		Disk:   g.Disk,
		Image:  g.Image,
		Pool:   g.Pool,
		Tags:   g.workerTags(),
	}); err != nil {
		return provider.ProviderInfo{}, fmt.Errorf("invalid worker configuration: %w", err)
	}
	if g.client == nil {
		g.client = client
	}

	g.log = log.With("fleet_id", g.FleetID)
	g.settings = settings
	g.instances = make(map[string]exedev.VM)
	g.inflight = make(map[string]time.Time)
	g.reclaiming = make(map[string]struct{})

	return provider.ProviderInfo{
		ID:        fmt.Sprintf("exe.dev/%s", g.FleetID),
		MaxSize:   g.MaxSize,
		Version:   Version.String(),
		BuildInfo: Version.BuildInfo(),
	}, nil
}

func (g *InstanceGroup) Update(ctx context.Context, update func(string, provider.State)) error {
	vms, err := g.client.List(ctx)
	if err != nil {
		return fmt.Errorf("list exe.dev VMs: %w", err)
	}

	if err := g.ensureOwnershipObservable(vms); err != nil {
		return err
	}
	current := g.filterOwned(vms)
	g.observeInflight(current)
	if err := g.reclaimUnknownStatuses(ctx, current); err != nil {
		return err
	}

	g.stateMu.Lock()
	previous := g.instances
	g.instances = current
	g.stateMu.Unlock()

	for name, vm := range current {
		update(name, g.stateForStatus(vm.Status))
	}
	for name := range previous {
		if _, exists := current[name]; !exists {
			update(name, provider.StateDeleted)
		}
	}

	return nil
}

func (g *InstanceGroup) Increase(ctx context.Context, n int) (int, error) {
	if n < 1 {
		return 0, nil
	}

	g.lifecycleMu.Lock()
	defer g.lifecycleMu.Unlock()

	vms, err := g.client.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("list exe.dev VMs before increase: %w", err)
	}
	if err := g.ensureOwnershipObservable(vms); err != nil {
		return 0, err
	}
	owned := g.filterOwned(vms)
	inflight := g.activeInflight(time.Now())
	available := g.MaxSize - len(owned) - inflight
	if available <= 0 {
		return 0, nil
	}
	if n > available {
		n = available
	}

	succeeded := 0
	for range n {
		name, err := instanceName(g.NamePrefix)
		if err != nil {
			return succeeded, fmt.Errorf("generate VM name: %w", err)
		}
		request := exedev.CreateRequest{
			Name:   name,
			CPU:    strconv.Itoa(g.CPU),
			Memory: g.Memory,
			Disk:   g.Disk,
			Image:  g.Image,
			Pool:   g.Pool,
			Tags:   g.workerTags(),
		}
		g.trackInflight(name, time.Now())
		if err := g.client.Create(ctx, request); err != nil {
			createErr := fmt.Errorf("create exe.dev VM %q (outcome may be ambiguous): %w", name, err)
			if succeeded > 0 {
				g.log.Error("partially increased exe.dev fleet", "succeeded", succeeded, "requested", n, "error", createErr)
				return succeeded, nil
			}
			return 0, createErr
		}
		g.log.Info("requested exe.dev worker creation", "instance", name)
		succeeded++
	}

	return succeeded, nil
}

func (g *InstanceGroup) Decrease(ctx context.Context, instances []string) ([]string, error) {
	if len(instances) == 0 {
		return nil, nil
	}

	g.lifecycleMu.Lock()
	defer g.lifecycleMu.Unlock()

	vms, err := g.client.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list exe.dev VMs before decrease: %w", err)
	}
	if err := g.ensureOwnershipObservable(vms); err != nil {
		return nil, err
	}
	all := make(map[string]exedev.VM, len(vms))
	for _, vm := range vms {
		all[vm.VMName] = vm
	}

	seen := make(map[string]struct{}, len(instances))
	succeeded := make([]string, 0, len(instances))
	var failures []error
	for _, name := range instances {
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}

		vm, exists := all[name]
		if !exists {
			// Deletion is idempotent: an absent VM is already deleted.
			succeeded = append(succeeded, name)
			continue
		}
		if !g.owns(vm) {
			failures = append(failures, fmt.Errorf("refusing to delete VM %q without fleet ownership tags", name))
			continue
		}
		if err := g.client.Delete(ctx, []string{name}); err != nil {
			failures = append(failures, fmt.Errorf("delete exe.dev VM %q: %w", name, err))
			continue
		}
		g.log.Info("requested exe.dev worker deletion", "instance", name)
		succeeded = append(succeeded, name)
	}

	joined := errors.Join(failures...)
	if joined != nil && len(succeeded) > 0 {
		g.log.Error("partially decreased exe.dev fleet", "succeeded", succeeded, "error", joined)
		return succeeded, nil
	}
	return succeeded, joined
}

func (g *InstanceGroup) ConnectInfo(ctx context.Context, instance string) (provider.ConnectInfo, error) {
	vm, err := g.findOwned(ctx, instance)
	if err != nil {
		return provider.ConnectInfo{}, err
	}
	if g.stateForStatus(vm.Status) != provider.StateRunning {
		return provider.ConnectInfo{}, fmt.Errorf("%w: VM %q has status %q", provider.ErrInstanceUnhealthy, instance, vm.Status)
	}
	host, user := sshDestination(vm)
	if host == "" {
		return provider.ConnectInfo{}, fmt.Errorf("%w: VM %q has no SSH destination", provider.ErrInstanceUnhealthy, instance)
	}

	connector := g.settings.ConnectorConfig
	connector.Protocol = provider.ProtocolSSH
	if connector.ProtocolPort == 0 {
		connector.ProtocolPort = provider.DefaultProtocolPorts[provider.ProtocolSSH]
	}
	if user != "" {
		connector.Username = user
	}
	if connector.Username == "" {
		return provider.ConnectInfo{}, errors.New("connector username is required when exe.dev does not return an SSH username")
	}

	return provider.ConnectInfo{
		ConnectorConfig: connector,
		ID:              vm.VMName,
		ExternalAddr:    host,
		InternalAddr:    host,
	}, nil
}

func (g *InstanceGroup) Heartbeat(ctx context.Context, instance string) error {
	vm, err := g.findOwned(ctx, instance)
	if err != nil {
		return err
	}
	if g.stateForStatus(vm.Status) != provider.StateRunning {
		return fmt.Errorf("%w: VM %q has status %q", provider.ErrInstanceUnhealthy, instance, vm.Status)
	}
	return nil
}

func (g *InstanceGroup) Suspend(context.Context, []string) ([]string, error) {
	return nil, provider.ErrSuspendResumeNotSupported
}

func (g *InstanceGroup) Resume(context.Context, []string) ([]string, error) {
	return nil, provider.ErrSuspendResumeNotSupported
}

func (g *InstanceGroup) Shutdown(context.Context) error {
	return nil
}

func (g *InstanceGroup) applyDefaults() {
	g.FleetID = strings.TrimSpace(g.FleetID)
	g.NamePrefix = strings.TrimSpace(g.NamePrefix)
	g.ControlHost = strings.TrimSpace(g.ControlHost)
	g.ControlIdentityFile = strings.TrimSpace(g.ControlIdentityFile)
	g.ControlKnownHostsFile = strings.TrimSpace(g.ControlKnownHostsFile)
	g.Image = strings.TrimSpace(g.Image)
	g.Memory = strings.TrimSpace(g.Memory)
	g.Disk = strings.TrimSpace(g.Disk)
	g.Pool = strings.TrimSpace(g.Pool)
	if g.ControlHost == "" {
		g.ControlHost = "exe.dev"
	}
}

func (g *InstanceGroup) validate() error {
	if !isSafeName(g.FleetID) {
		return errors.New("fleet_id must contain only lowercase letters, numbers, and hyphens")
	}
	if !isSafePrefix(g.NamePrefix) {
		return errors.New("name_prefix must contain only lowercase letters, numbers, and hyphens")
	}
	if g.MaxSize < 1 {
		return errors.New("max_size must be greater than zero")
	}
	if g.CPU < 1 {
		return errors.New("cpu must be greater than zero")
	}
	if g.Memory == "" {
		return errors.New("memory is required")
	}
	if g.Disk == "" {
		return errors.New("disk is required")
	}
	if g.Image == "" {
		return errors.New("image is required")
	}
	if (g.ControlIdentityFile == "") != (g.ControlKnownHostsFile == "") {
		return errors.New("control_identity_file and control_known_hosts_file must be configured together")
	}
	probe, err := instanceName(g.NamePrefix)
	if err != nil {
		return fmt.Errorf("validate generated VM name: %w", err)
	}
	if err := exedev.ValidateVMName(probe); err != nil {
		return fmt.Errorf("generated VM name: %w", err)
	}
	seenTags := map[string]struct{}{managedTag: {}, g.fleetTag(): {}}
	for _, tag := range g.ExtraTags {
		if tag == managedTag || strings.HasPrefix(tag, "fleet-") {
			return fmt.Errorf("extra tag %q conflicts with reserved ownership tags", tag)
		}
		if _, duplicate := seenTags[tag]; duplicate {
			return fmt.Errorf("extra tag %q is duplicated", tag)
		}
		seenTags[tag] = struct{}{}
	}
	return nil
}

func (g *InstanceGroup) ensureOwnershipObservable(vms []exedev.VM) error {
	for _, vm := range vms {
		if !strings.HasPrefix(vm.VMName, g.NamePrefix) {
			continue
		}
		if !vm.TagsPresent {
			return fmt.Errorf("cannot safely manage VM %q: exe.dev list response omitted tags", vm.VMName)
		}
		if !g.owns(vm) {
			return fmt.Errorf("cannot safely manage VM %q: name matches fleet prefix but ownership tags do not", vm.VMName)
		}
	}
	return nil
}

func (g *InstanceGroup) trackInflight(name string, createdAt time.Time) {
	g.inflightMu.Lock()
	defer g.inflightMu.Unlock()
	g.inflight[name] = createdAt
}

func (g *InstanceGroup) observeInflight(current map[string]exedev.VM) {
	g.inflightMu.Lock()
	defer g.inflightMu.Unlock()
	for name := range current {
		delete(g.inflight, name)
	}
}

func (g *InstanceGroup) activeInflight(now time.Time) int {
	const inflightTTL = 10 * time.Minute
	g.inflightMu.Lock()
	defer g.inflightMu.Unlock()
	for name, createdAt := range g.inflight {
		if now.Sub(createdAt) >= inflightTTL {
			g.log.Warn("expiring unobserved worker creation", "instance", name, "age", now.Sub(createdAt))
			delete(g.inflight, name)
		}
	}
	return len(g.inflight)
}

func (g *InstanceGroup) reclaimUnknownStatuses(ctx context.Context, current map[string]exedev.VM) error {
	for name, vm := range current {
		if _, known := classifyStatus(vm.Status); known {
			g.clearReclaiming(name)
			continue
		}
		if !g.isReclaiming(name) {
			g.log.Warn("unknown exe.dev VM status; requesting deletion", "instance", name, "status", vm.Status)
			if err := g.client.Delete(ctx, []string{name}); err != nil {
				return fmt.Errorf("delete VM %q with unknown status %q: %w", name, vm.Status, err)
			}
			g.markReclaiming(name)
		}
		vm.Status = "deleting"
		current[name] = vm
	}

	g.reclaimMu.Lock()
	for name := range g.reclaiming {
		if _, exists := current[name]; !exists {
			delete(g.reclaiming, name)
		}
	}
	g.reclaimMu.Unlock()
	return nil
}

func (g *InstanceGroup) isReclaiming(name string) bool {
	g.reclaimMu.Lock()
	defer g.reclaimMu.Unlock()
	_, ok := g.reclaiming[name]
	return ok
}

func (g *InstanceGroup) markReclaiming(name string) {
	g.reclaimMu.Lock()
	defer g.reclaimMu.Unlock()
	g.reclaiming[name] = struct{}{}
}

func (g *InstanceGroup) clearReclaiming(name string) {
	g.reclaimMu.Lock()
	defer g.reclaimMu.Unlock()
	delete(g.reclaiming, name)
}

func (g *InstanceGroup) filterOwned(vms []exedev.VM) map[string]exedev.VM {
	owned := make(map[string]exedev.VM)
	for _, vm := range vms {
		if g.owns(vm) {
			owned[vm.VMName] = vm
		}
	}
	return owned
}

func (g *InstanceGroup) owns(vm exedev.VM) bool {
	return hasTag(vm.Tags, managedTag) && hasTag(vm.Tags, g.fleetTag())
}

func (g *InstanceGroup) fleetTag() string {
	return "fleet-" + g.FleetID
}

func (g *InstanceGroup) workerTags() []string {
	tags := []string{managedTag, g.fleetTag(), "role-ci-worker", "lifecycle-ephemeral"}
	tags = append(tags, g.ExtraTags...)
	return tags
}

func (g *InstanceGroup) findOwned(ctx context.Context, name string) (exedev.VM, error) {
	vms, err := g.client.List(ctx)
	if err != nil {
		return exedev.VM{}, fmt.Errorf("list exe.dev VMs: %w", err)
	}
	if err := g.ensureOwnershipObservable(vms); err != nil {
		return exedev.VM{}, err
	}
	for _, vm := range vms {
		if vm.VMName != name {
			continue
		}
		if !g.owns(vm) {
			return exedev.VM{}, fmt.Errorf("%w: VM %q is not owned by fleet %q", provider.ErrInstanceUnhealthy, name, g.FleetID)
		}
		return vm, nil
	}
	return exedev.VM{}, fmt.Errorf("%w: VM %q not found", provider.ErrInstanceUnhealthy, name)
}

func (g *InstanceGroup) stateForStatus(status string) provider.State {
	state, known := classifyStatus(status)
	if !known {
		return provider.StateDeleting
	}
	return state
}

func classifyStatus(status string) (provider.State, bool) {
	normalized := strings.ToLower(strings.TrimSpace(status))
	switch normalized {
	case "running":
		return provider.StateRunning, true
	case "creating", "pending", "starting", "booting":
		return provider.StateCreating, true
	case "deleting", "terminating":
		return provider.StateDeleting, true
	case "deleted", "terminated":
		return provider.StateDeleted, true
	default:
		return provider.StateDeleting, false
	}
}

func sshDestination(vm exedev.VM) (host, user string) {
	destination := strings.TrimSpace(vm.SSHDest)
	if destination != "" {
		if at := strings.LastIndex(destination, "@"); at >= 0 {
			return destination[at+1:], destination[:at]
		}
		return destination, strings.TrimSpace(vm.SSHUser)
	}
	return strings.TrimSpace(vm.SSHHost), strings.TrimSpace(vm.SSHUser)
}

func hasTag(tags []string, wanted string) bool {
	for _, tag := range tags {
		if tag == wanted {
			return true
		}
	}
	return false
}

func isSafeName(value string) bool {
	if value == "" || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

func isSafePrefix(value string) bool {
	if value == "" || value[0] == '-' {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

func instanceName(prefix string) (string, error) {
	var random [5]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return prefix + strconv.FormatInt(time.Now().UTC().UnixMilli(), 36) + "-" + hex.EncodeToString(random[:]), nil
}
