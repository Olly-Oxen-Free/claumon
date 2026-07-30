package forecast

import (
	"database/sql"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/fabioconcina/claumon/internal/store"
)

// sqlStoreAdapter mirrors main.go's storeAdapter: it bridges the real SQLite
// store to the forecast.Store interface, so this test runs the same pipeline
// the dashboard uses (store -> Refit -> SampleFor), short of the HTTP shims.
type sqlStoreAdapter struct{ st *store.Store }

func (a *sqlStoreAdapter) GetWindowSnapshots(gauge, resetAt string, since time.Time) ([]StoreSnapshot, error) {
	rows, err := a.st.GetWindowSnapshots(gauge, resetAt, since)
	if err != nil {
		return nil, err
	}
	out := make([]StoreSnapshot, len(rows))
	for i, r := range rows {
		out[i] = StoreSnapshot{Time: r.Time, U: r.U}
	}
	return out, nil
}

func (a *sqlStoreAdapter) GetCompletedSessions(gauge string, before time.Time, limit int) ([]StoreSession, error) {
	rows, err := a.st.GetCompletedSessions(gauge, before, limit)
	if err != nil {
		return nil, err
	}
	out := make([]StoreSession, len(rows))
	for i, r := range rows {
		snaps := make([]StoreSnapshot, len(r.Snapshots))
		for j, sn := range r.Snapshots {
			snaps[j] = StoreSnapshot{Time: sn.Time, U: sn.U}
		}
		out[i] = StoreSession{ResetAt: r.ResetAt, UFinal: r.UFinal, Snapshots: snaps}
	}
	return out, nil
}

// TestWeeklyPlateauForecastDoesNotCollapse is the integration-level regression
// test for the flat weekly forecast: a real SQLite store seeded with several
// completed weekly windows plus a current window whose recent snapshots are a
// plateau (OLS rate -> ~0). Under v2.0-v2.1 the whole pipeline then collapsed
// to a point forecast (CI hi == lo == uNow, p_inf = 1); v2.2 must keep the
// fan open using the calibrated historical rate variance.
func TestWeeklyPlateauForecastDoesNotCollapse(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	// Seed with backdated snapshots via a direct connection (the store API
	// stamps CURRENT_TIMESTAMP, so history cannot be written through it).
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC().Truncate(time.Minute)
	const week = 7 * 24 * time.Hour
	resetNow := now.Add(48 * time.Hour) // current weekly window: 5 days in

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	stmt, err := tx.Prepare(
		`INSERT INTO usage_snapshots (timestamp, session_pct, weekly_pct, session_reset_at, weekly_reset_at, raw_json)
		 VALUES (?, 0, ?, '', ?, '{}')`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	insert := func(ts time.Time, pct float64, resetAt time.Time) {
		if _, err := stmt.Exec(
			ts.UTC().Format("2006-01-02 15:04:05"), pct,
			store.NormalizeResetAt(resetAt.UTC().Format(time.RFC3339))); err != nil {
			t.Fatalf("insert snapshot: %v", err)
		}
	}

	// 8 completed weekly windows with varied final utilization and bursty
	// within-week growth. The burstiness matters: perfectly smooth synthetic
	// curves calibrate to sigma^2 ~ 0 and bar_tau^2 = 0, and with no
	// historical rate variance the model has nothing to fan out with (a
	// collapse that would be correct). Deterministic pseudo-noise keeps the
	// fixture reproducible.
	// Snapshots every 15 minutes: the calibration replays OLS on a
	// 30-minute recency window at each pivot, so sparse fixtures (hours
	// apart) leave it nothing to fit and bar_tau^2 degenerates to 0.
	finals := []float64{55, 70, 30, 62, 45, 80, 38, 58}
	const stepH = 0.25
	for k, final := range finals {
		reset := resetNow.Add(-time.Duration(k+1) * week)
		start := reset.Add(-week)
		meanRate := final / week.Hours() // pct per hour
		u := 0.0
		for h := 0.0; h <= week.Hours(); h += stepH {
			ts := start.Add(time.Duration(h * float64(time.Hour)))
			insert(ts, u, reset)
			// Rate switches regime every ~30h and wobbles within a regime:
			// idle stretches and bursts, like real usage.
			phase := h/30.0 + float64(k)*1.7
			burst := 0.5 + 1.5*math.Abs(math.Sin(phase)) // 0.5x..2x mean rate
			jitter := 0.3 * math.Sin(11.3*phase+float64(k))
			u += math.Max(0, meanRate*(burst+jitter)*stepH)
		}
	}

	// Current window: normal growth to 49%, then a hard plateau for the last
	// 3 hours. The recency window (TauRecent) sees only flat points, so the
	// OLS posterior rate is ~0 - the exact state observed in the field.
	start := resetNow.Add(-week)
	plateauStart := now.Add(-3 * time.Hour)
	for ts := start; ts.Before(plateauStart); ts = ts.Add(15 * time.Minute) {
		frac := ts.Sub(start).Hours() / plateauStart.Sub(start).Hours()
		insert(ts, 49*frac, resetNow)
	}
	for ts := plateauStart; !ts.After(now); ts = ts.Add(5 * time.Minute) {
		insert(ts, 49, resetNow)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Run the service exactly as main.go wires it.
	svc := NewService(&sqlStoreAdapter{st: st}, DefaultConfig())
	if !svc.Refit(GaugeWeekly, now) {
		t.Fatal("Refit(weekly) failed: seeded history not usable")
	}

	resetISO := resetNow.Format(time.RFC3339)
	payload, ok := svc.SampleFor(GaugeWeekly, resetISO, 49.0, now, 100, 120, 80)
	if !ok {
		t.Fatal("SampleFor returned !ok")
	}

	// The posterior rate must actually be degenerate, or this test isn't
	// exercising the collapse scenario at all.
	fc, ok := svc.ForecastFor(GaugeWeekly, resetISO, 49.0, now, []float64{100})
	if !ok {
		t.Fatal("ForecastFor returned !ok")
	}
	if fc.Posterior.RHat > 1e-3 {
		t.Fatalf("scenario not degenerate: posterior rate %v is not ~0", fc.Posterior.RHat)
	}

	// v2.0-v2.1 failure mode: CIHi == CILo == uNow and p_inf == 1.
	if payload.CIHi-payload.CILo < 0.01 {
		t.Errorf("CI collapsed: [%v, %v]", payload.CILo, payload.CIHi)
	}
	if payload.CIHi <= 0.49+1e-6 {
		t.Errorf("CI upper edge did not rise above uNow: %v", payload.CIHi)
	}
	// Note: p_inf = 1 is legitimate here - reaching 100% from 49% in 48h
	// would need a rate far beyond the historical spread. The collapse
	// signature is the degenerate CI, which the assertions above cover.
	fanned := 0
	for _, path := range payload.Trajectories {
		if path[len(path)-1] > 0.49+1e-9 {
			fanned++
		}
	}
	if fanned == 0 {
		t.Errorf("all %d trajectories flat: fan collapsed", len(payload.Trajectories))
	}
	t.Logf("v2.2 plateau forecast: CI [%.1f%%, %.1f%%], p_inf %.2f, %d/%d trajectories grow",
		payload.CILo*100, payload.CIHi*100, payload.PInf, fanned, len(payload.Trajectories))
}
