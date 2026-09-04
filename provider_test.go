package exe

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/rodrigorm/fleeting-plugin-exe/internal/exedev"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"
)

type fakeClient struct {
	listResponses [][]exedev.VM
	listErr       error
	createErrAt   int
	createCalls   []exedev.CreateRequest
	deleteCalls   [][]string
	deleteErr     map[string]error
}

func (f *fakeClient) List(context.Context) ([]exedev.VM, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if len(f.listResponses) == 0 {
		return nil, nil
	}
	response := f.listResponses[0]
	if len(f.listResponses) > 1 {
		f.listResponses = f.listResponses[1:]
	}
	return append([]exedev.VM(nil), response...), nil
}

func (f *fakeClient) Create(_ context.Context, request exedev.CreateRequest) error {
	f.createCalls = append(f.createCalls, request)
	if f.createErrAt > 0 && len(f.createCalls) == f.createErrAt {
		return errors.New("create failed")
	}
	return nil
}

func (f *fakeClient) Delete(_ context.Context, names []string) error {
	f.deleteCalls = append(f.deleteCalls, append([]string(nil), names...))
	if len(names) == 1 {
		return f.deleteErr[names[0]]
	}
	return nil
}

func validGroup(client exedev.Client) *InstanceGroup {
	return &InstanceGroup{
		FleetID:     "queen-a",
		NamePrefix:  "glr-queen-a-",
		MaxSize:     2,
		ControlHost: "exe.dev",
		Image:       "ubuntu:24.04",
		CPU:         1,
		Memory:      "3GB",
		Disk:        "25GB",
		client:      client,
	}
}

func initGroup(t *testing.T, group *InstanceGroup, settings provider.Settings) provider.ProviderInfo {
	t.Helper()
	info, err := group.Init(context.Background(), hclog.NewNullLogger(), settings)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	return info
}

func ownedVM(name, status string) exedev.VM {
	return exedev.VM{
		VMName:      name,
		Status:      status,
		SSHHost:     name + ".exe.xyz",
		Tags:        []string{managedTag, "fleet-queen-a"},
		TagsPresent: true,
	}
}

func TestInit(t *testing.T) {
	group := validGroup(&fakeClient{})
	info := initGroup(t, group, provider.Settings{})

	if info.ID != "exe.dev/queen-a" {
		t.Fatalf("Init() ID = %q, want %q", info.ID, "exe.dev/queen-a")
	}
	if info.MaxSize != 2 {
		t.Fatalf("Init() MaxSize = %d, want 2", info.MaxSize)
	}
	if len(info.Capabilities) != 0 {
		t.Fatalf("Init() Capabilities = %v, want none", info.Capabilities)
	}
	if group.ControlHost != "exe.dev" {
		t.Fatalf("ControlHost = %q, want default exe.dev", group.ControlHost)
	}
}

func TestInitValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*InstanceGroup)
	}{
		{name: "missing fleet", mutate: func(g *InstanceGroup) { g.FleetID = "" }},
		{name: "unsafe fleet", mutate: func(g *InstanceGroup) { g.FleetID = "queen/a" }},
		{name: "missing prefix", mutate: func(g *InstanceGroup) { g.NamePrefix = "" }},
		{name: "invalid max size", mutate: func(g *InstanceGroup) { g.MaxSize = 0 }},
		{name: "invalid cpu", mutate: func(g *InstanceGroup) { g.CPU = 0 }},
		{name: "missing memory", mutate: func(g *InstanceGroup) { g.Memory = "" }},
		{name: "missing disk", mutate: func(g *InstanceGroup) { g.Disk = "" }},
		{name: "missing image", mutate: func(g *InstanceGroup) { g.Image = "" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			group := validGroup(&fakeClient{})
			tt.mutate(group)
			if _, err := group.Init(context.Background(), hclog.NewNullLogger(), provider.Settings{}); err == nil {
				t.Fatal("Init() error = nil, want validation error")
			}
		})
	}
}

func TestInitRejectsNonSSHConnector(t *testing.T) {
	group := validGroup(&fakeClient{})
	_, err := group.Init(context.Background(), hclog.NewNullLogger(), provider.Settings{
		ConnectorConfig: provider.ConnectorConfig{Protocol: provider.ProtocolWinRM},
	})
	if err == nil {
		t.Fatal("Init() error = nil, want unsupported protocol error")
	}
}

func TestUpdateReconcilesOwnedVMsAndDeletion(t *testing.T) {
	client := &fakeClient{listResponses: [][]exedev.VM{
		{
			ownedVM("runner-a", "running"),
			ownedVM("runner-b", "creating"),
			{VMName: "unrelated", Status: "running", Tags: []string{"other"}},
		},
		{ownedVM("runner-a", "deleting")},
	}}
	group := validGroup(client)
	initGroup(t, group, provider.Settings{})

	var first []string
	if err := group.Update(context.Background(), func(id string, state provider.State) {
		first = append(first, id+":"+string(state))
	}); err != nil {
		t.Fatalf("first Update() error = %v", err)
	}
	sort.Strings(first)
	if want := []string{"runner-a:running", "runner-b:creating"}; !reflect.DeepEqual(first, want) {
		t.Fatalf("first events = %v, want %v", first, want)
	}

	var second []string
	if err := group.Update(context.Background(), func(id string, state provider.State) {
		second = append(second, id+":"+string(state))
	}); err != nil {
		t.Fatalf("second Update() error = %v", err)
	}
	sort.Strings(second)
	if want := []string{"runner-a:deleting", "runner-b:deleted"}; !reflect.DeepEqual(second, want) {
		t.Fatalf("second events = %v, want %v", second, want)
	}
}

func TestUpdateFailsClosedWhenFleetVMOmitsTags(t *testing.T) {
	client := &fakeClient{listResponses: [][]exedev.VM{{{
		VMName: "glr-queen-a-existing",
		Status: "running",
	}}}}
	group := validGroup(client)
	initGroup(t, group, provider.Settings{})

	err := group.Update(context.Background(), func(string, provider.State) {})
	if err == nil {
		t.Fatal("Update() error = nil, want missing-tags safety error")
	}
}

func TestUpdateUnknownStatusFailsTowardReclamation(t *testing.T) {
	client := &fakeClient{listResponses: [][]exedev.VM{{ownedVM("glr-queen-a-existing", "mystery")}}}
	group := validGroup(client)
	initGroup(t, group, provider.Settings{})

	var got provider.State
	if err := group.Update(context.Background(), func(_ string, state provider.State) { got = state }); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if got != provider.StateDeleting {
		t.Fatalf("unknown status state = %q, want %q", got, provider.StateDeleting)
	}
}
func TestIncreaseCreatesOnlyAvailableCapacity(t *testing.T) {
	client := &fakeClient{listResponses: [][]exedev.VM{{ownedVM("runner-existing", "running")}}}
	group := validGroup(client)
	group.Pool = "ci-pool"
	group.ExtraTags = []string{"team-ci"}
	initGroup(t, group, provider.Settings{})

	succeeded, err := group.Increase(context.Background(), 3)
	if err != nil {
		t.Fatalf("Increase() error = %v", err)
	}
	if succeeded != 1 || len(client.createCalls) != 1 {
		t.Fatalf("Increase() succeeded=%d creates=%d, want 1", succeeded, len(client.createCalls))
	}
	request := client.createCalls[0]
	if request.CPU != "1" || request.Memory != "3GB" || request.Disk != "25GB" || request.Pool != "ci-pool" {
		t.Fatalf("CreateRequest resources = %#v", request)
	}
	if len(request.Name) <= len(group.NamePrefix) || request.Name[:len(group.NamePrefix)] != group.NamePrefix {
		t.Fatalf("CreateRequest name = %q, want prefix %q", request.Name, group.NamePrefix)
	}
	for _, tag := range []string{managedTag, "fleet-queen-a", "role-ci-worker", "lifecycle-ephemeral", "team-ci"} {
		if !hasTag(request.Tags, tag) {
			t.Fatalf("CreateRequest tags = %v, missing %q", request.Tags, tag)
		}
	}
}

func TestIncreaseTracksUnobservedCreations(t *testing.T) {
	client := &fakeClient{listResponses: [][]exedev.VM{{}, {}}}
	group := validGroup(client)
	initGroup(t, group, provider.Settings{})

	first, err := group.Increase(context.Background(), 1)
	if err != nil || first != 1 {
		t.Fatalf("first Increase() = %d, %v", first, err)
	}
	second, err := group.Increase(context.Background(), 2)
	if err != nil || second != 1 {
		t.Fatalf("second Increase() = %d, %v; want one remaining slot", second, err)
	}
	third, err := group.Increase(context.Background(), 1)
	if err != nil || third != 0 {
		t.Fatalf("third Increase() = %d, %v; want fleet capped by inflight creations", third, err)
	}
	if len(client.createCalls) != 2 {
		t.Fatalf("create calls = %d, want 2", len(client.createCalls))
	}
}

func TestIncreaseReportsPartialSuccess(t *testing.T) {
	client := &fakeClient{listResponses: [][]exedev.VM{{}}, createErrAt: 2}
	group := validGroup(client)
	initGroup(t, group, provider.Settings{})

	succeeded, err := group.Increase(context.Background(), 2)
	if err != nil {
		t.Fatalf("Increase() error = %v, want partial success without gRPC-discarded error", err)
	}
	if succeeded != 1 || len(client.createCalls) != 2 {
		t.Fatalf("Increase() succeeded=%d creates=%d, want 1 and 2", succeeded, len(client.createCalls))
	}
}

func TestDecreaseIsIdempotentAndProtectsUnownedVMs(t *testing.T) {
	client := &fakeClient{listResponses: [][]exedev.VM{{
		ownedVM("owned", "running"),
		{VMName: "foreign", Status: "running", Tags: []string{"other"}},
	}}}
	group := validGroup(client)
	initGroup(t, group, provider.Settings{})

	succeeded, err := group.Decrease(context.Background(), []string{"owned", "missing", "foreign", "owned"})
	if err != nil {
		t.Fatalf("Decrease() error = %v, want partial success without gRPC-discarded error", err)
	}
	if want := []string{"owned", "missing"}; !reflect.DeepEqual(succeeded, want) {
		t.Fatalf("Decrease() succeeded = %v, want %v", succeeded, want)
	}
	if want := [][]string{{"owned"}}; !reflect.DeepEqual(client.deleteCalls, want) {
		t.Fatalf("Delete calls = %v, want %v", client.deleteCalls, want)
	}
}

func TestConnectInfoUsesExeRoutingAndStaticCredentials(t *testing.T) {
	vm := ownedVM("runner-a", "running")
	vm.SSHDest = "vm+runner-a@router.exe.xyz"
	vm.SSHHost = "ignored.exe.xyz"
	vm.SSHUser = "vm+runner-a"
	client := &fakeClient{listResponses: [][]exedev.VM{{vm}}}
	group := validGroup(client)
	settings := provider.Settings{ConnectorConfig: provider.ConnectorConfig{
		Username:             "fallback",
		Key:                  []byte("private-key"),
		UseStaticCredentials: true,
	}}
	initGroup(t, group, settings)

	info, err := group.ConnectInfo(context.Background(), "runner-a")
	if err != nil {
		t.Fatalf("ConnectInfo() error = %v", err)
	}
	if info.ID != "runner-a" || info.ExternalAddr != "router.exe.xyz" || info.InternalAddr != "router.exe.xyz" || info.Username != "vm+runner-a" {
		t.Fatalf("ConnectInfo() = %#v", info)
	}
	if info.Protocol != provider.ProtocolSSH || info.ProtocolPort != 22 {
		t.Fatalf("ConnectInfo() protocol = %q port=%d", info.Protocol, info.ProtocolPort)
	}
	if string(info.Key) != "private-key" || !info.UseStaticCredentials {
		t.Fatal("ConnectInfo() did not preserve connector credentials")
	}
}

func TestHeartbeatRejectsMissingOrNonRunningVM(t *testing.T) {
	client := &fakeClient{listResponses: [][]exedev.VM{{ownedVM("runner-a", "stopped")}, {}}}
	group := validGroup(client)
	initGroup(t, group, provider.Settings{})

	if err := group.Heartbeat(context.Background(), "runner-a"); !errors.Is(err, provider.ErrInstanceUnhealthy) {
		t.Fatalf("Heartbeat(stopped) error = %v, want ErrInstanceUnhealthy", err)
	}
	if err := group.Heartbeat(context.Background(), "missing"); !errors.Is(err, provider.ErrInstanceUnhealthy) {
		t.Fatalf("Heartbeat(missing) error = %v, want ErrInstanceUnhealthy", err)
	}
}

func TestSuspendResumeUnsupported(t *testing.T) {
	group := &InstanceGroup{}
	if _, err := group.Suspend(context.Background(), []string{"vm"}); err != provider.ErrSuspendResumeNotSupported {
		t.Fatalf("Suspend() error = %v", err)
	}
	if _, err := group.Resume(context.Background(), []string{"vm"}); err != provider.ErrSuspendResumeNotSupported {
		t.Fatalf("Resume() error = %v", err)
	}
}
