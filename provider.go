package exe

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/go-hclog"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"
)

var (
	ErrNotImplemented                        = errors.New("exe.dev provider lifecycle is not implemented")
	_                 provider.InstanceGroup = (*InstanceGroup)(nil)
)

// InstanceGroup is configured from runners.autoscaler.plugin_config.
type InstanceGroup struct {
	FleetID    string `json:"fleet_id"`
	NamePrefix string `json:"name_prefix"`
	MaxSize    int    `json:"max_size"`

	log      hclog.Logger
	settings provider.Settings
}

func (g *InstanceGroup) Init(_ context.Context, log hclog.Logger, settings provider.Settings) (provider.ProviderInfo, error) {
	g.FleetID = strings.TrimSpace(g.FleetID)
	g.NamePrefix = strings.TrimSpace(g.NamePrefix)

	if g.FleetID == "" {
		return provider.ProviderInfo{}, errors.New("fleet_id is required")
	}
	if g.NamePrefix == "" {
		return provider.ProviderInfo{}, errors.New("name_prefix is required")
	}
	if g.MaxSize < 1 {
		return provider.ProviderInfo{}, errors.New("max_size must be greater than zero")
	}

	g.log = log.With("fleet_id", g.FleetID)
	g.settings = settings

	return provider.ProviderInfo{
		ID:        fmt.Sprintf("exe.dev/%s", g.FleetID),
		MaxSize:   g.MaxSize,
		Version:   Version.String(),
		BuildInfo: Version.BuildInfo(),
	}, nil
}

func (g *InstanceGroup) Update(context.Context, func(string, provider.State)) error {
	return ErrNotImplemented
}

func (g *InstanceGroup) Increase(context.Context, int) (int, error) {
	return 0, ErrNotImplemented
}

func (g *InstanceGroup) Decrease(context.Context, []string) ([]string, error) {
	return nil, ErrNotImplemented
}

func (g *InstanceGroup) ConnectInfo(context.Context, string) (provider.ConnectInfo, error) {
	return provider.ConnectInfo{}, ErrNotImplemented
}

func (g *InstanceGroup) Heartbeat(context.Context, string) error {
	return ErrNotImplemented
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
