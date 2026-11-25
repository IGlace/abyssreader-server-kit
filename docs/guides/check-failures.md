<!--
SPDX-License-Identifier: CC-BY-NC-SA-4.0
Copyright 2026 Abyss Reader
-->
# When the app's server check cannot pass

Ask the reader to select **Copy report for the operator** in the app. Keep the app and kit versions with the report. Use the health check in your Compose install folder.

```sh
sudo docker compose exec server abyssreader-server doctor
```

For a direct DNS setup, supply each address you intended to publish. Repeat `--expected-ip` for each A or AAAA address. If a CDN or tunnel serves the public name, compare with the addresses it expects.

```sh
sudo docker compose exec server abyssreader-server doctor --expected-ip 192.0.2.10
```

The health check reads settings, resolves the public name, verifies HTTPS and reaches the public status route. It samples a stored cover and page when available, using no access key or session. It checks free space and local backup metadata. A backup date confirms the presence of a complete generation record. Test restores on a separate system to verify recovery. Run the app check again after correcting the setup.

| Report text | Likely causes | What to check |
| --- | --- | --- |
| `failed (unreachable)` | Reverse proxy routes are missing, the upstream is down, or images are inaccessible. | Run the health check. Inspect the server and proxy logs for the time of the report. Check that the public address routes to the kit and that image paths can be read without credentials. |
| `failed (wrong response)` | The proxy served a login or error page, or the installed build and enabled features disagree. | Confirm the public address points to the intended kit. Check the installed version and the flags for the feature named in the report. |
| `failed (wrong behaviour)` | A kit bug, a fork's changes or a proxy cache affected the response. | Check that the proxy passes the app's methods and requests through and does not cache personalized API responses. Compare the installed build with the official release. |
| `not tested (could not finish)` | DNS, TLS, timeout, rate limit or temporary service trouble. | Run the health check, confirm network reachability and retry once the server is ready. Respect any wait the app reports. |
| `not tested (needs sign-up)` or `checked (read-only)` | Sign-up is closed or the check was read-only. | Confirm the intended sign-up policy. Run a write check only when the operator chooses to permit it. |
| `not tested (an earlier step failed)` | This feature depends on an earlier check. | Resolve the earliest reported failure, then repeat the app check. |
| `not tested (nothing on this server to test it with)` or `not tested (not checked by this app version)` | No suitable content is available, or the app version has no check for that feature. | Check which feature the report names and whether content or an app update is expected. |
| `not tested (the check could not run)` | The app or device could not complete its own check. | Retry from a working app install. Include the app version and report if it repeats. |
| `switched off` | The feature is disabled. | Check that the result matches the operator's intended settings. |

## Share a report

Use the appropriate kit issue form if setup checks pass and the app still reports a failure. Include the kit version, health output, sanitized recent logs and the app's operator report. Review all of it and remove tokens, access and signing keys, contacts and other personal details. Use the private reporting path for security issues. [Security policy](../../SECURITY.md)
