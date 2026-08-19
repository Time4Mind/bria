package telegramapp

import (
	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/i18n"
)

func (h *Handler) voiceEnablePlans(actor application.Principal) ([]string, error) {
	nodes, err := h.service.ListNodes(actor)
	if err != nil {
		return nil, err
	}
	plans := make([]string, 0, len(nodes))
	for _, item := range nodes {
		if !item.Node.Enabled() {
			continue
		}
		plans = append(plans, speechPlan(item.Node.Name, item.Node.OS))
	}
	if len(plans) == 0 {
		plans = append(plans, h.copy(actor).Text(i18n.ToastUnavailable))
	}
	return plans, nil
}
