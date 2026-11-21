// Package work is Launchway's work tracking: issues, bugs, stories, tasks and
// epics with a small workflow, links to the code and to what is running,
// planning (boards, backlog order, sprints, milestones, charts) and agents that
// may be assigned work. It keeps its tables in the engine's database
// (migration 56) and knows nothing of HTTP: who may do what is decided by the
// caller, which passes an Actor and an Access.
package work
