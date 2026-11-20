// Package verify judges deployments with plain statistics; this part of
// its documentation says exactly which, and where they can be wrong.
//
// # Metrics
//
// The window is split into intervals. In each interval a metric's value
// is the mean of the new version's readings (readings that are not
// numbers are dropped; an interval with fewer than MinPoints readings, or
// whose read failed, has no data). That value is checked, in order:
//
//   - against fail-fast bounds: a breach fails the whole verification at
//     once;
//   - against fixed bounds (Max, Min);
//   - against the baseline, when there are at least 10 earlier interval
//     means from passed verifications: z = (value - mean) / sd of those
//     means;
//   - against the stable version, when it has readings in the interval:
//     z = (value - stable mean in the interval) / sd of all stable
//     readings so far.
//
// The sd is never taken as less than 5% of the reference mean, nor less
// than 1e-9, so a reference that never moves still gives a finite z. A
// comparison fails the interval only if z goes the wrong way (by the
// metric's direction) by more than k standard deviations and the
// difference is at least minEffect of the reference mean: a statistically
// clear but tiny change (an error rate of 0 against 0.000000001) does not
// count. k and minEffect depend on the sensitivity; see Thresholds.
//
// A metric fails when Failures consecutive intervals fail (default 2).
// When every interval is in, it passes only if at least MinIntervals had
// data (default half the window) and at least one check could run;
// otherwise it is inconclusive, never a silent pass.
//
// # Logs
//
// Log lines are grouped into templates with the Drain algorithm (see
// Clusterer). The new version's clusters are compared with the stable
// version's (when the step reads them) or else with the baseline's: a
// template that matches no reference template is unknown; one that
// appears at least 5 times and RateFactor times more often than in the
// reference (per minute against a baseline, per line read against the
// stable version) is unexpected_frequency. Only clusters that read as
// errors can fail a log query.
//
// # When the statistics pass a bad version
//
//   - Too short a window, or too few intervals: a regression that builds
//     up slowly (a leak, a queue filling) may not show yet.
//   - Low traffic: with few requests a canary's error rate is mostly 0 or
//     a large jump, and an interval with one or two readings says little.
//     MinPoints and MinIntervals stop the worst of this, but a small
//     canary can still look healthy by luck.
//   - A regressed baseline: the baseline is made of earlier verifications
//     that passed. If a regression passed once (or grew slowly over many
//     passes), it is part of what normal looks like.
//   - Queries that do not separate versions: if the metric query sums the
//     canary and stable pods together, a canary taking a small share of
//     traffic moves the total little, and the comparison with stable
//     compares a series with itself.
//   - The stable comparison uses the sd of single readings, not of means,
//     so it is deliberately less sensitive than a significance test.
//
// # When the statistics fail a good version
//
//   - Seasonality: a baseline recorded at another time of day or week
//     (daily traffic peaks, nightly batch jobs) differs from now for
//     reasons that have nothing to do with the deployment. Comparing with
//     the stable version in the same interval avoids this; the baseline
//     does not.
//   - Warm-up: right after a deploy, caches are cold, connection pools
//     and JIT compilers are warming up, and latency and errors are
//     briefly higher. Start the window after a delay.
//   - Autocorrelation: readings next to each other in time are not
//     independent, so the sd of readings in a short window under-estimates
//     how much the series really varies; z comes out larger than it
//     should, and two versions that differ only by chance (a pod on a
//     busier node) can fail. minEffect limits this.
//   - A very quiet reference: when the reference barely varies, the 5%
//     floor on the sd and minEffect decide; a small but real change in a
//     flat series may then fail where a person would shrug.
//   - New log templates that are not errors never fail a check, but a
//     changed error message (same error, new wording) is new to the
//     reference and can fail at high sensitivity.
package verify
