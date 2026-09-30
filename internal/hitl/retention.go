package hitl

import (
	"time"

	"cyberstrike-ai/internal/config"

	"go.uber.org/zap"
)

const retentionPurgeInterval = time.Hour

// DecidedLogStore is the persistence surface retention needs: drop the terminal
// interrupt rows older than a cutoff. The HITL domain store implements it, so this
// package no longer needs the whole data layer handed to it.
type DecidedLogStore interface {
	PurgeDecidedBefore(cutoff time.Time) (int64, error)
}

// Service manages HITL audit log retention (decided hitl_interrupts rows).
type Service struct {
	logs   DecidedLogStore
	cfg    *config.Config
	logger *zap.Logger
}

// NewService creates a HITL audit log retention service.
func NewService(logs DecidedLogStore, cfg *config.Config, logger *zap.Logger) *Service {
	return &Service{logs: logs, cfg: cfg, logger: logger}
}

// RetentionDays returns configured retention; 0 means keep forever.
func (s *Service) RetentionDays() int {
	if s == nil || s.cfg == nil {
		return config.HitlConfig{}.RetentionDaysEffective()
	}
	return s.cfg.Hitl.RetentionDaysEffective()
}

// PurgeExpired deletes decided HITL log rows older than retention_days when configured.
func (s *Service) PurgeExpired() {
	if s == nil || s.logs == nil || s.cfg == nil {
		return
	}
	days := s.cfg.Hitl.RetentionDaysEffective()
	if days <= 0 {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	n, err := s.logs.PurgeDecidedBefore(cutoff)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("清理过期人机协同审计日志失败", zap.Error(err))
		}
		return
	}
	if n > 0 && s.logger != nil {
		s.logger.Info("已清理过期人机协同审计日志", zap.Int64("deleted", n), zap.Int("retention_days", days))
	}
}

// StartRetentionLoop periodically purges expired HITL audit log rows.
func StartRetentionLoop(s *Service, logger *zap.Logger) {
	if s == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(retentionPurgeInterval)
		defer ticker.Stop()
		for range ticker.C {
			s.PurgeExpired()
			if logger != nil {
				logger.Debug("hitl audit log retention tick completed")
			}
		}
	}()
}
