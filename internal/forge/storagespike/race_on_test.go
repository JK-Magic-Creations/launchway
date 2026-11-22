//go:build race

package storagespike

// raceBuild is true when the tests are built with the race detector. Every git hook and the
// receive-pack wrapper is this test binary started again, and a race-instrumented binary takes seconds
// to start on a small runner, so the injection campaign is shortened there (see campaignRounds).
const raceBuild = true
