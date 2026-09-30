package hitl

import (
	"errors"
	"testing"
	"time"

	appconfig "cyberstrike-ai/internal/config"

	"go.uber.org/zap"
)

// purgeRecorder stands in for the HITL store: this package's job is deciding
// *whether* and *how far back* to purge, and that is what these tests pin. The
// SQL itself is covered against a real database in internal/store.
type purgeRecorder struct {
	cutoffs []time.Time
	err     error
}

func (p *purgeRecorder) PurgeDecidedBefore(cutoff time.Time) (int64, error) {
	p.cutoffs = append(p.cutoffs, cutoff)
	if p.err != nil {
		return 0, p.err
	}
	return 1, nil
}

func configWithRetention(days int) *appconfig.Config {
	return &appconfig.Config{Hitl: appconfig.HitlConfig{RetentionDays: &days}}
}

func TestServicePurgeExpired_respectsZeroRetention(t *testing.T) {
	rec := &purgeRecorder{}
	NewService(rec, configWithRetention(0), zap.NewNop()).PurgeExpired()
	if len(rec.cutoffs) != 0 {
		t.Fatalf("retention 0 must keep everything, store called %d times", len(rec.cutoffs))
	}
}

func TestServicePurgeExpired_passesTheConfiguredCutoff(t *testing.T) {
	rec := &purgeRecorder{}
	NewService(rec, configWithRetention(30), zap.NewNop()).PurgeExpired()
	if len(rec.cutoffs) != 1 {
		t.Fatalf("store calls = %d, want 1", len(rec.cutoffs))
	}
	want := time.Now().AddDate(0, 0, -30)
	if delta := rec.cutoffs[0].Sub(want); delta < -time.Minute || delta > time.Minute {
		t.Fatalf("cutoff = %v, want about %v", rec.cutoffs[0], want)
	}
}

// A purge runs on an hourly ticker with nobody watching: a store failure must be
// logged and swallowed, not panic the process.
func TestServicePurgeExpired_survivesAStoreFailure(t *testing.T) {
	rec := &purgeRecorder{err: errors.New("disk full")}
	NewService(rec, configWithRetention(7), zap.NewNop()).PurgeExpired()
	if len(rec.cutoffs) != 1 {
		t.Fatalf("store calls = %d, want the attempt to have been made", len(rec.cutoffs))
	}
}

func TestServicePurgeExpired_requiresAStore(t *testing.T) {
	NewService(nil, configWithRetention(7), zap.NewNop()).PurgeExpired()
	fallback := (appconfig.HitlConfig{}).RetentionDaysEffective()
	var nilService *Service
	nilService.PurgeExpired()
	if got := nilService.RetentionDays(); got != fallback {
		t.Fatalf("nil service retention = %d, want the config default %d", got, fallback)
	}
}
