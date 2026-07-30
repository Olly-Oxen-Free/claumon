## Forecast model v2.2

- **The weekly forecast no longer flatlines after an idle stretch.** When the
  recent snapshot window was a plateau (common for the slow-moving weekly
  gauge), the Monte Carlo's rate draw could degenerate and the forecast
  modal showed a single flat line with a zero-width interval ("80% CI
  49%-49%"). The per-path rate is now drawn from a rectified Gaussian
  instead of a moment-matched Gamma: "no further growth" remains a possible
  outcome with honest probability, while the fan stays open according to the
  historical rate variance. Path increments are unchanged (still the v2.0
  Gamma process), so trajectories remain monotone.

- **Better-calibrated intervals across the board.** On the real-data
  benchmark (LOSO replay), 80% CI coverage moves from 55% to 89% on the
  session gauge and from 73% to 74% on the weekly gauge, with CRPS slightly
  improved on both - the wider coverage is earned, not bought with a blanket
  widening.

- **Spec updated.** MODEL.tex and MODEL.pdf describe the new rate law, the
  v2.1 spec is archived under `internal/forecast/archive/`, and the forecast
  CHANGELOG has the full rationale. New regression tests pin the plateau
  scenario at both the sampler and the store-to-payload integration level.
