package taskflow

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type Engine struct {
	Store   *Store
	Runtime Runtime
	mu      sync.Mutex
	active  map[string]context.CancelFunc
	closed  bool
	wg      sync.WaitGroup
}

func NewEngine(s *Store, r Runtime) *Engine {
	return &Engine{Store: s, Runtime: r, active: map[string]context.CancelFunc{}}
}

func (e *Engine) Start(id, executor string, revision int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return fmt.Errorf("task service is shutting down")
	}
	if _, ok := e.active[id]; ok {
		return fmt.Errorf("task is still running or stopping")
	}
	if len(e.active) >= 8 {
		return fmt.Errorf("all task slots are busy")
	}
	var seconds int64
	err := e.Store.Change(id, "", "started", "开始/继续任务", func(t *Task) error {
		if t.Revision != revision || Active(t.Status) || t.Status == "completed" || t.Status == "cancelled" {
			return ErrConflict
		}
		for _, step := range t.Steps {
			if step.Status == "cleanup_unconfirmed" {
				return fmt.Errorf("请先确认未停止的执行已结束，再明确重试该步骤")
			}
		}
		if t.Question != "" {
			return fmt.Errorf("answer the pending question before continuing")
		}
		if t.Rounds >= t.MaxRounds || t.RemainingSeconds <= 0 {
			return fmt.Errorf("extend the task budget before continuing")
		}
		t.Status = "running"
		t.ExecutorID = executor
		now := time.Now().UTC()
		t.RunStartedAt = &now
		seconds = t.RemainingSeconds
		return nil
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(seconds)*time.Second)
	e.active[id] = cancel
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer cancel()
		defer func() { e.mu.Lock(); delete(e.active, id); e.mu.Unlock() }()
		e.run(ctx, id)
	}()
	return nil
}

func (e *Engine) Stop(id string, revision int64, cancelTask bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	cancel, running := e.active[id]
	err := e.Store.Change(id, "", "stop_requested", "停止派发新步骤，等待执行资源收尾。", func(t *Task) error {
		if t.Revision != revision || t.Status == "completed" || t.Status == "cancelled" {
			return ErrConflict
		}
		if cancelTask {
			t.Status = "cancelled"
			if running {
				t.Status = "cancelling"
			}
		} else {
			t.Status = "paused"
			if running {
				t.Status = "pausing"
			}
		}
		return nil
	})
	if err == nil && cancel != nil {
		cancel()
	}
	return err
}

func (e *Engine) Close() {
	e.mu.Lock()
	e.closed = true
	for id, cancel := range e.active {
		_ = e.Store.Change(id, "", "shutdown", "服务关闭，保存进度并停止执行。", func(t *Task) error {
			if t.Status == "running" {
				t.Status = "pausing"
			}
			return nil
		})
		cancel()
	}
	e.mu.Unlock()
	e.wg.Wait()
}

func (e *Engine) Delete(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.active[id]; ok {
		return ErrConflict
	}
	return e.Store.Delete(id)
}

type result struct {
	kind, id string
	proposal Proposal
	outcome  Outcome
	verdict  Verdict
	err      error
	guidance int
	revision int64
}

func (e *Engine) run(parent context.Context, id string) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	results := make(chan result, 8)
	var jobs sync.WaitGroup
	defer func() {
		cancel()
		jobs.Wait()
		_ = e.Store.Change(id, "", "settled", "本轮调度结束，进度已保存。", func(t *Task) error {
			accountTime(t)
			for i := range t.Steps {
				if t.Steps[i].Status == "running" {
					t.Steps[i].Status = "interrupted"
				}
			}
			switch t.Status {
			case "cancelling":
				t.Status = "cancelled"
			case "pausing":
				t.Status = "paused"
			case "running":
				if parent.Err() == context.DeadlineExceeded {
					t.Status = "budget_exhausted"
					t.Summary = "时间预算已耗尽，已保留未完成工作。"
				} else {
					t.Status = "paused"
				}
			}
			return nil
		})
	}()
	workers := map[string]bool{}
	planning, reviewing, needPlan := false, false, true
	noProgress := 0
	stop := func(status, message string) {
		_ = e.Store.Change(id, "", status, message, func(t *Task) error {
			if t.Status == "running" {
				t.Status = status
				t.Summary = Clip(message, 12000)
			}
			return nil
		})
	}
	launch := func(fn func() result) {
		jobs.Add(1)
		go func() {
			defer jobs.Done()
			r := fn()
			select {
			case results <- r:
			case <-ctx.Done():
			}
		}()
	}
	for {
		t, err := e.Store.Get(id)
		if err != nil {
			return
		}
		if ctx.Err() != nil || t.Status != "running" {
			return
		}
		// A worker completion makes dependent work runnable immediately, even
		// while another worker or the planner is still waiting for a model.
		for _, step := range t.Steps {
			if len(workers) >= t.Concurrency {
				break
			}
			if !t.Ready(step) {
				continue
			}
			err = e.Store.Change(id, step.ID, "step_started", step.Title, func(current *Task) error {
				s := current.Step(step.ID)
				if current.Status != "running" || ctx.Err() != nil || s == nil || !current.Ready(*s) {
					return ErrConflict
				}
				s.Status = "running"
				s.Attempts++
				return nil
			})
			if err != nil {
				stop("blocked", err.Error())
				return
			}
			workers[step.ID] = true
			launch(func() result {
				out, runErr := e.Runtime.Run(ctx, t, step, func(conversation, summary string) error {
					return e.Store.Change(id, step.ID, "checkpoint", summary, func(current *Task) error {
						s := current.Step(step.ID)
						if s == nil || s.Status != "running" {
							return ErrConflict
						}
						if conversation != "" {
							if s.ConversationID != conversation {
								s.Conversations = append(s.Conversations, conversation)
							}
							s.ConversationID = conversation
						}
						if summary != "" {
							s.Summary = Clip(summary, 12000)
						}
						return nil
					})
				})
				// Persist observations even if a pause wins the scheduling race.
				resultMessage := out.Summary
				if runErr != nil {
					resultMessage = runErr.Error()
				}
				persistErr := e.Store.Change(id, step.ID, "step_result", resultMessage, func(current *Task) error {
					s := current.Step(step.ID)
					if s == nil || s.Status != "running" {
						return ErrConflict
					}
					if out.ConversationID != "" {
						s.ConversationID = out.ConversationID
					}
					if out.Summary != "" {
						s.Summary = Clip(out.Summary, 16000)
					}
					if out.Status == "cleanup_unconfirmed" {
						s.Status = "cleanup_unconfirmed"
						if runErr != nil {
							s.Summary = runErr.Error()
						}
					} else if ctx.Err() != nil {
						s.Status = "interrupted"
					} else if runErr != nil {
						s.Status = "failed"
						s.Summary = Clip(runErr.Error(), 16000)
					} else if (out.Status == "completed" || out.Status == "blocked") && out.Summary != "" {
						s.Status = out.Status
					} else {
						s.Status = "failed"
						s.Summary = "执行未提交有效结果。"
					}
					return nil
				})
				return result{kind: "worker", id: step.ID, err: persistErr}
			})
		}
		if needPlan && !planning && !reviewing {
			if t.Rounds >= t.MaxRounds {
				if len(workers) == 0 {
					allSettled := len(t.Steps) > 0
					for _, step := range t.Steps {
						if step.Status != "completed" && step.Status != "retired" {
							allSettled = false
						}
					}
					if !allSettled {
						stop("budget_exhausted", "规划轮数已耗尽，未完成的步骤与结果已保存。")
						return
					}
					// Keep a final acceptance check after the last planning round.
					needPlan = false
				}
			} else {
				err = e.Store.Change(id, "", "planning", "根据最新结果更新计划。", func(current *Task) error {
					if current.Status != "running" {
						return ErrConflict
					}
					current.Rounds++
					return nil
				})
				if err != nil {
					return
				}
				snapshot, err := e.Store.Get(id)
				if err != nil {
					return
				}
				planning = true
				needPlan = false
				launch(func() result {
					p, err := e.Runtime.Plan(ctx, snapshot)
					return result{kind: "plan", proposal: p, err: err, guidance: len(snapshot.Guidance), revision: snapshot.Revision}
				})
			}
		}
		if len(workers) == 0 && !planning && !reviewing && !needPlan {
			if t.Question != "" {
				stop("waiting_input", t.Question)
				return
			}
			ready := false
			for _, s := range t.Steps {
				ready = ready || t.Ready(s)
			}
			if ready {
				continue
			}
			if len(t.Steps) == 0 {
				stop("blocked", "规划没有产生可执行步骤，请补充目标或方向。")
				return
			}
			for _, s := range t.Steps {
				if s.Status != "completed" && s.Status != "retired" {
					stop("blocked", "存在失败、中断或依赖未满足的步骤。请检查结果、补充方向或明确重试。")
					return
				}
			}
			reviewing = true
			launch(func() result {
				v, err := e.Runtime.Review(ctx, t)
				return result{kind: "review", verdict: v, err: err, guidance: len(t.Guidance)}
			})
		}
		select {
		case <-ctx.Done():
			return
		case r := <-results:
			switch r.kind {
			case "worker":
				delete(workers, r.id)
				needPlan = true
				if r.err != nil {
					stop("blocked", "保存执行结果失败: "+r.err.Error())
					return
				}
			case "plan":
				planning = false
				if r.err != nil {
					stop("blocked", "规划失败: "+r.err.Error())
					return
				}
				err = e.Store.Change(id, "", "plan_updated", r.proposal.Summary, func(current *Task) error {
					if current.Status != "running" || len(current.Guidance) != r.guidance {
						return ErrConflict
					}
					err := ApplyProposal(current, r.proposal)
					if err != nil && current.Revision != r.revision {
						return ErrConflict
					}
					return err
				})
				if err == ErrConflict {
					needPlan = true
					continue
				}
				if err != nil {
					stop("blocked", "计划无效: "+err.Error())
					return
				}
				// Keep a replan requested by a completion during this model call.
				if len(r.proposal.Steps) > 0 {
					noProgress = 0
				}
			case "review":
				reviewing = false
				if r.err != nil {
					stop("blocked", "验收失败: "+r.err.Error())
					return
				}
				current, err := e.Store.Get(id)
				if err != nil {
					return
				}
				if len(current.Guidance) != r.guidance {
					needPlan = true
					continue
				}
				if AcceptVerdict(current, r.verdict) {
					err = e.Store.Change(id, "", "completed", r.verdict.Summary, func(latest *Task) error {
						if latest.Status != "running" || latest.Revision != current.Revision {
							return ErrConflict
						}
						latest.Status = "completed"
						latest.Summary = Clip(r.verdict.Summary, 16000)
						return nil
					})
					if err == ErrConflict {
						needPlan = true
						continue
					}
					return
				}
				if current.Rounds >= current.MaxRounds {
					stop("budget_exhausted", "规划轮数已耗尽；验收缺口: "+r.verdict.Missing)
					return
				}
				noProgress++
				if noProgress >= 2 {
					stop("blocked", "验收仍有缺口且没有新增进展: "+r.verdict.Missing)
					return
				}
				if err = e.Store.Change(id, "", "review_gap", r.verdict.Missing, func(latest *Task) error {
					latest.Summary = "验收缺口: " + Clip(r.verdict.Missing, 12000)
					return nil
				}); err != nil {
					return
				}
				needPlan = true
			}
		}
	}
}
