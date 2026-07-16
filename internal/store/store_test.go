package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func params(key string, now time.Time) ReserveParams {
	return ReserveParams{
		Key:         key,
		RequestID:   "req-" + key,
		Alias:       "self-gmail",
		SubjectHash: "subj-hash",
		BodyHash:    "body-hash",
		Now:         now,
		Limits:      RateLimits{GlobalPerHour: 100, GlobalPerDay: 100},
	}
}

func mustReserve(t *testing.T, s *Store, p ReserveParams) {
	t.Helper()
	res, err := s.Reserve(context.Background(), p)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if res.Kind != Reserved {
		t.Fatalf("Reserve kind = %v, want Reserved", res.Kind)
	}
}

func TestReserveAndReplay(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Now()

	mustReserve(t, s, params("k1", now))

	row, err := s.Get(ctx, "k1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.State != StateSending {
		t.Fatalf("state = %q, want sending", row.State)
	}

	if err := s.Complete(ctx, "k1", StateSent, "250", "<mid@x>", now); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	res, err := s.Reserve(ctx, params("k1", now))
	if err != nil {
		t.Fatalf("Reserve dup: %v", err)
	}
	if res.Kind != Replay {
		t.Fatalf("dup kind = %v, want Replay", res.Kind)
	}
	if res.Existing.State != StateSent || res.Existing.MessageID != "<mid@x>" || res.Existing.ResultCode != "250" {
		t.Fatalf("replay row = %+v", res.Existing)
	}
}

func TestReplayForEveryTerminalState(t *testing.T) {
	for _, state := range []string{StateSent, StateFailed, StateAmbiguous} {
		t.Run(state, func(t *testing.T) {
			s, _ := openTest(t)
			ctx := context.Background()
			now := time.Now()
			mustReserve(t, s, params("k", now))
			if err := s.Complete(ctx, "k", state, "x", "", now); err != nil {
				t.Fatalf("Complete: %v", err)
			}
			res, err := s.Reserve(ctx, params("k", now))
			if err != nil {
				t.Fatalf("Reserve: %v", err)
			}
			if res.Kind != Replay || res.Existing.State != state {
				t.Fatalf("kind=%v state=%q, want Replay/%s", res.Kind, res.Existing.State, state)
			}
		})
	}
}

func TestKeyReuseWithDifferentContent(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Now()
	mustReserve(t, s, params("k1", now))
	s.Complete(ctx, "k1", StateSent, "250", "", now)

	p := params("k1", now)
	p.BodyHash = "different"
	res, err := s.Reserve(ctx, p)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if res.Kind != KeyReuse {
		t.Fatalf("kind = %v, want KeyReuse", res.Kind)
	}
}

func TestInFlightConflict(t *testing.T) {
	s, _ := openTest(t)
	now := time.Now()
	mustReserve(t, s, params("k1", now))

	res, err := s.Reserve(context.Background(), params("k1", now))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if res.Kind != InFlight {
		t.Fatalf("kind = %v, want InFlight", res.Kind)
	}
}

func TestGlobalHourlyLimitBoundary(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Now()

	limits := RateLimits{GlobalPerHour: 2, GlobalPerDay: 10}
	for i, key := range []string{"a", "b"} {
		p := params(key, now)
		p.Limits = limits
		res, err := s.Reserve(ctx, p)
		if err != nil || res.Kind != Reserved {
			t.Fatalf("reserve %d: kind=%v err=%v", i, res.Kind, err)
		}
	}
	p := params("c", now)
	p.Limits = limits
	res, err := s.Reserve(ctx, p)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if res.Kind != RateLimited || res.Limit != "per_hour" {
		t.Fatalf("kind=%v limit=%q, want RateLimited/per_hour", res.Kind, res.Limit)
	}
}

func TestHourlyWindowSlides(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Now()

	limits := RateLimits{GlobalPerHour: 1, GlobalPerDay: 10}
	p := params("old", now.Add(-61*time.Minute))
	p.Limits = limits
	if res, err := s.Reserve(ctx, p); err != nil || res.Kind != Reserved {
		t.Fatalf("old reserve: kind=%v err=%v", res.Kind, err)
	}
	p = params("new", now)
	p.Limits = limits
	res, err := s.Reserve(ctx, p)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if res.Kind != Reserved {
		t.Fatalf("kind = %v, want Reserved (old row outside window)", res.Kind)
	}
}

func TestGlobalDailyLimit(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Now()

	limits := RateLimits{GlobalPerHour: 10, GlobalPerDay: 1}
	p := params("a", now.Add(-2*time.Hour)) // outside hourly, inside daily window
	p.Limits = limits
	if res, err := s.Reserve(ctx, p); err != nil || res.Kind != Reserved {
		t.Fatalf("reserve: kind=%v err=%v", res.Kind, err)
	}
	p = params("b", now)
	p.Limits = limits
	res, err := s.Reserve(ctx, p)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if res.Kind != RateLimited || res.Limit != "per_day" {
		t.Fatalf("kind=%v limit=%q, want RateLimited/per_day", res.Kind, res.Limit)
	}
}

func TestPerRecipientVsGlobal(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Now()

	// Recipient limit of 1 for alias "self-gmail"; other aliases unaffected.
	p := params("a", now)
	p.Limits = RateLimits{GlobalPerHour: 10, GlobalPerDay: 10, RecipientPerHour: 1, RecipientPerDay: 10}
	if res, err := s.Reserve(ctx, p); err != nil || res.Kind != Reserved {
		t.Fatalf("reserve: kind=%v err=%v", res.Kind, err)
	}

	p = params("b", now)
	p.Limits = RateLimits{GlobalPerHour: 10, GlobalPerDay: 10, RecipientPerHour: 1, RecipientPerDay: 10}
	res, err := s.Reserve(ctx, p)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if res.Kind != RateLimited || res.Limit != "recipient per_hour" {
		t.Fatalf("kind=%v limit=%q, want RateLimited/recipient per_hour", res.Kind, res.Limit)
	}

	// A different alias only counts against the global budget.
	p = params("c", now)
	p.Alias = "other-alias"
	p.Limits = RateLimits{GlobalPerHour: 10, GlobalPerDay: 10, RecipientPerHour: 1, RecipientPerDay: 10}
	if res, err := s.Reserve(ctx, p); err != nil || res.Kind != Reserved {
		t.Fatalf("other alias: kind=%v err=%v", res.Kind, err)
	}
}

func TestLimitsSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	now := time.Now()
	p := params("a", now)
	p.Limits = RateLimits{GlobalPerHour: 1, GlobalPerDay: 10}
	if res, err := s.Reserve(context.Background(), p); err != nil || res.Kind != Reserved {
		t.Fatalf("reserve: kind=%v err=%v", res.Kind, err)
	}
	s.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	p = params("b", now)
	p.Limits = RateLimits{GlobalPerHour: 1, GlobalPerDay: 10}
	res, err := s2.Reserve(context.Background(), p)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if res.Kind != RateLimited {
		t.Fatalf("kind = %v, want RateLimited after restart", res.Kind)
	}
}

func TestReserveFailsWhenDBUnavailable(t *testing.T) {
	s, _ := openTest(t)
	s.Close()
	_, err := s.Reserve(context.Background(), params("k", time.Now()))
	if err == nil {
		t.Fatal("Reserve on closed store succeeded; want error (fail closed)")
	}
}

func TestSweepSending(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Now()

	mustReserve(t, s, params("stuck", now))
	mustReserve(t, s, params("done", now))
	s.Complete(ctx, "done", StateSent, "250", "", now)

	n, err := s.SweepSending(ctx, now)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("swept %d rows, want 1", n)
	}
	row, _ := s.Get(ctx, "stuck")
	if row.State != StateAmbiguous {
		t.Fatalf("stuck state = %q, want ambiguous", row.State)
	}
	row, _ = s.Get(ctx, "done")
	if row.State != StateSent {
		t.Fatalf("done state = %q, want sent (untouched)", row.State)
	}

	// A duplicate of the swept key replays the ambiguous result.
	res, err := s.Reserve(ctx, params("stuck", now))
	if err != nil || res.Kind != Replay || res.Existing.State != StateAmbiguous {
		t.Fatalf("replay after sweep: kind=%v err=%v", res.Kind, err)
	}
}

func TestPurgeRetention(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Now()

	mustReserve(t, s, params("old", now.Add(-91*24*time.Hour)))
	mustReserve(t, s, params("new", now))

	n, err := s.Purge(ctx, now.Add(-90*24*time.Hour))
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if n != 1 {
		t.Fatalf("purged %d rows, want 1", n)
	}
	if _, err := s.Get(ctx, "old"); err == nil {
		t.Fatal("old row still present after purge")
	}
	if _, err := s.Get(ctx, "new"); err != nil {
		t.Fatalf("new row missing after purge: %v", err)
	}
}

func TestNoPlaintextInDatabaseFile(t *testing.T) {
	s, path := openTest(t)
	ctx := context.Background()
	now := time.Now()

	// The store only ever receives hashes; prove the file holds no plaintext.
	subject := "very-unique-subject-string-zq81"
	body := "very-unique-body-string-xk42"
	p := params("k1", now)
	p.SubjectHash = "sha256-of-subject"
	p.BodyHash = "sha256-of-body"
	mustReserve(t, s, p)
	s.Complete(ctx, "k1", StateSent, "250", "<mid@x>", now)
	s.Close()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read db: %v", err)
	}
	for _, secret := range []string{subject, body, "credential-string"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("database file contains plaintext %q", secret)
		}
	}
}
