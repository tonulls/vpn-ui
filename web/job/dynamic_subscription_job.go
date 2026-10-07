package job

import (
	"context"

	"github.com/mhsanaei/3x-ui/v2/web/service"
)

// DynamicSubscriptionJob polls configured external VLESS lists, then checks only the
// selected node until it fails over. All scheduling state is persisted per slot.
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
