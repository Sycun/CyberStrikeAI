package handler

import (
	"strings"

	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/store"

	"go.uber.org/zap"
)

// Every path that ends a run abnormally overwrites the assistant placeholder it already
// inserted - task already running, wait timeout, client failure, batch failure, finalizer
// refusal. That was one UPDATE statement repeated fifteen times across six files, each with
// its own copy of the SQL and its own argument order, which is how a missed `updated_at` or
// a swapped pair becomes invisible in review.

// newSessionStore mirrors newHITLStore: the HTTP layer holds a domain store rather than
// writing SQL inline, and a nil connection yields a nil store the callers tolerate.
func newSessionStore(db *database.DB) *store.Session {
	if db == nil {
		return nil
	}
	return store.NewSession(db.DB)
}

// sessionStore is the messages domain store. It is built in NewAgentHandler, because
// store.Session sits on the raw *sql.DB and the handler's own storage field is deliberately a
// narrow interface that cannot hand one over - leaking the connection through an accessor would
// undo the narrowing. A handler assembled as a struct literal sets the same field the same way.
func (h *AgentHandler) sessionStore() *store.Session {
	return h.sessions
}

// setMessageContent rewrites a message body. An empty id is a no-op rather than an update
// with a fresh timestamp, which is what the guarded call sites used to spell out inline.
func (h *AgentHandler) setMessageContent(messageID, content string) error {
	if strings.TrimSpace(messageID) == "" {
		return nil
	}
	sessions := h.sessionStore()
	if sessions == nil {
		return nil
	}
	_, err := sessions.SetMessageContent(messageID, content)
	if err != nil && h.logger != nil {
		h.logger.Warn("更新助手消息内容失败", zap.String("messageId", messageID), zap.Error(err))
	}
	return err
}
