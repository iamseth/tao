package plandelta

import (
	"time"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/workspace"
)

type Collector struct {
	Runner commandrunner.Runner
	Config workspace.Config
	Now    func() time.Time
}

func NewCollector(runner commandrunner.Runner) Collector {
	return Collector{Runner: runner, Config: workspace.DefaultConfig(), Now: time.Now}
}
