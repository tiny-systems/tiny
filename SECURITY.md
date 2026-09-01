# Security

The threat model — what a hostile agent can and cannot do, including the
holes we know about — is documented at
https://tinysystems.io/docs/threat-model/.

Found something it misses? Please report privately to
**hello@tinysystems.io** rather than opening a public issue. You will get
a reply from a human; there is no bounty program, only gratitude and
credit.

Facts that matter for triage:

- Agent pods hold no git or GitHub credentials; work leaves via git
  bundles pushed by a short-lived CI job.
- The sidecar's ServiceAccount can create Question objects and update its
  own session's status, nothing else.
- The courier's exec channel is argv-only with an allowlist on bundle
  names.
