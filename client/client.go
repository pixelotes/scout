// Package client builds the scout.Backend an app should use from its config:
// local, service, or both chained in the configured order.
package client

import (
	"log/slog"

	"github.com/pixelotes/scout"
	"github.com/pixelotes/scout/chain"
	"github.com/pixelotes/scout/config"
	"github.com/pixelotes/scout/local"
	"github.com/pixelotes/scout/remote"
)

// New returns the backend described by c. With one backend it is returned
// as is; with two they are wrapped in a chain, the service with a breaker.
func New(c config.Config, log *slog.Logger) (scout.Backend, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	var members []chain.Member
	for _, name := range c.Backends {
		switch name {
		case "local":
			members = append(members, chain.Member{Name: "local", Backend: local.New(c.Local)})
		case "service":
			members = append(members, chain.Member{
				Name:        "service",
				Backend:     remote.New(c.Service.URL, c.Service.Token, c.Service.Timeout),
				MaxFailures: c.Service.MaxFailures,
				Cooldown:    c.Service.Cooldown,
			})
		}
	}
	if len(members) == 1 {
		return members[0].Backend, nil
	}
	return chain.New(log, members...), nil
}
