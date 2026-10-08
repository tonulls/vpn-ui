package job

import (
	"context"

	"github.com/mhsanaei/3x-ui/v2/web/service"
)

// DynamicSubscriptionJob performs bounded source/health work for external VLESS slots.
// Each RunDue pass fetches at most one source or probes one candidate set globally;
// pool state and cursors are persisted so later ticks can resume fairly.
type DynamicSubscriptionJob struct {
	ctx     context.Context
	service service.DynamicSubscriptionService
}

func NewDynamicSubscriptionJob() *DynamicSubscriptionJob {
	return &DynamicSubscriptionJob{ctx: context.Background()}
}

func (j *DynamicSubscriptionJob) SetContext(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	j.ctx = ctx
}

func (j *DynamicSubscriptionJob) Run() {
	j.service.RunDue(j.ctx)
}
