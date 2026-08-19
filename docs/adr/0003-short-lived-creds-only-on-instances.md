# Instances hold only short-lived or scoped credentials

Instances are ephemeral and disposable, so the credential stance is: nothing long-lived rig
controls ever lands on one. SSH agent forwarding covers git; `rig fetch s3://` forwards
short-lived AWS session credentials — forwarding the local session as-is when it already is one
(SSO, assumed role), exchanging a long-lived access key via STS first so it never leaves the
operator's machine — and feeds them to the single remote command over stdin, never argv or disk.
Static third-party API keys (wandb, HuggingFace) can't be shortened, only transported: the user
pushes them explicitly as env files, owns their scoping, and the instance TTL bounds the
exposure. Decided with the repo owner (2026-08-18) over the alternatives of presigned-URL-only
fetches (too narrow — scripts on the instance need credentials too) and long-lived keys on instances
(breaks the stance).
