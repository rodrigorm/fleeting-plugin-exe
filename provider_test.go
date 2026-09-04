package exe

import (
	"context"
	"testing"

	"github.com/hashicorp/go-hclog"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"
)

func TestInit(t *testing.T) {
	group := &InstanceGroup{
		FleetID:    "queen-a",
		NamePrefix: "glr-queen-a-",
		MaxSize:    2,
	}

	info, err := group.Init(context.Background(), hclog.NewNullLogger(), provider.Settings{})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if info.ID != "exe.dev/queen-a" {
		t.Fatalf("Init() ID = %q, want %q", info.ID, "exe.dev/queen-a")
	}
	if info.MaxSize != 2 {
		t.Fatalf("Init() MaxSize = %d, want 2", info.MaxSize)
	}
	if len(info.Capabilities) != 0 {
		t.Fatalf("Init() Capabilities = %v, want none", info.Capabilities)
	}
}

func TestInitValidation(t *testing.T) {
	tests := []struct {
		name  string
		group InstanceGroup
	}{
		{name: "missing fleet", group: InstanceGroup{NamePrefix: "glr-", MaxSize: 1}},
		{name: "missing prefix", group: InstanceGroup{FleetID: "queen-a", MaxSize: 1}},
		{name: "invalid max size", group: InstanceGroup{FleetID: "queen-a", NamePrefix: "glr-"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.group.Init(context.Background(), hclog.NewNullLogger(), provider.Settings{})
			if err == nil {
				t.Fatal("Init() error = nil, want validation error")
			}
		})
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
