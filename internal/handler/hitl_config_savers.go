package handler

// HITL 的三条 config.yaml 写入通道：会话增量白名单、审计 agent 策略、全局默认配置。
// 原本是 AgentHandler 上三对字段加三个 setter（装配代码要记得调三次，少调一次就是
// 「界面能改、重启就丢」那类问题），现在收成一个协作者和一个注入点。

// HitlToolWhitelistSaver 写会话/全局 HITL 工具白名单。
type HitlToolWhitelistSaver interface {
	SetHitlToolWhitelist(tools []string) error
	MergeHitlToolWhitelistIntoConfig(tools []string) error
}

// HitlAuditStrategySaver 写审计 agent 的两段提示词。
type HitlAuditStrategySaver interface {
	UpdateHitlAuditAgentStrategy(approvalPrompt, reviewEditPrompt string) error
}

// HitlDefaultReviewerSaver 写全局默认人机协同配置。
type HitlDefaultReviewerSaver interface {
	UpdateHitlDefaultConfig(mode, reviewer string, timeoutSeconds int) error
	UpdateHitlDefaultReviewer(reviewer string) error
}

// HitlConfigSaver is what a deployment wires once. ConfigHandler implements all three;
// the interfaces stay split so each write path can still be faked on its own.
type HitlConfigSaver interface {
	HitlToolWhitelistSaver
	HitlAuditStrategySaver
	HitlDefaultReviewerSaver
}

type hitlConfigSavers struct {
	whitelist       HitlToolWhitelistSaver
	strategy        HitlAuditStrategySaver
	defaultReviewer HitlDefaultReviewerSaver
}

// SetHitlConfigSaver installs the three write channels. A nil saver is not an error:
// every endpoint that needs one answers with its own "not configured" status, which is
// how the handler behaves before the config layer is attached.
func (h *AgentHandler) SetHitlConfigSaver(s HitlConfigSaver) {
	if s == nil {
		return
	}
	h.hitlSavers = hitlConfigSavers{whitelist: s, strategy: s, defaultReviewer: s}
	if h.hitlPolicy != nil {
		// Forwarded, not copied at construction: the policy answers "persistence unavailable"
		// from the same value the run path uses, so the two cannot disagree about whether a
		// write channel exists.
		h.hitlPolicy.setSavers(h.hitlSavers)
	}
}
