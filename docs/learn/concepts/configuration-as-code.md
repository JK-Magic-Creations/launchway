# Configuration as code

Everything that configures a machine or a cloud is code in Git, and a change to it goes the same way as a change to software: a plan, a review, **Clearance**, an apply of exactly what was reviewed, and a watch for drift.

The path of a change: it is committed to Git, a plan or check run says what would change, a person gives Clearance, exactly that is applied, a report shows each host, and a scheduled check watches for drift and sends the next change back to the start.

Launchway drives the tools; it does not reimplement them. **Ansible** configures machines, for example after an image boots or on virtual machines. **OpenTofu** (or Terraform) creates the infrastructure underneath, and Launchway keeps its state for you, sealed in its own database.

- A **check-mode run** says what would change and changes nothing. Scheduled, it is the drift report: `3 hosts drifted`.
- A **plan** is stored with its commit and the state it was made against, and an apply that does not match is refused.
- A **report** shows hosts by plays, what changed, the failed task first, and the hosts to run again.
- **Secrets** are references. Keys and passwords reach the step in memory only, and never reach the report.

Honest limits: Ansible cannot apply a saved plan the way OpenTofu can, so what you review is the check run, and a change can differ if the hosts change in between. Undoing a configuration change means applying the previous definition; some changes, such as a deleted database, cannot be undone by any code.

Read more in [Ansible](/guide/ansible) and [Terraform and OpenTofu](/guide/terraform-opentofu).
