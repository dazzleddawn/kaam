package drainer

import (
	"context"
	"time"

	"github.com/pkg/errors"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

type Drain struct {
	avgInterval time.Duration
}

func New(avgInterval time.Duration) *Drain {
	return &Drain{
		avgInterval: avgInterval,
	}
}

func (d *Drain) Start(ctx context.Context) error {
	logger := logf.FromContext(ctx)
	timer := time.NewTimer(d.avgInterval)
	go func() {
		for {
			select {
			case <-ctx.Done():
				logger.Info("shutting down drainer")
				return
			case <-timer.C:
				if err := d.exec(); err != nil {
					logger.Error(err, "exec: drain job")
				}
				timer.Reset(d.avgInterval)
			}
		}
	}()

	return nil
}

func (d *Drain) exec() error {
	return errors.New("not implemented")
}
