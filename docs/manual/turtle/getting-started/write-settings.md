---
type: Guide
title: Write the settings file
description: Create the JSON settings file with the server address, accounts, folders and RPC token.
tags: [turtle, getting-started, settings, config, rpc-auth]
status: stable
generated: { by: forever-agent docs, at: 2026-11-05T20:25:00Z }
sources:
  - id: config
    resource: internal/config/config.go
  - id: readme
    resource: README.md
---

This page describes the Turtle 1.12 stack. These commands refuse a ForeverCore server.

# Write the settings file

The settings file is a JSON file with the server address, your account passwords, and folder paths. The agent reads it at `login`, `fleet`, and `slot setup`. It holds passwords, so no value from it may reach a log line, an error, an event or a capture.

## Steps

1. Create the file with mode `0600`:

   ```sh
   touch <settings-file>
   chmod 600 <settings-file>
   ```

   The agent refuses a file other users can read: `settings <file> has mode <mode>; it holds passwords, run: chmod 600 <file>`.

2. Write the smallest file that logs in:

   ```json
   {
     "realmd": "<server>:3724",
     "accounts": { "<account>": "<password>" }
   }
   ```

3. Add the optional pieces you need:

   ```json
   {
     "rpc": "127.0.0.1:8765",
     "mmaps_dir": "<mmaps-dir>",
     "dungeons_dir": "<dungeons-dir>",
     "traces_dir": "<traces-dir>"
   }
   ```

   - `rpc` is the RPC listen address the agent binds when `login` gets no `--rpc`. Default `127.0.0.1:8765`. It must be a loopback address.
   - `mmaps_dir` holds copies of the server's `data/mmaps` files, for `path_to` and `walk_path`. Without it the agent logs in fine but those calls answer that no navmesh folder is set. See [Navigation and world data](../../dungeons/index.md).
   - `dungeons_dir` is the folder `dungeon_run` reads plans from. Default `dungeons` under the working directory. See [Dungeon runs](../dungeons/index.md).
   - `traces_dir` is where run traces go. Default `traces` under the working directory. See [Observability and testing](../observability/index.md).

4. For scripts that already send an Authorization header, set your own token:

   ```json
   {
     "rpc_token": "<16-or-more-visible-characters>"
   }
   ```

   Without `rpc_token`, the agent makes a random token at each login and writes it, mode `0600`, to `rpc-<port>.token` next to the settings file. The startup line `rpc: calls need the token in <file>` names the file, never the token. To turn the check off while old scripts catch up, set `"rpc_auth": "off"`; the agent then says on stderr that authentication is OFF. `"rpc_token"` together with `"rpc_auth": "off"` is refused.

5. Start from this file instead of writing your own when a slot owns the character: `slot setup` writes a private `settings.json` into the slot dir with the right account, port and folders. See [Lease a slot and set it up](../../getting-started/settings-and-slots.md).

Every key, with defaults and validation, is listed in [Settings file](settings-reference.md).
