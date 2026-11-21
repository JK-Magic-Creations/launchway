#!/bin/sh
# Reads go test -json from the file given and prints, one line per package, how
# it ended, and for what failed the reason: each failed test with the last lines
# of its output, and each package that failed with no failing test (a build error,
# a crash before the first test) with the last lines of the package's output.
# It prints nothing about packages without tests. It never fails: the caller keeps
# go test's own exit status.
#
#   scripts/ci/go-test-summary.sh reports/go-test.json
#
# Needs only sh and awk (POSIX; mawk, gawk and busybox all work).
# TAIL_LINES (default 8) is how many lines of each output are kept; MAX_FAILURES
# (default 25) how many failures are described before "and N more".
f=${1:?usage: go-test-summary.sh go-test.json}
awk -v tail="${TAIL_LINES:-8}" -v maxfail="${MAX_FAILURES:-25}" '
function field(s, key,    i, j, v) {
	i = index(s, "\"" key "\":\"")
	if (i == 0) return ""
	v = substr(s, i + length(key) + 4)
	j = index(v, "\"")
	return substr(v, 1, j - 1)
}
function unescape(s) {
	gsub(/\\\\/, "\001", s)
	gsub(/\\n/, "\n", s)
	gsub(/\\r/, "", s)
	gsub(/\\t/, "\t", s)
	gsub(/\\"/, "\"", s)
	gsub(/\\u003c/, "<", s)
	gsub(/\\u003e/, ">", s)
	gsub(/\\u0026/, "\\&", s)
	gsub(/\\u001b\[[0-9;]*m/, "", s)
	gsub(/\001/, "\\", s)
	return s
}
# The Output field is last on its line, or followed by OutputType (Go 1.27):
# {"Time":"..","Action":"output","Package":"..","Test":"..","Output":"..","OutputType":"error"}
function output(s,    i, v) {
	i = index(s, ",\"Output\":\"")
	if (i == 0) return ""
	v = substr(s, i + 11)
	sub(/"(,"OutputType":"[a-zA-Z]*")?}[ \t\r]*$/, "", v)
	return unescape(v)
}
function keep(old, add,    n) {
	old = old add
	if (length(old) > 16000) old = substr(old, length(old) - 15999)
	return old
}
function show(head, text,    n, lines, i, from, line) {
	n = split(text, lines, "\n")
	while (n > 0 && lines[n] == "") n--
	if (n == 0) { print head " (no output)"; return }
	print head
	from = n - tail + 1
	# a panic is shown from its first line, with the stack that follows (a few more lines)
	for (i = 1; i <= n; i++) if (lines[i] ~ /^panic: /) { if (i < from) from = i; if (n - from + 1 > tail + 6) n = from + tail + 5; break }
	if (from < 1) from = 1
	if (from > 1) print "      ... " (from - 1) " earlier lines left out"
	for (i = from; i <= n; i++) {
		line = lines[i]
		if (length(line) > 240) line = substr(line, 1, 237) "..."
		print "      " line
	}
}
function noise(l) { return l ~ /^[ \t]*(=== (RUN|PAUSE|CONT|NAME)|--- (PASS|FAIL|SKIP): |FAIL$|FAIL[ \t]|PASS$|exit status)/ }
{
	act = field($0, "Action")
	pkg = field($0, "Package")
	if (act == "build-output") {
		p = field($0, "ImportPath"); sub(/ .*/, "", p)
		if (p != "") bout[p] = keep(bout[p], output($0))
		next
	}
	test = field($0, "Test")
	if (act == "output") {
		o = output($0)
		if (test != "") {
			# lines the framework writes are left out, the rest is the test own output
			n = split(o, ol, "\n"); kept = ""
			for (i = 1; i <= n; i++) if (ol[i] != "" && !noise(ol[i])) kept = kept ol[i] "\n"
			tout[pkg SUBSEP test] = keep(tout[pkg SUBSEP test], kept)
		} else {
			n = split(o, ol, "\n"); kept = ""
			for (i = 1; i <= n; i++) if (ol[i] != "" && !noise(ol[i]) && ol[i] !~ /^(ok|\?)[ \t]/) kept = kept ol[i] "\n"
			pout[pkg] = keep(pout[pkg], kept)
		}
		next
	}
	if (act == "run" && test != "") { running[pkg SUBSEP test] = 1; seen[pkg] = seen[pkg] + 1; order[pkg SUBSEP seen[pkg]] = test; next }
	if (act != "pass" && act != "fail" && act != "skip") next
	if (test != "") {
		delete running[pkg SUBSEP test]
		if (act == "fail") {
			bad[pkg]++
			nfail++
			if (nfail <= maxfail) { failed[++nf] = pkg SUBSEP test; saved[pkg SUBSEP test] = tout[pkg SUBSEP test] }
		}
		delete tout[pkg SUBSEP test]
		next
	}
	# a package line
	el = ""
	if (match($0, /"Elapsed":[0-9.]+/)) el = substr($0, RSTART + 10, RLENGTH - 10)
	if (act == "fail") {
		# tests that started and never finished: the test binary died (a panic, a timeout)
		for (i = 1; i <= seen[pkg]; i++) {
			t = order[pkg SUBSEP i]
			if ((pkg SUBSEP t) in running) {
				delete running[pkg SUBSEP t]
				nfail++; bad[pkg]++
				if (nfail <= maxfail) { failed[++nf] = pkg SUBSEP t; saved[pkg SUBSEP t] = tout[pkg SUBSEP t]; unfinished[pkg SUBSEP t] = 1 }
			}
		}
		if (bad[pkg] == 0) pkgfail[++np] = pkg
		else if (pout[pkg] != "") pkgnote[pkg] = pout[pkg]
	}
	line[++nl] = act "  " pkg "  " (el == "" ? "0" : el) "s"
}
END {
	for (i = 1; i <= nl; i++) print line[i]
	if (nfail + np == 0) exit 0
	print ""
	print "Failures: " (nfail + 0) " failed tests, " (np + 0) " packages that failed with no failing test:"
	for (i = 1; i <= nf; i++) {
		split(failed[i], kv, SUBSEP)
		# a parent test with nothing of its own is left out when a subtest of it is listed
		if (saved[failed[i]] == "") {
			sub_ = 0
			for (j = 1; j <= nf; j++) if (index(failed[j], failed[i] "/") == 1) sub_ = 1
			if (sub_) continue
		}
		note = ""
		if (failed[i] in unfinished) note = " (did not finish: the test binary stopped)"
		show("  FAIL " kv[1] " " kv[2] note, saved[failed[i]])
	}
	for (p in pkgnote) show("  " p " package output", pkgnote[p])
	if (nfail > maxfail) print "  ... and " (nfail - maxfail) " more failed test cases (all of them are in the Tests tab)"
	for (i = 1; i <= np; i++) {
		p = pkgfail[i]
		show("  FAIL " p " (the package failed, no test failed)", bout[p] pout[p])
	}
}
' "$f"
