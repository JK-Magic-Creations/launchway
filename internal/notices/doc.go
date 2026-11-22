// Package notices is how Launchway tells people about what concerns them:
// one model for a review asked of you, a comment or mention, a pull request
// merged or closed, a run of yours that failed or was fixed, an approval
// waiting for you, a deployment you approved that finished or was rolled
// back, a release published in a Flight you watch, and a credential of yours
// about to expire.
//
// A notice goes to the in-app inbox and, if the person's preferences say so,
// by e-mail (internal/mail). Each person chooses, per kind, in-app, e-mail or
// off; a watch level per repository and Flight (all, participating, none);
// quiet hours, during which mail waits; and whether e-mail comes as a daily
// digest. By default the things that wait on a person (a review asked, a
// mention, an approval, an expiring credential) come by e-mail and everything
// else stays in the inbox, and everyone watches what they take part in.
//
// E-mail leaves the trust boundary of the installation, so it carries less
// than the inbox: for a repository that is not public the subject names only
// the repository and the message holds a link, never a title, a comment or
// code, unless the administrator turned that on (mail.include_private_details).
// Every message has a link that mutes its kind without signing in, signed so
// that it cannot be forged or reused for another person.
package notices
