package healthcheck

import "context"

type Checker interface {
	Run(ctx context.Context)
}
