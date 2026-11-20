# Add a policy

A policy is a set of rules every run must meet, which a pipeline cannot opt out of: for example
that deploys to production have passing tests and an SBOM. The wizard builds one from the rule
catalogue.

## What it asks

1. **What is it for?** A name and a sentence describing it.
2. **Which rules?** The wizard lists every rule in the [catalogue](../reference/policy-rules.md) with a
   plain explanation, and what each one needs you to fill in (a minimum number of tests, the library
   versions allowed, and so on).
3. **Enforce, or only report?** *Enforce* stops a run that breaks a rule. *Report* lets it through and
   records the verdict, which is the safe way to see what a policy would do before it blocks anyone.
4. **To whom does it apply?** Projects, environments, branches and events it covers; a rule about
   deploys may be limited to environments.

## What it creates

The policy. Cancelling creates nothing. Verdicts appear on each run's policy page and in **Policy**; a
person with the right role may grant a time-limited waiver for one rule, with a reason.

From the command line: `launchway policies set prod-evidence -f policy.yaml`.
