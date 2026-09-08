# Security and data

CordBrief is intended for a trusted machine. The Web UI and setup screen have no
application authentication. Keep the default loopback bindings; use an SSH tunnel
for remote access. The setup screen gives access to a signed-in Discord session.

The exchange volume contains raw watched messages. Core stores digest artifacts
and credentials; Collector keeps recovery metadata and uses a Discord profile
and keyring in separate volumes. Protect the Docker host and backups accordingly.
Local storage is not an encryption guarantee.

Generating a digest sends selected messages to the configured model endpoint.
Enabling Telegram sends the digest to that destination. Use accounts and channels
you are permitted to process, and review your provider's data handling.

Do not attach `.env`, secrets, profiles, journals, volume archives, or unreviewed
logs to a public issue. If you find a vulnerability involving credentials or
private data, use GitHub's private vulnerability reporting if available. Otherwise
ask the maintainer for a private contact without including the sensitive details.
